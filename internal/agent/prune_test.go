package agent

import (
	"bufio"
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/dreamstation625/ovpanel/internal/admission"
)

func TestPruneSessionsKillsUnavailable(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(time.Hour)
	rt := New(t.TempDir(), t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	rt.now = func() time.Time { return now }
	rt.snapshot = admission.Snapshot{
		Users: []admission.User{
			{ID: "u1", Status: "active"},
			{ID: "u2", Status: "active"},
		},
		Profiles: []admission.Profile{
			{
				ID: "A", UserID: "u1", AuthType: admission.AuthCertificate, Status: "active",
				CertSerial: "0A1B", CertStatus: "active", CertExpiresAt: &future,
			},
			{
				ID: "B", UserID: "u2", AuthType: admission.AuthPassword,
				Username: "bob", PasswordHash: "hash", Status: "disabled",
			},
		},
	}
	rt.sessions = map[string]sessionInfo{
		"1": {profileID: "A", certSerial: "0A1B"},
		"2": {profileID: "B"},
		"3": {profileID: "A", certSerial: "0C0D"},
	}

	var buf bytes.Buffer
	writer := bufio.NewWriter(&buf)
	rt.mgmtWriter = writer
	rt.pruneSessions()
	writer.Flush()

	commands := buf.String()
	if !strings.Contains(commands, "client-kill 2") {
		t.Fatalf("停用配置的会话应被断开：%q", commands)
	}
	if !strings.Contains(commands, "client-kill 3") {
		t.Fatalf("证书轮换后的旧会话应被断开：%q", commands)
	}
	if strings.Contains(commands, "client-kill 1") {
		t.Fatalf("仍然可用的会话不应被断开：%q", commands)
	}
	if strings.Contains(commands, "配置或证书已变更") {
		t.Fatalf("client-kill 不应附带自由文本：%q", commands)
	}
	if _, ok := rt.sessions["1"]; !ok {
		t.Fatal("可用会话未被保留")
	}
	if _, ok := rt.sessions["2"]; ok {
		t.Fatal("停用会话仍在映射中")
	}
}
