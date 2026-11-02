package vpnruntime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/dreamstation625/ovpanel/internal/accounts"
	"github.com/dreamstation625/ovpanel/internal/agentproto"
	"github.com/dreamstation625/ovpanel/internal/pki"
	"github.com/dreamstation625/ovpanel/internal/secretbox"
	"github.com/dreamstation625/ovpanel/internal/store"
)

func TestApplyWritesBundle(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	key, err := secretbox.LoadOrCreate(filepath.Join(dir, "master.key"), "")
	if err != nil {
		t.Fatalf("主密钥失败：%v", err)
	}
	pkiService := pki.New(st, key)
	accountsService := accounts.New(st, pkiService, key)
	if _, err := accountsService.SaveExportSettings(ctx, accounts.ExportSettings{
		Host: "vpn.example.com", Port: 1194, Proto: "udp",
	}); err != nil {
		t.Fatalf("保存导出设置失败：%v", err)
	}

	now := time.Now().UTC()
	user := store.User{ID: "u1", DisplayName: "用户", Status: store.StatusActive, CreatedAt: now, UpdatedAt: now}
	profile := store.Profile{
		ID: "p1", UserID: "u1", Name: "配置", AuthType: store.AuthCertificate,
		Status: store.StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	cert := &store.CertificateRecord{
		Certificate: store.Certificate{
			ID: "c1", ProfileID: "p1", SerialNumber: "0A1B", Fingerprint: "fp",
			CommonName: "ovp-p1", Status: store.CertificateActive,
			IssuedAt: now, ExpiresAt: now.Add(time.Hour),
		},
		CertPEM: "CERT", KeyCiphertext: []byte("key"),
	}
	if err := st.CreateUserWithProfile(ctx, user, profile, cert); err != nil {
		t.Fatalf("创建用户/配置失败：%v", err)
	}

	controlDir := filepath.Join(dir, "control")
	service := New(st, pkiService, accountsService, controlDir)
	result, err := service.Apply(ctx)
	if err != nil {
		t.Fatalf("下发控制包失败：%v", err)
	}
	if result.ConfigHash == "" || result.Profiles != 1 || !result.AgentPending {
		t.Fatalf("下发结果异常：%+v", result)
	}

	raw, err := os.ReadFile(filepath.Join(controlDir, BundleFileName))
	if err != nil {
		t.Fatalf("读取控制包失败：%v", err)
	}
	var bundle agentproto.Bundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatalf("解析控制包失败：%v", err)
	}
	if bundle.Version != agentproto.Version || bundle.ConfigHash != result.ConfigHash {
		t.Fatalf("控制包版本/哈希异常：%+v", bundle)
	}
	if bundle.CAPEM == "" || bundle.ServerCertPEM == "" || bundle.ServerKeyPEM == "" || bundle.TLSCryptKey == "" {
		t.Fatal("控制包缺少证书或 tls-crypt 材料")
	}
	if len(bundle.Snapshot.Profiles) != 1 || bundle.Snapshot.Profiles[0].ID != "p1" {
		t.Fatalf("认证快照异常：%+v", bundle.Snapshot)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(controlDir, BundleFileName))
		if err != nil {
			t.Fatalf("读取控制包信息失败：%v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("控制包权限 = %o", info.Mode().Perm())
		}
	}
}

func TestApplyRequiresExportSettings(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	key, _ := secretbox.LoadOrCreate(filepath.Join(dir, "master.key"), "")
	pkiService := pki.New(st, key)
	accountsService := accounts.New(st, pkiService, key)
	service := New(st, pkiService, accountsService, filepath.Join(dir, "control"))

	if _, err := service.Apply(ctx); err == nil {
		t.Fatal("未配置导出地址时不应下发控制包")
	}
}
