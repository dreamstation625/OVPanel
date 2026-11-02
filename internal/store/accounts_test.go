package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func testUser(id string) User {
	now := time.Now().UTC()
	return User{
		ID:          id,
		DisplayName: "用户 " + id,
		Notes:       "备注",
		Tags:        []string{"家庭"},
		Status:      StatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func testProfile(id, userID, authType, username string) Profile {
	now := time.Now().UTC()
	p := Profile{
		ID:        id,
		UserID:    userID,
		Name:      "配置 " + id,
		AuthType:  authType,
		Username:  username,
		Status:    StatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if authType != AuthCertificate {
		p.HasPassword = true
		p.PasswordHash = "hash-" + id
	}
	return p
}

func testCert(id, profileID string, serial int) CertificateRecord {
	now := time.Now().UTC()
	return CertificateRecord{
		Certificate: Certificate{
			ID:           id,
			ProfileID:    profileID,
			SerialNumber: "S" + string(rune('0'+serial)),
			Fingerprint:  "fp-" + id,
			CommonName:   "ovp-" + profileID,
			Status:       CertificateActive,
			IssuedAt:     now,
			ExpiresAt:    now.Add(24 * time.Hour),
		},
		CertPEM:       "CERT-" + id,
		KeyCiphertext: []byte("key-" + id),
	}
}

func TestCreateUserWithProfileAndCounts(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	user := testUser("u1")
	profile := testProfile("p1", "u1", AuthCertificate, "")
	cert := testCert("c1", "p1", 1)
	if err := s.CreateUserWithProfile(ctx, user, profile, &cert); err != nil {
		t.Fatalf("创建用户与配置失败：%v", err)
	}

	got, ok, err := s.User(ctx, "u1")
	if err != nil || !ok {
		t.Fatalf("读取用户失败：%v ok=%v", err, ok)
	}
	if got.DisplayName != user.DisplayName || len(got.Tags) != 1 {
		t.Fatalf("用户内容异常：%+v", got)
	}

	detail, ok, err := s.Profile(ctx, "p1")
	if err != nil || !ok {
		t.Fatalf("读取配置失败：%v ok=%v", err, ok)
	}
	if detail.UserDisplayName != user.DisplayName {
		t.Fatalf("配置未关联用户：%+v", detail)
	}
	if detail.Certificate == nil || detail.Certificate.ID != "c1" {
		t.Fatalf("配置未关联生效证书：%+v", detail.Certificate)
	}

	users, err := s.ListUsers(ctx, "", "")
	if err != nil {
		t.Fatalf("列出用户失败：%v", err)
	}
	if len(users) != 1 || users[0].ProfileCount != 1 || users[0].ActiveProfileCount != 1 {
		t.Fatalf("用户列表统计异常：%+v", users)
	}

	uCount, pCount, err := s.AccountCounts(ctx)
	if err != nil || uCount != 1 || pCount != 1 {
		t.Fatalf("统计异常：users=%d profiles=%d err=%v", uCount, pCount, err)
	}
}

func TestUsernameIsCaseInsensitiveUnique(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	user := testUser("u1")
	profile := testProfile("p1", "u1", AuthCertificatePassword, "Alice")
	if err := s.CreateUserWithProfile(ctx, user, profile, nil); err != nil {
		t.Fatalf("创建首个配置失败：%v", err)
	}

	duplicate := testProfile("p2", "u1", AuthPassword, "alice")
	err := s.CreateProfileWithCertificate(ctx, duplicate, nil)
	if !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("大小写不同的重复登录名应被拒绝，实际：%v", err)
	}
}

func TestUpdateProfileNetworkFixedIP(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateUserWithProfile(ctx, testUser("u1"), testProfile("p1", "u1", AuthCertificate, ""), nil); err != nil {
		t.Fatalf("创建首个配置失败：%v", err)
	}
	if err := s.CreateUserWithProfile(ctx, testUser("u2"), testProfile("p2", "u2", AuthCertificate, ""), nil); err != nil {
		t.Fatalf("创建第二个配置失败：%v", err)
	}
	if err := s.UpdateProfileNetwork(ctx, "p1", RouteInherit, AccessInherit, nil, nil, ClientLANInherit, "10.8.255.200"); err != nil {
		t.Fatalf("设置固定 IP 失败：%v", err)
	}
	detail, ok, err := s.Profile(ctx, "p1")
	if err != nil || !ok || detail.FixedVPNIP != "10.8.255.200" {
		t.Fatalf("固定 IP 未保存：%+v ok=%v err=%v", detail, ok, err)
	}
	if detail.ConcurrentLimit == nil || *detail.ConcurrentLimit != 1 {
		t.Fatalf("设置固定 IP 应锁定并发为 1：%+v", detail.ConcurrentLimit)
	}

	err = s.UpdateProfileNetwork(ctx, "p2", RouteInherit, AccessInherit, nil, nil, ClientLANInherit, "10.8.255.200")
	if !errors.Is(err, ErrFixedIPTaken) {
		t.Fatalf("重复固定 IP 应返回 ErrFixedIPTaken：%v", err)
	}

	if err := s.UpdateProfileNetwork(ctx, "p1", RouteInherit, AccessInherit, nil, nil, ClientLANInherit, ""); err != nil {
		t.Fatalf("清除固定 IP 失败：%v", err)
	}
	detail, _, _ = s.Profile(ctx, "p1")
	if detail.FixedVPNIP != "" {
		t.Fatalf("固定 IP 未清除：%+v", detail)
	}
}

func TestCertificateRotationAndRevocation(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	user := testUser("u1")
	profile := testProfile("p1", "u1", AuthCertificate, "")
	first := testCert("c1", "p1", 1)
	if err := s.CreateUserWithProfile(ctx, user, profile, &first); err != nil {
		t.Fatalf("创建失败：%v", err)
	}

	second := testCert("c2", "p1", 2)
	if err := s.ReplaceActiveCertificate(ctx, "p1", second); err != nil {
		t.Fatalf("轮换失败：%v", err)
	}
	active, ok, err := s.ActiveCertificate(ctx, "p1")
	if err != nil || !ok || active.ID != "c2" {
		t.Fatalf("轮换后生效证书异常：%+v ok=%v err=%v", active, ok, err)
	}

	history, err := s.CertificateHistory(ctx, "p1")
	if err != nil {
		t.Fatalf("读取历史失败：%v", err)
	}
	if len(history) != 2 {
		t.Fatalf("证书历史应为 2 条：%+v", history)
	}
	revokedCount := 0
	for _, c := range history {
		if c.Status == CertificateRevoked {
			revokedCount++
			if c.RevokeReason != "superseded" {
				t.Fatalf("轮换原因异常：%q", c.RevokeReason)
			}
		}
	}
	if revokedCount != 1 {
		t.Fatalf("应有一张旧证书被吊销：%+v", history)
	}

	revoked, err := s.RevokeActiveCertificate(ctx, "p1", "manual", time.Now().UTC())
	if err != nil || !revoked {
		t.Fatalf("吊销失败：revoked=%v err=%v", revoked, err)
	}
	revoked, err = s.RevokeActiveCertificate(ctx, "p1", "manual", time.Now().UTC())
	if err != nil || revoked {
		t.Fatalf("重复吊销不应成功：revoked=%v err=%v", revoked, err)
	}
	if _, ok, _ := s.ActiveCertificate(ctx, "p1"); ok {
		t.Fatal("吊销后不应还有生效证书")
	}
}

func TestProfilePasswordHash(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	user := testUser("u1")
	profile := testProfile("p1", "u1", AuthPassword, "bob")
	if err := s.CreateUserWithProfile(ctx, user, profile, nil); err != nil {
		t.Fatalf("创建失败：%v", err)
	}

	hash, has, found, err := s.ProfilePasswordHash(ctx, "p1")
	if err != nil || !found || !has {
		t.Fatalf("读取密码哈希失败：hash=%q has=%v found=%v err=%v", hash, has, found, err)
	}
	if hash != "hash-p1" {
		t.Fatalf("密码哈希 = %q", hash)
	}

	if err := s.UpdateProfilePassword(ctx, "p1", "new-hash"); err != nil {
		t.Fatalf("更新密码失败：%v", err)
	}
	hash, _, _, _ = s.ProfilePasswordHash(ctx, "p1")
	if hash != "new-hash" {
		t.Fatalf("密码哈希未更新：%q", hash)
	}
}

func TestExportSettingUpsert(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if _, ok, err := s.ExportSetting(ctx); err != nil || ok {
		t.Fatalf("初始不应有导出设置：ok=%v err=%v", ok, err)
	}

	now := time.Now().UTC()
	if err := s.SaveExportSetting(ctx, ExportSetting{
		Host: "vpn.example.com", Port: 1194, Proto: "udp",
		TLSCryptCiphertext: []byte("cipher-1"), UpdatedAt: now,
	}); err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	setting, ok, err := s.ExportSetting(ctx)
	if err != nil || !ok {
		t.Fatalf("读取失败：ok=%v err=%v", ok, err)
	}
	if setting.Host != "vpn.example.com" || setting.Port != 1194 || string(setting.TLSCryptCiphertext) != "cipher-1" {
		t.Fatalf("设置内容异常：%+v", setting)
	}

	if err := s.SaveExportSetting(ctx, ExportSetting{
		Host: "vpn2.example.com", Port: 443, Proto: "tcp",
		TLSCryptCiphertext: []byte("cipher-1"), UpdatedAt: now,
	}); err != nil {
		t.Fatalf("更新失败：%v", err)
	}
	setting, _, _ = s.ExportSetting(ctx)
	if setting.Host != "vpn2.example.com" || setting.Port != 443 || setting.Proto != "tcp" {
		t.Fatalf("设置未更新：%+v", setting)
	}
}

func TestUserAndProfileStatusUpdates(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	user := testUser("u1")
	profile := testProfile("p1", "u1", AuthPassword, "bob")
	if err := s.CreateUserWithProfile(ctx, user, profile, nil); err != nil {
		t.Fatalf("创建失败：%v", err)
	}

	if err := s.UpdateUserStatus(ctx, "u1", StatusDisabled); err != nil {
		t.Fatalf("停用用户失败：%v", err)
	}
	detail, _, _ := s.Profile(ctx, "p1")
	if detail.UserStatus != StatusDisabled {
		t.Fatalf("用户状态未更新：%+v", detail)
	}

	if err := s.UpdateProfileStatus(ctx, "p1", StatusDisabled); err != nil {
		t.Fatalf("停用配置失败：%v", err)
	}
	detail, _, _ = s.Profile(ctx, "p1")
	if detail.Status != StatusDisabled {
		t.Fatalf("配置状态未更新：%+v", detail)
	}

	if err := s.UpdateProfileName(ctx, "p1", "新名称"); err != nil {
		t.Fatalf("改名失败：%v", err)
	}
	detail, _, _ = s.Profile(ctx, "p1")
	if detail.Name != "新名称" {
		t.Fatalf("名称未更新：%q", detail.Name)
	}
}
