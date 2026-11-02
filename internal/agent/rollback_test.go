package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dreamstation625/ovpanel/internal/agentproto"
	"github.com/dreamstation625/ovpanel/internal/netpolicy"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func makeBundle(t *testing.T, config, pool string) []byte {
	t.Helper()
	sum := sha256.Sum256([]byte(config))
	bundle := agentproto.Bundle{
		Version:       agentproto.Version,
		ConfigHash:    hex.EncodeToString(sum[:]),
		Config:        config,
		CAPEM:         "ca",
		ServerCertPEM: "cert",
		ServerKeyPEM:  "key",
		TLSCryptKey:   "tls-crypt",
		CRLPEM:        "crl",
		Policy:        netpolicy.Policy{VPNPool: pool, TunDevice: "tun0"},
		IssuedAt:      time.Now().UTC(),
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("编码控制包失败：%v", err)
	}
	return data
}

func writeBundle(t *testing.T, dir string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, bundleFile), data, 0o600); err != nil {
		t.Fatalf("写入控制包失败：%v", err)
	}
}

// newTestRuntime 使用临时证书目录与可注入的策略/启动函数，避免测试依赖 nftables 与 OpenVPN。
func newTestRuntime(t *testing.T, controlDir, stateDir, badConfigHash string) (*Runtime, *[]string) {
	t.Helper()
	rt := New(controlDir, stateDir, testLogger())
	rt.configDir = t.TempDir()
	restarts := make([]string, 0, 2)
	rt.applyPolicyFn = func(policy netpolicy.Policy) error {
		if policy.VPNPool == "10.9.0.0/16" {
			return errors.New("nft 策略注入失败")
		}
		return nil
	}
	rt.startVPNFn = func(_ context.Context, configHash string) error {
		restarts = append(restarts, configHash)
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if configHash == badConfigHash {
			rt.openvpnRunning = false
			rt.managementConnected = false
			return errors.New("OpenVPN 启动失败")
		}
		rt.configHash = configHash
		rt.openvpnRunning = true
		rt.managementConnected = true
		return nil
	}
	return rt, &restarts
}

func TestApplyFailureRollsBackToLastGood(t *testing.T) {
	controlDir, stateDir := t.TempDir(), t.TempDir()
	good := makeBundle(t, "server 10.8.0.0 255.255.0.0\n", "10.8.0.0/16")
	rt, restarts := newTestRuntime(t, controlDir, stateDir, "")
	writeBundle(t, controlDir, good)
	if err := rt.applyOnce(context.Background()); err != nil {
		t.Fatalf("首次应用失败：%v", err)
	}
	if status := rt.Status(); !status.Configured || status.RolledBack {
		t.Fatalf("首次应用状态异常：%+v", status)
	}
	if _, err := os.Stat(filepath.Join(stateDir, lastGoodFile)); err != nil {
		t.Fatalf("最近成功控制包未持久化：%v", err)
	}

	// 新策略在 nftables 阶段失败；上一次 OpenVPN 仍在运行，回退不需要重启。
	bad := makeBundle(t, "server 10.9.0.0 255.255.0.0\n", "10.9.0.0/16")
	writeBundle(t, controlDir, bad)
	if err := rt.applyOnce(context.Background()); err != nil {
		t.Fatalf("回退成功后不应返回错误：%v", err)
	}
	status := rt.Status()
	if !status.RolledBack {
		t.Fatalf("状态应标记已回退：%+v", status)
	}
	if !strings.Contains(status.RollbackReason, "nft 策略注入失败") {
		t.Fatalf("回退原因缺失：%q", status.RollbackReason)
	}
	if status.ConfigHash != rt.lastGoodHashConfigForTest() {
		t.Fatalf("运行中的配置应回到上一版本：%q", status.ConfigHash)
	}
	if !status.OpenVPNRunning || !status.ManagementConnected {
		t.Fatalf("回退后 OpenVPN 应保持可用：%+v", status)
	}
	if len(*restarts) != 1 {
		t.Fatalf("策略失败发生在重启前，不应重复启动：%v", *restarts)
	}

	// 同一份失败控制包不应每秒重复回退。
	if err := rt.applyOnce(context.Background()); err != nil {
		t.Fatalf("重复读取失败控制包出错：%v", err)
	}
	if len(*restarts) != 1 {
		t.Fatalf("同一失败控制包被重复应用：%v", *restarts)
	}
}

