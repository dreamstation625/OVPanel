package store

import (
	"context"
	"testing"
	"time"

	"github.com/dreamstation625/ovpanel/internal/telemetry"
)

func createUsageProfile(t *testing.T, s *Store, id, userID string) {
	t.Helper()
	now := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	if _, ok, err := s.User(context.Background(), userID); err != nil {
		t.Fatalf("读取用户失败：%v", err)
	} else if !ok {
		if err := s.CreateUser(context.Background(), User{
			ID: userID, DisplayName: userID, Status: StatusActive, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("创建用户失败：%v", err)
		}
	}
	if err := s.CreateProfileWithCertificate(context.Background(), Profile{
		ID: id, UserID: userID, Name: id, AuthType: AuthCertificate,
		Status: StatusActive, RouteMode: RouteInherit, AccessMode: AccessInherit,
		CreatedAt: now, UpdatedAt: now,
	}, nil); err != nil {
		t.Fatalf("创建配置失败：%v", err)
	}
}

func TestApplyUsageEventsBuildsHistoryAndDailyTotals(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	createUsageProfile(t, s, "p1", "u1")
	createUsageProfile(t, s, "p2", "u1")

	at := time.Date(2026, 10, 30, 16, 30, 0, 0, time.UTC) // Asia/Shanghai 2026-10-31
	connected := at.Add(-5 * time.Minute)
	events := []telemetry.Event{
		{Version: 1, Seq: 1, Generation: "g1", Kind: telemetry.EventSessionStart,
			At: connected, SessionID: "g1:1", CID: "1", ProfileID: "p1",
			VPNIP: "10.8.0.2", SourceIP: "203.0.113.5", ConnectedAt: &connected},
		{Version: 1, Seq: 2, Generation: "g1", Kind: telemetry.EventUsage,
			At: at, SessionID: "g1:1", CID: "1", ProfileID: "p1",
			UpBytes: 100, DownBytes: 200, DeltaUp: 100, DeltaDown: 200},
		{Version: 1, Seq: 3, Generation: "g1", Kind: telemetry.EventSessionEnd,
			At: at.Add(time.Minute), SessionID: "g1:1", CID: "1", ProfileID: "p1",
			UpBytes: 150, DownBytes: 250, DeltaUp: 50, DeltaDown: 50, Reason: "client_disconnect"},
	}
	if n, err := s.ApplyUsageEvents(ctx, events); err != nil || n != 3 {
		t.Fatalf("写入事件失败：n=%d err=%v", n, err)
	}

	// 同一批事件重复应用不会重复累计。
	if n, err := s.ApplyUsageEvents(ctx, events); err != nil || n != 0 {
		t.Fatalf("重复事件不应累计：n=%d err=%v", n, err)
	}

	history, err := s.SessionHistory(ctx, "p1", 10)
	if err != nil || len(history) != 1 {
		t.Fatalf("连接历史异常：%+v %v", history, err)
	}
	if history[0].VPNIP != "10.8.0.2" || history[0].UpBytes != 150 || history[0].DownBytes != 250 {
		t.Fatalf("连接历史内容异常：%+v", history[0])
	}
	if history[0].Disconnected == nil || history[0].EndReason != "client_disconnect" {
		t.Fatalf("连接结束状态异常：%+v", history[0])
	}

	usage, err := s.UsageByProfile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if usage["p1"].UpBytes != 150 || usage["p1"].DownBytes != 250 {
		t.Fatalf("日汇总异常：%+v", usage)
	}
	byUser, err := s.UsageByUser(ctx)
	if err != nil || byUser["u1"].UpBytes != 150 {
		t.Fatalf("用户汇总异常：%+v %v", byUser, err)
	}
	trend, err := s.UsageTrend(ctx, "2026-10-31")
	if err != nil || len(trend) != 1 || trend[0].Day != "2026-10-31" {
		t.Fatalf("流量趋势异常：%+v %v", trend, err)
	}

	cursors, err := s.UsageCursor(ctx)
	if err != nil || cursors["g1"] != 3 {
		t.Fatalf("同步游标异常：%+v %v", cursors, err)
	}
}

func TestQuotaResetIncrementsEpochAndKeepsHistory(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	createUsageProfile(t, s, "p1", "u1")
	now := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)

	epoch, err := s.IncrementQuotaEpoch(ctx, "p1", now)
	if err != nil || epoch != 1 {
		t.Fatalf("首次重置失败：%d %v", epoch, err)
	}
	epoch, err = s.IncrementQuotaEpoch(ctx, "p1", now.Add(time.Hour))
	if err != nil || epoch != 2 {
		t.Fatalf("再次重置失败：%d %v", epoch, err)
	}
	detail, ok, err := s.Profile(ctx, "p1")
	if err != nil || !ok || detail.QuotaEpoch != 2 {
		t.Fatalf("配置代次未更新：%+v %v", detail, err)
	}
	reset, ok, err := s.LatestQuotaReset(ctx, "p1")
	if err != nil || !ok || reset.Epoch != 2 {
		t.Fatalf("重置记录异常：%+v %v", reset, err)
	}
	if _, err := s.IncrementQuotaEpoch(ctx, "missing", now); err != ErrNotFound {
		t.Fatalf("不存在的配置应返回 ErrNotFound：%v", err)
	}
}

