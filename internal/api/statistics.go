package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dreamstation625/ovpanel/internal/accounts"
	"github.com/dreamstation625/ovpanel/internal/store"
	"github.com/dreamstation625/ovpanel/internal/telemetry"
)

// ---------- 实时连接与统计 ----------

type connectionDTO struct {
	ID              string  `json:"id"`
	CID             string  `json:"cid"`
	ProfileID       string  `json:"profileId"`
	ProfileName     string  `json:"profileName"`
	UserID          string  `json:"userId"`
	UserDisplayName string  `json:"userDisplayName"`
	AuthType        string  `json:"authType"`
	VPNIP           string  `json:"vpnIp"`
	SourceIP        string  `json:"sourceIp"`
	ConnectedAt     string  `json:"connectedAt"`
	DurationSeconds int64   `json:"durationSeconds"`
	UpBytes         uint64  `json:"upBytes"`
	DownBytes       uint64  `json:"downBytes"`
	UpRate          float64 `json:"upRate"`
	DownRate        float64 `json:"downRate"`
	Blocked         bool    `json:"blocked"`
	BlockReason     string  `json:"blockReason,omitempty"`
	KnownProfile    bool    `json:"knownProfile"`
}

type usagePoint struct {
	Day       string `json:"day"`
	UpBytes   uint64 `json:"upBytes"`
	DownBytes uint64 `json:"downBytes"`
}

