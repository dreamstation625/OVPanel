package agentusage

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dreamstation625/ovpanel/internal/admission"
	"github.com/dreamstation625/ovpanel/internal/telemetry"
)

func testStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	stateDir, controlDir := t.TempDir(), t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := New(stateDir, controlDir, "gen-1", log)
	if err := store.Load(); err != nil {
		t.Fatalf("加载账本失败：%v", err)
	}
	return store, stateDir, controlDir
}

func TestRecordUsesHighWaterAndTracksRollback(t *testing.T) {
	store, _, _ := testStore(t)

	first := store.Record("p1", "gen-1:1", 100, 40)
	if first.DeltaUp != 100 || first.DeltaDown != 40 {
		t.Fatalf("首次采样增量异常：%+v", first)
	}
	repeat := store.Record("p1", "gen-1:1", 100, 40)
	if repeat.DeltaUp != 0 || repeat.DeltaDown != 0 {
		t.Fatalf("重复采样不应累计：%+v", repeat)
	}
	rollback := store.Record("p1", "gen-1:1", 80, 60)
	if !rollback.Rollback || rollback.DeltaUp != 0 || rollback.DeltaDown != 20 {
		t.Fatalf("计数回退应只记录断档并累计新的下行：%+v", rollback)
	}
	if usage := store.Usage("p1"); usage != 160 {
		t.Fatalf("用量 = %d，期望 160", usage)
	}
	if gaps := store.ProfileStats()[0].Gaps; gaps != 1 {
		t.Fatalf("断档计数 = %d，期望 1", gaps)
	}
}

func TestEpochResetSetsBaselineAndKeepsHistory(t *testing.T) {
	store, _, _ := testStore(t)
	store.Record("p1", "gen-1:1", 100, 0)
	store.SyncEpochs(admission.Snapshot{Profiles: []admission.Profile{{ID: "p1", QuotaEpoch: 1}}})
	if usage := store.Usage("p1"); usage != 0 {
		t.Fatalf("重置后用量应为 0，实际 %d", usage)
	}
	store.Record("p1", "gen-1:1", 180, 0)
	if usage := store.Usage("p1"); usage != 80 {
		t.Fatalf("重置后的新增用量 = %d，期望 80", usage)
	}
	// 同一代次重复同步不会再次清零。
	store.SyncEpochs(admission.Snapshot{Profiles: []admission.Profile{{ID: "p1", QuotaEpoch: 1}}})
	if usage := store.Usage("p1"); usage != 80 {
		t.Fatalf("重复代次不应重置，实际 %d", usage)
	}
	store.SyncEpochs(admission.Snapshot{Profiles: []admission.Profile{{ID: "p1", QuotaEpoch: 2}}})
	if usage := store.Usage("p1"); usage != 0 {
		t.Fatalf("新代次应重新清零，实际 %d", usage)
	}
	if stats := store.ProfileStats()[0]; stats.TotalBytes != 180 {
		t.Fatalf("历史累计不应被删除：%+v", stats)
	}
}

func TestSaveLoadKeepsTotalsAndDropsSessionHighWater(t *testing.T) {
	store, stateDir, _ := testStore(t)
	store.Record("p1", "gen-1:1", 100, 50)
	store.SyncEpochs(admission.Snapshot{Profiles: []admission.Profile{{ID: "p1", QuotaEpoch: 3}}})
	if err := store.Save(); err != nil {
		t.Fatalf("保存账本失败：%v", err)
	}
	if info, err := os.Stat(filepath.Join(stateDir, stateFileName)); err != nil {
		t.Fatalf("账本文件不存在：%v", err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("账本文件权限异常：%o", info.Mode().Perm())
	}

	reloaded := New(stateDir, t.TempDir(), "gen-2", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := reloaded.Load(); err != nil {
		t.Fatalf("重新加载失败：%v", err)
	}
	if usage := reloaded.Usage("p1"); usage != 0 {
		t.Fatalf("余额代次应保持，实际用量 %d", usage)
	}
	if stats := reloaded.ProfileStats()[0]; stats.TotalBytes != 150 || stats.QuotaEpoch != 3 {
		t.Fatalf("重新加载后账本异常：%+v", stats)
	}
	// 新代次的会话从 0 开始累计。
	res := reloaded.Record("p1", "gen-2:1", 10, 5)
	if res.DeltaUp != 10 || res.DeltaDown != 5 {
		t.Fatalf("新代次会话应从零累计：%+v", res)
	}
}

func TestAppendEventIsolatesPartialTail(t *testing.T) {
	store, _, controlDir := testStore(t)
	if err := os.WriteFile(filepath.Join(controlDir, activeEvents), []byte(`{"partial":`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(telemetry.Event{Kind: telemetry.EventSessionStart, SessionID: "gen-1:1", ProfileID: "p1"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(controlDir)
	if err != nil {
		t.Fatal(err)
	}
	foundPartial := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "events-partial-") {
			foundPartial = true
		}
	}
	if !foundPartial {
		t.Fatal("不完整末尾未被隔离")
	}
	data, err := os.ReadFile(filepath.Join(controlDir, activeEvents))
	if err != nil {
		t.Fatal(err)
	}
	var event telemetry.Event
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("新事件被拼接到不完整行：%v（%q）", err, data)
	}
}

func TestAppendEventAssignsSequenceAndWritesJSONL(t *testing.T) {
	store, _, controlDir := testStore(t)
	first, err := store.AppendEvent(telemetry.Event{Kind: telemetry.EventSessionStart, SessionID: "gen-1:1", ProfileID: "p1"})
	if err != nil {
		t.Fatalf("写入事件失败：%v", err)
	}
	second, err := store.AppendEvent(telemetry.Event{Kind: telemetry.EventSessionEnd, SessionID: "gen-1:1", ProfileID: "p1"})
	if err != nil {
		t.Fatalf("写入事件失败：%v", err)
	}
	if first.Seq != 1 || second.Seq != 2 || first.Generation != "gen-1" {
		t.Fatalf("事件序号异常：%+v %+v", first, second)
	}

	file, err := os.Open(filepath.Join(controlDir, activeEvents))
	if err != nil {
		t.Fatalf("事件文件不存在：%v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	count := 0
	for scanner.Scan() {
		var event telemetry.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("事件不是合法 JSON：%v", err)
		}
		count++
	}
	if count != 2 {
		t.Fatalf("事件条数 = %d，期望 2", count)
	}
}
