package auth

import (
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// argon2KeyForTest 与 encodeForTest 只用于测试历史参数哈希的兼容性，
// 不参与生产路径。
func argon2KeyForTest(password string, salt []byte, iterations uint32, memory uint32, parallelism uint8) []byte {
	return argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, 32)
}

func encodeForTest(salt, key []byte, memory uint32, iterations uint32, parallelism uint8) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memory, iterations, parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)
}
