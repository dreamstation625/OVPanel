package store

import (
	"context"
	"testing"
	"time"
)

func TestServerConfigDraftAndActivation(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	now := time.Now().UTC()

	first, err := s.SaveServerConfigDraft(ctx, `{"v":1}`, "hash-1", now)
	if err != nil {
		t.Fatalf("保存草稿失败：%v", err)
	}
	second, err := s.SaveServerConfigDraft(ctx, `{"v":2}`, "hash-2", now.Add(time.Second))
	if err != nil {
		t.Fatalf("保存第二份草稿失败：%v", err)
	}
	if second.Revision <= first.Revision {
		t.Fatalf("修订号未递增：%d → %d", first.Revision, second.Revision)
	}

	state, err := s.ServerConfigState(ctx)
	if err != nil {
		t.Fatalf("读取状态失败：%v", err)
	}
	if state.Active != nil {
		t.Fatalf("尚未激活却存在生效修订：%+v", state.Active)
	}
	if state.Draft == nil || state.Draft.Revision != second.Revision {
		t.Fatalf("草稿指针错误：%+v", state.Draft)
	}
	oldDraft, _, _ := s.ServerConfigRevision(ctx, first.Revision)
	if oldDraft.State != ServerConfigDiscarded {
		t.Fatalf("旧草稿状态 = %q", oldDraft.State)
	}

	active, err := s.ActivateServerConfig(ctx, second.Revision, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("激活失败：%v", err)
	}
	if active.State != ServerConfigActive || active.AppliedAt == nil {
		t.Fatalf("激活结果异常：%+v", active)
	}
	state, _ = s.ServerConfigState(ctx)
	if state.Active == nil || state.Active.Revision != second.Revision || state.Draft != nil {
		t.Fatalf("激活后状态异常：%+v", state)
	}

	third, err := s.SaveServerConfigDraft(ctx, `{"v":3}`, "hash-3", now.Add(3*time.Second))
	if err != nil {
		t.Fatalf("保存第三份草稿失败：%v", err)
	}
	if _, err := s.ActivateServerConfig(ctx, third.Revision, now.Add(4*time.Second)); err != nil {
		t.Fatalf("第二次激活失败：%v", err)
	}
	oldActive, _, _ := s.ServerConfigRevision(ctx, second.Revision)
	if oldActive.State != ServerConfigSuperseded {
		t.Fatalf("旧生效修订状态 = %q", oldActive.State)
	}
}

func TestActivateMissingDraftFails(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if _, err := s.ActivateServerConfig(ctx, 999, time.Now().UTC()); err == nil {
		t.Fatal("激活不存在的修订应失败")
	}
}
