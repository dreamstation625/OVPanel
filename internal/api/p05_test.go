package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dreamstation625/ovpanel/internal/telemetry"
)

func writeTelemetryFixture(t *testing.T, controlDir, profileID string, connectedAt time.Time) {
	t.Helper()
	if err := os.MkdirAll(controlDir, 0o700); err != nil {
		t.Fatal(err)
	}
	events := []telemetry.Event{
		{
			Version: 1, Seq: 1, Generation: "g1", Kind: telemetry.EventSessionStart,
			At: connectedAt, SessionID: "g1:1", CID: "1", ProfileID: profileID,
			VPNIP: "10.8.0.2", SourceIP: "203.0.113.7", ConnectedAt: &connectedAt,
		},
		{
			Version: 1, Seq: 2, Generation: "g1", Kind: telemetry.EventUsage,
			At: connectedAt.Add(5 * time.Minute), SessionID: "g1:1", CID: "1", ProfileID: profileID,
			VPNIP: "10.8.0.2", SourceIP: "203.0.113.7", UpBytes: 100, DownBytes: 200,
			DeltaUp: 100, DeltaDown: 200,
		},
	}
	file, err := os.Create(filepath.Join(controlDir, "events-active.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(append(data, '\n')); err != nil {
			t.Fatal(err)
		}
	}

	snapshot := telemetry.Snapshot{
		Version: 1, Generation: "g1", UpdatedAt: time.Now().UTC(), EventSeq: 2,
		Profiles: []telemetry.ProfileUsage{{ProfileID: profileID, UsageBytes: 100, TotalBytes: 300}},
		Sessions: []telemetry.LiveSession{{
			ID: "g1:1", CID: "1", ProfileID: profileID, VPNIP: "10.8.0.2", SourceIP: "203.0.113.7",
			ConnectedAt: connectedAt, UpBytes: 100, DownBytes: 200, UpRate: 10, DownRate: 20,
		}},
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlDir, telemetry.SnapshotFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestP05LimitsQuotaAndStatistics(t *testing.T) {
	srv := newTestServer(t)
	h, cookies := setupAndLogin(t, srv)
	base := srv.now()

	create := doJSON(t, h, http.MethodPost, "/api/users", "127.0.0.1:5000", map[string]any{
		"displayName": "李四",
		"profile": map[string]any{
			"name":     "笔记本",
			"authType": "certificate",
		},
	}, cookies...)
	if create.Code != http.StatusCreated {
		t.Fatalf("创建用户失败：%d %s", create.Code, create.Body.String())
	}
	var created struct {
		Profile profileDTO `json:"profile"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	profileID := created.Profile.ID

	// 限额保存与校验。
	limits := doJSON(t, h, http.MethodPut, "/api/profiles/"+profileID+"/limits", "127.0.0.1:5000", map[string]any{
		"expiresAt":       base.Add(24 * time.Hour).Format(time.RFC3339),
		"concurrentLimit": 2,
		"quotaBytes":      1_000_000_000,
		"uploadBps":       5_000_000,
		"downloadBps":     10_000_000,
	}, cookies...)
	if limits.Code != http.StatusOK {
		t.Fatalf("保存限额失败：%d %s", limits.Code, limits.Body.String())
	}
	var limited struct {
		Profile profileDTO `json:"profile"`
	}
	if err := json.Unmarshal(limits.Body.Bytes(), &limited); err != nil {
		t.Fatal(err)
	}
	if limited.Profile.ConcurrentLimit != 2 || limited.Profile.QuotaBytes != 1_000_000_000 ||
		limited.Profile.UploadBps != 5_000_000 || limited.Profile.DownloadBps != 10_000_000 {
		t.Fatalf("限额未保存：%+v", limited.Profile)
	}

	bad := doJSON(t, h, http.MethodPut, "/api/profiles/"+profileID+"/limits", "127.0.0.1:5000", map[string]any{
		"quotaBytes": 1,
	}, cookies...)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("非法额度应被拒绝：%d %s", bad.Code, bad.Body.String())
	}

	// 写入运行代理实时快照与事件，统计接口应同步并展示。
	writeTelemetryFixture(t, filepath.Join(srv.cfg.DataDir, "control"), profileID, base.Add(-10*time.Minute))

	overview := doJSON(t, h, http.MethodGet, "/api/overview", "127.0.0.1:5000", nil, cookies...)
	if overview.Code != http.StatusOK {
		t.Fatalf("概览失败：%d %s", overview.Code, overview.Body.String())
	}
	var summary overviewResponse
	if err := json.Unmarshal(overview.Body.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if !summary.AgentPresent || summary.OnlineConnections != 1 || summary.TodayUpBytes != 100 || summary.TodayDownBytes != 200 {
		t.Fatalf("概览统计异常：%+v", summary)
	}

	connections := doJSON(t, h, http.MethodGet, "/api/connections", "127.0.0.1:5000", nil, cookies...)
	if connections.Code != http.StatusOK || !strings.Contains(connections.Body.String(), "笔记本") {
		t.Fatalf("在线连接异常：%d %s", connections.Code, connections.Body.String())
	}

	history := doJSON(t, h, http.MethodGet, "/api/connections/history?limit=10", "127.0.0.1:5000", nil, cookies...)
	if history.Code != http.StatusOK || !strings.Contains(history.Body.String(), "10.8.0.2") {
		t.Fatalf("连接历史异常：%d %s", history.Code, history.Body.String())
	}

	trend := doJSON(t, h, http.MethodGet, "/api/usage/trend?days=7", "127.0.0.1:5000", nil, cookies...)
	if trend.Code != http.StatusOK || !strings.Contains(trend.Body.String(), "2026-10-30") {
		t.Fatalf("流量趋势异常：%d %s", trend.Code, trend.Body.String())
	}

	detail := doJSON(t, h, http.MethodGet, "/api/profiles/"+profileID, "127.0.0.1:5000", nil, cookies...)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"usageBytes":100`) ||
		!strings.Contains(detail.Body.String(), `"onlineSessions":1`) {
		t.Fatalf("配置用量未富集：%d %s", detail.Code, detail.Body.String())
	}

	// 断开指定连接会写入控制命令。
	disconnect := doJSON(t, h, http.MethodPost, "/api/connections/g1:1/disconnect", "127.0.0.1:5000", map[string]any{}, cookies...)
	if disconnect.Code != http.StatusOK {
		t.Fatalf("断开连接失败：%d %s", disconnect.Code, disconnect.Body.String())
	}
	commandData, err := os.ReadFile(filepath.Join(srv.cfg.DataDir, "control", "command.json"))
	if err != nil {
		t.Fatalf("未写入控制命令：%v", err)
	}
	var command map[string]string
	if err := json.Unmarshal(commandData, &command); err != nil || command["action"] != "disconnect" || command["cid"] != "1" {
		t.Fatalf("控制命令异常：%s %v", commandData, err)
	}

	// 额度重置需要管理员密码并推进代次。
	wrong := doJSON(t, h, http.MethodPost, "/api/profiles/"+profileID+"/quota/reset", "127.0.0.1:5000",
		map[string]string{"adminPassword": "wrong-password"}, cookies...)
	if wrong.Code != http.StatusForbidden {
		t.Fatalf("错误管理员密码应被拒绝：%d", wrong.Code)
	}
	reset := doJSON(t, h, http.MethodPost, "/api/profiles/"+profileID+"/quota/reset", "127.0.0.1:5000",
		map[string]string{"adminPassword": "a-long-enough-password"}, cookies...)
	if reset.Code != http.StatusOK {
		t.Fatalf("额度重置失败：%d %s", reset.Code, reset.Body.String())
	}
	var resetResult struct {
		Epoch   int64      `json:"epoch"`
		Profile profileDTO `json:"profile"`
	}
	if err := json.Unmarshal(reset.Body.Bytes(), &resetResult); err != nil || resetResult.Epoch != 1 {
		t.Fatalf("额度重置结果异常：%s %v", reset.Body.String(), err)
	}
	if resetResult.Profile.QuotaEpoch != 1 || resetResult.Profile.QuotaResetAt == "" {
		t.Fatalf("重置后的配置未显示代次：%+v", resetResult.Profile)
	}
}
