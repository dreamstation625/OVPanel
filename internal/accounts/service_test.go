package accounts

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dreamstation625/ovpanel/internal/auth"
	"github.com/dreamstation625/ovpanel/internal/pki"
	"github.com/dreamstation625/ovpanel/internal/secretbox"
	"github.com/dreamstation625/ovpanel/internal/store"
)

func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	old := auth.DefaultParams
	auth.DefaultParams = auth.Params{
		Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 8, KeyLength: 16,
	}
	t.Cleanup(func() { auth.DefaultParams = old })

	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	key, err := secretbox.LoadOrCreate(filepath.Join(dir, "master.key"), "")
	if err != nil {
		t.Fatalf("生成主密钥失败：%v", err)
	}
	pkiService := pki.New(st, key)
	return New(st, pkiService, key), st
}

func TestCreateUserWithAllAuthTypes(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	certOnly, err := svc.CreateUserWithProfile(ctx,
		UserInput{DisplayName: "张三", Tags: []string{"家庭"}},
		ProfileInput{Name: "电脑", AuthType: store.AuthCertificate})
	if err != nil {
		t.Fatalf("创建仅证书配置失败：%v", err)
	}
	if certOnly.Password != "" || certOnly.Profile == nil || certOnly.Profile.Certificate == nil {
		t.Fatalf("仅证书结果异常：%+v", certOnly)
	}

	certPassword, err := svc.CreateUserWithProfile(ctx,
		UserInput{DisplayName: "李四"},
		ProfileInput{Name: "手机", AuthType: store.AuthCertificatePassword, Username: "lisi"})
	if err != nil {
		t.Fatalf("创建证书+密码配置失败：%v", err)
	}
	if len(certPassword.Password) != generatedPasswordLength {
		t.Fatalf("自动密码长度 = %d", len(certPassword.Password))
	}
	if certPassword.Profile.Certificate == nil {
		t.Fatal("证书+密码配置应有证书")
	}
	hash, has, found, err := st.ProfilePasswordHash(ctx, certPassword.Profile.ID)
	if err != nil || !found || !has {
		t.Fatalf("读取密码哈希失败：%v", err)
	}
	if ok, err := auth.VerifyPassword(hash, certPassword.Password); err != nil || !ok {
		t.Fatalf("自动密码无法通过校验：%v", err)
	}

	passwordOnly, err := svc.CreateUserWithProfile(ctx,
		UserInput{DisplayName: "王五"},
		ProfileInput{Name: "共享", AuthType: store.AuthPassword, Username: "wangwu"})
	if err != nil {
		t.Fatalf("创建仅密码配置失败：%v", err)
	}
	if passwordOnly.Profile.Certificate != nil {
		t.Fatal("仅密码配置不应有证书")
	}
	if passwordOnly.Password == "" {
		t.Fatal("仅密码配置应返回一次性密码")
	}
}

func TestUsernameConflictIsCaseInsensitive(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	if _, err := svc.CreateUserWithProfile(ctx,
		UserInput{DisplayName: "甲"},
		ProfileInput{Name: "一", AuthType: store.AuthPassword, Username: "Alice"}); err != nil {
		t.Fatalf("创建失败：%v", err)
	}
	_, err := svc.CreateUserWithProfile(ctx,
		UserInput{DisplayName: "乙"},
		ProfileInput{Name: "二", AuthType: store.AuthPassword, Username: "alice"})
	if !errors.Is(err, store.ErrUsernameTaken) {
		t.Fatalf("重复登录名应被拒绝：%v", err)
	}
}

func TestValidation(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	cases := []struct {
		name string
		user UserInput
		prof ProfileInput
	}{
		{"空名称", UserInput{}, ProfileInput{Name: "配置", AuthType: store.AuthCertificate}},
		{"仅证书带登录名", UserInput{DisplayName: "用户"}, ProfileInput{Name: "配置", AuthType: store.AuthCertificate, Username: "bob"}},
		{"非法登录名", UserInput{DisplayName: "用户"}, ProfileInput{Name: "配置", AuthType: store.AuthPassword, Username: "a b"}},
		{"短密码", UserInput{DisplayName: "用户"}, ProfileInput{Name: "配置", AuthType: store.AuthPassword, Username: "bob", Password: "short"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.CreateUserWithProfile(ctx, tc.user, tc.prof); !errors.Is(err, ErrValidation) {
				t.Fatalf("应返回校验错误：%v", err)
			}
		})
	}

	past := time.Now().Add(-time.Hour)
	if _, err := svc.CreateUserWithProfile(ctx,
		UserInput{DisplayName: "用户"},
		ProfileInput{Name: "配置", AuthType: store.AuthCertificate, ExpiresAt: &past}); !errors.Is(err, ErrValidation) {
		t.Fatalf("过去时间应被拒绝：%v", err)
	}
}

