// Package auth 提供密码哈希与会话令牌。
//
// 密码使用 Argon2id，参数随哈希串一起保存，因此后续调整参数
// 不会让已有密码失效；校验时按哈希串里的参数重算。
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params 是 Argon2id 参数。
type Params struct {
	Memory      uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultParams 是当前默认参数（64 MiB / 3 轮 / 4 并行）。
//
// 参数写进哈希串，历史哈希仍可校验；如需提高强度，改这里即可。
var DefaultParams = Params{
	Memory:      64 * 1024,
	Iterations:  3,
	Parallelism: 4,
	SaltLength:  16,
	KeyLength:   32,
}

// 错误值。调用方据此决定响应码，不解析字符串。
var (
	ErrMalformedHash = errors.New("密码哈希格式非法")
	ErrEmptyPassword = errors.New("密码不能为空")
)

// HashPassword 生成 PHC 格式的 Argon2id 哈希。
func HashPassword(password string) (string, error) {
	if strings.TrimSpace(password) == "" {
		return "", ErrEmptyPassword
	}
	salt := make([]byte, DefaultParams.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成盐值: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt,
		DefaultParams.Iterations, DefaultParams.Memory, DefaultParams.Parallelism, DefaultParams.KeyLength)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		DefaultParams.Memory,
		DefaultParams.Iterations,
		DefaultParams.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword 校验密码是否与哈希串匹配。
//
// 参数从哈希串解析，因此不同参数生成的旧哈希仍可验证。
func VerifyPassword(encoded, password string) (bool, error) {
	p, salt, expected, err := decodeHash(encoded)
	if err != nil {
		return false, err
	}

	actual := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLength)
	// 使用常数时间比较，避免通过响应时间推断正确前缀。
	return subtle.ConstantTimeCompare(expected, actual) == 1, nil
}

func decodeHash(encoded string) (Params, []byte, []byte, error) {
	var zero Params

	parts := strings.Split(encoded, "$")
	// 形如 ["", "argon2id", "v=19", "m=65536,t=3,p=4", salt, key]
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return zero, nil, nil, ErrMalformedHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return zero, nil, nil, ErrMalformedHash
	}
	if version != argon2.Version {
		return zero, nil, nil, fmt.Errorf("%w：不支持的版本 %d", ErrMalformedHash, version)
	}

	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Iterations, &p.Parallelism); err != nil {
		return zero, nil, nil, ErrMalformedHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return zero, nil, nil, ErrMalformedHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return zero, nil, nil, ErrMalformedHash
	}

	p.SaltLength = uint32(len(salt))
	p.KeyLength = uint32(len(key))
	if p.Memory == 0 || p.Iterations == 0 || p.Parallelism == 0 {
		return zero, nil, nil, ErrMalformedHash
	}
	return p, salt, key, nil
}

// Limiter 限制同时进行的密码校验数量。
//
// Argon2id 每次校验占用数十 MiB 内存，并发过高会把容器内存打满；
// 这里用信号量把并发压到固定上限。
type Limiter struct {
	slots chan struct{}
}

// NewLimiter 创建并发限制器。n 小于 1 时按 CPU 数计算。
func NewLimiter(n int) *Limiter {
	if n < 1 {
		n = runtime.NumCPU()
	}
	if n > 4 {
		n = 4
	}
	return &Limiter{slots: make(chan struct{}, n)}
}

// Acquire 取得一个校验名额，返回释放函数。
func (l *Limiter) Acquire(ctx context.Context) (func(), error) {
	select {
	case l.slots <- struct{}{}:
		return func() { <-l.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// NewSessionToken 生成会话令牌及其存储用哈希。
//
// 明文令牌只回给浏览器，数据库只保存哈希；
// 即使数据库泄露也无法直接冒用会话。
func NewSessionToken() (token string, hash []byte, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("生成会话令牌: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, HashToken(token), nil
}

// HashToken 计算令牌的 SHA-256 摘要，用作数据库主键。
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
