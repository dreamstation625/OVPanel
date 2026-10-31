package store

import (
	"context"
	"fmt"
	"time"
)

// CreateSession 保存一条会话记录。
//
// 只保存令牌哈希，不保存明文令牌；expiresAt 由调用方按配置计算。
func (s *Store) CreateSession(ctx context.Context, tokenHash []byte, expiresAt time.Time, remoteIP, userAgent string) error {
	now := formatTime(nowUTC())
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO admin_session (token_hash, created_at, expires_at, last_seen_at, remote_ip, user_agent)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		tokenHash, now, formatTime(expiresAt), now, remoteIP, truncate(userAgent, 200))
	if err != nil {
		return fmt.Errorf("创建会话: %w", err)
	}
	return nil
}

// SessionValid 判断令牌是否仍然有效（存在且未过期）。
func (s *Store) SessionValid(ctx context.Context, tokenHash []byte, now time.Time) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM admin_session WHERE token_hash = ? AND expires_at > ?`,
		tokenHash, formatTime(now)).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("校验会话: %w", err)
	}
	return n > 0, nil
}

// TouchSession 更新最近活跃时间。
func (s *Store) TouchSession(ctx context.Context, tokenHash []byte, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE admin_session SET last_seen_at = ? WHERE token_hash = ?`,
		formatTime(now), tokenHash)
	if err != nil {
		return fmt.Errorf("更新会话活跃时间: %w", err)
	}
	return nil
}

// DeleteSession 删除单条会话（退出登录）。
func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM admin_session WHERE token_hash = ?`, tokenHash)
	if err != nil {
		return fmt.Errorf("删除会话: %w", err)
	}
	return nil
}

// DeleteExpiredSessions 清理过期会话，返回删除条数。
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM admin_session WHERE expires_at <= ?`, formatTime(now))
	if err != nil {
		return 0, fmt.Errorf("清理过期会话: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("统计清理会话: %w", err)
	}
	return n, nil
}

// CountSessions 返回当前会话总数，用于系统页展示。
func (s *Store) CountSessions(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_session`).Scan(&n); err != nil {
		return 0, fmt.Errorf("统计会话: %w", err)
	}
	return n, nil
}

// truncate 截断过长字符串，避免把完整 UA 写进库。
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
