package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/dreamstation625/ovpanel/internal/telemetry"
)

// 中国不实行夏令时，固定时区即可正确划分自然日，也不依赖运行环境 tzdata。
var usageZone = time.FixedZone("Asia/Shanghai", 8*3600)

// UsageDay 返回时间所属的 Asia/Shanghai 自然日。
func UsageDay(t time.Time) string {
	return t.In(usageZone).Format("2006-01-02")
}

// UpdateProfileLimits 更新到期、并发、额度与每连接限速。
// 传入 nil 表示不限制；面板输入 0 时由上层转换为 nil。
func (s *Store) UpdateProfileLimits(ctx context.Context, id string, expiresAt *time.Time,
	concurrentLimit, quotaBytes, uploadBps, downloadBps *int64, now time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE client_profile
		    SET expires_at = ?, concurrent_limit = ?, quota_bytes = ?,
		        upload_bps = ?, download_bps = ?, updated_at = ?
		  WHERE id = ?`,
		nullableTimeValue(expiresAt), nullableInt(concurrentLimit), nullableInt(quotaBytes),
		nullableInt(uploadBps), nullableInt(downloadBps), formatTime(now), id)
	if err != nil {
		return fmt.Errorf("更新配置使用限制 %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// IncrementQuotaEpoch 开启新的额度代次并记录重置时间。
// 运行代理看到更大的代次后把当前累计设为新基线。
func (s *Store) IncrementQuotaEpoch(ctx context.Context, profileID string, now time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("开始额度重置事务: %w", err)
	}
	defer tx.Rollback()

	var epoch int64
	err = tx.QueryRowContext(ctx,
		`SELECT quota_epoch FROM client_profile WHERE id = ?`, profileID).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("读取额度代次: %w", err)
	}
	epoch++
	if _, err := tx.ExecContext(ctx,
		`UPDATE client_profile SET quota_epoch = ?, updated_at = ? WHERE id = ?`,
		epoch, formatTime(now), profileID); err != nil {
		return 0, fmt.Errorf("更新额度代次: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO quota_reset (profile_id, epoch, reset_at) VALUES (?, ?, ?)`,
		profileID, epoch, formatTime(now)); err != nil {
		return 0, fmt.Errorf("记录额度重置: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("提交额度重置事务: %w", err)
	}
	return epoch, nil
}

// QuotaResetInfo 是最近一次手动重置的摘要。
type QuotaResetInfo struct {
	Epoch   int64
	ResetAt time.Time
}

// LatestQuotaReset 返回配置最近一次手动重置；没有记录时 ok=false。
func (s *Store) LatestQuotaReset(ctx context.Context, profileID string) (QuotaResetInfo, bool, error) {
	var (
		info QuotaResetInfo
		at   string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT epoch, reset_at FROM quota_reset WHERE profile_id = ? ORDER BY id DESC LIMIT 1`,
		profileID).Scan(&info.Epoch, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return QuotaResetInfo{}, false, nil
	}
	if err != nil {
		return QuotaResetInfo{}, false, fmt.Errorf("读取额度重置记录: %w", err)
	}
	info.ResetAt = parseTime(at)
	return info, true, nil
}

// UsageCursor 返回每个运行代次已同步的最大事件序号。
func (s *Store) UsageCursor(ctx context.Context) (map[string]uint64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT generation, max_seq FROM usage_sync_state`)
	if err != nil {
		return nil, fmt.Errorf("读取用量同步游标: %w", err)
	}
	defer rows.Close()
	out := map[string]uint64{}
	for rows.Next() {
		var (
			generation string
			seq        uint64
		)
		if err := rows.Scan(&generation, &seq); err != nil {
			return nil, fmt.Errorf("读取用量同步游标: %w", err)
		}
		out[generation] = seq
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取用量同步游标: %w", err)
	}
	return out, nil
}

