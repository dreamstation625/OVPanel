package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/dreamstation625/ovpanel/internal/admission"
	"github.com/dreamstation625/ovpanel/internal/agentproto"
	"github.com/dreamstation625/ovpanel/internal/connectpolicy"
	"github.com/dreamstation625/ovpanel/internal/netpolicy"
)

func policyBundle(t *testing.T) []byte {
	t.Helper()
	config := "server 10.8.0.0 255.255.0.0\n"
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
		Policy: netpolicy.Policy{
			VPNPool:         "10.8.0.0/16",
			TunDevice:       "tun0",
			GlobalExit:      false,
			AllowedInternal: []string{"10.0.16.0/22"},
		},
		Snapshot: admission.Snapshot{
			Users: []admission.User{{ID: "u1", Status: "active"}},
			Profiles: []admission.Profile{
				{
					ID: "A", UserID: "u1", AuthType: admission.AuthCertificate, Status: "active",
					CertSerial: "0A1B", CertFingerprint: "aa:bb", CertStatus: "active",
					RouteMode: "global", AccessMode: "allow",
					AllowedInternal: []string{"192.168.50.0/24"},
					FixedVPNIP:      "10.8.255.200", ConcurrentLimit: 1,
				},
				{
					ID: "B", UserID: "u1", AuthType: admission.AuthCertificatePassword, Status: "active",
					Username: "bob", PasswordHash: "hash", CertSerial: "0C0D",
					CertFingerprint: "cc:dd", CertStatus: "active",
					RouteMode: "inherit", AccessMode: "deny",
				},
			},
		},
		IssuedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("编码控制包失败：%v", err)
	}
	return data
}

func TestApplyWritesConnectPolicyAndSessionACL(t *testing.T) {
	controlDir, stateDir := t.TempDir(), t.TempDir()
	rt := New(controlDir, stateDir, testLogger())
	rt.configDir = t.TempDir()
	applied := make([]netpolicy.Policy, 0, 2)
	rt.applyPolicyFn = func(policy netpolicy.Policy) error {
		applied = append(applied, policy)
		return nil
	}
	rt.startVPNFn = func(_ context.Context, hash string) error {
		rt.mu.Lock()
		rt.configHash = hash
		rt.openvpnRunning = true
		rt.managementConnected = true
		rt.mu.Unlock()
		return nil
	}
	writeBundle(t, controlDir, policyBundle(t))
	if err := rt.applyOnce(context.Background()); err != nil {
		t.Fatalf("应用失败：%v", err)
	}

	path := filepath.Join(stateDir, connectPolicyFile)
	file, err := connectpolicy.Load(path)
	if err != nil {
		t.Fatalf("连接策略未写入：%v", err)
	}
	a, ok := connectpolicy.Lookup(file, "aa:bb", "")
	if !ok || !a.GlobalExit || len(a.AllowedInternal) != 1 || a.AllowedInternal[0] != "192.168.50.0/24" {
		t.Fatalf("A 的连接策略异常：%+v", a)
	}
	if a.FixedIP != "10.8.255.200" || a.VPNMask != "255.255.0.0" {
		t.Fatalf("A 的固定地址策略异常：%+v", a)
	}
	b, ok := connectpolicy.Lookup(file, "", "bob")
	if !ok || b.GlobalExit || len(b.AllowedInternal) != 0 {
		t.Fatalf("B 的连接策略异常：%+v", b)
	}

	rt.sessions["1"] = sessionInfo{profileID: "A", vpnIP: "10.8.0.2"}
	rt.sessions["2"] = sessionInfo{profileID: "B", vpnIP: "10.8.0.3"}
	if err := rt.refreshPolicy(); err != nil {
		t.Fatalf("重建会话策略失败：%v", err)
	}
	last := applied[len(applied)-1]
	if len(last.Sessions) != 2 {
		t.Fatalf("会话策略数量异常：%+v", last.Sessions)
	}
	for _, session := range last.Sessions {
		switch session.IP {
		case "10.8.0.2":
			if !session.GlobalExit || len(session.AllowedInternal) != 1 {
				t.Fatalf("A 的会话规则异常：%+v", session)
			}
		case "10.8.0.3":
			if session.GlobalExit || len(session.AllowedInternal) != 0 {
				t.Fatalf("B 的会话规则异常：%+v", session)
			}
		}
	}

	// 地址事件应更新会话并重建规则。
	rt.sessions["3"] = sessionInfo{profileID: "A"}
	rt.setSessionAddress("3", "10.8.0.4")
	last = applied[len(applied)-1]
	found := false
	for _, session := range last.Sessions {
		if session.IP == "10.8.0.4" && session.GlobalExit {
			found = true
		}
	}
	if !found {
		t.Fatalf("地址事件后缺少新会话规则：%+v", last.Sessions)
	}

	// 断线重连复用同一地址时，旧会话记录必须被移除，避免沿用旧策略。
	rt.sessions["old"] = sessionInfo{profileID: "A", vpnIP: "10.8.0.9"}
	rt.sessions["new"] = sessionInfo{profileID: "B"}
	rt.setSessionAddress("new", "10.8.0.9")
	if _, ok := rt.sessions["old"]; ok {
		t.Fatal("旧会话不应继续持有已被复用的地址")
	}
}

