package runtimeauth

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/dreamstation625/ovpanel/internal/admission"
	"github.com/dreamstation625/ovpanel/internal/auth"
	"github.com/dreamstation625/ovpanel/internal/store"
)

func TestBuildSnapshotFromStore(t *testing.T) {
	old := auth.DefaultParams
	auth.DefaultParams = auth.Params{
		Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 8, KeyLength: 16,
	}
	t.Cleanup(func() { auth.DefaultParams = old })

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

	now := time.Now().UTC()
	hash := func(password string) string {
		value, err := auth.HashPassword(password)
		if err != nil {
			t.Fatalf("生成哈希失败：%v", err)
		}
		return value
	}
	cert := func(id, profileID, serial string) *store.CertificateRecord {
		return &store.CertificateRecord{
			Certificate: store.Certificate{
				ID: id, ProfileID: profileID, SerialNumber: serial,
				Fingerprint: "fp-" + id, CommonName: "ovp-" + profileID,
				Status: store.CertificateActive, IssuedAt: now, ExpiresAt: now.Add(time.Hour),
			},
			CertPEM: "CERT-" + id, KeyCiphertext: []byte("key-" + id),
		}
	}

	userA := store.User{ID: "u1", DisplayName: "用户一", Status: store.StatusActive, CreatedAt: now, UpdatedAt: now}
	profileA := store.Profile{ID: "A", UserID: "u1", Name: "A", AuthType: store.AuthCertificate, Status: store.StatusActive, CreatedAt: now, UpdatedAt: now}
	if err := st.CreateUserWithProfile(ctx, userA, profileA, cert("c1", "A", "0A1B")); err != nil {
		t.Fatalf("创建 A 失败：%v", err)
	}

	profileB := store.Profile{
		ID: "B", UserID: "u1", Name: "B", AuthType: store.AuthCertificatePassword,
		Username: "bob", PasswordHash: hash("bob-secret"), HasPassword: true,
		Status: store.StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateProfileWithCertificate(ctx, profileB, cert("c2", "B", "0C0D")); err != nil {
		t.Fatalf("创建 B 失败：%v", err)
	}

	userC := store.User{ID: "u2", DisplayName: "用户二", Status: store.StatusActive, CreatedAt: now, UpdatedAt: now}
	profileC := store.Profile{
		ID: "C", UserID: "u2", Name: "C", AuthType: store.AuthPassword,
		Username: "carol", PasswordHash: hash("carol-secret"), HasPassword: true,
		Status: store.StatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateUserWithProfile(ctx, userC, profileC, nil); err != nil {
		t.Fatalf("创建 C 失败：%v", err)
	}

	snapshot, err := Build(ctx, st)
	if err != nil {
		t.Fatalf("构建快照失败：%v", err)
	}
	if len(snapshot.Users) != 2 || len(snapshot.Profiles) != 3 {
		t.Fatalf("快照数量异常：users=%d profiles=%d", len(snapshot.Users), len(snapshot.Profiles))
	}

	nowCheck := time.Now().UTC()
	if decision := admission.Decide(snapshot, admission.Incoming{
		CertPresent: true, CertVerified: true, CertSerial: "2587",
	}, nowCheck); !decision.Allow || decision.ProfileID != "A" {
		t.Fatalf("A 准入失败：%+v", decision)
	}
	if decision := admission.Decide(snapshot, admission.Incoming{
		CertPresent: true, CertVerified: true, CertSerial: "0x0C0D",
		Username: "bob", Password: "bob-secret",
	}, nowCheck); !decision.Allow || decision.ProfileID != "B" {
		t.Fatalf("B 准入失败：%+v", decision)
	}
	if decision := admission.Decide(snapshot, admission.Incoming{
		Username: "carol", Password: "carol-secret",
	}, nowCheck); !decision.Allow || decision.ProfileID != "C" {
		t.Fatalf("C 准入失败：%+v", decision)
	}

	// 吊销 A 的证书后，快照仍可应用，但 A 不能再凭旧证书准入。
	if _, err := st.RevokeActiveCertificate(ctx, "A", "manual", now); err != nil {
		t.Fatalf("吊销失败：%v", err)
	}
	snapshot, err = Build(ctx, st)
	if err != nil {
		t.Fatalf("吊销后构建快照失败：%v", err)
	}
	if decision := admission.Decide(snapshot, admission.Incoming{
		CertPresent: true, CertVerified: true, CertSerial: "2587",
	}, nowCheck); decision.Allow {
		t.Fatalf("吊销证书不应再准入：%+v", decision)
	}
}