// ApplyUsageEvents 在一个事务内写入事件、连接历史与日汇总，并推进游标。
//
// 调用方必须按时间顺序提供同一批未同步事件；重复或未知事件会被跳过。
// 事务同时提交游标与统计，进程中断不会留下“已记游标但未累计”的半状态。
func (s *Store) ApplyUsageEvents(ctx context.Context, events []telemetry.Event) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("开始用量同步事务: %w", err)
	}
	defer tx.Rollback()

	cursors := map[string]uint64{}
	cursorRows, err := tx.QueryContext(ctx, `SELECT generation, max_seq FROM usage_sync_state`)
	if err != nil {
		return 0, fmt.Errorf("读取用量同步游标: %w", err)
	}
	for cursorRows.Next() {
		var (
			generation string
			seq        int64
		)
		if err := cursorRows.Scan(&generation, &seq); err != nil {
			cursorRows.Close()
			return 0, fmt.Errorf("读取用量同步游标: %w", err)
		}
		cursors[generation] = uint64(maxInt64(seq))
	}
	cursorRows.Close()
	if err := cursorRows.Err(); err != nil {
		return 0, fmt.Errorf("读取用量同步游标: %w", err)
	}

	seen := make(map[string]struct{}, len(events))
	maxSeqs := map[string]uint64{}
	applied := 0
	for _, event := range events {
		if event.Version != telemetry.Version || event.Seq == 0 ||
			event.Generation == "" || event.ProfileID == "" || event.SessionID == "" {
			continue
		}
		if event.Seq <= cursors[event.Generation] {
			continue
		}
		key := event.Generation + ":" + fmt.Sprint(event.Seq)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}

		// 配置已不存在时不写入，避免外键失败阻断整批历史同步。
		var exists int
		if err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM client_profile WHERE id = ?`, event.ProfileID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				if event.Seq > maxSeqs[event.Generation] {
					maxSeqs[event.Generation] = event.Seq
				}
				continue
			}
			return 0, fmt.Errorf("检查配置 %s: %w", event.ProfileID, err)
		}

		switch event.Kind {
		case telemetry.EventSessionStart:
			connectedAt := event.At
			if event.ConnectedAt != nil {
				connectedAt = *event.ConnectedAt
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT OR IGNORE INTO vpn_session
				   (id, generation, cid, profile_id, vpn_ip, source_ip, connected_at, up_bytes, down_bytes)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				event.SessionID, event.Generation, event.CID, event.ProfileID,
				event.VPNIP, event.SourceIP, formatTime(connectedAt),
				int64(event.UpBytes), int64(event.DownBytes)); err != nil {
				return 0, fmt.Errorf("写入连接历史 %s: %w", event.SessionID, err)
			}
		case telemetry.EventSessionAddress:
			if _, err := tx.ExecContext(ctx,
				`UPDATE vpn_session
				    SET vpn_ip = CASE WHEN ? <> '' THEN ? ELSE vpn_ip END,
				        source_ip = CASE WHEN ? <> '' THEN ? ELSE source_ip END
				  WHERE id = ?`,
				event.VPNIP, event.VPNIP, event.SourceIP, event.SourceIP, event.SessionID); err != nil {
				return 0, fmt.Errorf("更新连接地址 %s: %w", event.SessionID, err)
			}
		case telemetry.EventUsage, telemetry.EventSessionEnd:
			if _, err := tx.ExecContext(ctx,
				`UPDATE vpn_session
				    SET up_bytes = MAX(up_bytes, ?), down_bytes = MAX(down_bytes, ?)
				  WHERE id = ?`,
				int64(event.UpBytes), int64(event.DownBytes), event.SessionID); err != nil {
				return 0, fmt.Errorf("更新连接流量 %s: %w", event.SessionID, err)
			}
			if event.Kind == telemetry.EventSessionEnd {
				if _, err := tx.ExecContext(ctx,
					`UPDATE vpn_session
					    SET disconnected_at = ?, end_reason = ?
					  WHERE id = ? AND disconnected_at IS NULL`,
					formatTime(event.At), event.Reason, event.SessionID); err != nil {
					return 0, fmt.Errorf("结束连接历史 %s: %w", event.SessionID, err)
				}
			}
			if event.DeltaUp > 0 || event.DeltaDown > 0 {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO usage_daily (day, profile_id, up_bytes, down_bytes)
					 VALUES (?, ?, ?, ?)
					 ON CONFLICT(day, profile_id) DO UPDATE SET
					   up_bytes = up_bytes + excluded.up_bytes,
					   down_bytes = down_bytes + excluded.down_bytes`,
					UsageDay(event.At), event.ProfileID,
					int64(event.DeltaUp), int64(event.DeltaDown)); err != nil {
					return 0, fmt.Errorf("累计日流量 %s: %w", event.ProfileID, err)
				}
			}
		default:
			// 未知事件类型不阻断整批同步；保留游标推进，避免反复读取。
		}

		if event.Seq > maxSeqs[event.Generation] {
			maxSeqs[event.Generation] = event.Seq
		}
		applied++
	}

	for generation, seq := range maxSeqs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO usage_sync_state (generation, max_seq, updated_at)
			 VALUES (?, ?, ?)
			 ON CONFLICT(generation) DO UPDATE SET
			   max_seq = MAX(max_seq, excluded.max_seq),
			   updated_at = excluded.updated_at`,
			generation, int64(seq), formatTime(nowUTC())); err != nil {
			return 0, fmt.Errorf("推进用量同步游标: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("提交用量同步事务: %w", err)
	}
	return applied, nil
}

// CloseStaleSessions 关闭已不在实时快照中的连接。
//
// 运行代次变化说明代理或 OpenVPN 已重启，旧代次的连接全部结束；
// 当前代次中已消失且超过宽限期的连接，记录为未收到结束事件。
func (s *Store) CloseStaleSessions(ctx context.Context, generation string, liveIDs map[string]struct{}, now time.Time) (int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, generation, connected_at FROM vpn_session WHERE disconnected_at IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("读取未结束连接: %w", err)
	}
	type stale struct {
		id          string
		generation  string
		connectedAt time.Time
		reason      string
	}
	var closings []stale
	for rows.Next() {
		var (
			id, gen, connected string
		)
		if err := rows.Scan(&id, &gen, &connected); err != nil {
			rows.Close()
			return 0, fmt.Errorf("读取未结束连接: %w", err)
		}
		if gen != generation {
			closings = append(closings, stale{id: id, generation: gen, reason: "代理重启或离线"})
			continue
		}
		if _, ok := liveIDs[id]; ok {
			continue
		}
		if now.Sub(parseTime(connected)) < 10*time.Second {
			continue
		}
		closings = append(closings, stale{id: id, generation: gen, reason: "未收到结束事件"})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("读取未结束连接: %w", err)
	}

	for _, item := range closings {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE vpn_session SET disconnected_at = ?, end_reason = ?
			  WHERE id = ? AND disconnected_at IS NULL`,
			formatTime(now), item.reason, item.id); err != nil {
			return 0, fmt.Errorf("关闭过期连接 %s: %w", item.id, err)
		}
	}
	return len(closings), nil
}

