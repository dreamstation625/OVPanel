package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrAdminExists 表示已经存在管理员，单管理员模型下不允许再创建。
var ErrAdminExists = errors.New("管理员已存在")

// Admin 是唯一的管理员账号。
type Admin struct {
	Username     string
	PasswordHash string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// AdminExists 判断是否已完成初始化。
func (s *Store) AdminExists(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin`).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("查询管理员: %w", err)
	}
	return n > 0, nil
}

// CreateAdmin 创建管理员。
//
// 表上以 CHECK(id = 1) 约束单行，插入第二条会被数据库拒绝；
// 这里把冲突转换成明确错误，避免调用方依赖驱动错误文本。
func (s *Store) CreateAdmin(ctx context.Context, username, passwordHash string) error {
	now := formatTime(nowUTC())
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO admin (id, username, password_hash, created_at, updated_at)
		 VALUES (1, ?, ?, ?, ?)`,
		username, passwordHash, now, now)
	if err != nil {
		// 唯一约束冲突说明已有管理员。
		if isConstraintError(err) {
			return ErrAdminExists
		}
		return fmt.Errorf("创建管理员: %w", err)
	}
	return nil
}

// Admin 读取管理员；不存在时返回 ok=false。
func (s *Store) Admin(ctx context.Context) (Admin, bool, error) {
	var (
		a            Admin
		created, upd string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT username, password_hash, created_at, updated_at FROM admin WHERE id = 1`,
	).Scan(&a.Username, &a.PasswordHash, &created, &upd)
	if errors.Is(err, sql.ErrNoRows) {
		return Admin{}, false, nil
	}
	if err != nil {
		return Admin{}, false, fmt.Errorf("读取管理员: %w", err)
	}
	a.CreatedAt = parseTime(created)
	a.UpdatedAt = parseTime(upd)
	return a, true, nil
}

// UpdateAdminPassword 更新密码哈希。
func (s *Store) UpdateAdminPassword(ctx context.Context, passwordHash string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE admin SET password_hash = ?, updated_at = ? WHERE id = 1`,
		passwordHash, formatTime(nowUTC()))
	if err != nil {
		return fmt.Errorf("更新管理员密码: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("更新管理员密码: 管理员不存在")
	}
	return nil
}

// isConstraintError 判断是否为唯一/约束冲突。
func isConstraintError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range []string{"UNIQUE constraint failed", "constraint failed", "PRIMARY KEY"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