func TestCloseStaleSessionsClosesOldGeneration(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	createUsageProfile(t, s, "p1", "u1")
	old := time.Now().UTC().Add(-time.Hour)
	events := []telemetry.Event{
		{Version: 1, Seq: 1, Generation: "old", Kind: telemetry.EventSessionStart,
			At: old, SessionID: "old:1", CID: "1", ProfileID: "p1", ConnectedAt: &old},
	}
	if _, err := s.ApplyUsageEvents(ctx, events); err != nil {
		t.Fatal(err)
	}
	closed, err := s.CloseStaleSessions(ctx, "new", map[string]struct{}{}, time.Now().UTC())
	if err != nil || closed != 1 {
		t.Fatalf("旧代次连接应被关闭：closed=%d err=%v", closed, err)
	}
	history, err := s.SessionHistory(ctx, "p1", 10)
	if err != nil || history[0].Disconnected == nil || history[0].EndReason != "代理重启或离线" {
		t.Fatalf("旧连接结束原因异常：%+v %v", history, err)
	}
}

func TestUpdateProfileLimitsStoresNullableValues(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	createUsageProfile(t, s, "p1", "u1")
	now := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	expires := now.Add(24 * time.Hour)
	concurrent, quota, up, down := int64(2), int64(5_000_000_000), int64(10_000_000), int64(50_000_000)
	if err := s.UpdateProfileLimits(ctx, "p1", &expires, &concurrent, &quota, &up, &down, now); err != nil {
		t.Fatal(err)
	}
	detail, ok, err := s.Profile(ctx, "p1")
	if err != nil || !ok {
		t.Fatalf("读取配置失败：%v", err)
	}
	if detail.ConcurrentLimit == nil || *detail.ConcurrentLimit != 2 ||
		detail.QuotaBytes == nil || *detail.QuotaBytes != quota ||
		detail.UploadBps == nil || *detail.UploadBps != up ||
		detail.DownloadBps == nil || *detail.DownloadBps != down ||
		detail.ExpiresAt == nil || !detail.ExpiresAt.Equal(expires) {
		t.Fatalf("限额保存异常：%+v", detail.Profile)
	}
	if err := s.UpdateProfileLimits(ctx, "p1", nil, nil, nil, nil, nil, now); err != nil {
		t.Fatal(err)
	}
	detail, _, _ = s.Profile(ctx, "p1")
	if detail.ConcurrentLimit != nil || detail.QuotaBytes != nil || detail.ExpiresAt != nil {
		t.Fatalf("清除限额失败：%+v", detail.Profile)
	}
}
