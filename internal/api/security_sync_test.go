package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dreamstation625/ovpanel/internal/admission"
	"github.com/dreamstation625/ovpanel/internal/agentproto"
)

func setupRuntimeSync(t *testing.T) (*Server, http.Handler, []*http.Cookie, string) {
	t.Helper()
	srv := newTestServer(t)
	h, cookies := setupAndLogin(t, srv)
	created := doJSON(t, h, http.MethodPost, "/api/users", "127.0.0.1:5000", map[string]any{
		"displayName": "安全回归", "profile": map[string]any{"name": "client", "authType": "certificate"},
	}, cookies...)
	if created.Code != http.StatusCreated {
		t.Fatal(created.Body.String())
	}
	var result struct {
		User userDTO `json:"user"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	saved := doJSON(t, h, http.MethodPut, "/api/settings/export", "127.0.0.1:5000", map[string]any{
		"host": "vpn.example.test", "port": 1194, "proto": "udp",
	}, cookies...)
	if saved.Code != http.StatusOK {
		t.Fatal(saved.Body.String())
	}
	if _, err := srv.runtime.Apply(t.Context()); err != nil {
		t.Fatal(err)
	}
	return srv, h, cookies, result.User.ID
}

func TestDisableSyncIgnoresExportPortAndRequiresAgentConfirmation(t *testing.T) {
	srv, h, cookies, userID := setupRuntimeSync(t)
	path := filepath.Join(srv.cfg.ControlDir, "bundle.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := doJSON(t, h, http.MethodPut, "/api/settings/export", "127.0.0.1:5000", map[string]any{
		"host": "vpn.example.test", "port": 21194, "proto": "udp",
	}, cookies...)
	if changed.Code != http.StatusOK {
		t.Fatal(changed.Body.String())
	}
	disabled := doJSON(t, h, http.MethodPatch, "/api/users/"+userID, "127.0.0.1:5000", map[string]any{"status": "disabled"}, cookies...)
	if disabled.Code != http.StatusAccepted || disabled.Header().Get("X-OVPanel-Runtime-State") != "pending" {
		t.Fatalf("未收到代理确认时应明确返回 202：%d %s", disabled.Code, disabled.Body.String())
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var old, updated agentproto.Bundle
	if err := json.Unmarshal(before, &old); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(after, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.ConfigHash != old.ConfigHash || updated.Config != old.Config || updated.TLSCryptKey != old.TLSCryptKey {
		t.Fatal("凭据更新不能重写服务端配置或 tls-crypt 材料")
	}
	incoming := admission.Incoming{CertPresent: true, CertVerified: true, CertSerial: old.Snapshot.Profiles[0].CertSerial}
	if decision := admission.Decide(updated.Snapshot, incoming, time.Now().UTC()); decision.Allow {
		t.Fatal("停用后下发的快照仍接受旧证书")
	}
	for _, raw := range [][]byte{before, after} {
		sum := sha256.Sum256(raw)
		status, _ := json.Marshal(map[string]string{"bundleHash": hex.EncodeToString(sum[:])})
		if err := os.WriteFile(filepath.Join(srv.cfg.ControlDir, "status.json"), status, 0o600); err != nil {
			t.Fatal(err)
		}
		pending, err := srv.runtime.AccountsPending(t.Context())
		if err != nil || pending != (string(raw) != string(after)) {
			t.Fatalf("必须只接受最新控制包的确认：pending=%v err=%v", pending, err)
		}
	}
	stable, err := srv.runtime.SyncAccounts(t.Context())
	if err != nil || stable {
		t.Fatalf("已确认且未变化的快照不应反复下发：%v %v", stable, err)
	}
	unchanged, _ := os.ReadFile(path)
	if string(unchanged) != string(after) {
		t.Fatal("后台核对不应不断改写已确认的控制包")
	}
}

func TestAccountSyncFailureIsVisibleAndRetriedFromDatabase(t *testing.T) {
	srv, h, cookies, userID := setupRuntimeSync(t)
	path := filepath.Join(srv.cfg.ControlDir, "bundle.json")
	backupPath := path + ".test-backup"
	if err := os.Rename(path, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	disabled := doJSON(t, h, http.MethodPatch, "/api/users/"+userID, "127.0.0.1:5000", map[string]any{"status": "disabled"}, cookies...)
	if disabled.Code != http.StatusAccepted || disabled.Header().Get("X-OVPanel-Runtime-State") != "failed" {
		t.Fatalf("下发失败不能冒充已生效：%d %s", disabled.Code, disabled.Body.String())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backupPath, path); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.runtime.SyncAccounts(t.Context()); err != nil {
		t.Fatalf("排除 I/O 故障后应能从数据库补发：%v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var bundle agentproto.Bundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Snapshot.Users[0].Status != "disabled" {
		t.Fatal("重试没有补发已保存的撤销状态")
	}
}