func TestCredentialUpdateFailureStopsVPNInsteadOfRestoringOldAuthorization(t *testing.T) {
	controlDir, stateDir := t.TempDir(), t.TempDir()
	good := policyBundle(t)
	rt, _ := newTestRuntime(t, controlDir, stateDir, "")
	writeBundle(t, controlDir, good)
	if err := rt.applyOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	var changed agentproto.Bundle
	if err := json.Unmarshal(good, &changed); err != nil {
		t.Fatal(err)
	}
	changed.Snapshot.Users[0].Status = "disabled"
	changed.IssuedAt = changed.IssuedAt.Add(time.Second)
	data, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	rt.applyPolicyFn = func(netpolicy.Policy) error { return errors.New("临时 nft 故障") }
	writeBundle(t, controlDir, data)
	if err := rt.applyOnce(t.Context()); err == nil {
		t.Fatal("凭据更新失败应上报并停止服务")
	}
	if status := rt.Status(); status.OpenVPNRunning || status.RolledBack || status.BundleHash == hashBytes(data) {
		t.Fatalf("不能运行旧授权或确认失败的快照：%+v", status)
	}
	if rt.snapshot.Users[0].Status != "disabled" {
		t.Fatal("不得撤销本次停用状态")
	}
	// 面板补发新尝试后，短暂故障消失即可恢复；不得要求手工撤销停用。
	rt.applyPolicyFn = func(netpolicy.Policy) error { return nil }
	changed.IssuedAt = changed.IssuedAt.Add(time.Second)
	data, _ = json.Marshal(changed)
	writeBundle(t, controlDir, data)
	if err := rt.applyOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if status := rt.Status(); !status.OpenVPNRunning || status.BundleHash != hashBytes(data) {
		t.Fatalf("重试后应恢复服务并确认最新控制包：%+v", status)
	}
}

func TestConfigRollbackCannotResurrectRevokedCredentials(t *testing.T) {
	controlDir, stateDir := t.TempDir(), t.TempDir()
	good := policyBundle(t)
	rt, _ := newTestRuntime(t, controlDir, stateDir, "")
	writeBundle(t, controlDir, good)
	if err := rt.applyOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	var changed agentproto.Bundle
	if err := json.Unmarshal(good, &changed); err != nil {
		t.Fatal(err)
	}
	changed.Snapshot.Users[0].Status = "disabled"
	changed.Config += "# 新的服务端候选\n"
	sum := sha256.Sum256([]byte(changed.Config))
	changed.ConfigHash = hex.EncodeToString(sum[:])
	rt.applyPolicyFn = func(netpolicy.Policy) error { return errors.New("候选规则失败") }
	for attempt := 0; attempt < 2; attempt++ {
		changed.IssuedAt = changed.IssuedAt.Add(time.Second)
		data, _ := json.Marshal(changed)
		writeBundle(t, controlDir, data)
		if err := rt.applyOnce(t.Context()); err == nil {
			t.Fatal("包含凭据变更的配置失败不能自动恢复旧授权")
		}
		if rt.Status().OpenVPNRunning || rt.snapshot.Users[0].Status != "disabled" {
			t.Fatal("配置重试恢复了已撤销的授权")
		}
	}
}

func TestStartFailureRollsBackAndRestartsPreviousConfig(t *testing.T) {
	controlDir, stateDir := t.TempDir(), t.TempDir()
	good := makeBundle(t, "server 10.8.0.0 255.255.0.0\n", "10.8.0.0/16")
	// 策略合法，失败发生在启动阶段。
	bad := makeBundle(t, "server 10.9.0.0 255.255.0.0\n", "10.8.0.0/16")
	badHash := bundleConfigHash(t, bad)
	rt, restarts := newTestRuntime(t, controlDir, stateDir, badHash)
	writeBundle(t, controlDir, good)
	if err := rt.applyOnce(context.Background()); err != nil {
		t.Fatalf("首次应用失败：%v", err)
	}

	writeBundle(t, controlDir, bad)
	if err := rt.applyOnce(context.Background()); err != nil {
		t.Fatalf("回退成功后不应返回错误：%v", err)
	}
	status := rt.Status()
	if !status.RolledBack || !strings.Contains(status.RollbackReason, "OpenVPN 启动失败") {
		t.Fatalf("未记录启动失败回退：%+v", status)
	}
	if status.ConfigHash == badHash {
		t.Fatal("回退后不应继续使用启动失败的配置")
	}
	if len(*restarts) != 3 {
		t.Fatalf("应依次启动正常配置、失败配置、回退配置：%v", *restarts)
	}
	if (*restarts)[2] != status.ConfigHash || !status.OpenVPNRunning {
		t.Fatalf("回退后应重新启动上一版本：%v status=%+v", *restarts, status)
	}
}

func TestApplyFailureWithoutLastGoodReturnsError(t *testing.T) {
	controlDir, stateDir := t.TempDir(), t.TempDir()
	rt, _ := newTestRuntime(t, controlDir, stateDir, "")
	writeBundle(t, controlDir, makeBundle(t, "server 10.9.0.0 255.255.0.0\n", "10.9.0.0/16"))
	if err := rt.applyOnce(context.Background()); err == nil {
		t.Fatal("没有可回退版本时应返回错误")
	}
	status := rt.Status()
	if status.Configured || status.RolledBack {
		t.Fatalf("失败且无可回退版本时状态异常：%+v", status)
	}
}

