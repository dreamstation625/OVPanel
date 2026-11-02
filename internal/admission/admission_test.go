package admission

import (
	"strings"
	"testing"
	"time"

	"github.com/dreamstation625/ovpanel/internal/auth"
)

func weakAuth(t *testing.T) {
	t.Helper()
	old := auth.DefaultParams
	auth.DefaultParams = auth.Params{
		Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 8, KeyLength: 16,
	}
	t.Cleanup(func() { auth.DefaultParams = old })
}

func hash(t *testing.T, password string) string {
	t.Helper()
	value, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("生成哈希失败：%v", err)
	}
	return value
}

func testSnapshot(t *testing.T) Snapshot {
	t.Helper()
	weakAuth(t)
	now := time.Now().UTC()
	future := now.Add(24 * time.Hour)
	return Snapshot{
		Users: []User{
			{ID: "u1", Status: "active"},
			{ID: "u2", Status: "active"},
			{ID: "u3", Status: "disabled"},
		},
		Profiles: []Profile{
			{
				ID: "A", UserID: "u1", AuthType: AuthCertificate, Status: "active",
				CertSerial: "0A1B", CertFingerprint: "aa:bb:cc", CertStatus: "active",
				CertExpiresAt: &future,
			},
			{
				ID: "B", UserID: "u1", AuthType: AuthCertificatePassword, Username: "bob",
				PasswordHash: hash(t, "bob-secret"), Status: "active",
				CertSerial: "0C0D", CertStatus: "active", CertExpiresAt: &future,
			},
			{
				ID: "C", UserID: "u2", AuthType: AuthPassword, Username: "carol",
				PasswordHash: hash(t, "carol-secret"), Status: "active",
			},
			{
				ID: "D", UserID: "u3", AuthType: AuthPassword, Username: "disabled",
				PasswordHash: hash(t, "disabled-secret"), Status: "active",
			},
		},
	}
}

func certIncoming(serial string) Incoming {
	return Incoming{CertPresent: true, CertVerified: true, CertSerial: serial}
}

func passwordIncoming(username, password string) Incoming {
	return Incoming{Username: username, Password: password}
}

func TestSerialMatchesOpenVPNFormats(t *testing.T) {
	const stored = "5FAA1D9924DD64A665000C418CD5C83A52225B2E"
	const decimal = "546147828280510867565033957221463861557700221742"
	if !SerialMatches(stored, "5f:aa:1d:99:24:dd:64:a6:65:00:0c:41:8c:d5:c8:3a:52:22:5b:2e") {
		t.Fatal("OpenVPN 冒号分组的十六进制序列号应能匹配")
	}
	if !SerialMatches(stored, decimal) {
		t.Fatal("十进制序列号应能匹配")
	}
	if !SerialMatches(stored, "0x5faa1d9924dd64a665000c418cd5c83a52225b2e") {
		t.Fatal("0x 前缀序列号应能匹配")
	}
	if SerialMatches(stored, "5f:aa:1d:99:24:dd:64:a6:65:00:0c:41:8c:d5:c8:3a:52:22:5b:2f") {
		t.Fatal("不同序列号不应匹配")
	}
}

func TestDecideAllowsThreeAuthTypes(t *testing.T) {
	snapshot := testSnapshot(t)
	now := time.Now().UTC()

	if decision := Decide(snapshot, certIncoming("2587"), now); !decision.Allow || decision.ProfileID != "A" {
		t.Fatalf("A 仅证书应允许（0A1B=2587）：%+v", decision)
	}

	b := certIncoming("0x0C0D")
	b.Username, b.Password = "bob", "bob-secret"
	if decision := Decide(snapshot, b, now); !decision.Allow || decision.ProfileID != "B" {
		t.Fatalf("B 证书+密码应允许：%+v", decision)
	}

	if decision := Decide(snapshot, passwordIncoming("carol", "carol-secret"), now); !decision.Allow || decision.ProfileID != "C" {
		t.Fatalf("C 仅密码应允许：%+v", decision)
	}
}

func TestDecideRejectsDowngradeAndMismatch(t *testing.T) {
	snapshot := testSnapshot(t)
	now := time.Now().UTC()

	cases := []struct {
		name     string
		incoming Incoming
		code     string
	}{
		{"匿名", Incoming{}, CodeNoCredentials},
		{"证书未校验", Incoming{CertPresent: true, CertSerial: "0A1B"}, CodeCertNotVerified},
		{"未知证书", certIncoming("FFFF"), CodeUnknownCert},
		{"A 额外密码", func() Incoming {
			in := certIncoming("0A1B")
			in.Username, in.Password = "bob", "bob-secret"
			return in
		}(), CodeCrossIdentityCredentials},
		{"B 缺密码", certIncoming("0C0D"), CodeMissingPassword},
		{"B 错误密码", func() Incoming {
			in := certIncoming("0C0D")
			in.Username, in.Password = "bob", "wrong"
			return in
		}(), CodeWrongPassword},
		{"B 错误用户名", func() Incoming {
			in := certIncoming("0C0D")
			in.Username, in.Password = "carol", "carol-secret"
			return in
		}(), CodeWrongUsername},
		{"C 用 B 账号省略证书", passwordIncoming("bob", "bob-secret"), CodeMissingCert},
		{"C 错误密码", passwordIncoming("carol", "wrong"), CodeWrongPassword},
		{"未知账号", passwordIncoming("nobody", "whatever"), CodeUnknownAccount},
		{"C 证书混搭", func() Incoming {
			in := certIncoming("0A1B")
			in.Username, in.Password = "carol", "carol-secret"
			return in
		}(), CodeCrossIdentityCredentials},
		{"用户停用", passwordIncoming("disabled", "disabled-secret"), CodeUserDisabled},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			decision := Decide(snapshot, test.incoming, now)
			if decision.Allow || decision.Code != test.code {
				t.Fatalf("期望拒绝 %s，实际 %+v", test.code, decision)
			}
		})
	}
}

