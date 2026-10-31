// Package secretbox 用 AES-256-GCM 保护持久化中的私钥与共享密钥。
//
// 主密钥独立于数据库：可通过 OVPANEL_MASTER_KEY 注入，或首次启动时
// 生成到数据目录的 master.key（0600）。数据库泄露但主密钥未泄露时，
// 证书私钥与 tls-crypt 密钥仍是密文。失去主密钥将无法解密已有材料。
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// KeySize 是 AES-256 的密钥长度。
const KeySize = 32

// ErrInvalidKey 表示主密钥缺失、长度错误或格式非法。
var ErrInvalidKey = errors.New("主密钥必须是 32 字节")

// Key 是不透明的主密钥。
type Key struct {
	bytes [KeySize]byte
}

// LoadOrCreate 读取或生成主密钥。
//
// encoded 非空时优先使用它；否则读取 path，文件不存在时生成并写入。
// 空文件和非法内容不会被静默替换，避免覆盖后让已有密文永久不可解。
func LoadOrCreate(path, encoded string) (Key, error) {
	if strings.TrimSpace(encoded) != "" {
		return parseEncoded(strings.TrimSpace(encoded))
	}

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		value := strings.TrimSpace(string(data))
		if value == "" {
			return Key{}, fmt.Errorf("主密钥文件为空：%s", path)
		}
		key, err := parseEncoded(value)
		if err != nil {
			return Key{}, fmt.Errorf("解析主密钥 %s: %w", path, err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return Key{}, fmt.Errorf("收紧主密钥权限 %s: %w", path, err)
		}
		return key, nil
	case !errors.Is(err, os.ErrNotExist):
		return Key{}, fmt.Errorf("读取主密钥 %s: %w", path, err)
	}

	key, err := generate()
	if err != nil {
		return Key{}, err
	}
	if err := write(path, key); err != nil {
		// 并发启动时另一个进程可能已写入；以磁盘上的文件为准。
		if existing, readErr := LoadOrCreate(path, ""); readErr == nil {
			return existing, nil
		}
		return Key{}, err
	}
	return key, nil
}

// Seal 加密明文，返回包含随机 nonce 的密文。
func (k Key) Seal(plaintext, aad []byte) ([]byte, error) {
	if k.isZero() {
		return nil, ErrInvalidKey
	}
	gcm, err := k.gcm()
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("生成加密 nonce: %w", err)
	}
	out := make([]byte, len(nonce), len(nonce)+len(plaintext)+gcm.Overhead())
	copy(out, nonce)
	return gcm.Seal(out, nonce, plaintext, aad), nil
}

// Open 解密 Seal 生成的数据。
func (k Key) Open(blob, aad []byte) ([]byte, error) {
	if k.isZero() {
		return nil, ErrInvalidKey
	}
	gcm, err := k.gcm()
	if err != nil {
		return nil, err
	}
	if len(blob) < gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("密文长度非法")
	}
	nonce := blob[:gcm.NonceSize()]
	plaintext, err := gcm.Open(nil, nonce, blob[gcm.NonceSize():], aad)
	if err != nil {
		return nil, fmt.Errorf("解密失败: %w", err)
	}
	return plaintext, nil
}

func (k Key) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(k.bytes[:])
	if err != nil {
		return nil, fmt.Errorf("创建 AES: %w", err)
	}
	return cipher.NewGCM(block)
}

func (k Key) isZero() bool {
	var zero [KeySize]byte
	return k.bytes == zero
}

func generate() (Key, error) {
	var key Key
	if _, err := rand.Read(key.bytes[:]); err != nil {
		return Key{}, fmt.Errorf("生成主密钥: %w", err)
	}
	return key, nil
}

func write(path string, key Key) error {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("创建主密钥目录 %s: %w", dir, err)
		}
	}

	encoded := base64.RawURLEncoding.EncodeToString(key.bytes[:]) + "\n"
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("写入主密钥 %s: %w", path, err)
	}
	if _, err := f.WriteString(encoded); err != nil {
		f.Close()
		return fmt.Errorf("写入主密钥 %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("关闭主密钥 %s: %w", path, err)
	}
	return nil
}

func parseEncoded(value string) (Key, error) {
	var raw []byte
	var err error
	for _, decode := range []func(string) ([]byte, error){
		base64.RawURLEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.StdEncoding.DecodeString,
		hex.DecodeString,
	} {
		if raw, err = decode(value); err == nil && len(raw) == KeySize {
			break
		}
		raw = nil
	}
	if len(raw) != KeySize {
		return Key{}, ErrInvalidKey
	}

	var key Key
	copy(key.bytes[:], raw)
	if key.isZero() {
		return Key{}, ErrInvalidKey
	}
	return key, nil
}
