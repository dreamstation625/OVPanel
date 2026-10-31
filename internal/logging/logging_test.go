package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactsTopLevelSensitiveKeys(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "debug")

	log.Info("login attempt", "password", "super-secret", "user", "admin")

	out := buf.String()
	if strings.Contains(out, "super-secret") {
		t.Fatalf("密码未脱敏：%s", out)
	}
	if !strings.Contains(out, redacted) {
		t.Fatalf("缺少脱敏标记：%s", out)
	}
	if !strings.Contains(out, "admin") {
		t.Fatalf("非敏感字段不应被脱敏：%s", out)
	}
}

func TestRedactsNestedGroups(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "debug")

	log.Info("request", "auth", map[string]any{
		"password": "nested-secret",
		"user":     "admin",
	})

	out := buf.String()
	if strings.Contains(out, "nested-secret") {
		t.Fatalf("嵌套密码未脱敏：%s", out)
	}
}

func TestRedactsWithAttrs(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "debug").With("session_token", "abc123")

	log.Info("served")

	out := buf.String()
	if strings.Contains(out, "abc123") {
		t.Fatalf("会话令牌未脱敏：%s", out)
	}
}

func TestKeepsOrdinaryFields(t *testing.T) {
	var buf bytes.Buffer
	log := New(&buf, "debug")

	log.Info("migrated", "schema", 3, "duration_ms", 12)

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("日志应为合法 JSON：%v", err)
	}
	if entry["schema"] != float64(3) {
		t.Fatalf("schema 字段丢失：%v", entry)
	}
	if entry["msg"] != "migrated" {
		t.Fatalf("msg 字段异常：%v", entry)
	}
}