// SessionRecord 是连接历史行。
type SessionRecord struct {
	ID           string     `json:"id"`
	Generation   string     `json:"generation"`
	CID          string     `json:"cid"`
	ProfileID    string     `json:"profileId"`
	VPNIP        string     `json:"vpnIp"`
	SourceIP     string     `json:"sourceIp"`
	ConnectedAt  time.Time  `json:"connectedAt"`
	Disconnected *time.Time `json:"disconnectedAt,omitempty"`
	EndReason    string     `json:"endReason"`
	UpBytes      uint64     `json:"upBytes"`
	DownBytes    uint64     `json:"downBytes"`
}

// SessionHistory 返回连接历史；profileID 为空时返回全部。
func (s *Store) SessionHistory(ctx context.Context, profileID string, limit int) ([]SessionRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, generation, cid, profile_id, vpn_ip, source_ip, connected_at,
		        disconnected_at, end_reason, up_bytes, down_bytes
		   FROM vpn_session
		  WHERE (? = '' OR profile_id = ?)
		  ORDER BY connected_at DESC
		  LIMIT ?`, profileID, profileID, limit)
	if err != nil {
		return nil, fmt.Errorf("读取连接历史: %w", err)
	}
	defer rows.Close()

	out := make([]SessionRecord, 0, limit)
	for rows.Next() {
		var (
			record       SessionRecord
			connected    string
			disconnected sql.NullString
			up, down     int64
		)
		if err := rows.Scan(&record.ID, &record.Generation, &record.CID, &record.ProfileID,
			&record.VPNIP, &record.SourceIP, &connected, &disconnected, &record.EndReason,
			&up, &down); err != nil {
			return nil, fmt.Errorf("读取连接历史: %w", err)
		}
		record.ConnectedAt = parseTime(connected)
		record.Disconnected = nullableTime(disconnected)
		record.UpBytes = uint64(maxInt64(up))
		record.DownBytes = uint64(maxInt64(down))
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取连接历史: %w", err)
	}
	return out, nil
}

// DailyUsage 是一个自然日的流量汇总。
type DailyUsage struct {
	Day       string `json:"day"`
	UpBytes   uint64 `json:"upBytes"`
	DownBytes uint64 `json:"downBytes"`
}

// UsageByProfile 返回各配置的累计流量。
func (s *Store) UsageByProfile(ctx context.Context) (map[string]DailyUsage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT profile_id, SUM(up_bytes), SUM(down_bytes)
		   FROM usage_daily GROUP BY profile_id`)
	if err != nil {
		return nil, fmt.Errorf("读取配置用量: %w", err)
	}
	defer rows.Close()
	out := map[string]DailyUsage{}
	for rows.Next() {
		var (
			profileID string
			up, down  int64
		)
		if err := rows.Scan(&profileID, &up, &down); err != nil {
			return nil, fmt.Errorf("读取配置用量: %w", err)
		}
		out[profileID] = DailyUsage{UpBytes: uint64(maxInt64(up)), DownBytes: uint64(maxInt64(down))}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取配置用量: %w", err)
	}
	return out, nil
}