type overviewResponse struct {
	AgentPresent       bool            `json:"agentPresent"`
	AgentUpdatedAt     string          `json:"agentUpdatedAt,omitempty"`
	OnlineConnections  int             `json:"onlineConnections"`
	UpRate             float64         `json:"upRate"`
	DownRate           float64         `json:"downRate"`
	TodayUpBytes       uint64          `json:"todayUpBytes"`
	TodayDownBytes     uint64          `json:"todayDownBytes"`
	Users              int             `json:"users"`
	Profiles           int             `json:"profiles"`
	ActiveCertificates int             `json:"activeCertificates"`
	AdminSessions      int             `json:"adminSessions"`
	Trend              []usagePoint    `json:"trend"`
	Connections        []connectionDTO `json:"connections"`
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s.syncStatistics(ctx)
	snapshot, present := s.liveSnapshot()
	now := s.now()

	users, profiles, err := s.store.AccountCounts(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	activeCerts, _, expiredCerts, err := s.store.CertificateStats(ctx, now)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	adminSessions, err := s.store.CountSessions(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	details, err := s.store.AllProfiles(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	connections := buildConnections(snapshot, details, now)

	trend, err := s.recentTrend(ctx, 7, now)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	var upRate, downRate float64
	for _, session := range snapshot.Sessions {
		upRate += session.UpRate
		downRate += session.DownRate
	}

	resp := overviewResponse{
		AgentPresent:       present,
		OnlineConnections:  len(snapshot.Sessions),
		UpRate:             upRate,
		DownRate:           downRate,
		TodayUpBytes:       trend[len(trend)-1].UpBytes,
		TodayDownBytes:     trend[len(trend)-1].DownBytes,
		Users:              users,
		Profiles:           profiles,
		ActiveCertificates: activeCerts - expiredCerts,
		AdminSessions:      adminSessions,
		Trend:              trend,
		Connections:        connections,
	}
	if present {
		resp.AgentUpdatedAt = snapshot.UpdatedAt.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s.syncStatistics(ctx)
	snapshot, present := s.liveSnapshot()
	details, err := s.store.AllProfiles(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	connections := buildConnections(snapshot, details, s.now())
	var upRate, downRate float64
	for _, session := range snapshot.Sessions {
		upRate += session.UpRate
		downRate += session.DownRate
	}
	resp := map[string]any{
		"agentPresent": present,
		"connections":  connections,
		"online":       len(connections),
		"upRate":       upRate,
		"downRate":     downRate,
	}
	if present {
		resp["updatedAt"] = snapshot.UpdatedAt.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}

type historyDTO struct {
	ID              string `json:"id"`
	ProfileID       string `json:"profileId"`
	ProfileName     string `json:"profileName"`
	UserDisplayName string `json:"userDisplayName"`
	VPNIP           string `json:"vpnIp"`
	SourceIP        string `json:"sourceIp"`
	ConnectedAt     string `json:"connectedAt"`
	DisconnectedAt  string `json:"disconnectedAt,omitempty"`
	DurationSeconds int64  `json:"durationSeconds"`
	EndReason       string `json:"endReason"`
	UpBytes         uint64 `json:"upBytes"`
	DownBytes       uint64 `json:"downBytes"`
}

func (s *Server) handleConnectionHistory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s.syncStatistics(ctx)
	profileID := strings.TrimSpace(r.URL.Query().Get("profileId"))
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "limit 必须是正整数")
			return
		}
		limit = value
	}
	records, err := s.store.SessionHistory(ctx, profileID, limit)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	details, err := s.store.AllProfiles(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	profiles := profileMap(details)
	out := make([]historyDTO, 0, len(records))
	for _, record := range records {
		item := historyDTO{
			ID:          record.ID,
			ProfileID:   record.ProfileID,
			VPNIP:       record.VPNIP,
			SourceIP:    record.SourceIP,
			ConnectedAt: record.ConnectedAt.Format(time.RFC3339),
			EndReason:   record.EndReason,
			UpBytes:     record.UpBytes,
			DownBytes:   record.DownBytes,
		}
		if detail, ok := profiles[record.ProfileID]; ok {
			item.ProfileName = detail.Name
			item.UserDisplayName = detail.UserDisplayName
		}
		if record.Disconnected != nil {
			item.DisconnectedAt = record.Disconnected.Format(time.RFC3339)
			item.DurationSeconds = int64(record.Disconnected.Sub(record.ConnectedAt).Seconds())
		} else {
			item.DurationSeconds = int64(s.now().Sub(record.ConnectedAt).Seconds())
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (s *Server) handleUsageTrend(w http.ResponseWriter, r *http.Request) {
	days := 30
	if raw := r.URL.Query().Get("days"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 || value > 365 {
			writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "days 必须在 1 到 365 之间")
			return
		}
		days = value
	}
	s.syncStatistics(r.Context())
	trend, err := s.recentTrend(r.Context(), days, s.now())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"trend": trend})
}

func (s *Server) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	if s.runtime == nil {
		writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "运行控制未配置")
		return
	}
	id := r.PathValue("id")
	snapshot, present := s.liveSnapshot()
	if !present {
		writeError(w, r, http.StatusConflict, CodeConflict, "运行代理未提供实时状态")
		return
	}
	var target *telemetry.LiveSession
	for i := range snapshot.Sessions {
		if snapshot.Sessions[i].ID == id {
			target = &snapshot.Sessions[i]
			break
		}
	}
	if target == nil {
		writeError(w, r, http.StatusNotFound, CodeNotFound, "连接已结束")
		return
	}
	if err := s.runtime.Disconnect(target.CID); err != nil {
		s.internalError(w, r, err)
		return
	}
	s.log.Warn("session disconnect requested", "session_id", id, "cid", target.CID, "profile_id", target.ProfileID)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- 配置限额 ----------

type limitsRequest struct {
	ExpiresAt       string `json:"expiresAt"`
	ConcurrentLimit int64  `json:"concurrentLimit"`
	QuotaBytes      int64  `json:"quotaBytes"`
	UploadBps       int64  `json:"uploadBps"`
	DownloadBps     int64  `json:"downloadBps"`
}

func (s *Server) handleUpdateProfileLimits(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req limitsRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if msg := validateLimits(req, s.now()); msg != "" {
		writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, msg)
		return
	}
	expiresAt, err := parseOptionalTime(req.ExpiresAt)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, err.Error())
		return
	}
	if err := s.store.UpdateProfileLimits(r.Context(), id, expiresAt,
		optionalPositive(req.ConcurrentLimit), optionalPositive(req.QuotaBytes),
		optionalPositive(req.UploadBps), optionalPositive(req.DownloadBps), s.now()); err != nil {
		s.writeAccountError(w, r, err)
		return
	}
	s.applyRuntimeBundle(r.Context(), "profile limits updated")
	detail, ok, err := s.store.Profile(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !ok {
		writeError(w, r, http.StatusNotFound, CodeNotFound, "客户端配置不存在")
		return
	}
	dto := profileToDTO(detail, s.now())
	s.enrichProfileDTO(r.Context(), detail, &dto)
	s.log.Info("profile limits updated", "profile_id", id)
	writeJSON(w, http.StatusOK, map[string]any{"profile": dto})
}

