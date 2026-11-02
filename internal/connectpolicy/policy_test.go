package connectpolicy

import (
	"path/filepath"
	"testing"

	"github.com/dreamstation625/ovpanel/internal/admission"
)

func testSnapshot() admission.Snapshot {
	return admission.Snapshot{
		Users: []admission.User{{ID: "u1", Status: "active"}},
		Profiles: []admission.Profile{
			{
				ID: "A", UserID: "u1", AuthType: admission.AuthCertificate,
				Status: "active", CertFingerprint: "AA:BB:CC",
				RouteMode: "global", AccessMode: "allow",
				AllowedInternal: []string{"192.168.50.0/24"},
			},
			{
				ID: "B", UserID: "u1", AuthType: admission.AuthCertificatePassword,
				Status: "active", Username: "bob", CertFingerprint: "dd:ee:ff",
				RouteMode: "inherit", AccessMode: "deny",
			},
			{
				ID: "C", UserID: "u1", AuthType: admission.AuthPassword,
				Status: "active", Username: "carol",
				RouteMode: "split", AccessMode: "inherit",
			},
			{
				// 证书已吊销且没有登录名：无法通过准入，生成策略时应跳过。
				ID: "D", UserID: "u1", AuthType: admission.AuthCertificate,
				Status: "active",
			},
		},
	}
}

func TestBuildAndLookupEffectivePolicies(t *testing.T) {
	server := Policy{GlobalExit: true, AllowedInternal: []string{"10.0.0.0/8"}}
	file, err := Build(testSnapshot(), server)
	if err != nil {
		t.Fatalf("生成策略失败：%v", err)
	}

	a, ok := Lookup(file, "aa:bb:cc", "")
	if !ok || !a.GlobalExit || len(a.AllowedInternal) != 1 || a.AllowedInternal[0] != "192.168.50.0/24" {
		t.Fatalf("A 的有效策略异常：%+v ok=%v", a, ok)
	}
	b, ok := Lookup(file, "DD:EE:FF", "bob")
	if !ok || !b.GlobalExit || len(b.AllowedInternal) != 0 {
		t.Fatalf("B 应继承全局出口并禁止私有网段：%+v", b)
	}
	c, ok := Lookup(file, "", "carol")
	if !ok || c.GlobalExit || len(c.AllowedInternal) != 1 || c.AllowedInternal[0] != "10.0.0.0/8" {
		t.Fatalf("C 应仅访问继承的目标网段：%+v", c)
	}
	if _, ok := Lookup(file, "00:00", "unknown"); ok {
		t.Fatal("未知身份不应匹配策略")
	}
}

func TestClientLinesWithFixedIP(t *testing.T) {
	lines, err := ClientLines(Policy{
		FixedIP: "10.8.255.200", VPNMask: "255.255.0.0", GlobalExit: true,
	})
	if err != nil {
		t.Fatalf("生成固定地址配置失败：%v", err)
	}
	if len(lines) != 2 || lines[0] != "ifconfig-push 10.8.255.200 255.255.0.0" ||
		lines[1] != `push "redirect-gateway def1 bypass-dhcp"` {
		t.Fatalf("固定地址配置异常：%v", lines)
	}
	if _, err := ClientLines(Policy{FixedIP: "10.8.255.200"}); err == nil {
		t.Fatal("缺少掩码应被拒绝")
	}
}

func TestPushLines(t *testing.T) {
	lines, err := PushLines(Policy{GlobalExit: true})
	if err != nil || len(lines) != 1 || lines[0] != `push "redirect-gateway def1 bypass-dhcp"` {
		t.Fatalf("全局出口推送异常：%v %v", lines, err)
	}
	lines, err = PushLines(Policy{AllowedInternal: []string{"10.0.16.0/22", "192.168.99.0/24"}})
	if err != nil {
		t.Fatalf("生成路由失败：%v", err)
	}
	if len(lines) != 2 || lines[0] != `push "route 10.0.16.0 255.255.252.0"` ||
		lines[1] != `push "route 192.168.99.0 255.255.255.0"` {
		t.Fatalf("目标网段路由推送异常：%v", lines)
	}
	lines, err = PushLines(Policy{AllowedInternal: []string{"1.1.1.1", "203.0.113.5/24"}})
	if err != nil {
		t.Fatalf("生成公网路由失败：%v", err)
	}
	if len(lines) != 2 || lines[0] != `push "route 1.1.1.1 255.255.255.255"` ||
		lines[1] != `push "route 203.0.113.0 255.255.255.0"` {
		t.Fatalf("公网目标路由推送异常：%v", lines)
	}
	for _, bad := range []string{"bad", "127.0.0.1", "2001:db8::/32"} {
		if _, err := PushLines(Policy{AllowedInternal: []string{bad}}); err == nil {
			t.Fatalf("非法目标网段应被拒绝：%s", bad)
		}
	}
}

func TestWriteAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connect-policy.json")
	file, err := Build(testSnapshot(), Policy{GlobalExit: false})
	if err != nil {
		t.Fatalf("生成策略失败：%v", err)
	}
	if err := Write(path, file); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if _, ok := Lookup(loaded, "aa:bb:cc", ""); !ok {
		t.Fatal("读取后应能匹配证书身份")
	}
}

func TestEffectiveWithSharedNetworks(t *testing.T) {
	snapshot := admission.Snapshot{
		Users: []admission.User{{ID: "u1", Status: "active"}},
		Profiles: []admission.Profile{
			{
				ID: "A", UserID: "u1", Status: "active",
				ClientNetworks: []string{"192.168.50.0/24"},
				RouteMode:      "split", AccessMode: "allow",
				AllowedInternal: []string{"192.168.50.0/24"},
			},
			{ID: "B", UserID: "u1", Status: "active", ClientLANAccess: "inherit"},
			{ID: "C", UserID: "u1", Status: "active", ClientLANAccess: "deny"},
			{ID: "D", UserID: "u1", Status: "disabled", ClientNetworks: []string{"192.168.60.0/24"}, ClientLANAccess: "deny"},
		},
	}
	advertised, err := AdvertisedNetworks(snapshot)
	if err != nil {
		t.Fatalf("汇总共享内网失败：%v", err)
	}
	if len(advertised) != 1 || len(advertised["A"]) != 1 {
		t.Fatalf("只应汇总启用配置的共享内网：%+v", advertised)
	}
	server := Policy{AllowClientNetworks: true}
	a, err := EffectiveWithShared(snapshot.Profiles[0], server, advertised)
	if err != nil {
		t.Fatalf("计算 A 策略失败：%v", err)
	}
	if len(a.OwnClientNetworks) != 1 || a.OwnClientNetworks[0] != "192.168.50.0/24" {
		t.Fatalf("A 应记录自己的共享内网：%+v", a)
	}
	if len(a.AllowedInternal) != 0 {
		t.Fatalf("A 不应把自己的共享内网推给自己：%+v", a.AllowedInternal)
	}
	b, err := EffectiveWithShared(snapshot.Profiles[1], server, advertised)
	if err != nil {
		t.Fatalf("计算 B 策略失败：%v", err)
	}
	if len(b.AllowedInternal) != 1 || b.AllowedInternal[0] != "192.168.50.0/24" {
		t.Fatalf("B 应继承共享内网：%+v", b.AllowedInternal)
	}
	c, err := EffectiveWithShared(snapshot.Profiles[2], server, advertised)
	if err != nil {
		t.Fatalf("计算 C 策略失败：%v", err)
	}
	if len(c.AllowedInternal) != 0 {
		t.Fatalf("C 禁止访问共享内网：%+v", c.AllowedInternal)
	}
	d, err := EffectiveWithShared(snapshot.Profiles[3], server, advertised)
	if err != nil {
		t.Fatalf("计算 D 策略失败：%v", err)
	}
	if len(d.AllowedInternal) != 0 {
		t.Fatalf("停用配置不应获得共享内网：%+v", d.AllowedInternal)
	}

	// 服务端默认关闭时，继承者不应获得共享内网；显式允许仍可访问。
	off := Policy{}
	b2, _ := EffectiveWithShared(snapshot.Profiles[1], off, advertised)
	if len(b2.AllowedInternal) != 0 {
		t.Fatalf("服务端关闭时继承者不应获得共享内网：%+v", b2.AllowedInternal)
	}
	allow := snapshot.Profiles[1]
	allow.ClientLANAccess = "allow"
	b3, _ := EffectiveWithShared(allow, off, advertised)
	if len(b3.AllowedInternal) != 1 {
		t.Fatalf("显式允许应获得共享内网：%+v", b3.AllowedInternal)
	}
}

func TestClientLinesWithSharedNetworks(t *testing.T) {
	lines, err := ClientLines(Policy{
		OwnClientNetworks: []string{"192.168.50.0/24"},
		AllowedInternal:   []string{"10.0.16.0/22"},
	})
	if err != nil {
		t.Fatalf("生成共享内网配置失败：%v", err)
	}
	if len(lines) != 2 ||
		lines[0] != "iroute 192.168.50.0 255.255.255.0" ||
		lines[1] != `push "route 10.0.16.0 255.255.252.0"` {
		t.Fatalf("共享内网 iroute 异常：%v", lines)
	}
}

func TestAdvertisedNetworksRejectsInvalid(t *testing.T) {
	snapshot := admission.Snapshot{
		Users: []admission.User{{ID: "u1", Status: "active"}},
		Profiles: []admission.Profile{{
			ID: "A", UserID: "u1", Status: "active",
			ClientNetworks: []string{"8.8.8.0/24"},
		}},
	}
	if _, err := AdvertisedNetworks(snapshot); err == nil {
		t.Fatal("公网共享内网应被拒绝")
	}
}
