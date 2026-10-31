package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// 任务状态。面板重启后仍按这些状态展示。
const (
	OperationPending   = "pending"
	OperationRunning   = "running"
	OperationSucceeded = "succeeded"
	OperationFailed    = "failed"
)

// Operation 是一条长任务记录。
type Operation struct {
	ID         string
	Kind       string
	State      string
	Progress   int
	Message    string
	ErrorCode  string
	StartedAt  time.Time
	UpdatedAt  time.Time
	FinishedAt time.Time
}

// CreateOperation 登记一条任务。
func (s *Store) CreateOperation(ctx context.Context, id, kind string) error {
	now := formatTime(nowUTC())
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO operation (id, kind, state, progress, started_at, updated_at)
		 VALUES (?, ?, ?, 0, ?, ?)`,
		id, kind, OperationPending, now, now)
	if err != nil {
		return fmt.Errorf("创建任务: %w", err)
	}
	return nil
}

// UpdateOperation 更新任务状态。finishedAt 为零值时表示任务仍在进行。
func (s *Store) UpdateOperation(ctx context.Context, op Operation) error {
	var finished any
	if !op.FinishedAt.IsZero() {
		finished = formatTime(op.FinishedAt)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE operation
		    SET state = ?, progress = ?, message = ?, error_code = ?, updated_at = ?, finished_at = ?
		  WHERE id = ?`,
		op.State, op.Progress, op.Message, op.ErrorCode,
		formatTime(nowUTC()), finished, op.ID)
	if err != nil {
		return fmt.Errorf("更新任务 %s: %w", op.ID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return fmt.Errorf("更新任务 %s: 任务不存在", op.ID)
	}
	return nil
}

// Operation 读取单条任务。
func (s *Store) Operation(ctx context.Context, id string) (Operation, bool, error) {
	var (
		op               Operation
		started, updated string
		finished         sql.NullString
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, kind, state, progress, message, error_code, started_at, updated_at, finished_at
		   FROM operation WHERE id = ?`, id,
	).Scan(&op.ID, &op.Kind, &op.State, &op.Progress, &op.Message, &op.ErrorCode,
		&started, &updated, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation{}, false, nil
	}
	if err != nil {
		return Operation{}, false, fmt.Errorf("读取任务: %w", err)
	}
	op.StartedAt = parseTime(started)
	op.UpdatedAt = parseTime(updated)
	if finished.Valid {
		op.FinishedAt = parseTime(finished.String)
	}
	return op, true, nil
}

// ListOperations 按更新时间倒序返回最近的任务。
func (s *Store) ListOperations(ctx context.Context, limit int) ([]Operation, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, kind, state, progress, message, error_code, started_at, updated_at, finished_at
		   FROM operation ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("列出任务: %w", err)
	}
	defer rows.Close()

	out := make([]Operation, 0, limit)
	for rows.Next() {
		var (
			op               Operation
			started, updated string
			finished         sql.NullString
		)
		if err := rows.Scan(&op.ID, &op.Kind, &op.State, &op.Progress, &op.Message,
			&op.ErrorCode, &started, &updated, &finished); err != nil {
			return nil, fmt.Errorf("读取任务: %w", err)
		}
		op.StartedAt = parseTime(started)
		op.UpdatedAt = parseTime(updated)
		if finished.Valid {
			op.FinishedAt = parseTime(finished.String)
		}
		out = append(out, op)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取任务: %w", err)
	}
	return out, nil
}