func sharedPolicyBundle(t *testing.T) []byte {
	t.Helper()
	var bundle agentproto.Bundle
	if err := json.Unmarshal(policyBundle(t), &bundle); err != nil {
		t.Fatalf("解析控制包失败：%v", err)
	}
	bundle.Policy.AllowClientNetworks = true
	bundle.Snapshot.Profiles[0].ClientNetworks = []string{"192.168.50.0/24"}
	bundle.Snapshot.Profiles[0].ClientLANAccess = "deny"
	bundle.Snapshot.Profiles[1].ClientLANAccess = "inherit"
	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("编码控制包失败：%v", err)
	}
	return data
}

func TestSharedNetworksRoutesAndPolicy(t *testing.T) {
	controlDir, stateDir := t.TempDir(), t.TempDir()
	rt := New(controlDir, stateDir, testLogger())
	rt.configDir = t.TempDir()
	rt.localNetsFn = func(string) []string { return nil }
	applied := make([]netpolicy.Policy, 0, 2)
	rt.applyPolicyFn = func(policy netpolicy.Policy) error {
		applied = append(applied, policy)
		return nil
	}
	var routeCalls []struct {
		networks []string
		viaTun   bool
		tun      string
	}
	rt.syncRoutesFn = func(networks []string, viaTun bool, tun string) error {
		routeCalls = append(routeCalls, struct {
			networks []string
			viaTun   bool
			tun      string
		}{append([]string(nil), networks...), viaTun, tun})
		return nil
	}
	rt.startVPNFn = func(_ context.Context, hash string) error {
		rt.mu.Lock()
		rt.configHash = hash
		rt.openvpnRunning = true
		rt.managementConnected = true
		rt.mu.Unlock()
		return nil
	}
	writeBundle(t, controlDir, sharedPolicyBundle(t))
	if err := rt.applyOnce(context.Background()); err != nil {
		t.Fatalf("应用失败：%v", err)
	}
	if len(routeCalls) == 0 || len(routeCalls[0].networks) != 1 || routeCalls[0].networks[0] != "192.168.50.0/24" {
		t.Fatalf("共享网段路由未同步：%+v", routeCalls)
	}
	if routeCalls[0].viaTun {
		t.Fatal("OpenVPN 启动前应使用 blackhole 路由")
	}
	if err := rt.refreshPolicy(); err != nil {
		t.Fatalf("重建策略失败：%v", err)
	}
	if last := routeCalls[len(routeCalls)-1]; !last.viaTun || last.tun != "tun0" {
		t.Fatalf("OpenVPN 运行后应把共享网段指向 tun：%+v", last)
	}

	file, err := connectpolicy.Load(filepath.Join(stateDir, connectPolicyFile))
	if err != nil {
		t.Fatalf("读取连接策略失败：%v", err)
	}
	a, ok := connectpolicy.Lookup(file, "aa:bb", "")
	if !ok || len(a.OwnClientNetworks) != 1 || a.OwnClientNetworks[0] != "192.168.50.0/24" {
		t.Fatalf("A 缺少 iroute 策略：%+v", a)
	}
	if len(a.AllowedInternal) != 0 {
		t.Fatalf("A 不应把自己的共享网段推给自己：%+v", a.AllowedInternal)
	}
	b, ok := connectpolicy.Lookup(file, "", "bob")
	if !ok || len(b.AllowedInternal) != 1 || b.AllowedInternal[0] != "192.168.50.0/24" {
		t.Fatalf("B 应获得共享网段：%+v", b)
	}
	last := applied[len(applied)-1]
	if len(last.SharedNetworks) != 1 || last.SharedNetworks[0] != "192.168.50.0/24" {
		t.Fatalf("nft 策略缺少共享网段：%+v", last.SharedNetworks)
	}
}
