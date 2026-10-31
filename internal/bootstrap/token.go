// Package bootstrap 管理首次初始化的一次性口令。
//
// 口令只用于「面板尚未创建管理员」的最初阶段：远端请求必须携带它，
// 管理员创建成功后立即删除文件；初始化完成后接口会永久关闭，
// 即使口令仍在磁盘上也不能再次使用。
package bootstrap

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// MinTokenLength 是外部环境变量口令的最小长度。
const MinTokenLength = 16

// maxTokenFileBytes 防止从被替换的异常文件读入巨量内容。
const maxTokenFileBytes = 4096

// Token 是本次启动使用的初始化口令。
type Token struct {
	Value     string
	Path      string
	FromEnv   bool
	Generated bool
}

// LoadOrCreate 读取或生成初始化口令。
//
// 已初始化时不会生成新口令，并会清理残留的口令文件。
// 外部显式提供的口令优先于文件，便于通过容器密钥注入。
func LoadOrCreate(path, envToken string, initialized bool) (Token, error) {
	if initialized {
		if err := Remove(path); err != nil {
			return Token{}, err
		}
		return Token{}, nil
	}

	envToken = strings.TrimSpace(envToken)
	if envToken != "" {
		if len(envToken) < MinTokenLength {
			return Token{}, fmt.Errorf("初始化口令至少需要 %d 个字符", MinTokenLength)
		}
		return Token{Value: envToken, FromEnv: true}, nil
	}

	if value, ok, err := readToken(path); err != nil {
		return Token{}, err
	} else if ok && value != "" {
		return Token{Value: value, Path: path}, nil
	} else if ok {
		// 上次写入可能在中途退出，留下空文件；删除后重新生成。
		if err := os.Remove(path); err != nil {
			return Token{}, fmt.Errorf("删除空的引导口令 %s: %w", path, err)
		}
	}

	value, err := generate()
	if err != nil {
		return Token{}, err
	}
	if err := writeToken(path, value); err != nil {
		// 另一个进程可能刚写完同一文件；此时以磁盘上的口令为准。
		if existing, ok, readErr := readToken(path); readErr == nil && ok {
			return Token{Value: existing, Path: path}, nil
		}
		return Token{}, err
	}
	return Token{Value: value, Path: path, Generated: true}, nil
}

// Remove 删除引导口令文件；文件不存在视为成功。
func Remove(path string) error {
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("删除引导口令 %s: %w", path, err)
	}
	return nil
}

func readToken(path string) (string, bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("读取引导口令信息 %s: %w", path, err)
	}
	if info.Size() > maxTokenFileBytes {
		return "", false, fmt.Errorf("引导口令文件过大：%s", path)
	}
	// 旧文件可能由更宽松的 umask 创建；读取时顺带收紧权限。
	if err := os.Chmod(path, 0o600); err != nil {
		return "", false, fmt.Errorf("收紧引导口令权限 %s: %w", path, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, fmt.Errorf("读取引导口令 %s: %w", path, err)
	}
	return strings.TrimSpace(string(data)), true, nil
}

func writeToken(path, value string) error {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("创建引导口令目录 %s: %w", dir, err)
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("写入引导口令 %s: %w", path, err)
	}
	if _, err := f.WriteString(value + "\n"); err != nil {
		f.Close()
		return fmt.Errorf("写入引导口令 %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("关闭引导口令 %s: %w", path, err)
	}
	return nil
}

func generate() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("生成引导口令: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
