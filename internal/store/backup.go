package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BackupKind 是备份来源。
const (
	BackupManual     = "manual"
	BackupScheduled  = "scheduled"
	BackupPreRestore = "pre_restore"
)

// BackupSettings 是加密备份的公开设置；恢复密码只保存哈希与密文。
type BackupSettings struct {
	Enabled          bool
	ScheduleTime     string
	RetentionCount   int
	PassphraseCipher []byte
	UpdatedAt        time.Time
}

// BackupRecord 是一个已完成的备份包。
type BackupRecord struct {
	ID              string
	Filename        string
	Kind            string
	Note            string
	SizeBytes       int64
	SHA256          string
	AppVersion      string
	ConfigRevision  int64
	AgentGeneration string
	UsageSeq        uint64
	ManifestJSON    string
	CreatedAt       time.Time
}

// BackupSettings 读取备份设置；迁移保证该行存在。
func (s *Store) BackupSettings(ctx context.Context) (BackupSettings, error) {
	var (
		settings   BackupSettings
		enabled    int
		ciphertext []byte
		updatedAt  string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT enabled, schedule_time, retention_count,
		        passphrase_ciphertext, updated_at
		   FROM backup_settings WHERE id = 1`,
	).Scan(&enabled, &settings.ScheduleTime, &settings.RetentionCount,
		&ciphertext, &updatedAt)
	if err != nil {
		return BackupSettings{}, fmt.Errorf("读取备份设置: %w", err)
	}
	settings.Enabled = enabled == 1
	settings.PassphraseCipher = ciphertext
	settings.UpdatedAt = parseTime(updatedAt)
	return settings, nil
}

// SaveBackupSettings 更新备份设置。
func (s *Store) SaveBackupSettings(ctx context.Context, settings BackupSettings, now time.Time) error {
	enabled := 0
	if settings.Enabled {
		enabled = 1
	}
	var cipher any
	if len(settings.PassphraseCipher) > 0 {
		cipher = settings.PassphraseCipher
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE backup_settings
		    SET enabled = ?, schedule_time = ?, retention_count = ?,
		        passphrase_ciphertext = ?, updated_at = ?
		  WHERE id = 1`,
		enabled, settings.ScheduleTime, settings.RetentionCount,
		cipher, formatTime(now))
	if err != nil {
		return fmt.Errorf("保存备份设置: %w", err)
	}
	return nil
}

// CreateBackupRecord 登记成功的备份包。
func (s *Store) CreateBackupRecord(ctx context.Context, record BackupRecord) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO backup
		   (id, filename, kind, note, size_bytes, sha256, app_version,
		    config_revision, agent_generation, usage_seq, manifest_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID, record.Filename, record.Kind, record.Note, record.SizeBytes,
		record.SHA256, record.AppVersion, record.ConfigRevision,
		record.AgentGeneration, int64(record.UsageSeq), record.ManifestJSON,
		formatTime(record.CreatedAt))
	if err != nil {
		return fmt.Errorf("登记备份: %w", err)
	}
	return nil
}

// BackupRecord 读取单个备份。
func (s *Store) BackupRecord(ctx context.Context, id string) (BackupRecord, bool, error) {
	var (
		record    BackupRecord
		createdAt string
		usageSeq  int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, filename, kind, note, size_bytes, sha256, app_version,
		        config_revision, agent_generation, usage_seq, manifest_json, created_at
		   FROM backup WHERE id = ?`, id,
	).Scan(&record.ID, &record.Filename, &record.Kind, &record.Note, &record.SizeBytes,
		&record.SHA256, &record.AppVersion, &record.ConfigRevision, &record.AgentGeneration,
		&usageSeq, &record.ManifestJSON, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return BackupRecord{}, false, nil
	}
	if err != nil {
		return BackupRecord{}, false, fmt.Errorf("读取备份: %w", err)
	}
	record.UsageSeq = uint64(maxInt64(usageSeq))
	record.CreatedAt = parseTime(createdAt)
	return record, true, nil
}

// ListBackups 按创建时间倒序返回备份。
func (s *Store) ListBackups(ctx context.Context, limit int) ([]BackupRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, filename, kind, note, size_bytes, sha256, app_version,
		        config_revision, agent_generation, usage_seq, manifest_json, created_at
		   FROM backup ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("列出备份: %w", err)
	}
	defer rows.Close()
	out := make([]BackupRecord, 0, limit)
	for rows.Next() {
		var (
			record    BackupRecord
			createdAt string
			usageSeq  int64
		)
		if err := rows.Scan(&record.ID, &record.Filename, &record.Kind, &record.Note,
			&record.SizeBytes, &record.SHA256, &record.AppVersion, &record.ConfigRevision,
			&record.AgentGeneration, &usageSeq, &record.ManifestJSON, &createdAt); err != nil {
			return nil, fmt.Errorf("读取备份列表: %w", err)
		}
		record.UsageSeq = uint64(maxInt64(usageSeq))
		record.CreatedAt = parseTime(createdAt)
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取备份列表: %w", err)
	}
	return out, nil
}

// DeleteBackupRecord 删除备份元数据。
func (s *Store) DeleteBackupRecord(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM backup WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除备份记录: %w", err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// BackupStorageBytes 返回全部备份包占用。
func (s *Store) BackupStorageBytes(ctx context.Context) (int64, error) {
	var total sql.NullInt64
	if err := s.db.QueryRowContext(ctx,
		`SELECT SUM(size_bytes) FROM backup`).Scan(&total); err != nil {
		return 0, fmt.Errorf("统计备份占用: %w", err)
	}
	return total.Int64, nil
}

// QuotaResetCount 返回额度重置记录总数，用于恢复前影响比较。
func (s *Store) QuotaResetCount(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM quota_reset`).Scan(&count); err != nil {
		return 0, fmt.Errorf("统计额度重置: %w", err)
	}
	return count, nil
}

