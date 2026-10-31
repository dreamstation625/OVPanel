package bootstrap

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGeneratesStableToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bootstrap-token")

	first, err := LoadOrCreate(path, "", false)
	if err != nil {
		t.Fatalf("生成口令失败：%v", err)
	}
	if !first.Generated || first.Path != path || len(first.Value) < MinTokenLength {
		t.Fatalf("生成结果异常：%+v", first)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取口令文件失败：%v", err)
	}
	if got := strings.TrimSpace(string(data)); got != first.Value {
		t.Fatalf("文件内容 = %q，期望 %q", got, first.Value)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("读取文件信息失败：%v", err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("口令文件权限 = %o，期望 600", mode)
		}
	}

	second, err := LoadOrCreate(path, "", false)
	if err != nil {
		t.Fatalf("重新读取失败：%v", err)
	}
	if second.Generated || second.Value != first.Value {
		t.Fatalf("重启后口令应保持不变：%+v", second)
	}
}

func TestEnvTokenWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bootstrap-token")

	tok, err := LoadOrCreate(path, "  external-token-1234  ", false)
	if err != nil {
		t.Fatalf("读取环境口令失败：%v", err)
	}
	if !tok.FromEnv || tok.Value != "external-token-1234" {
		t.Fatalf("环境口令未被采用：%+v", tok)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("使用环境口令时不应创建文件：%v", err)
	}
}

func TestRejectsShortEnvToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bootstrap-token")
	if _, err := LoadOrCreate(path, "short", false); err == nil {
		t.Fatal("过短的外部口令应被拒绝")
	}
}

func TestInitializedInstanceRemovesToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bootstrap-token")
	if _, err := LoadOrCreate(path, "", false); err != nil {
		t.Fatalf("生成口令失败：%v", err)
	}

	tok, err := LoadOrCreate(path, "", true)
	if err != nil {
		t.Fatalf("清理失败：%v", err)
	}
	if tok.Value != "" || tok.Path != "" {
		t.Fatalf("已初始化时不应返回口令：%+v", tok)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("已初始化后口令文件应被删除：%v", err)
	}
}

func TestRecoversFromEmptyTokenFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bootstrap-token")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("写入空文件失败：%v", err)
	}

	tok, err := LoadOrCreate(path, "", false)
	if err != nil {
		t.Fatalf("空文件应可恢复：%v", err)
	}
	if !tok.Generated || len(tok.Value) < MinTokenLength {
		t.Fatalf("应重新生成口令：%+v", tok)
	}
}

func TestRemoveMissingFileIsOK(t *testing.T) {
	if err := Remove(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatalf("删除不存在的口令不应报错：%v", err)
	}
}
