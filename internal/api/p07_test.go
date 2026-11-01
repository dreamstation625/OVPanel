package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dreamstation625/ovpanel/internal/maintapi"
	"github.com/dreamstation625/ovpanel/internal/maintclient"
)

func startFakeMaintenance(t *testing.T) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "maint.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("当前平台无法创建 Unix Socket：%v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/state", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(maintapi.State{
			Available:         true,
			AllowedRepository: "dreamstation625/ovpanel-openvpn",
			Container:         &maintapi.Container{Name: "ovpanel-openvpn", Image: "dreamstation625/ovpanel-openvpn:0.0.1", Running: true, Health: "healthy"},
		})
	})
	mux.HandleFunc("POST /v1/update", func(w http.ResponseWriter, r *http.Request) {
		var req maintapi.UpdateRequest
		json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(map[string]any{"task": maintapi.Task{
			ID: "task-1", State: maintapi.TaskPending, Phase: "准备",
			TargetImage: "dreamstation625/ovpanel-openvpn:" + req.TargetTag,
			OldImage:    "dreamstation625/ovpanel-openvpn:0.0.1",
			BackupID:    req.BackupID, StartedAt: time.Now().UTC(),
		}})
	})
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	return socket
}

func TestP07MaintenanceUnavailableState(t *testing.T) {
	srv := newTestServer(t)
	h, cookies := setupAndLogin(t, srv)
	state := doJSON(t, h, http.MethodGet, "/api/maintenance/openvpn", "127.0.0.1:5000", nil, cookies...)
	if state.Code != http.StatusOK || !strings.Contains(state.Body.String(), "更新服务未配置") {
		t.Fatalf("维护状态异常：%d %s", state.Code, state.Body.String())
	}
	update := doJSON(t, h, http.MethodPost, "/api/maintenance/openvpn/update", "127.0.0.1:5000", map[string]any{
		"adminPassword": "a-long-enough-password",
		"targetTag":     "0.0.2",
	}, cookies...)
	if update.Code != http.StatusServiceUnavailable {
		t.Fatalf("维护进程缺失时应拒绝更新：%d %s", update.Code, update.Body.String())
	}
}

func TestP07UpdateRequiresBackupThenStartsTask(t *testing.T) {
	srv := newTestServer(t)
	srv.maintenance = maintclient.New(startFakeMaintenance(t))
	h, cookies := setupAndLogin(t, srv)

	blocked := doJSON(t, h, http.MethodPost, "/api/maintenance/openvpn/update", "127.0.0.1:5000", map[string]any{
		"adminPassword": "a-long-enough-password",
		"targetTag":     "0.0.2",
	}, cookies...)
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), "恢复密码") {
		t.Fatalf("未配置备份时不应启动更新：%d %s", blocked.Code, blocked.Body.String())
	}

	settings := doJSON(t, h, http.MethodPut, "/api/backups/settings", "127.0.0.1:5000", map[string]any{
		"backupPassword": "correct-horse-backup-password",
		"enabled":        false,
		"scheduleTime":   "03:00",
		"retentionCount": 7,
	}, cookies...)
	if settings.Code != http.StatusOK {
		t.Fatalf("配置备份失败：%d %s", settings.Code, settings.Body.String())
	}

	started := doJSON(t, h, http.MethodPost, "/api/maintenance/openvpn/update", "127.0.0.1:5000", map[string]any{
		"adminPassword": "a-long-enough-password",
		"targetTag":     "0.0.2",
	}, cookies...)
	if started.Code != http.StatusAccepted {
		t.Fatalf("启动更新失败：%d %s", started.Code, started.Body.String())
	}
	var result struct {
		Task   maintapi.Task `json:"task"`
		Backup backupDTO     `json:"backup"`
	}
	if err := json.Unmarshal(started.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Task.ID != "task-1" || result.Backup.Kind != "pre_restore" || result.Task.BackupID != result.Backup.ID {
		t.Fatalf("更新任务或升级前备份异常：%+v", result)
	}
}