// SnapshotDatabase 使用 SQLite VACUUM INTO 生成一致快照。
func (s *Store) SnapshotDatabase(ctx context.Context, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建数据库快照目录: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("清理旧数据库快照: %w", err)
	}
	escaped := strings.ReplaceAll(path, "'", "''")
	if _, err := s.db.ExecContext(ctx, "VACUUM INTO '"+escaped+"'"); err != nil {
		return fmt.Errorf("生成数据库快照: %w", err)
	}
	return os.Chmod(path, 0o600)
}

// InspectDatabase 校验 SQLite 文件完整性并返回 schema 版本。
func InspectDatabase(path string) (int, error) {
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro&_pragma=query_only(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return 0, fmt.Errorf("打开数据库快照: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return 0, fmt.Errorf("校验数据库快照: %w", err)
	}
	if integrity != "ok" {
		return 0, fmt.Errorf("数据库快照完整性检查失败：%s", integrity)
	}
	var version int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, fmt.Errorf("读取数据库快照版本: %w", err)
	}
	return version, nil
}

// ReplaceDatabase 用已验证的快照替换当前数据库。
//
// recoveryPath 保存替换前的当前数据库；替换或迁移失败时用它回退。
// 成功后清空管理员会话，要求重新登录。
func (s *Store) ReplaceDatabase(ctx context.Context, snapshotPath, recoveryPath string) (int, error) {
	snapshotVersion, err := InspectDatabase(snapshotPath)
	if err != nil {
		return 0, err
	}
	if snapshotVersion == 0 {
		return 0, errors.New("备份中的数据库没有 schema 版本")
	}
	currentVersion, err := s.SchemaVersion(ctx)
	if err != nil {
		return 0, err
	}
	if snapshotVersion > currentVersion {
		return 0, fmt.Errorf("备份数据库版本 %d 高于当前程序支持的 %d", snapshotVersion, currentVersion)
	}
	if err := s.SnapshotDatabase(ctx, recoveryPath); err != nil {
		return 0, fmt.Errorf("创建恢复前数据库快照: %w", err)
	}

	restoreRecovery := func(cause error) error {
		if err := copyFile(recoveryPath, s.path); err != nil {
			return fmt.Errorf("%v；恢复原数据库失败：%w（恢复点：%s）", cause, err, recoveryPath)
		}
		_ = os.Remove(s.path + "-wal")
		_ = os.Remove(s.path + "-shm")
		db, openErr := openDatabase(ctx, s.path)
		if openErr != nil {
			return fmt.Errorf("%v；重新打开原数据库失败：%w（恢复点：%s）", cause, openErr, recoveryPath)
		}
		s.db = db
		return cause
	}

	if err := s.db.Close(); err != nil {
		return 0, fmt.Errorf("关闭当前数据库: %w", err)
	}
	_ = os.Remove(s.path + "-wal")
	_ = os.Remove(s.path + "-shm")
	if err := copyFile(snapshotPath, s.path); err != nil {
		return 0, restoreRecovery(fmt.Errorf("写入恢复数据库: %w", err))
	}
	db, err := openDatabase(ctx, s.path)
	if err != nil {
		return 0, restoreRecovery(err)
	}
	s.db = db
	version, err := s.Migrate(ctx)
	if err != nil {
		return 0, restoreRecovery(fmt.Errorf("迁移恢复数据库: %w", err))
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM admin_session`); err != nil {
		return 0, restoreRecovery(fmt.Errorf("使旧管理员会话失效: %w", err))
	}
	_ = os.Remove(recoveryPath)
	return version, nil
}

func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".restore-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, target)
}
