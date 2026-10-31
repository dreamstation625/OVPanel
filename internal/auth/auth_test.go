package auth

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestHashAndVerifyRoundTrip(t *testing.T) {
	encoded, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("生成哈希失败：%v", err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$") {
		t.Fatalf("哈希格式异常：%s", encoded)
	}

	ok, err := VerifyPassword(encoded, "correct horse battery staple")
	if err != nil {
		t.Fatalf("校验失败：%v", err)
	}
	if !ok {
		t.Fatal("正确密码应通过校验")
	}
}

func TestVerifyRejectsWrongPassword(t *testing.T) {
	encoded, err := HashPassword("secret-one")
	if err != nil {
		t.Fatalf("生成哈希失败：%v", err)
	}

	ok, err := VerifyPassword(encoded, "secret-two")
	if err != nil {
		t.Fatalf("校验失败：%v", err)
	}
	if ok {
		t.Fatal("错误密码不应通过校验")
	}
}

// TestHashIsSalted 确认同一密码两次哈希结果不同（盐值随机）。
func TestHashIsSalted(t *testing.T) {
	a, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("生成哈希失败：%v", err)
	}
	b, err := HashPassword("same-password")
	if err != nil {
		t.Fatalf("生成哈希失败：%v", err)
	}
	if a == b {
		t.Fatal("同一密码的两次哈希应不同")
	}

	// 但两者都必须能校验通过。
	for _, h := range []string{a, b} {
		ok, err := VerifyPassword(h, "same-password")
		if err != nil || !ok {
			t.Fatalf("哈希 %s 校验失败：%v", h, err)
		}
	}
}

// TestVerifyRejectsMalformedHash 确认非法哈希串返回错误而不是误判通过。
func TestVerifyRejectsMalformedHash(t *testing.T) {
	cases := []string{
		"",
		"plain-text",
		"$argon2id$v=19$m=65536,t=3,p=4$onlyone",
		"$bcrypt$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA",
		"$argon2id$v=99$m=65536,t=3,p=4$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=0,t=0,p=0$c2FsdA$aGFzaA",
	}
	for _, c := range cases {
		if _, err := VerifyPassword(c, "whatever"); err == nil {
			t.Fatalf("非法哈希应返回错误：%q", c)
		}
	}
}

func TestHashRejectsEmptyPassword(t *testing.T) {
	if _, err := HashPassword("   "); err == nil {
		t.Fatal("空密码应被拒绝")
	}
}

// TestParamsAreReadFromHash 确认校验使用哈希串内记录的参数。
func TestParamsAreReadFromHash(t *testing.T) {
	// 用较弱参数直接构造哈希，模拟历史数据。
	salt := []byte("0123456789abcdef")
	key := argon2KeyForTest("legacy-password", salt, 1, 8*1024, 1)
	encoded := encodeForTest(salt, key, 8*1024, 1, 1)

	ok, err := VerifyPassword(encoded, "legacy-password")
	if err != nil {
		t.Fatalf("校验失败：%v", err)
	}
	if !ok {
		t.Fatal("使用历史参数生成的哈希应可校验")
	}
}

func TestLimiterBoundsConcurrency(t *testing.T) {
	l := NewLimiter(2)
	ctx := context.Background()

	r1, err := l.Acquire(ctx)
	if err != nil {
		t.Fatalf("取得名额失败：%v", err)
	}
	r2, err := l.Acquire(ctx)
	if err != nil {
		t.Fatalf("取得名额失败：%v", err)
	}

	// 第三个名额必须等待；用已取消的上下文验证不会永久阻塞。
	cctx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if _, err := l.Acquire(cctx); err == nil {
		t.Fatal("超出上限时应等待而不是立即成功")
	}

	r1()
	if _, err := l.Acquire(ctx); err != nil {
		t.Fatalf("释放后应能取得名额：%v", err)
	}
	r2()
}

func TestSessionTokenIsUniqueAndHashed(t *testing.T) {
	t1, h1, err := NewSessionToken()
	if err != nil {
		t.Fatalf("生成令牌失败：%v", err)
	}
	t2, h2, err := NewSessionToken()
	if err != nil {
		t.Fatalf("生成令牌失败：%v", err)
	}
	if t1 == t2 {
		t.Fatal("两次生成的令牌应不同")
	}
	if len(h1) != 32 || len(h2) != 32 {
		t.Fatalf("令牌哈希长度异常：%d %d", len(h1), len(h2))
	}
	if string(h1) == t1 {
		t.Fatal("数据库中不应保存令牌明文")
	}
	// 同一令牌再次计算哈希必须稳定。
	if string(HashToken(t1)) != string(h1) {
		t.Fatal("令牌哈希应可重复计算")
	}
}