func TestPasswordResetAndCertificateActions(t *testing.T) {
	svc, st := newTestService(t)
	ctx := context.Background()

	res, err := svc.CreateUserWithProfile(ctx,
		UserInput{DisplayName: "用户"},
		ProfileInput{Name: "配置", AuthType: store.AuthCertificatePassword, Username: "bob"})
	if err != nil {
		t.Fatalf("创建失败：%v", err)
	}
	oldPassword := res.Password

	reset, err := svc.ResetPassword(ctx, res.Profile.ID, "")
	if err != nil {
		t.Fatalf("重置密码失败：%v", err)
	}
	if reset.Password == oldPassword {
		t.Fatal("重置后的密码不应与旧密码相同")
	}
	hash, _, _, _ := st.ProfilePasswordHash(ctx, res.Profile.ID)
	if ok, _ := auth.VerifyPassword(hash, oldPassword); ok {
		t.Fatal("旧密码不应再有效")
	}
	if ok, _ := auth.VerifyPassword(hash, reset.Password); !ok {
		t.Fatal("新密码应通过校验")
	}

	rotated, err := svc.RotateCertificate(ctx, res.Profile.ID)
	if err != nil {
		t.Fatalf("轮换失败：%v", err)
	}
	if rotated.Certificate == nil || rotated.Certificate.ID == res.Profile.Certificate.ID {
		t.Fatal("轮换后应得到新证书")
	}
	history, err := st.CertificateHistory(ctx, res.Profile.ID)
	if err != nil || len(history) != 2 {
		t.Fatalf("证书历史异常：%v %+v", err, history)
	}

	revoked, err := svc.RevokeCertificate(ctx, res.Profile.ID, "manual")
	if err != nil {
		t.Fatalf("吊销失败：%v", err)
	}
	if revoked.Certificate != nil {
		t.Fatal("吊销后不应有生效证书")
	}
	if code, _ := EffectiveStatus(revoked, time.Now().UTC()); code != "certificate_missing" {
		t.Fatalf("吊销后有效状态 = %q", code)
	}

	restored, err := svc.RotateCertificate(ctx, res.Profile.ID)
	if err != nil || restored.Certificate == nil {
		t.Fatalf("吊销后应能轮换恢复：%+v %v", restored, err)
	}
}

func TestCertificateActionsRejectPasswordOnly(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	res, err := svc.CreateUserWithProfile(ctx,
		UserInput{DisplayName: "用户"},
		ProfileInput{Name: "配置", AuthType: store.AuthPassword, Username: "bob"})
	if err != nil {
		t.Fatalf("创建失败：%v", err)
	}
	if _, err := svc.RotateCertificate(ctx, res.Profile.ID); !errors.Is(err, ErrNoCertificateAuth) {
		t.Fatalf("仅密码配置不应轮换证书：%v", err)
	}
	if _, err := svc.RevokeCertificate(ctx, res.Profile.ID, "manual"); !errors.Is(err, ErrNoCertificateAuth) {
		t.Fatalf("仅密码配置不应吊销证书：%v", err)
	}
	if _, err := svc.ResetPassword(ctx, res.Profile.ID, ""); err != nil {
		t.Fatalf("仅密码配置应可重置密码：%v", err)
	}
}

