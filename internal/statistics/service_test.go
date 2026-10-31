package statistics

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dreamstation625/ovpanel/internal/store"
	"github.com/dreamstation625/ovpanel/internal/telemetry"
)

func testService(t *testing.T) (*Service, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	if err := st.CreateUser(context.Background(), store.User{
		ID: "u1", DisplayName: "u1", Status: store.StatusActive, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProfileWithCertificate(context.Background(), store.Profile{
		ID: "p1", UserID: "u1", Name: "p1", AuthType: store.AuthCertificate,
		Status: store.StatusActive, RouteMode: store.RouteInherit, AccessMode: store.AccessInherit,
		CreatedAt: now, UpdatedAt: now,
	}, nil); err != nil {
		t.Fatal(err)
	}
	controlDir := filepath.Join(dir, "control")
	if err := os.MkdirAll(controlDir, 0o700); err != nil {
		t.Fatal(err)
	}
	service := New(st, controlDir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return service, st, controlDir
}

func appendEvent(t *testing.T, dir string, event telemetry.Event) {
	t.Helper()
	path := filepath.Join(dir, "events-active.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
}

func writeSnapshot(t *testing.T, dir string, snapshot telemetry.Snapshot) {
	t.Helper()
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, telemetry.SnapshotFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSyncIngestsEventsOnceAndCatchesUp(t *testing.T) {
	ctx := context.Background()
	service, st, controlDir := testService(t)
	at := time.Now().UTC().Add(-time.Minute)
	connected := at.Add(-time.Minute)
	appendEvent(t, controlDir, telemetry.Event{
		Version: 1, Seq: 1, Generation: "g1", Kind: telemetry.EventSessionStart,
		At: connected, SessionID: "g1:1", CID: "1", ProfileID: "p1",
		VPNIP: "10.8.0.2", SourceIP: "203.0.113.9", ConnectedAt: &connected,
	})
	appendEvent(t, controlDir, telemetry.Event{
		Version: 1, Seq: 2, Generation: "g1", Kind: telemetry.EventUsage,
		At: at, SessionID: "g1:1", CID: "1", ProfileID: "p1",
		UpBytes: 10, DownBytes: 20, DeltaUp: 10, DeltaDown: 20,
	})
	writeSnapshot(t, controlDir, telemetry.Snapshot{
		Version: 1, Generation: "g1", UpdatedAt: time.Now().UTC(),
		Sessions: []telemetry.LiveSession{{ID: "g1:1", CID: "1", ProfileID: "p1", VPNIP: "10.8.0.2"}},
	})

	result, err := service.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Events != 2 || !result.Present || !result.Fresh {
		t.Fatalf("首次同步异常：%+v", result)
	}
	history, err := st.SessionHistory(ctx, "p1", 10)
	if err != nil || len(history) != 1 || history[0].Disconnected != nil {
		t.Fatalf("在线历史异常：%+v %v", history, err)
	}

	// 重复同步不会重复累计。
	result, err = service.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Events != 0 {
		t.Fatalf("重复同步事件数 = %d", result.Events)
	}
	usage, _ := st.UsageByProfile(ctx)
	if usage["p1"].UpBytes != 10 || usage["p1"].DownBytes != 20 {
		t.Fatalf("重复同步后用量异常：%+v", usage)
	}

	// 面板离线期间追加的事件在下次同步补传。
	appendEvent(t, controlDir, telemetry.Event{
		Version: 1, Seq: 3, Generation: "g1", Kind: telemetry.EventSessionEnd,
		At: time.Now().UTC(), SessionID: "g1:1", CID: "1", ProfileID: "p1",
		UpBytes: 15, DownBytes: 30, DeltaUp: 5, DeltaDown: 10, Reason: "client_disconnect",
	})
	result, err = service.Sync(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Events != 1 {
		t.Fatalf("补传事件数 = %d", result.Events)
	}
	history, _ = st.SessionHistory(ctx, "p1", 10)
	if history[0].Disconnected == nil || history[0].UpBytes != 15 || history[0].DownBytes != 30 {
		t.Fatalf("补传后历史异常：%+v", history[0])
	}
}

func TestSyncClosesSessionsFromPreviousGeneration(t *testing.T) {
	ctx := context.Background()
	service, st, controlDir := testService(t)
	old := time.Now().UTC().Add(-time.Hour)
	appendEvent(t, controlDir, telemetry.Event{
		Version: 1, Seq: 1, Generation: "old", Kind: telemetry.EventSessionStart,
		At: old, SessionID: "old:1", CID: "1", ProfileID: "p1", ConnectedAt: &old,
	})
	writeSnapshot(t, controlDir, telemetry.Snapshot{
		Version: 1, Generation: "new", UpdatedAt: time.Now().UTC(),
	})
	if _, err := service.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	history, err := st.SessionHistory(ctx, "p1", 10)
	if err != nil || len(history) != 1 || history[0].Disconnected == nil {
		t.Fatalf("旧代次连接应被关闭：%+v %v", history, err)
	}
	if history[0].EndReason != "代理重启或离线" {
		t.Fatalf("结束原因 = %q", history[0].EndReason)
	}
}

func TestSyncLeavesPartialLineForNextRound(t *testing.T) {
	ctx := context.Background()
	service, st, controlDir := testService(t)
	now := time.Now().UTC()
	line, _ := json.Marshal(telemetry.Event{
		Version: 1, Seq: 1, Generation: "g1", Kind: telemetry.EventSessionStart,
		At: now, SessionID: "g1:1", CID: "1", ProfileID: "p1", ConnectedAt: &now,
	})
	path := filepath.Join(controlDir, "events-active.jsonl")
	if err := os.WriteFile(path, line, 0o644); err != nil { // 没有换行，写到一半
		t.Fatal(err)
	}
	if _, err := service.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if history, _ := st.SessionHistory(ctx, "p1", 10); len(history) != 0 {
		t.Fatalf("未完成的记录不应入库：%+v", history)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if _, err := service.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if history, _ := st.SessionHistory(ctx, "p1", 10); len(history) != 1 {
		t.Fatalf("完整记录应被补传：%+v", history)
	}
}
