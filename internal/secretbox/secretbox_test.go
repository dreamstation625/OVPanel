package secretbox

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newTestKey(t *testing.T) Key {
	t.Helper()
	key, err := LoadOrCreate(filepath.Join(t.TempDir(), "master.key"), "")
	if err != nil {
		t.Fatalf("生成主密钥失败：%v", err)
	}
	return key
}

func TestLoadOrCreateIsStable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "master.key")
	first, err := LoadOrCreate(path, "")
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	second, err := LoadOrCreate(path, "")
	if err != nil {
		t.Fatalf("读取失败：%v", err)
	}
	if first != second {
		t.Fatal("同一文件应得到相同主密钥")
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("读取文件信息失败：%v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("主密钥权限 = %o，期望 600", info.Mode().Perm())
		}
	}
}

func TestEnvKeyWins(t *testing.T) {
	raw := strings.Repeat("ab", KeySize)
	key, err := LoadOrCreate(filepath.Join(t.TempDir(), "master.key"), raw)
	if err != nil {
		t.Fatalf("解析十六进制密钥失败：%v", err)
	}
	if key.isZero() {
		t.Fatal("密钥不应为零值")
	}

	other, err := LoadOrCreate(filepath.Join(t.TempDir(), "master.key"), strings.Repeat("cd", KeySize))
	if err != nil {
		t.Fatalf("解析第二个密钥失败：%v", err)
	}
	if key == other {
		t.Fatal("不同输入应得到不同密钥")
	}
}

func TestRejectsInvalidKeys(t *testing.T) {
	for _, value := range []string{"short", strings.Repeat("00", KeySize)} {
		if _, err := LoadOrCreate(filepath.Join(t.TempDir(), "master.key"), value); err == nil {
			t.Fatalf("非法密钥应被拒绝：%q", value)
		}
	}

	path := filepath.Join(t.TempDir(), "master.key")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("写入空文件失败：%v", err)
	}
	if _, err := LoadOrCreate(path, ""); err == nil {
		t.Fatal("空主密钥文件不应被静默替换")
	}
}

func TestSealOpenRoundTripAndAAD(t *testing.T) {
	key := newTestKey(t)
	plaintext := []byte("-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----\n")
	aad := []byte("cert-key:profile-1")

	blob, err := key.Seal(plaintext, aad)
	if err != nil {
		t.Fatalf("加密失败：%v", err)
	}
	if bytes.Contains(blob, plaintext) {
		t.Fatal("密文不应包含明文")
	}

	got, err := key.Open(blob, aad)
	if err != nil {
		t.Fatalf("解密失败：%v", err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("解密结果不一致：%q", got)
	}

	if _, err := key.Open(blob, []byte("other")); err == nil {
		t.Fatal("AAD 不匹配时应解密失败")
	}

	blob[len(blob)-1] ^= 0xff
	if _, err := key.Open(blob, aad); err == nil {
		t.Fatal("篡改密文应解密失败")
	}
}
