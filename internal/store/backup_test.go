package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReplaceDatabaseRestoresSnapshotAndInvalidatesSessions(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	now := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	if err := s.CreateAdmin(ctx, "admin", "hash"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, []byte("token-hash"), now.Add(time.Hour), "127.0.0.1", "test"); err != nil {
		t.Fatal(err)
	}
	createUsageProfile(t, s, "p1", "u1")

	dir := t.TempDir()
	snapshot := filepath.Join(dir, "panel.db")
	if err := s.SnapshotDatabase(ctx, snapshot); err != nil {
		t.Fatalf("生成快照失败：%v", err)
	}
	version, err := InspectDatabase(snapshot)
	if err != nil || version < 1 {
		t.Fatalf("检查快照失败：%d %v", version, err)
	}

	createUsageProfile(t, s, "p2", "u2")
	recovery := filepath.Join(dir, "recovery.db")
	restored, err := s.ReplaceDatabase(ctx, snapshot, recovery)
	if err != nil {
		t.Fatalf("恢复数据库失败：%v", err)
	}
	if restored != version {
		t.Fatalf("恢复后 schema = %d，期望 %d", restored, version)
	}
	if _, ok, err := s.User(ctx, "u1"); err != nil || !ok {
		t.Fatalf("恢复后缺少备份内用户：%v", err)
	}
	if _, ok, err := s.User(ctx, "u2"); err != nil || ok {
		t.Fatalf("恢复后仍存在备份之后的用户：%v", err)
	}
	if sessions, err := s.CountSessions(ctx); err != nil || sessions != 0 {
		t.Fatalf("恢复后管理员会话应失效：%d %v", sessions, err)
	}
	if _, err := os.Stat(recovery); !os.IsNotExist(err) {
		t.Fatalf("成功恢复后应删除临时恢复点：%v", err)
	}
}

func TestInspectDatabaseRejectsInvalidFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.db")
	if err := os.WriteFile(path, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectDatabase(path); err == nil {
		t.Fatal("无效数据库应被拒绝")
	}
}

func TestBackupSettingsAndRecords(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	now := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	if err := s.SaveBackupSettings(ctx, BackupSettings{
		Enabled: true, ScheduleTime: "03:30", RetentionCount: 5,
		PassphraseCipher: []byte{1, 2, 3},
	}, now); err != nil {
		t.Fatal(err)
	}
	settings, err := s.BackupSettings(ctx)
	if err != nil || !settings.Enabled || settings.ScheduleTime != "03:30" ||
		settings.RetentionCount != 5 || string(settings.PassphraseCipher) != string([]byte{1, 2, 3}) {
		t.Fatalf("备份设置异常：%+v %v", settings, err)
	}
	record := BackupRecord{
		ID: "b1", Filename: "b1.ovpbak", Kind: BackupManual, Note: "n",
		SizeBytes: 123, SHA256: "sum", AppVersion: "test", CreatedAt: now,
	}
	if err := s.CreateBackupRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := s.BackupRecord(ctx, "b1")
	if err != nil || !ok || loaded.SizeBytes != 123 {
		t.Fatalf("读取备份失败：%+v %v", loaded, err)
	}
	if total, err := s.BackupStorageBytes(ctx); err != nil || total != 123 {
		t.Fatalf("备份占用异常：%d %v", total, err)
	}
	if err := s.DeleteBackupRecord(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.BackupRecord(ctx, "b1"); ok {
		t.Fatal("删除后仍能读取备份记录")
	}
}
