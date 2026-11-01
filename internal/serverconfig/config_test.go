package serverconfig

import (
	"reflect"
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
		"port 1194", "proto udp", "mode server", "tls-server",
		"ifconfig 10.8.0.1 255.255.0.0",
		"ifconfig-pool 10.8.0.2 10.8.255.191 255.255.0.0",
		"push \"topology subnet\"", "push \"route-gateway 10.8.0.1\"",
		"management /run/ovpanel/mgmt.sock unix", "management-client-auth",
		"verify-client-cert optional", "auth-user-pass-optional", "duplicate-cn",
		"crl-verify /etc/openvpn/ovpanel/crl.pem",
		"script-security 2", "client-connect /usr/local/bin/vpn-connect",
		"push \"dhcp-option DNS 119.29.29.29\"",
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
		t.Fatal("路由必须逐连接下发，服务端不能全局推送")
	}
}

func TestTCPRenderUsesServerProtocol(t *testing.T) {
	cfg := Defaults()
	cfg.Protocol = "tcp"
	cfg.Port = 21194
	text, _, err := Render(cfg)
	if err != nil {
		t.Fatalf("TCP 渲染失败：%v", err)
	}
	if !strings.Contains(text, "proto tcp-server\n") {
		t.Fatalf("TCP 服务端配置应使用 tcp-server：\n%s", text)
	}
	if strings.Contains(text, "proto tcp\n") {
		t.Fatal("不能输出歧义的 proto tcp")
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
		"push \"dhcp-option DNS 1.1.1.1\"",
		"push \"dhcp-option DNS 8.8.8.8\"",
		"client-connect /usr/local/bin/vpn-connect",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺少 %q", want)
		}
	}
	if strings.Contains(text, "redirect-gateway") || strings.Contains(text, "push \"route ") {
		t.Fatal("全局出口与目标网段路由必须逐连接下发")
	}
	if strings.Contains(text, "client-to-client") {
		t.Fatal("互访不应通过 OpenVPN client-to-client 实现")
	}
}

func TestSplitRoutesNotPushedGlobally(t *testing.T) {
	cfg := Defaults()
	cfg.AllowedInternal = []string{"192.168.99.0/24"}
	text, _, err := Render(cfg)
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if strings.Contains(text, "push \"route 192.168.99.0 255.255.255.0\"") {
		t.Fatalf("目标网段路由不能在服务端全局推送：\n%s", text)
	}
	if !strings.Contains(text, "client-connect /usr/local/bin/vpn-connect") {
		t.Fatal("缺少按连接下发路由的 client-connect")
	}
}

func TestPrepareNormalizesTargets(t *testing.T) {
	cfg := Defaults()
	cfg.AllowedInternal = []string{"192.168.99.5/24", "1.1.1.1", "203.0.113.5/24"}
	prepared, err := Prepare(cfg)
	if err != nil {
		t.Fatalf("目标网段规范化失败：%v", err)
	}
	want := []string{"192.168.99.0/24", "1.1.1.1/32", "203.0.113.0/24"}
	if !reflect.DeepEqual(prepared.AllowedInternal, want) {
		t.Fatalf("目标网段未规范化：got=%v want=%v", prepared.AllowedInternal, want)
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
		func() Config { c := Defaults(); c.AllowedInternal = []string{"127.0.0.1"}; return c }(),
		func() Config { c := Defaults(); c.AllowedInternal = []string{"169.254.1.1"}; return c }(),
		func() Config { c := Defaults(); c.AllowedInternal = []string{"224.0.0.1"}; return c }(),
		func() Config { c := Defaults(); c.AllowedInternal = []string{"2001:db8::/32"}; return c }(),
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
