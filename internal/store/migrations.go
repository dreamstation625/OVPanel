package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migration 是一条待应用的迁移。
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// Migrate 依次应用未执行的迁移，返回应用后的最新版本。
//
// 每条迁移在独立事务内执行并登记版本号：中途失败时该条迁移整体回滚，
// 不会留下半应用的 schema。
func (s *Store) Migrate(ctx context.Context) (int, error) {
	migrations, err := loadMigrations()
	if err != nil {
		return 0, err
	}

	if err := s.ensureMigrationTable(ctx); err != nil {
		return 0, err
	}

	applied, err := s.appliedVersions(ctx)
	if err != nil {
		return 0, err
	}

	current := 0
	for _, m := range migrations {
		if _, done := applied[m.Version]; done {
			if m.Version > current {
				current = m.Version
			}
			continue
		}
		if err := s.applyMigration(ctx, m); err != nil {
			return current, err
		}
		current = m.Version
	}
	return current, nil
}

// SchemaVersion 返回当前已应用的最新版本。
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version)
	if err != nil {
		return 0, fmt.Errorf("读取 schema 版本: %w", err)
	}
	return version, nil
}

func (s *Store) ensureMigrationTable(ctx context.Context) error {
	const ddl = `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`
	if _, err := s.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("创建迁移记录表: %w", err)
	}
	return nil
}

func (s *Store) appliedVersions(ctx context.Context) (map[int]struct{}, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("读取迁移记录: %w", err)
	}
	defer rows.Close()

	out := map[int]struct{}{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("读取迁移记录: %w", err)
		}
		out[v] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取迁移记录: %w", err)
	}
	return out, nil
}

func (s *Store) applyMigration(ctx context.Context, m Migration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启迁移事务 %s: %w", m.Name, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return fmt.Errorf("应用迁移 %s: %w", m.Name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		m.Version, formatTime(nowUTC())); err != nil {
		return fmt.Errorf("登记迁移 %s: %w", m.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交迁移 %s: %w", m.Name, err)
	}
	return nil
}

// loadMigrations 读取内嵌迁移并按版本升序排列。
func loadMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("读取迁移目录: %w", err)
	}

	out := make([]Migration, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version, err := parseVersion(e.Name())
		if err != nil {
			return nil, err
		}
		data, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("读取迁移 %s: %w", e.Name(), err)
		}
		out = append(out, Migration{Version: version, Name: e.Name(), SQL: string(data)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })

	// 版本号必须唯一，否则同一版本的两条迁移只会应用一条。
	seen := map[int]string{}
	for _, m := range out {
		if other, dup := seen[m.Version]; dup {
			return nil, fmt.Errorf("迁移版本重复：%d（%s 与 %s）", m.Version, other, m.Name)
		}
		seen[m.Version] = m.Name
	}
	return out, nil
}

// parseVersion 从 0001_init.sql 中取出 1。
func parseVersion(name string) (int, error) {
	idx := strings.Index(name, "_")
	if idx < 1 {
		return 0, fmt.Errorf("迁移文件名缺少版本前缀：%s", name)
	}
	v, err := strconv.Atoi(name[:idx])
	if err != nil {
		return 0, fmt.Errorf("迁移文件名版本非法：%s", name)
	}
	return v, nil
}