func TestLoadLastGoodSurvivesRestart(t *testing.T) {
	controlDir, stateDir := t.TempDir(), t.TempDir()
	good := makeBundle(t, "server 10.8.0.0 255.255.0.0\n", "10.8.0.0/16")
	first, _ := newTestRuntime(t, controlDir, stateDir, "")
	writeBundle(t, controlDir, good)
	if err := first.applyOnce(context.Background()); err != nil {
		t.Fatalf("首次应用失败：%v", err)
	}

	// 模拟运行代理重启：内存清空，只保留 stateDir 中的上一版本。
	second, _ := newTestRuntime(t, controlDir, stateDir, "")
	second.loadLastGood()
	writeBundle(t, controlDir, makeBundle(t, "server 10.9.0.0 255.255.0.0\n", "10.9.0.0/16"))
	if err := second.applyOnce(context.Background()); err != nil {
		t.Fatalf("重启后回退失败：%v", err)
	}
	status := second.Status()
	if !status.RolledBack || !status.Configured {
		t.Fatalf("重启后应能回退到持久化的上一版本：%+v", status)
	}
}

func TestRestartCommandRestartsUnchangedConfig(t *testing.T) {
	controlDir, stateDir := t.TempDir(), t.TempDir()
	rt, restarts := newTestRuntime(t, controlDir, stateDir, "")
	writeBundle(t, controlDir, makeBundle(t, "server 10.8.0.0 255.255.0.0\n", "10.8.0.0/16"))
	if err := rt.applyOnce(context.Background()); err != nil {
		t.Fatalf("首次应用失败：%v", err)
	}
	if len(*restarts) != 1 {
		t.Fatalf("首次应用应启动一次：%v", *restarts)
	}

	command := []byte(`{"id":"cmd-1","action":"restart","issuedAt":"2026-10-31T00:00:00Z"}`)
	if err := os.WriteFile(filepath.Join(controlDir, commandFile), command, 0o600); err != nil {
		t.Fatalf("写入命令失败：%v", err)
	}
	if err := rt.handleCommand(context.Background()); err != nil {
		t.Fatalf("执行重启命令失败：%v", err)
	}
	if len(*restarts) != 2 {
		t.Fatalf("配置未变化时重启命令也必须重新启动 OpenVPN：%v", *restarts)
	}

	if err := rt.handleCommand(context.Background()); err != nil {
		t.Fatalf("重复读取命令失败：%v", err)
	}
	if len(*restarts) != 2 {
		t.Fatalf("同一命令 ID 不应重复执行：%v", *restarts)
	}
	if _, err := os.Stat(filepath.Join(controlDir, commandFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("已执行的命令应被删除，避免代理重启后重放")
	}
}

func TestStopCommandPersistsState(t *testing.T) {
	controlDir, stateDir := t.TempDir(), t.TempDir()
	rt, _ := newTestRuntime(t, controlDir, stateDir, "")
	stop := []byte(`{"id":"stop-1","action":"stop","issuedAt":"2026-10-31T00:00:00Z"}`)
	if err := os.WriteFile(filepath.Join(controlDir, commandFile), stop, 0o600); err != nil {
		t.Fatalf("写入命令失败：%v", err)
	}
	if err := rt.handleCommand(context.Background()); err != nil {
		t.Fatalf("执行停止命令失败：%v", err)
	}
	if !rt.Status().Stopped {
		t.Fatal("停止命令后状态应为已停止")
	}
	if _, err := os.Stat(filepath.Join(stateDir, stoppedFile)); err != nil {
		t.Fatalf("停止状态未持久化：%v", err)
	}

	restarted := New(controlDir, stateDir, testLogger())
	restarted.configDir = t.TempDir()
	restarted.loadPersistedState()
	if !restarted.Status().Stopped {
		t.Fatal("代理重启后应保持停止状态，不得自动拉起 OpenVPN")
	}

	start := []byte(`{"id":"start-1","action":"start","issuedAt":"2026-10-31T00:00:01Z"}`)
	if err := os.WriteFile(filepath.Join(controlDir, commandFile), start, 0o600); err != nil {
		t.Fatalf("写入启动命令失败：%v", err)
	}
	if err := restarted.handleCommand(context.Background()); err != nil {
		t.Fatalf("执行启动命令失败：%v", err)
	}
	if restarted.Status().Stopped {
		t.Fatal("启动命令后应清除停止状态")
	}
	if _, err := os.Stat(filepath.Join(stateDir, stoppedFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("启动后应删除停止标记")
	}
}

func bundleConfigHash(t *testing.T, data []byte) string {
	t.Helper()
	var bundle agentproto.Bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		t.Fatalf("解析控制包失败：%v", err)
	}
	return bundle.ConfigHash
}

// lastGoodHashConfigForTest 返回最近成功控制包中的配置哈希，便于断言运行配置。
func (r *Runtime) lastGoodHashConfigForTest() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	bundle, err := decodeBundle(r.lastGoodBundle)
	if err != nil {
		return ""
	}
	return bundle.ConfigHash
}
