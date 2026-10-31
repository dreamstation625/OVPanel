package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/dreamstation625/ovpanel/internal/auth"
	"github.com/dreamstation625/ovpanel/internal/secretbox"
	"github.com/dreamstation625/ovpanel/internal/store"
)

func newTestService(t *testing.T) (*Service, *store.Store, string) {
	t.Helper()
	old := auth.DefaultParams
	auth.DefaultParams = auth.Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 8, KeyLength: 16}
	t.Cleanup(func() { auth.DefaultParams = old })

	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	masterKey, err := secretbox.LoadOrCreate(filepath.Join(dir, "master.key"), "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 31, 2, 0, 0, 0, time.UTC)
	service := New(Options{
		Store:         st,
		MasterKey:     masterKey,
		MasterKeyPath: filepath.Join(dir, "master.key"),
		DataDir:       dir,
		ControlDir:    filepath.Join(dir, "control"),
		Version:       "test",
		Log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	service.now = func() time.Time { return now }
	return service, st, dir
}

func createTestUser(t *testing.T, st *store.Store, id string) {
	t.Helper()
	now := time.Date(2026, 10, 31, 1, 0, 0, 0, time.UTC)
	if err := st.CreateUser(context.Background(), store.User{
		ID: id, DisplayName: id, Status: store.StatusActive, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCreateBackupEncryptsAndLists(t *testing.T) {
	service, st, dir := newTestService(t)
	createTestUser(t, st, "u1")
	record, err := service.Create(context.Background(), store.BackupManual, "手动备份", "correct-horse-backup-password")
	if err != nil {
		t.Fatalf("创建备份失败：%v", err)
	}
	if record.SizeBytes <= 0 || record.SHA256 == "" || record.UsageSeq != 0 {
		t.Fatalf("备份元数据异常：%+v", record)
	}
	path := service.PackagePath(record)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("age-encryption.org/v1")) {
		t.Fatalf("备份包不是 age 格式：%q", data[:min(32, len(data))])
	}
	if info, err := os.Stat(path); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("备份包权限异常：%v %v", info, err)
	}
	records, total, err := service.List(context.Background())
	if err != nil || len(records) != 1 || total != record.SizeBytes {
		t.Fatalf("备份列表异常：%+v %d %v", records, total, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "backups")); err != nil {
		t.Fatalf("备份目录不存在：%v", err)
	}

	// 错误密码与损坏包都不能通过预览。
	if _, err := service.PreviewRestore(context.Background(), bytes.NewReader(data), "wrong-backup-password"); err == nil {
		t.Fatal("错误恢复密码不应通过")
	}
	corrupted := append([]byte(nil), data...)
	corrupted[len(corrupted)/2] ^= 0xff
	if _, err := service.PreviewRestore(context.Background(), bytes.NewReader(corrupted), "correct-horse-backup-password"); err == nil {
		t.Fatal("损坏备份包不应通过")
	}
	preview, err := service.PreviewRestore(context.Background(), bytes.NewReader(data), "correct-horse-backup-password")
	if err != nil {
		t.Fatalf("预览失败：%v", err)
	}
	if preview.Token == "" || preview.Manifest.Users != 1 || len(preview.Warnings) == 0 {
		t.Fatalf("预览内容异常：%+v", preview)
	}
}

func TestCreateRejectsDuringMaintenance(t *testing.T) {
	service, st, _ := newTestService(t)
	createTestUser(t, st, "u1")
	service.maintenanceBusy = func(context.Context) error { return ErrMaintenanceBusy }
	if _, err := service.Create(context.Background(), store.BackupManual, "", "correct-horse-backup-password"); !errors.Is(err, ErrMaintenanceBusy) {
		t.Fatalf("维护任务进行中应拒绝备份：%v", err)
	}
}

func TestRetentionPrunesOnlyScheduledBackups(t *testing.T) {
	service, st, _ := newTestService(t)
	createTestUser(t, st, "u1")
	if _, err := service.SaveSettings(context.Background(), SettingsInput{
		Enabled: false, ScheduleTime: "03:00", RetentionCount: 2,
		BackupPassword: "correct-horse-backup-password",
	}); err != nil {
		t.Fatal(err)
	}
	manual, err := service.Create(context.Background(), store.BackupManual, "保留", "")
	if err != nil {
		t.Fatal(err)
	}
	now := service.now()
	for i := 0; i < 4; i++ {
		if _, err := service.Create(context.Background(), store.BackupScheduled, "定时", ""); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
		service.now = func() time.Time { return now }
	}
	records, _, err := service.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	scheduled := 0
	manualFound := false
	for _, record := range records {
		switch record.Kind {
		case store.BackupScheduled:
			scheduled++
		case store.BackupManual:
			if record.ID == manual.ID {
				manualFound = true
			}
		}
	}
	if scheduled != 2 || !manualFound {
		t.Fatalf("保留策略异常：scheduled=%d manual=%v records=%+v", scheduled, manualFound, records)
	}

	// 失败的新备份不得触发保留清理，也不得删除最后可用备份。
	if _, err := service.Create(context.Background(), "invalid-kind", "失败", ""); err == nil {
		t.Fatal("非法备份类型应失败")
	}
	records, _, err = service.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	remaining := 0
	for _, record := range records {
		if record.Kind == store.BackupScheduled {
			remaining++
		}
	}
	if remaining != 2 {
		t.Fatalf("失败备份后定时备份数量 = %d，期望仍为 2", remaining)
	}
}

func TestScheduledBackupRunsOncePerDay(t *testing.T) {
	service, st, _ := newTestService(t)
	createTestUser(t, st, "u1")
	if _, err := service.SaveSettings(context.Background(), SettingsInput{
		Enabled: true, ScheduleTime: "03:00", RetentionCount: 7,
		BackupPassword: "correct-horse-backup-password",
	}); err != nil {
		t.Fatal(err)
	}
	// 2026-10-31 02:00 UTC = 10:00 Asia/Shanghai，已过 03:00。
	if err := service.RunScheduled(context.Background()); err != nil {
		t.Fatalf("执行定时备份失败：%v", err)
	}
	if err := service.RunScheduled(context.Background()); err != nil {
		t.Fatal(err)
	}
	records, _, err := service.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, record := range records {
		if record.Kind == store.BackupScheduled {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("同一天定时备份数量 = %d，期望 1", count)
	}
}
