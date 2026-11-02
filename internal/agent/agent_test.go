package agent

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dreamstation625/ovpanel/internal/agentproto"
)

func TestIncomingFromEnv(t *testing.T) {
	in := incomingFromEnv(map[string]string{
		"username":            "bob",
		"password":            "secret",
		"tls_serial_hex_0":    "0A1B",
		"tls_digest_sha256_0": "aa:bb",
	})
	if !in.CertPresent || !in.CertVerified || in.CertSerial != "0A1B" || in.CertFingerprint != "aa:bb" {
		t.Fatalf("证书身份解析异常：%+v", in)
	}
	if in.Username != "bob" || in.Password != "secret" {
		t.Fatalf("账号解析异常：%+v", in)
	}

	in = incomingFromEnv(map[string]string{"username": "carol", "password": "pw"})
	if in.CertPresent || in.CertSerial != "" {
		t.Fatalf("无证书请求不应被标记为有证书：%+v", in)
	}
}

func TestInitialStatusNotReady(t *testing.T) {
	rt := New(t.TempDir(), t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if rt.Ready() {
		t.Fatal("尚未应用控制包时不应 ready")
	}
	status := rt.Status()
	if status.Configured || status.OpenVPNRunning || status.ManagementConnected {
		t.Fatalf("初始状态异常：%+v", status)
	}
}

func TestWriteSecure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "secret.conf")
	if err := writeSecure(path, []byte("secret")); err != nil {
		t.Fatalf("写入失败：%v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "secret" {
		t.Fatalf("读取失败：%q %v", raw, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat 失败：%v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("权限 = %o", info.Mode().Perm())
		}
	}
}

func TestWriteMaterialsUsesConfigDir(t *testing.T) {
	rt := New(t.TempDir(), t.TempDir(), testLogger())
	rt.configDir = filepath.Join(t.TempDir(), "config")
	bundle := agentproto.Bundle{
		CAPEM: "ca", ServerCertPEM: "cert", ServerKeyPEM: "key",
		TLSCryptKey: "tls-crypt", CRLPEM: "crl", Config: "server config",
	}
	if err := rt.writeMaterials(bundle); err != nil {
		t.Fatalf("写入配置材料失败：%v", err)
	}
	for name, want := range map[string]string{
		"ca.crt": "ca", "server.crt": "cert", "server.key": "key",
		"ta.key": "tls-crypt", "crl.pem": "crl", "server.conf": "server config",
	} {
		got, err := os.ReadFile(filepath.Join(rt.configDir, name))
		if err != nil || string(got) != want {
			t.Errorf("实例目录中的 %s = %q，期望 %q；错误：%v", name, got, want, err)
		}
	}
}
