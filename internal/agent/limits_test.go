package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dreamstation625/ovpanel/internal/admission"
	"github.com/dreamstation625/ovpanel/internal/netpolicy"
	"github.com/dreamstation625/ovpanel/internal/telemetry"
	"github.com/dreamstation625/ovpanel/internal/traffic"
)

func newLimitRuntime(t *testing.T, profile admission.Profile) (*Runtime, *bytes.Buffer, string) {
	t.Helper()
	controlDir, stateDir := t.TempDir(), t.TempDir()
	rt := New(controlDir, stateDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	rt.configDir = t.TempDir()
	rt.applyPolicyFn = func(netpolicy.Policy) error { return nil }
	rt.applyTrafficFn = func([]traffic.Session) error { return nil }
	rt.usageHealthy = true
	rt.snapshot = admission.Snapshot{
		Users:    []admission.User{{ID: "u1", Status: "active"}},
		Profiles: []admission.Profile{profile},
	}
	buf := &bytes.Buffer{}
	rt.mgmtWriter = bufio.NewWriter(buf)
	return rt, buf, controlDir
}

func certAuth(profile admission.Profile) admission.Profile {
	profile.UserID = "u1"
	profile.AuthType = admission.AuthCertificate
	profile.Status = "active"
	profile.CertSerial = "0A"
	profile.CertFingerprint = "aa"
	profile.CertStatus = "active"
	if profile.ID == "" {
		profile.ID = "A"
	}
	return profile
}

func connect(rt *Runtime, cid, addr string) {
	rt.pendingAddr[cid] = addr
	rt.evaluate(&pendingAuth{
		cid: cid, kid: "0", kind: "connect",
		env: map[string]string{"tls_serial_hex_0": "0A", "trusted_ip": "203.0.113.5"},
	})
}

func TestConcurrencyLimitReservesOnlyNewSessions(t *testing.T) {
	rt, buf, _ := newLimitRuntime(t, certAuth(admission.Profile{ID: "A", ConcurrentLimit: 1}))
	connect(rt, "1", "10.8.0.2")
	rt.mgmtWriter.Flush()
	if !strings.Contains(buf.String(), "client-auth-nt 1 0") {
		t.Fatalf("第一条连接应被允许：%q", buf.String())
	}
	if len(rt.sessions) != 1 {
		t.Fatalf("在线会话数 = %d", len(rt.sessions))
	}

	connect(rt, "2", "10.8.0.3")
	rt.mgmtWriter.Flush()
	if !strings.Contains(buf.String(), "client-deny 2 0 \"concurrency_limit\"") {
		t.Fatalf("超过并发上限应被拒绝：%q", buf.String())
	}
	if len(rt.sessions) != 1 {
		t.Fatalf("被拒绝的连接不应占用名额：%d", len(rt.sessions))
	}

	// 重认证不新增占位。
	rt.evaluate(&pendingAuth{cid: "1", kid: "0", kind: "reauth", env: map[string]string{"tls_serial_hex_0": "0A"}})
	rt.mgmtWriter.Flush()
	if strings.Contains(buf.String(), "client-deny 1") {
		t.Fatalf("重认证不应因并发上限被拒绝：%q", buf.String())
	}
	if len(rt.sessions) != 1 {
		t.Fatalf("重认证后会话数异常：%d", len(rt.sessions))
	}
}

func TestQuotaExhaustionKillsSessionsAndDeniesNew(t *testing.T) {
	rt, buf, controlDir := newLimitRuntime(t, certAuth(admission.Profile{ID: "A", QuotaBytes: 100}))
	connect(rt, "1", "10.8.0.2")
	buf.Reset()

	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	rt.now = func() time.Time { return now }
	rt.recordUsage("1", 60, 39)
	rt.mgmtWriter.Flush()
	if strings.Contains(buf.String(), "client-kill 1") {
		t.Fatalf("未达额度不应断开：%q", buf.String())
	}
	now = now.Add(2 * time.Second)
	rt.recordUsage("1", 60, 40)
	rt.mgmtWriter.Flush()
	if !strings.Contains(buf.String(), "client-kill 1") {
		t.Fatalf("额度耗尽应立即断开：%q", buf.String())
	}
	if len(rt.sessions) != 0 {
		t.Fatal("额度耗尽的会话应从在线映射移除")
	}

	connect(rt, "2", "10.8.0.3")
	rt.mgmtWriter.Flush()
	if !strings.Contains(buf.String(), "client-deny 2 0 \"quota_exhausted\"") {
		t.Fatalf("额度耗尽后新连接应被拒绝：%q", buf.String())
	}

	// 结束事件必须补齐最后一个采样窗口的增量，日汇总才不会漏计。
	events := readEvents(t, filepath.Join(controlDir, "events-active.jsonl"))
	if len(events) == 0 {
		t.Fatal("未写入用量事件")
	}
	last := events[len(events)-1]
	if last.Kind != telemetry.EventSessionEnd || last.Reason != "quota_exhausted" {
		t.Fatalf("最后一条事件不是额度结束事件：%+v", last)
	}
	var totalUp, totalDown uint64
	for _, event := range events {
		totalUp += event.DeltaUp
		totalDown += event.DeltaDown
	}
	if totalUp != 60 || totalDown != 40 {
		t.Fatalf("事件增量合计 = %d/%d，期望 60/40", totalUp, totalDown)
	}
}

func TestPruneExpiredProfileEndsSession(t *testing.T) {
	past := time.Now().UTC().Add(-time.Minute)
	profile := certAuth(admission.Profile{ID: "A", ExpiresAt: &past})
	rt, buf, _ := newLimitRuntime(t, profile)
	rt.sessions["1"] = sessionInfo{id: "gen:1", profileID: "A", certSerial: "0A", vpnIP: "10.8.0.2", connectedAt: time.Now().UTC()}
	rt.pruneSessions()
	rt.mgmtWriter.Flush()
	if !strings.Contains(buf.String(), "client-kill 1") {
		t.Fatalf("到期配置的现有连接应被断开：%q", buf.String())
	}
	if len(rt.sessions) != 0 {
		t.Fatal("到期会话仍在映射中")
	}
}

func TestRecordUsageComputesRatesAndRollback(t *testing.T) {
	rt, _, _ := newLimitRuntime(t, certAuth(admission.Profile{ID: "A"}))
	base := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	rt.now = func() time.Time { return base }
	connect(rt, "1", "10.8.0.2")
	rt.recordUsage("1", 2000, 4000)
	rt.now = func() time.Time { return base.Add(2 * time.Second) }
	rt.recordUsage("1", 4000, 8000)
	info := rt.sessions["1"]
	if info.upRate != 1000 || info.downRate != 2000 {
		t.Fatalf("速率计算异常：%+v", info)
	}
	// 计数回退只记录断档，不产生负流量。
	rt.recordUsage("1", 1000, 9000)
	info = rt.sessions["1"]
	if info.upBytes != 1000 || info.downBytes != 9000 {
		t.Fatalf("采样值未更新：%+v", info)
	}
}

func TestDisconnectCommandEndsOnlyTargetCID(t *testing.T) {
	rt, buf, _ := newLimitRuntime(t, certAuth(admission.Profile{ID: "A"}))
	connect(rt, "1", "10.8.0.2")
	connect(rt, "2", "10.8.0.3")
	buf.Reset()
	if err := os.WriteFile(filepath.Join(rt.controlDir, commandFile),
		[]byte(`{"id":"cmd-d","action":"disconnect","cid":"1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rt.handleCommand(context.Background()); err != nil {
		t.Fatalf("执行断开命令失败：%v", err)
	}
	rt.mgmtWriter.Flush()
	if !strings.Contains(buf.String(), "client-kill 1") {
		t.Fatalf("未断开目标连接：%q", buf.String())
	}
	if len(rt.sessions) != 1 {
		t.Fatalf("断开范围错误，剩余 %d 条", len(rt.sessions))
	}
	if _, ok := rt.sessions["2"]; !ok {
		t.Fatal("其他连接不应被断开")
	}
}

func readEvents(t *testing.T, path string) []telemetry.Event {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("打开事件文件失败：%v", err)
	}
	defer file.Close()
	var out []telemetry.Event
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event telemetry.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("解析事件失败：%v", err)
		}
		out = append(out, event)
	}
	return out
}
