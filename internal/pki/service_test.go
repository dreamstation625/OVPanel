package pki

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dreamstation625/ovpanel/internal/secretbox"
	"github.com/dreamstation625/ovpanel/internal/store"
)

func newTestService(t *testing.T) (*Service, *store.Store, secretbox.Key) {
	t.Helper()
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
	return New(st, key), st, key
}

func TestBuildClientCertificateIsSignedByCA(t *testing.T) {
	svc, st, key := newTestService(t)
	ctx := context.Background()

	if info, err := svc.CAInfo(ctx); err != nil || info.Exists {
		t.Fatalf("初始不应有 CA：%+v %v", info, err)
	}

	record, caPEM, err := svc.BuildClientCertificate(ctx, "profile-1", "ovp-profile-1")
	if err != nil {
		t.Fatalf("签发失败：%v", err)
	}
	if caPEM == "" || !strings.Contains(record.CertPEM, "BEGIN CERTIFICATE") {
		t.Fatal("证书材料为空")
	}
	if record.Status != store.CertificateActive || record.ProfileID != "profile-1" {
		t.Fatalf("证书元信息异常：%+v", record.Certificate)
	}
	if len(record.Fingerprint) != 64 {
		t.Fatalf("SHA-256 指纹长度异常：%d", len(record.Fingerprint))
	}

	info, err := svc.CAInfo(ctx)
	if err != nil || !info.Exists {
		t.Fatalf("CA 信息异常：%+v %v", info, err)
	}
	if info.NotAfter.Before(time.Now()) {
		t.Fatal("CA 已过期")
	}

	caCert := parseTestCert(t, caPEM)
	leaf := parseTestCert(t, record.CertPEM)
	if err := leaf.CheckSignatureFrom(caCert); err != nil {
		t.Fatalf("客户端证书不是由 CA 签发：%v", err)
	}
	if leaf.Subject.CommonName != "ovp-profile-1" {
		t.Fatalf("CN = %q", leaf.Subject.CommonName)
	}
	hasClientAuth := false
	for _, usage := range leaf.ExtKeyUsage {
		if usage == x509.ExtKeyUsageClientAuth {
			hasClientAuth = true
		}
	}
	if !hasClientAuth {
		t.Fatal("缺少 ClientAuth 用途")
	}

	keyPEM, err := key.Open(record.KeyCiphertext, []byte(certKeyAAD+record.ID))
	if err != nil {
		t.Fatalf("解密客户端私钥失败：%v", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		t.Fatal("私钥 PEM 非法")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatalf("解析私钥失败：%v", err)
	}
	privateKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		t.Fatal("私钥不是 RSA")
	}
	publicKey, ok := leaf.PublicKey.(*rsa.PublicKey)
	if !ok || !privateKey.PublicKey.Equal(publicKey) {
		t.Fatal("私钥与证书公钥不匹配")
	}

	// 保存 profile 与证书后，导出材料必须与签发结果一致。
	user := store.User{
		ID: "u1", DisplayName: "用户", Status: store.StatusActive,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	profile := store.Profile{
		ID: "profile-1", UserID: "u1", Name: "配置", AuthType: store.AuthCertificate,
		Status: store.StatusActive, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreateUserWithProfile(ctx, user, profile, &record); err != nil {
		t.Fatalf("保存证书失败：%v", err)
	}

	exportCA, exportCert, exportKey, err := svc.ExportMaterial(ctx, "profile-1")
	if err != nil {
		t.Fatalf("导出材料失败：%v", err)
	}
	if exportCA != caPEM || exportCert != record.CertPEM || exportKey != string(keyPEM) {
		t.Fatal("导出材料与签发结果不一致")
	}
}

func TestCRLContainsRevokedCertificate(t *testing.T) {
	svc, st, _ := newTestService(t)
	ctx := context.Background()
	record, caPEM, err := svc.BuildClientCertificate(ctx, "profile-1", "ovp-profile-1")
	if err != nil {
		t.Fatalf("签发失败：%v", err)
	}
	user := store.User{ID: "u1", DisplayName: "用户", Status: store.StatusActive, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	profile := store.Profile{ID: "profile-1", UserID: "u1", Name: "配置", AuthType: store.AuthCertificate, Status: store.StatusActive, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := st.CreateUserWithProfile(ctx, user, profile, &record); err != nil {
		t.Fatalf("保存失败：%v", err)
	}
	if _, err := st.RevokeActiveCertificate(ctx, "profile-1", "manual", time.Now().UTC()); err != nil {
		t.Fatalf("吊销失败：%v", err)
	}
	crlPEM, err := svc.CRLPEM(ctx)
	if err != nil {
		t.Fatalf("生成 CRL 失败：%v", err)
	}
	block, _ := pem.Decode([]byte(crlPEM))
	if block == nil {
		t.Fatal("CRL PEM 非法")
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil {
		t.Fatalf("解析 CRL 失败：%v", err)
	}
	caCert := parseTestCert(t, caPEM)
	if err := crl.CheckSignatureFrom(caCert); err != nil {
		t.Fatalf("CRL 签名校验失败：%v", err)
	}
	serial := new(big.Int)
	if _, ok := serial.SetString(record.SerialNumber, 16); !ok {
		t.Fatalf("序列号非法：%s", record.SerialNumber)
	}
	found := false
	for _, entry := range crl.RevokedCertificateEntries {
		if entry.SerialNumber.Cmp(serial) == 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("CRL 未包含已吊销序列号 %s", record.SerialNumber)
	}
}

func TestCheckRejectsWrongMasterKey(t *testing.T) {
	svc, st, _ := newTestService(t)
	ctx := context.Background()
	if _, _, err := svc.BuildClientCertificate(ctx, "profile-1", "ovp-profile-1"); err != nil {
		t.Fatalf("签发失败：%v", err)
	}

	wrongDir := t.TempDir()
	wrongKey, err := secretbox.LoadOrCreate(filepath.Join(wrongDir, "master.key"), "")
	if err != nil {
		t.Fatalf("生成错误主密钥失败：%v", err)
	}
	wrongService := New(st, wrongKey)
	if err := wrongService.Check(ctx); err == nil {
		t.Fatal("主密钥不匹配时应拒绝启动")
	}
}

func TestExportWithoutCAOrCertificate(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	if _, err := svc.CAPEM(ctx); !errors.Is(err, ErrCANotCreated) {
		t.Fatalf("无 CA 时应返回 ErrCANotCreated：%v", err)
	}
	if _, _, _, err := svc.ExportMaterial(ctx, "missing"); !errors.Is(err, ErrCANotCreated) {
		t.Fatalf("无 CA 时导出应失败：%v", err)
	}
}

func parseTestCert(t *testing.T, certPEM string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		t.Fatal("证书 PEM 非法")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("解析证书失败：%v", err)
	}
	return cert
}