// UsageByUser 返回各用户的累计流量（其全部配置汇总）。
func (s *Store) UsageByUser(ctx context.Context) (map[string]DailyUsage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT p.user_id, SUM(d.up_bytes), SUM(d.down_bytes)
		   FROM usage_daily d JOIN client_profile p ON p.id = d.profile_id
		  GROUP BY p.user_id`)
	if err != nil {
		return nil, fmt.Errorf("读取用户用量: %w", err)
	}
	defer rows.Close()
	out := map[string]DailyUsage{}
	for rows.Next() {
		var (
			userID   string
			up, down int64
		)
		if err := rows.Scan(&userID, &up, &down); err != nil {
			return nil, fmt.Errorf("读取用户用量: %w", err)
		}
		out[userID] = DailyUsage{UpBytes: uint64(maxInt64(up)), DownBytes: uint64(maxInt64(down))}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取用户用量: %w", err)
	}
	return out, nil
}

// UsageTrend 返回从 sinceDay（含）开始的每日总流量。
func (s *Store) UsageTrend(ctx context.Context, sinceDay string) ([]DailyUsage, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT day, SUM(up_bytes), SUM(down_bytes)
		   FROM usage_daily WHERE day >= ? GROUP BY day ORDER BY day`, sinceDay)
	if err != nil {
		return nil, fmt.Errorf("读取流量趋势: %w", err)
	}
	defer rows.Close()
	out := make([]DailyUsage, 0, 32)
	for rows.Next() {
		var (
			row      DailyUsage
			up, down int64
		)
		if err := rows.Scan(&row.Day, &up, &down); err != nil {
			return nil, fmt.Errorf("读取流量趋势: %w", err)
		}
		row.UpBytes = uint64(maxInt64(up))
		row.DownBytes = uint64(maxInt64(down))
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取流量趋势: %w", err)
	}
	return out, nil
}

// AllProfiles 返回全部配置详情，供实时页面关联用户与名称。
func (s *Store) AllProfiles(ctx context.Context) ([]ProfileDetail, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+profileDetailSelect+`
		   FROM client_profile p
		   JOIN app_user u ON u.id = p.user_id
		   LEFT JOIN certificate c ON c.profile_id = p.id AND c.status = 'active'
		  ORDER BY p.created_at, p.id`)
	if err != nil {
		return nil, fmt.Errorf("列出全部配置: %w", err)
	}
	defer rows.Close()
	out := make([]ProfileDetail, 0, 32)
	for rows.Next() {
		detail, err := scanProfileDetail(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, detail)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("列出全部配置: %w", err)
	}
	return out, nil
}

func maxInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}
