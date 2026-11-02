// Package store 提供面板持久化访问。
//
// P02 冻结持久化布局：schema 变更只能追加迁移文件，不改已应用的迁移。
// 连接数限制为 1，因为 SQLite 的写入本身串行，而面板规模只需要
// 稳定的读一致性，不需要连接池带来的并发写竞争。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	// 纯 Go 的 SQLite 实现，便于跨架构交叉编译，不需要 cgo。
	_ "modernc.org/sqlite"
)

// Store 是数据库句柄。
type Store struct {
	db   *sql.DB
	path string
}

// Open 打开（必要时创建）数据库并设置运行参数。
func Open(ctx context.Context, path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("创建数据目录 %s: %w", dir, err)
		}
		// 已存在的数据目录可能由旧版本或挂载创建为更宽松的权限。
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, fmt.Errorf("收紧数据目录权限 %s: %w", dir, err)
		}
	}

	db, err := openDatabase(ctx, path)
	if err != nil {
		return nil, err
	}
	return &Store{db: db, path: path}, nil
}

func openDatabase(ctx context.Context, path string) (*sql.DB, error) {
	dsn := path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库: %w", err)
	}
	db.SetMaxOpenConns(1)

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("连接数据库: %w", err)
	}

	// 数据库包含管理员哈希与会话摘要，只允许服务进程所属用户读取。
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("收紧数据库权限 %s: %w", path, err)
	}
	return db, nil
}

// Close 关闭数据库。
func (s *Store) Close() error {
	return s.db.Close()
}

// Path 返回数据库文件路径。
func (s *Store) Path() string {
	return s.path
}

// timeFormat 是库内统一的时间格式。
const timeFormat = time.RFC3339

// formatTime 统一以 UTC 保存。
func formatTime(t time.Time) string {
	return t.UTC().Format(timeFormat)
}

// parseTime 解析库内时间；格式异常时返回零值而不是让调用方崩掉。
func parseTime(s string) time.Time {
	t, err := time.Parse(timeFormat, s)
	if err != nil {
		return time.Time{}
	}
	return t
}
