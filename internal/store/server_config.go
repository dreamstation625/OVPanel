package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// 服务端配置修订状态。
const (
	ServerConfigDraft      = "draft"
	ServerConfigActive     = "active"
	ServerConfigSuperseded = "superseded"
	ServerConfigDiscarded  = "discarded"
)

// ServerConfigRevision 是一次保存的服务端配置。
type ServerConfigRevision struct {
	Revision     int64
	State        string
	ConfigJSON   string
	RenderedHash string
	CreatedAt    time.Time
	AppliedAt    *time.Time
}

// ServerConfigState 是当前生效修订与最新草稿。
type ServerConfigState struct {
	Active *ServerConfigRevision
	Draft  *ServerConfigRevision
}

// ServerConfigState 读取当前生效修订与草稿。
func (s *Store) ServerConfigState(ctx context.Context) (ServerConfigState, error) {
	var activeID, draftID sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT active_revision, draft_revision FROM server_config_state WHERE id = 1`,
	).Scan(&activeID, &draftID)
	if errors.Is(err, sql.ErrNoRows) {
		return ServerConfigState{}, nil
	}
	if err != nil {
		return ServerConfigState{}, fmt.Errorf("读取服务端配置状态: %w", err)
	}

	out := ServerConfigState{}
	if activeID.Valid {
		revision, ok, err := s.ServerConfigRevision(ctx, activeID.Int64)
		if err != nil {
			return ServerConfigState{}, err
		}
		if ok {
			out.Active = &revision
		}
	}
	if draftID.Valid {
		revision, ok, err := s.ServerConfigRevision(ctx, draftID.Int64)
		if err != nil {
			return ServerConfigState{}, err
		}
		if ok {
			out.Draft = &revision
		}
	}
	return out, nil
}

// ServerConfigRevision 读取单条修订。
func (s *Store) ServerConfigRevision(ctx context.Context, revision int64) (ServerConfigRevision, bool, error) {
	var (
		r         ServerConfigRevision
		created   string
		appliedAt sql.NullString
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT revision, state, config_json, rendered_hash, created_at, applied_at
		   FROM server_config_revision WHERE revision = ?`, revision,
	).Scan(&r.Revision, &r.State, &r.ConfigJSON, &r.RenderedHash, &created, &appliedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ServerConfigRevision{}, false, nil
	}
	if err != nil {
		return ServerConfigRevision{}, false, fmt.Errorf("读取服务端配置修订: %w", err)
	}
	r.CreatedAt = parseTime(created)
	r.AppliedAt = nullableTime(appliedAt)
	return r, true, nil
}

// SaveServerConfigDraft 保存新的草稿修订，旧草稿标记为 discarded。
func (s *Store) SaveServerConfigDraft(ctx context.Context, configJSON, renderedHash string, now time.Time) (ServerConfigRevision, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ServerConfigRevision{}, fmt.Errorf("开始保存服务端草稿事务: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`UPDATE server_config_revision SET state = ? WHERE state = ?`,
		ServerConfigDiscarded, ServerConfigDraft); err != nil {
		return ServerConfigRevision{}, fmt.Errorf("弃用旧草稿: %w", err)
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO server_config_revision (state, config_json, rendered_hash, created_at)
		 VALUES (?, ?, ?, ?)`,
		ServerConfigDraft, configJSON, renderedHash, formatTime(now))
	if err != nil {
		return ServerConfigRevision{}, fmt.Errorf("写入服务端草稿: %w", err)
	}
	revision, err := res.LastInsertId()
	if err != nil {
		return ServerConfigRevision{}, fmt.Errorf("读取草稿修订号: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE server_config_state SET draft_revision = ?, updated_at = ? WHERE id = 1`,
		revision, formatTime(now)); err != nil {
		return ServerConfigRevision{}, fmt.Errorf("更新服务端草稿指针: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ServerConfigRevision{}, fmt.Errorf("提交服务端草稿事务: %w", err)
	}

	return ServerConfigRevision{
		Revision:     revision,
		State:        ServerConfigDraft,
		ConfigJSON:   configJSON,
		RenderedHash: renderedHash,
		CreatedAt:    now,
	}, nil
}

// ActivateServerConfig 将草稿修订设为生效修订。
//
// 这里只更新面板状态；运行代理实际应用与确认由后续代理接口完成。
func (s *Store) ActivateServerConfig(ctx context.Context, revision int64, now time.Time) (ServerConfigRevision, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ServerConfigRevision{}, fmt.Errorf("开始激活服务端配置事务: %w", err)
	}
	defer tx.Rollback()

	candidate, ok, err := serverConfigRevisionTx(ctx, tx, revision)
	if err != nil {
		return ServerConfigRevision{}, err
	}
	if !ok || candidate.State != ServerConfigDraft {
		return ServerConfigRevision{}, ErrNotFound
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE server_config_revision SET state = ? WHERE state = ?`,
		ServerConfigSuperseded, ServerConfigActive); err != nil {
		return ServerConfigRevision{}, fmt.Errorf("取代旧生效修订: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE server_config_revision SET state = ?, applied_at = ? WHERE revision = ?`,
		ServerConfigActive, formatTime(now), revision); err != nil {
		return ServerConfigRevision{}, fmt.Errorf("激活服务端修订: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE server_config_state SET active_revision = ?, draft_revision = NULL, updated_at = ? WHERE id = 1`,
		revision, formatTime(now)); err != nil {
		return ServerConfigRevision{}, fmt.Errorf("更新服务端生效指针: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return ServerConfigRevision{}, fmt.Errorf("提交激活服务端配置事务: %w", err)
	}

	candidate.State = ServerConfigActive
	candidate.AppliedAt = &now
	return candidate, nil
}

func serverConfigRevisionTx(ctx context.Context, tx *sql.Tx, revision int64) (ServerConfigRevision, bool, error) {
	var (
		r       ServerConfigRevision
		created string
		applied sql.NullString
	)
	err := tx.QueryRowContext(ctx,
		`SELECT revision, state, config_json, rendered_hash, created_at, applied_at
		   FROM server_config_revision WHERE revision = ?`, revision,
	).Scan(&r.Revision, &r.State, &r.ConfigJSON, &r.RenderedHash, &created, &applied)
	if errors.Is(err, sql.ErrNoRows) {
		return ServerConfigRevision{}, false, nil
	}
	if err != nil {
		return ServerConfigRevision{}, false, fmt.Errorf("读取服务端修订: %w", err)
	}
	r.CreatedAt = parseTime(created)
	r.AppliedAt = nullableTime(applied)
	return r, true, nil
}
