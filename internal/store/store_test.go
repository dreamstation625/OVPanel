package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(context.Background(), filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() { s.Close() })

	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}
	return s
}

func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	defer s.Close()

	v1, err := s.Migrate(ctx)
	if err != nil {
		t.Fatalf("首次迁移失败：%v", err)
	}
	if v1 < 1 {
		t.Fatalf("迁移版本 = %d，期望至少 1", v1)
	}

	v2, err := s.Migrate(ctx)
	if err != nil {
		t.Fatalf("重复迁移失败：%v", err)
	}
	if v2 != v1 {
		t.Fatalf("重复迁移后版本变化：%d → %d", v1, v2)
	}

	if got, err := s.SchemaVersion(ctx); err != nil || got != v1 {
		t.Fatalf("SchemaVersion = %d, %v；期望 %d", got, err, v1)
	}
}

// TestAdminIsSingleRow 确认单管理员模型由数据库约束。
func TestAdminIsSingleRow(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateAdmin(ctx, "admin", "$argon2id$fake"); err != nil {
		t.Fatalf("创建管理员失败：%v", err)
	}

	err := s.CreateAdmin(ctx, "second", "$argon2id$fake2")
	if !errors.Is(err, ErrAdminExists) {
		t.Fatalf("第二个管理员应被拒绝，实际错误：%v", err)
	}

	a, ok, err := s.Admin(ctx)
	if err != nil || !ok {
		t.Fatalf("读取管理员失败：%v ok=%v", err, ok)
	}
	if a.Username != "admin" {
		t.Fatalf("Username = %q", a.Username)
	}
}

func TestAdminExistsBeforeInitialization(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	exists, err := s.AdminExists(ctx)
	if err != nil {
		t.Fatalf("查询失败：%v", err)
	}
	if exists {
		t.Fatal("新库不应已有管理员")
	}

	if _, ok, err := s.Admin(ctx); err != nil || ok {
		t.Fatalf("空库读取管理员应为不存在：ok=%v err=%v", ok, err)
	}
}

func TestUpdateAdminPassword(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateAdmin(ctx, "admin", "old-hash"); err != nil {
		t.Fatalf("创建管理员失败：%v", err)
	}
	if err := s.UpdateAdminPassword(ctx, "new-hash"); err != nil {
		t.Fatalf("更新密码失败：%v", err)
	}

	a, ok, err := s.Admin(ctx)
	if err != nil || !ok {
		t.Fatalf("读取管理员失败：%v", err)
	}
	if a.PasswordHash != "new-hash" {
		t.Fatalf("密码哈希未更新：%q", a.PasswordHash)
	}
	if a.UpdatedAt.Before(a.CreatedAt) {
		t.Fatal("更新时间不应早于创建时间")
	}
}

func TestSessionLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	hash := []byte("01234567890123456789012345678901")
	now := time.Now().UTC()

	if err := s.CreateSession(ctx, hash, now.Add(time.Hour), "127.0.0.1", "test-agent"); err != nil {
		t.Fatalf("创建会话失败：%v", err)
	}

	valid, err := s.SessionValid(ctx, hash, now)
	if err != nil || !valid {
		t.Fatalf("新会话应有效：valid=%v err=%v", valid, err)
	}

	// 过期后不再有效。
	valid, err = s.SessionValid(ctx, hash, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("校验失败：%v", err)
	}
	if valid {
		t.Fatal("过期会话不应有效")
	}

	if err := s.TouchSession(ctx, hash, now); err != nil {
		t.Fatalf("更新活跃时间失败：%v", err)
	}

	n, err := s.CountSessions(ctx)
	if err != nil || n != 1 {
		t.Fatalf("会话数 = %d err=%v", n, err)
	}

	if err := s.DeleteSession(ctx, hash); err != nil {
		t.Fatalf("删除会话失败：%v", err)
	}
	if n, _ := s.CountSessions(ctx); n != 0 {
		t.Fatalf("删除后会话数 = %d", n)
	}
}

func TestDeleteExpiredSessions(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	now := time.Now().UTC()

	expired := []byte("expired-token-hash-000000000000")
	fresh := []byte("fresh-token-hash-00000000000000")

	if err := s.CreateSession(ctx, expired, now.Add(-time.Minute), "", ""); err != nil {
		t.Fatalf("创建过期会话失败：%v", err)
	}
	if err := s.CreateSession(ctx, fresh, now.Add(time.Hour), "", ""); err != nil {
		t.Fatalf("创建有效会话失败：%v", err)
	}

	deleted, err := s.DeleteExpiredSessions(ctx, now)
	if err != nil {
		t.Fatalf("清理失败：%v", err)
	}
	if deleted != 1 {
		t.Fatalf("删除条数 = %d，期望 1", deleted)
	}

	valid, err := s.SessionValid(ctx, fresh, now)
	if err != nil || !valid {
		t.Fatalf("有效会话不应被清理：valid=%v err=%v", valid, err)
	}
}

func TestOperationLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if err := s.CreateOperation(ctx, "op-1", "openvpn-upgrade"); err != nil {
		t.Fatalf("创建任务失败：%v", err)
	}

	op, ok, err := s.Operation(ctx, "op-1")
	if err != nil || !ok {
		t.Fatalf("读取任务失败：%v ok=%v", err, ok)
	}
	if op.State != OperationPending {
		t.Fatalf("初始状态 = %q", op.State)
	}

	op.State = OperationRunning
	op.Progress = 40
	op.Message = "拉取镜像"
	if err := s.UpdateOperation(ctx, op); err != nil {
		t.Fatalf("更新任务失败：%v", err)
	}

	op.State = OperationFailed
	op.ErrorCode = "image_incompatible"
	op.FinishedAt = time.Now().UTC()
	if err := s.UpdateOperation(ctx, op); err != nil {
		t.Fatalf("更新任务失败：%v", err)
	}

	got, _, err := s.Operation(ctx, "op-1")
	if err != nil {
		t.Fatalf("读取任务失败：%v", err)
	}
	if got.State != OperationFailed || got.ErrorCode != "image_incompatible" {
		t.Fatalf("任务状态未保存：%+v", got)
	}
	if got.FinishedAt.IsZero() {
		t.Fatal("完成时间应被记录")
	}

	list, err := s.ListOperations(ctx, 10)
	if err != nil {
		t.Fatalf("列出任务失败：%v", err)
	}
	if len(list) != 1 {
		t.Fatalf("任务数 = %d，期望 1", len(list))
	}
}

func TestUpdateMissingOperationFails(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	err := s.UpdateOperation(ctx, Operation{ID: "missing", State: OperationRunning})
	if err == nil {
		t.Fatal("更新不存在的任务应返回错误")
	}
}

func TestParseVersionRejectsBadNames(t *testing.T) {
	for _, name := range []string{"init.sql", "_init.sql", "abc_init.sql"} {
		if _, err := parseVersion(name); err == nil {
			t.Fatalf("非法迁移文件名应被拒绝：%s", name)
		}
	}
	if v, err := parseVersion("0007_add_users.sql"); err != nil || v != 7 {
		t.Fatalf("parseVersion = %d, %v", v, err)
	}
}