func (s *Server) handleResetQuota(w http.ResponseWriter, r *http.Request) {
	profileID := r.PathValue("id")
	var req struct {
		AdminPassword string `json:"adminPassword"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if _, ok := s.verifyAdminPassword(w, r, req.AdminPassword); !ok {
		return
	}
	epoch, err := s.store.IncrementQuotaEpoch(r.Context(), profileID, s.now())
	if err != nil {
		s.writeAccountError(w, r, err)
		return
	}
	s.applyRuntimeBundle(r.Context(), "quota reset")
	detail, ok, err := s.store.Profile(r.Context(), profileID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !ok {
		writeError(w, r, http.StatusNotFound, CodeNotFound, "客户端配置不存在")
		return
	}
	dto := profileToDTO(detail, s.now())
	s.enrichProfileDTO(r.Context(), detail, &dto)
	s.log.Warn("profile quota reset", "profile_id", profileID, "epoch", epoch)
	writeJSON(w, http.StatusOK, map[string]any{"profile": dto, "epoch": epoch})
}

func validateLimits(req limitsRequest, now time.Time) string {
	if req.ConcurrentLimit < 0 || req.ConcurrentLimit > 1000 {
		return "并发上限需在 0 到 1000 之间，0 表示不限"
	}
	if req.QuotaBytes < 0 || (req.QuotaBytes > 0 && req.QuotaBytes < 1_000_000) || req.QuotaBytes > 1_000_000_000_000_000 {
		return "流量额度需为 0 或至少 0.001 GB"
	}
	for _, bps := range []int64{req.UploadBps, req.DownloadBps} {
		if bps < 0 || (bps > 0 && bps < 8_000) || bps > 10_000_000_000 {
			return "限速需为 0 或至少 0.008 Mbps，最大 10000 Mbps"
		}
	}
	if strings.TrimSpace(req.ExpiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(req.ExpiresAt))
		if err != nil {
			return "到期时间格式必须为 RFC3339"
		}
		if !now.Before(parsed.UTC()) {
			return "到期时间必须晚于当前时间"
		}
	}
	return ""
}

func optionalPositive(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	copied := value
	return &copied
}

// ---------- 富集与辅助 ----------

func (s *Server) syncStatistics(ctx context.Context) {
	if s.statistics == nil {
		return
	}
	result, err := s.statistics.Sync(ctx)
	if err != nil {
		s.log.Warn("sync usage statistics failed", "error", err)
		return
	}
	if result.Events > 0 || result.Closed > 0 {
		s.log.Info("usage statistics synced", "events", result.Events, "closed", result.Closed)
	}
}

func (s *Server) liveSnapshot() (telemetry.Snapshot, bool) {
	if s.statistics == nil {
		return telemetry.Snapshot{}, false
	}
	return s.statistics.Live()
}

func (s *Server) enrichProfileDTO(ctx context.Context, detail store.ProfileDetail, dto *profileDTO) {
	snapshot, present := s.liveSnapshot()
	if present {
		dto.UsageKnown = true
		for _, usage := range snapshot.Profiles {
			if usage.ProfileID != detail.ID {
				continue
			}
			dto.UsageBytes = usage.UsageBytes
			dto.UsageGaps = usage.Gaps
			if dto.QuotaEpoch == 0 && usage.QuotaEpoch > 0 {
				dto.QuotaEpoch = usage.QuotaEpoch
			}
			break
		}
		for _, session := range snapshot.Sessions {
			if session.ProfileID == detail.ID {
				dto.OnlineSessions++
			}
		}
	}
	if dto.QuotaBytes > 0 {
		dto.UsagePercent = float64(dto.UsageBytes) / float64(dto.QuotaBytes) * 100
	}
	if dto.QuotaBytes > 0 && dto.UsageBytes >= uint64(dto.QuotaBytes) && dto.EffectiveStatus == "active" {
		dto.EffectiveStatus = "quota_exhausted"
		dto.StatusReason = "流量额度已用尽"
	}
	if reset, ok, err := s.store.LatestQuotaReset(ctx, detail.ID); err == nil && ok {
		dto.QuotaResetAt = reset.ResetAt.Format(time.RFC3339)
	}
}

func (s *Server) enrichUserList(ctx context.Context, users []userDTO) {
	snapshot, present := s.liveSnapshot()
	usageByProfile, err := s.store.UsageByProfile(ctx)
	if err != nil {
		s.log.Warn("read usage by profile failed", "error", err)
	}
	details, err := s.store.AllProfiles(ctx)
	if err != nil {
		return
	}
	profileUser := make(map[string]string, len(details))
	for _, detail := range details {
		profileUser[detail.ID] = detail.UserID
	}
	onlineByUser := map[string]int{}
	if present {
		for _, session := range snapshot.Sessions {
			if userID, ok := profileUser[session.ProfileID]; ok {
				onlineByUser[userID]++
			}
		}
	}
	for i := range users {
		users[i].OnlineSessions = onlineByUser[users[i].ID]
		var total uint64
		for profileID, userID := range profileUser {
			if userID != users[i].ID {
				continue
			}
			if usage, ok := usageByProfile[profileID]; ok {
				total += usage.UpBytes + usage.DownBytes
			}
		}
		users[i].UsageBytes = total
	}
}

func (s *Server) enrichUserDetail(ctx context.Context, detail *userDetailDTO) {
	snapshot, present := s.liveSnapshot()
	usageByProfile, err := s.store.UsageByProfile(ctx)
	if err == nil {
		for i := range detail.Profiles {
			if usage, ok := usageByProfile[detail.Profiles[i].ID]; ok {
				detail.Profiles[i].UsageBytes = usage.UpBytes + usage.DownBytes
			}
		}
	}
	if present {
		for i := range detail.Profiles {
			detail.Profiles[i].UsageKnown = true
			for _, usage := range snapshot.Profiles {
				if usage.ProfileID == detail.Profiles[i].ID {
					detail.Profiles[i].UsageBytes = usage.UsageBytes
					break
				}
			}
			for _, session := range snapshot.Sessions {
				if session.ProfileID == detail.Profiles[i].ID {
					detail.Profiles[i].OnlineSessions++
				}
			}
			if detail.Profiles[i].QuotaBytes > 0 {
				detail.Profiles[i].UsagePercent = float64(detail.Profiles[i].UsageBytes) /
					float64(detail.Profiles[i].QuotaBytes) * 100
				if detail.Profiles[i].UsageBytes >= uint64(detail.Profiles[i].QuotaBytes) &&
					detail.Profiles[i].EffectiveStatus == "active" {
					detail.Profiles[i].EffectiveStatus = "quota_exhausted"
					detail.Profiles[i].StatusReason = "流量额度已用尽"
				}
			}
		}
		detail.User.OnlineSessions = 0
		for i := range detail.Profiles {
			detail.User.OnlineSessions += detail.Profiles[i].OnlineSessions
		}
	}
	for i := range detail.Profiles {
		detail.User.UsageBytes += detail.Profiles[i].UsageBytes
	}
}

func (s *Server) recentTrend(ctx context.Context, days int, now time.Time) ([]usagePoint, error) {
	if days < 1 {
		days = 1
	}
	start := now.AddDate(0, 0, -(days - 1))
	rows, err := s.store.UsageTrend(ctx, store.UsageDay(start))
	if err != nil {
		return nil, err
	}
	byDay := make(map[string]usagePoint, len(rows))
	for _, row := range rows {
		byDay[row.Day] = usagePoint{Day: row.Day, UpBytes: row.UpBytes, DownBytes: row.DownBytes}
	}
	out := make([]usagePoint, 0, days)
	for i := 0; i < days; i++ {
		day := store.UsageDay(start.AddDate(0, 0, i))
		point, ok := byDay[day]
		if !ok {
			point = usagePoint{Day: day}
		}
		out = append(out, point)
	}
	return out, nil
}

func buildConnections(snapshot telemetry.Snapshot, details []store.ProfileDetail, now time.Time) []connectionDTO {
	profiles := profileMap(details)
	out := make([]connectionDTO, 0, len(snapshot.Sessions))
	for _, session := range snapshot.Sessions {
		item := connectionDTO{
			ID:              session.ID,
			CID:             session.CID,
			ProfileID:       session.ProfileID,
			VPNIP:           session.VPNIP,
			SourceIP:        session.SourceIP,
			ConnectedAt:     session.ConnectedAt.Format(time.RFC3339),
			DurationSeconds: int64(now.Sub(session.ConnectedAt).Seconds()),
			UpBytes:         session.UpBytes,
			DownBytes:       session.DownBytes,
			UpRate:          session.UpRate,
			DownRate:        session.DownRate,
			Blocked:         session.Blocked,
			BlockReason:     session.BlockReason,
		}
		if detail, ok := profiles[session.ProfileID]; ok {
			item.ProfileName = detail.Name
			item.UserID = detail.UserID
			item.UserDisplayName = detail.UserDisplayName
			item.AuthType = detail.AuthType
			item.KnownProfile = true
		} else {
			item.ProfileName = "未知配置"
		}
		out = append(out, item)
	}
	return out
}

func profileMap(details []store.ProfileDetail) map[string]store.ProfileDetail {
	out := make(map[string]store.ProfileDetail, len(details))
	for _, detail := range details {
		out[detail.ID] = detail
	}
	return out
}

// applyRuntimeBundle 把账号、限额或额度变更同步给运行代理。
// 尚未配置导出地址时没有可下发的运行配置，不视为操作失败。
func (s *Server) applyRuntimeBundle(ctx context.Context, reason string) {
	if s.runtime == nil {
		return
	}
	if _, err := s.runtime.Apply(ctx); err != nil && !errors.Is(err, accounts.ErrExportNotConfigured) {
		s.log.Warn("apply runtime bundle failed", "reason", reason, "error", err)
	}
}