func TestExportProfiles(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	certRes, err := svc.CreateUserWithProfile(ctx,
		UserInput{DisplayName: "证书用户"},
		ProfileInput{Name: "证书配置", AuthType: store.AuthCertificate})
	if err != nil {
		t.Fatalf("创建失败：%v", err)
	}
	passwordRes, err := svc.CreateUserWithProfile(ctx,
		UserInput{DisplayName: "密码用户"},
		ProfileInput{Name: "密码配置", AuthType: store.AuthPassword, Username: "carol"})
	if err != nil {
		t.Fatalf("创建失败：%v", err)
	}

	if _, err := svc.Export(ctx, certRes.Profile.ID); !errors.Is(err, ErrExportNotConfigured) {
		t.Fatalf("未配置连接地址时应拒绝导出：%v", err)
	}

	if _, err := svc.SaveExportSettings(ctx, ExportSettings{Host: "vpn.example.com", Port: 1194, Proto: "udp"}); err != nil {
		t.Fatalf("保存导出设置失败：%v", err)
	}

	certExport, err := svc.Export(ctx, certRes.Profile.ID)
	if err != nil {
		t.Fatalf("导出证书配置失败：%v", err)
	}
	content := string(certExport.Content)
	for _, want := range []string{"<ca>", "<cert>", "<key>", "<tls-crypt>", "remote vpn.example.com 1194"} {
		if !strings.Contains(content, want) {
			t.Fatalf("证书配置缺少 %q", want)
		}
	}
	if strings.Contains(content, "auth-user-pass") {
		t.Fatal("仅证书配置不应要求账号密码")
	}
	if certExport.Filename != "证书用户-证书配置.ovpn" {
		t.Fatalf("文件名异常：%q", certExport.Filename)
	}

	passwordExport, err := svc.Export(ctx, passwordRes.Profile.ID)
	if err != nil {
		t.Fatalf("导出密码配置失败：%v", err)
	}
	content = string(passwordExport.Content)
	if strings.Contains(content, "<cert>") || strings.Contains(content, "<key>") {
		t.Fatal("仅密码配置不应包含证书或私钥")
	}
	if !strings.Contains(content, "auth-user-pass") {
		t.Fatal("仅密码配置应要求账号密码")
	}
}

func TestExportSettingValidation(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	for _, input := range []ExportSettings{
		{Host: "", Port: 1194, Proto: "udp"},
		{Host: "vpn.example.com\nmalicious", Port: 1194, Proto: "udp"},
		{Host: "vpn.example.com", Port: 0, Proto: "udp"},
		{Host: "vpn.example.com", Port: 1194, Proto: "icmp"},
	} {
		if _, err := svc.SaveExportSettings(ctx, input); !errors.Is(err, ErrValidation) {
			t.Fatalf("非法设置应被拒绝：%+v err=%v", input, err)
		}
	}
}

func TestEffectiveStatus(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()
	res, err := svc.CreateUserWithProfile(ctx,
		UserInput{DisplayName: "用户"},
		ProfileInput{Name: "配置", AuthType: store.AuthCertificate})
	if err != nil {
		t.Fatalf("创建失败：%v", err)
	}
	if code, _ := EffectiveStatus(*res.Profile, now); code != "active" {
		t.Fatalf("初始状态 = %q", code)
	}

	disabledUser, err := svc.SetUserStatus(ctx, res.Profile.UserID, store.StatusDisabled)
	if err != nil {
		t.Fatalf("停用用户失败：%v", err)
	}
	detail, _, _ := svc.store.Profile(ctx, res.Profile.ID)
	if code, _ := EffectiveStatus(detail, now); code != "user_disabled" {
		t.Fatalf("用户停用后状态 = %q", code)
	}
	if disabledUser.Status != store.StatusDisabled {
		t.Fatalf("用户状态 = %q", disabledUser.Status)
	}

	if _, err := svc.SetUserStatus(ctx, res.Profile.UserID, store.StatusActive); err != nil {
		t.Fatalf("启用用户失败：%v", err)
	}
	if _, err := svc.SetProfileStatus(ctx, res.Profile.ID, store.StatusDisabled); err != nil {
		t.Fatalf("停用配置失败：%v", err)
	}
	detail, _, _ = svc.store.Profile(ctx, res.Profile.ID)
	if code, _ := EffectiveStatus(detail, now); code != "profile_disabled" {
		t.Fatalf("配置停用后状态 = %q", code)
	}
}

func TestGeneratePassword(t *testing.T) {
	first, err := GeneratePassword(20)
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	second, _ := GeneratePassword(20)
	if len(first) != 20 || first == second {
		t.Fatalf("密码生成异常：%q %q", first, second)
	}
	if strings.ContainsAny(first, "IO01l") {
		t.Fatalf("密码包含歧义字符：%q", first)
	}
}
