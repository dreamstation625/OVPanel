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
		"ifconfig-pool 10.8.64.0 10.8.191.255 255.255.0.0",
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

func TestPrepareAcceptsPrivateSlash24Network(t *testing.T) {
	cfg := Defaults()
	cfg.VPNNetwork = "192.168.99.0/24"
	prepared, err := Prepare(cfg)
	if err != nil {
		t.Fatalf("192.168.99.0/24 应可作为 VPN 网段：%v", err)
	}
	plan, err := PlanAddresses(prepared.VPNNetwork)
	if err != nil {
		t.Fatalf("计算 /24 地址规划失败：%v", err)
	}
	if plan.Server != "192.168.99.1" ||
		plan.PoolStart != "192.168.99.64" ||
		plan.PoolEnd != "192.168.99.191" ||
		plan.FixedStart != "192.168.99.2" ||
		plan.FixedEnd != "192.168.99.254" {
		t.Fatalf("地址规划异常：%+v", plan)
	}
	text, _, err := Render(prepared)
	if err != nil {
		t.Fatalf("/24 配置渲染失败：%v", err)
	}
	for _, want := range []string{
		"ifconfig 192.168.99.1 255.255.255.0",
		"ifconfig-pool 192.168.99.64 192.168.99.191 255.255.255.0",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("渲染结果缺少 %q：\n%s", want, text)
		}
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

func TestCryptoAndLimitsRender(t *testing.T) {
	cfg := Defaults()
	cfg.DataCiphers = []string{"CHACHA20-POLY1305", "AES-128-GCM"}
	cfg.AuthDigest = "sha512"
	cfg.TLSMinVersion = "1.3"
	cfg.KeepaliveInterval = 20
	cfg.KeepaliveTimeout = 120
	cfg.MaxClients = 50
	prepared, err := Prepare(cfg)
	if err != nil {
		t.Fatalf("加密配置被拒绝：%v", err)
	}
	if !reflect.DeepEqual(prepared.DataCiphers, []string{"AES-128-GCM", "CHACHA20-POLY1305"}) {
		t.Fatalf("加密算法顺序异常：%v", prepared.DataCiphers)
	}
	if prepared.AuthDigest != "SHA512" {
		t.Fatalf("认证算法未规范化：%q", prepared.AuthDigest)
	}
	text, _, err := Render(cfg)
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	for _, want := range []string{
		"data-ciphers AES-128-GCM:CHACHA20-POLY1305",
		"auth SHA512",
		"tls-version-min 1.3",
		"keepalive 20 120",
		"max-clients 50",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺少 %q：\n%s", want, text)
		}
	}
}

func TestCryptoAndLimitsValidation(t *testing.T) {
	cases := []Config{
		func() Config { c := Defaults(); c.DataCiphers = []string{}; return c }(),
		func() Config { c := Defaults(); c.DataCiphers = []string{"RC4"}; return c }(),
		func() Config { c := Defaults(); c.AuthDigest = "MD5"; return c }(),
		func() Config { c := Defaults(); c.TLSMinVersion = "1.1"; return c }(),
		func() Config { c := Defaults(); c.KeepaliveInterval = -1; return c }(),
		func() Config { c := Defaults(); c.KeepaliveTimeout = 90000; return c }(),
		func() Config { c := Defaults(); c.MaxClients = 10001; return c }(),
	}
	for _, cfg := range cases {
		if _, err := Prepare(cfg); err == nil {
			t.Fatalf("非法加密/连接配置应被拒绝：%+v", cfg)
		}
	}
}

func TestDefaultCryptoValues(t *testing.T) {
	input := Defaults()
	input.DataCiphers = nil
	input.AuthDigest = ""
	input.TLSMinVersion = ""
	input.KeepaliveInterval = 0
	input.KeepaliveTimeout = 0
	cfg, err := Prepare(input)
	if err != nil {
		t.Fatalf("旧配置补齐默认值失败：%v", err)
	}
	if !reflect.DeepEqual(cfg.DataCiphers, []string{"AES-256-GCM", "AES-128-GCM"}) ||
		cfg.AuthDigest != "SHA256" || cfg.TLSMinVersion != "1.2" ||
		cfg.KeepaliveInterval != 10 || cfg.KeepaliveTimeout != 60 ||
		cfg.MaxClients != 0 {
		t.Fatalf("默认加密/连接值异常：%+v", cfg)
	}
}

func TestCBCCompatibilityCiphers(t *testing.T) {
	cfg := Defaults()
	cfg.DataCiphers = []string{"AES-128-CBC", "AES-256-CBC"}
	prepared, err := Prepare(cfg)
	if err != nil {
		t.Fatalf("CBC 兼容算法被拒绝：%v", err)
	}
	want := []string{"AES-256-CBC", "AES-128-CBC"}
	if !reflect.DeepEqual(prepared.DataCiphers, want) {
		t.Fatalf("CBC 顺序异常：got=%v want=%v", prepared.DataCiphers, want)
	}
	text, _, err := Render(cfg)
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	if !strings.Contains(text, "data-ciphers AES-256-CBC:AES-128-CBC") {
		t.Fatalf("CBC 配置未渲染：\n%s", text)
	}
}
