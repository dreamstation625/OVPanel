package netpolicy

import (
	"strings"
	"testing"
)

func TestRenderPolicyOrderAndRules(t *testing.T) {
	script, err := Render(Policy{
		VPNPool:          "10.8.0.0/16",
		TunDevice:        "tun0",
		AllowInterClient: false,
		BlockIPv6:        true,
		ProtectTargets:   []string{"172.18.0.0/16"},
		Sessions: []Session{
			{IP: "10.8.0.2", AllowedInternal: []string{"192.168.99.0/24"}},
			{IP: "10.8.0.3", GlobalExit: true},
		},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	for _, want := range []string{
		"table inet ovpanel",
		"ip daddr 169.254.169.254/32 counter drop",
		"ip daddr 172.18.0.0/16 counter drop",
		"meta nfproto ipv6 counter drop",
		"ip saddr 10.8.0.0/16 ip daddr 10.8.0.0/16 counter drop",
		"ip saddr 10.8.0.2 ip daddr 192.168.99.0/24 counter accept",
		"ip saddr 10.8.0.0/16 ip daddr 10.0.0.0/8 counter drop",
		"ip saddr 10.8.0.3 counter accept",
		"ip saddr 10.8.0.0/16 counter drop",
		"ct state established,related counter accept",
		"masquerade",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("缺少规则 %q：\n%s", want, script)
		}
	}
	if strings.Contains(script, "ip saddr 10.8.0.0/16 counter accept") {
		t.Fatal("不应存在整个地址池的全局放行")
	}
	allowIndex := strings.Index(script, "ip saddr 10.8.0.2 ip daddr 192.168.99.0/24 counter accept")
	privateIndex := strings.Index(script, "ip saddr 10.8.0.0/16 ip daddr 10.0.0.0/8 counter drop")
	globalIndex := strings.Index(script, "ip saddr 10.8.0.3 counter accept")
	defaultIndex := strings.Index(script, "ip saddr 10.8.0.0/16 counter drop")
	establishedIndex := strings.Index(script, "ct state established,related counter accept")
	if allowIndex > privateIndex {
		t.Fatal("授权内网必须排在私有网段拒绝之前")
	}
	if globalIndex > defaultIndex {
		t.Fatal("全局出口会话必须在默认拒绝之前放行")
	}
	if defaultIndex > establishedIndex {
		t.Fatal("默认拒绝必须在 established 放行之前")
	}
}

func TestRenderGlobalAndInterClient(t *testing.T) {
	script, err := Render(Policy{
		VPNPool:          "10.8.0.0/16",
		AllowInterClient: true,
		BlockIPv6:        false,
		Sessions:         []Session{{IP: "10.8.0.2", GlobalExit: true}},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if !strings.Contains(script, "ip saddr 10.8.0.0/16 ip daddr 10.8.0.0/16 counter accept") {
		t.Fatal("互访开启规则缺失")
	}
	if !strings.Contains(script, "ip saddr 10.8.0.2 counter accept") {
		t.Fatal("全局出口会话规则缺失")
	}
	if strings.Contains(script, "meta nfproto ipv6") {
		t.Fatal("关闭 IPv6 阻断时不应出现 IPv6 drop")
	}
}

func TestRenderRebuildsOnlyOwnTable(t *testing.T) {
	script, err := Render(Policy{VPNPool: "10.8.0.0/16"})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	prefix := "# OVPanel 网络策略：仅操作项目专用表\nadd table inet ovpanel\nflush table inet ovpanel\n"
	if !strings.HasPrefix(script, prefix) {
		t.Fatalf("应用前必须先重建专用表，避免规则重复追加：\n%s", script)
	}
	if strings.Contains(script, "delete table") {
		t.Fatal("不应删除其他表或使用 delete table")
	}
}

func TestRejectsBadCIDRAndSession(t *testing.T) {
	if _, err := Render(Policy{VPNPool: "bad"}); err == nil {
		t.Fatal("非法地址池应被拒绝")
	}
	if _, err := Render(Policy{
		VPNPool:  "10.8.0.0/16",
		Sessions: []Session{{IP: "10.8.0.2", AllowedInternal: []string{"0.0.0.0/0.0.0.0"}}},
	}); err == nil {
		t.Fatal("非法内网网段应被拒绝")
	}
	if _, err := Render(Policy{
		VPNPool:  "10.8.0.0/16",
		Sessions: []Session{{IP: "192.168.1.2"}},
	}); err == nil {
		t.Fatal("地址池外的会话地址应被拒绝")
	}
	if _, err := Render(Policy{
		VPNPool: "10.8.0.0/16",
		Sessions: []Session{
			{IP: "10.8.0.2"},
			{IP: "10.8.0.2"},
		},
	}); err == nil {
		t.Fatal("重复会话地址应被拒绝")
	}
}
