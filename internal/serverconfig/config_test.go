package serverconfig

import (
	"strings"
	"testing"
)

func TestDefaultsRender(t *testing.T) {
	text, hash, err := Render(Defaults())
	if err != nil {
		t.Fatalf("默认配置渲染失败：%v", err)
	}
	if len(hash) != 64 {
		t.Fatalf("哈希长度异常：%d", len(hash))
	}
	for _, want := range []string{
		"port 1194", "proto udp", "server 10.8.0.0 255.255.0.0",
		"management /run/ovpanel/mgmt.sock unix", "management-client-auth",
		"verify-client-cert optional", "auth-user-pass-optional", "duplicate-cn",
		"crl-verify /etc/openvpn/ovpanel/crl.pem",
		"push \"block-ipv6\"", "keepalive 10 60",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("渲染结果缺少 %q：\n%s", want, text)
		}
	}
	if strings.Contains(text, "client-to-client") {
		t.Fatal("默认不应启用 client-to-client")
	}
	if strings.Contains(text, "redirect-gateway") {
		t.Fatal("默认分流不应推送全局出口")
	}
}

func TestGlobalAndDNSRender(t *testing.T) {
	cfg := Defaults()
	cfg.RouteMode = RouteGlobal
	cfg.DNS = []string{"1.1.1.1", "8.8.8.8"}
	cfg.AllowInterClient = true
	text, _, err := Render(cfg)
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	for _, want := range []string{
		"push \"redirect-gateway def1\"",
		"push \"dhcp-option DNS 1.1.1.1\"",
		"push \"dhcp-option DNS 8.8.8.8\"",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺少 %q", want)
		}
	}
	if strings.Contains(text, "client-to-client") {
		t.Fatal("互访不应通过 OpenVPN client-to-client 实现")
	}
}

func TestSplitRoutes(t *testing.T) {
	cfg := Defaults()
	cfg.AllowedInternal = []string{"192.168.99.0/24"}
	text, _, err := Render(cfg)
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if !strings.Contains(text, "push \"route 192.168.99.0 255.255.255.0\"") {
		t.Fatalf("缺少内网路由：\n%s", text)
	}
}

func TestValidateRejectsBadInput(t *testing.T) {
	cases := []Config{
		func() Config { c := Defaults(); c.VPNNetwork = "10.8.0.1/16"; return c }(),
		func() Config { c := Defaults(); c.VPNNetwork = "10.8.0.0/32"; return c }(),
		func() Config { c := Defaults(); c.Protocol = "icmp"; return c }(),
		func() Config { c := Defaults(); c.Port = 0; return c }(),
		func() Config { c := Defaults(); c.DNS = []string{"8.8.8.8:53"}; return c }(),
		func() Config { c := Defaults(); c.AllowedInternal = []string{"0.0.0.0/0"}; return c }(),
		func() Config { c := Defaults(); c.AllowedInternal = []string{"10.8.0.0/24"}; return c }(),
	}
	for _, cfg := range cases {
		if _, err := Prepare(cfg); err == nil {
			t.Fatalf("非法配置应被拒绝：%+v", cfg)
		}
	}
}

func TestExtraDirectiveCompatibility(t *testing.T) {
	cfg := Defaults()
	cfg.ExtraDirectives = []string{"max-routes-per-client 1000", "mssfix 1400", "tun-mtu 1500"}
	text, _, err := Render(cfg)
	if err != nil {
		t.Fatalf("合法额外指令被拒绝：%v", err)
	}
	for _, want := range []string{"max-routes-per-client 1000", "mssfix 1400", "tun-mtu 1500"} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺少额外指令 %q", want)
		}
	}

	for _, line := range []string{
		"management-client-auth",
		"push \"route 10.0.0.0 255.0.0.0\"",
		"plugin /tmp/x.so",
		"script-security 3",
		"unknown-directive 1",
		"tun-mtu 99999",
		"mssfix 1400 extra",
		"mssfix 'unterminated",
	} {
		cfg := Defaults()
		cfg.ExtraDirectives = []string{line}
		if _, err := Prepare(cfg); err == nil {
			t.Fatalf("危险/非法额外指令应被拒绝：%q", line)
		}
	}
}

func TestSupportedDirectivesIsStable(t *testing.T) {
	names := SupportedDirectives()
	if len(names) < 5 {
		t.Fatalf("兼容清单过短：%v", names)
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("兼容清单未排序：%v", names)
		}
	}
}