func TestDecideRejectsExpiredAndDisabled(t *testing.T) {
	snapshot := testSnapshot(t)
	past := time.Now().Add(-time.Hour)
	for i := range snapshot.Profiles {
		if snapshot.Profiles[i].ID == "B" {
			snapshot.Profiles[i].Status = "disabled"
		}
		if snapshot.Profiles[i].ID == "C" {
			snapshot.Profiles[i].ExpiresAt = &past
		}
		if snapshot.Profiles[i].ID == "A" {
			snapshot.Profiles[i].CertExpiresAt = &past
		}
	}
	now := time.Now().UTC()

	b := certIncoming("0C0D")
	b.Username, b.Password = "bob", "bob-secret"
	if decision := Decide(snapshot, b, now); decision.Allow || decision.Code != CodeProfileDisabled {
		t.Fatalf("停用配置应拒绝：%+v", decision)
	}
	if decision := Decide(snapshot, passwordIncoming("carol", "carol-secret"), now); decision.Allow || decision.Code != CodeProfileExpired {
		t.Fatalf("到期配置应拒绝：%+v", decision)
	}
	if decision := Decide(snapshot, certIncoming("0A1B"), now); decision.Allow || decision.Code != CodeCertificateExpired {
		t.Fatalf("过期证书应拒绝：%+v", decision)
	}
}

func TestSnapshotValidation(t *testing.T) {
	snapshot := testSnapshot(t)
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("合法快照被拒绝：%v", err)
	}

	dupUsername := snapshot
	dupUsername.Profiles = append(dupUsername.Profiles, Profile{
		ID: "X", UserID: "u1", AuthType: AuthPassword,
		Username: "CAROL", PasswordHash: "hash", Status: "active",
	})
	if err := dupUsername.Validate(); err == nil || !strings.Contains(err.Error(), "登录名") {
		t.Fatalf("重复登录名应被拒绝：%v", err)
	}

	dupSerial := snapshot
	dupSerial.Profiles = append(dupSerial.Profiles, Profile{
		ID: "Y", UserID: "u1", AuthType: AuthCertificate,
		CertSerial: "0A1B", CertStatus: "active", Status: "active",
	})
	if err := dupSerial.Validate(); err == nil || !strings.Contains(err.Error(), "序列号") {
		t.Fatalf("重复证书序列号应被拒绝：%v", err)
	}
}

func TestSnapshotAcceptsPublicTargets(t *testing.T) {
	snapshot := testSnapshot(t)
	snapshot.Profiles[0].RouteMode = "split"
	snapshot.Profiles[0].AccessMode = "allow"
	snapshot.Profiles[0].AllowedInternal = []string{"1.1.1.1", "203.0.113.5/24"}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("公网目标应被准入快照接受：%v", err)
	}

	snapshot.Profiles[0].AllowedInternal = []string{"127.0.0.1"}
	if err := snapshot.Validate(); err == nil {
		t.Fatal("回环目标应被准入快照拒绝")
	}
}

func TestSnapshotValidatesClientNetworks(t *testing.T) {
	snapshot := testSnapshot(t)
	snapshot.Profiles[0].ClientNetworks = []string{"192.168.50.0/24"}
	snapshot.Profiles[1].ClientNetworks = []string{"192.168.60.0/24"}
	snapshot.Profiles[1].ClientLANAccess = "allow"
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("合法共享内网被拒绝：%v", err)
	}

	snapshot.Profiles[1].ClientNetworks = []string{"192.168.50.128/25"}
	if err := snapshot.Validate(); err == nil || !strings.Contains(err.Error(), "共享内网") {
		t.Fatalf("重叠共享内网应被拒绝：%v", err)
	}

	snapshot.Profiles[1].ClientNetworks = []string{"8.8.8.0/24"}
	if err := snapshot.Validate(); err == nil || !strings.Contains(err.Error(), "私网") {
		t.Fatalf("公网共享内网应被拒绝：%v", err)
	}

	snapshot.Profiles[1].ClientNetworks = nil
	snapshot.Profiles[1].ClientLANAccess = "unknown"
	if err := snapshot.Validate(); err == nil || !strings.Contains(err.Error(), "客户端内网权限") {
		t.Fatalf("未知客户端内网权限应被拒绝：%v", err)
	}
}
