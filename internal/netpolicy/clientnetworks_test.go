package netpolicy

import (
	"strings"
	"testing"
)

func TestNormalizeClientNetworksPrivateOnly(t *testing.T) {
	got, err := NormalizeClientNetworks([]string{
		"192.168.50.5/24", "10.1.2.3", "172.16.0.0/12", "100.64.0.0/10",
	})
	if err != nil {
		t.Fatalf("规范化失败：%v", err)
	}
	want := []string{"192.168.50.0/24", "10.1.2.3/32", "172.16.0.0/12", "100.64.0.0/10"}
	if len(got) != len(want) {
		t.Fatalf("共享内网数量异常：%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("共享内网未规范化：got=%v want=%v", got, want)
		}
	}
	for _, bad := range []string{"8.8.8.8", "198.18.0.0/15", "127.0.0.1", "2001:db8::/32", "10.0.0.0/7"} {
		if _, err := NormalizeClientNetworks([]string{bad}); err == nil {
			t.Fatalf("应拒绝非私网/非法网段：%s", bad)
		}
	}
}

func TestPrepareClientNetworksRejectsVPNOverlap(t *testing.T) {
	for _, cidr := range []string{"10.8.0.0/16", "10.8.128.0/17", "10.0.0.0/8"} {
		if _, err := PrepareClientNetworks("10.8.0.0/16", []string{cidr}); err == nil {
			t.Fatalf("与 VPN 池重叠应被拒绝：%s", cidr)
		}
	}
}

func TestNetworksOverlap(t *testing.T) {
	if !NetworksOverlap("192.168.0.0/16", "192.168.50.0/24") {
		t.Fatal("包含关系应判定为重叠")
	}
	if NetworksOverlap("192.168.1.0/24", "192.168.2.0/24") {
		t.Fatal("不同网段不应判定为重叠")
	}
}

func TestRenderSharedNetworks(t *testing.T) {
	script, err := Render(Policy{
		VPNPool:        "10.8.0.0/16",
		SharedNetworks: []string{"192.168.50.0/24"},
		Sessions:       []Session{{IP: "10.8.0.2"}},
	})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	for _, want := range []string{
		"set shared {",
		"elements = { 192.168.50.0/24 }",
		"ip saddr @shared ct state new counter drop",
		"ip daddr @shared ip saddr != 10.8.0.0/16 ct state new counter drop",
		"ip saddr 10.8.0.0/16 ip daddr != 10.8.0.0/16 ip daddr != @shared counter masquerade",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("缺少共享内网规则 %q：\n%s", want, script)
		}
	}
}

func TestRenderWithoutSharedKeepsMasquerade(t *testing.T) {
	script, err := Render(Policy{VPNPool: "10.8.0.0/16"})
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if strings.Contains(script, "set shared") {
		t.Fatal("没有共享内网时不应生成 set")
	}
	if !strings.Contains(script, "ip saddr 10.8.0.0/16 ip daddr != 10.8.0.0/16 counter masquerade") {
		t.Fatal("缺少原有 SNAT 规则")
	}
}
