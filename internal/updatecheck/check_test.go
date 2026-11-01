package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T, releases []Release, tagCommit map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/test/repo/releases":
			json.NewEncoder(w).Encode(releases)
		case strings.HasPrefix(r.URL.Path, "/repos/test/repo/git/ref/tags/"):
			tag := strings.TrimPrefix(r.URL.Path, "/repos/test/repo/git/ref/tags/")
			sha, ok := tagCommit[tag]
			if !ok {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"object": map[string]string{"sha": sha, "type": "commit"}})
		default:
			http.NotFound(w, r)
		}
	}))
}

func newTestChecker(t *testing.T, server *httptest.Server) *Checker {
	return NewWithOptions(Options{
		Client: server.Client(), APIBase: server.URL, Repository: "test/repo",
		TTL: time.Minute, Now: func() time.Time { return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) },
	})
}

func TestStableUpdate(t *testing.T) {
	releases := []Release{
		{TagName: "0.0.2", Name: "v0.0.2", Body: "修复内容", HTMLURL: "https://example.test/r/0.0.2", PublishedAt: "2026-11-02T00:00:00Z"},
		{TagName: "0.0.1", Name: "v0.0.1", Body: "旧版", PublishedAt: "2026-11-01T00:00:00Z"},
	}
	server := testServer(t, releases, nil)
	defer server.Close()
	result, err := newTestChecker(t, server).Check(context.Background(), "0.0.1", "aaaaaaa", "2026-11-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if !result.UpdateAvailable || result.LatestVersion != "0.0.2" || result.Notes != "修复内容" {
		t.Fatalf("稳定版更新判断异常：%+v", result)
	}
}

func TestStableNoUpdate(t *testing.T) {
	releases := []Release{{TagName: "0.0.1", Name: "v0.0.1", PublishedAt: "2026-11-01T00:00:00Z"}}
	server := testServer(t, releases, nil)
	defer server.Close()
	result, err := newTestChecker(t, server).Check(context.Background(), "0.0.1", "aaaaaaa", "2026-11-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateAvailable {
		t.Fatalf("同版本不应提示更新：%+v", result)
	}
}

func TestDevBuildUsesLatestStable(t *testing.T) {
	releases := []Release{
		{TagName: "0.0.2", Name: "v0.0.2", Body: "正式版", PublishedAt: "2026-11-02T00:00:00Z"},
		{TagName: "dev", Name: "dev", Body: "开发版", PublishedAt: "2026-11-03T00:00:00Z", Prerelease: true},
	}
	server := testServer(t, releases, nil)
	defer server.Close()
	result, err := newTestChecker(t, server).Check(context.Background(), "dev", "aaaaaaa", "2026-11-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if !result.UpdateAvailable || result.LatestVersion != "0.0.2" || result.Notes != "正式版" {
		t.Fatalf("开发版应只对照正式 Release：%+v", result)
	}
}

func TestDevBuildNoUpdateWhenStableReleasedBeforeBuild(t *testing.T) {
	releases := []Release{{TagName: "0.0.2", Name: "v0.0.2", PublishedAt: "2026-10-31T00:00:00Z"}}
	server := testServer(t, releases, map[string]string{"0.0.2": "bbbbbbbbbbbb"})
	defer server.Close()
	result, err := newTestChecker(t, server).Check(context.Background(), "dev", "aaaaaaaaaaaa", "2026-11-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateAvailable {
		t.Fatalf("早于当前构建发布的正式版不应提示更新：%+v", result)
	}
}

func TestDevBuildFallsBackToTagCommit(t *testing.T) {
	releases := []Release{{TagName: "0.0.2", Name: "v0.0.2", PublishedAt: "2026-11-02T00:00:00Z"}}
	server := testServer(t, releases, map[string]string{"0.0.2": "bbbbbbbbbbbb"})
	defer server.Close()
	checker := newTestChecker(t, server)

	result, err := checker.Check(context.Background(), "dev", "aaaaaaaaaaaa", "unknown")
	if err != nil {
		t.Fatal(err)
	}
	if !result.UpdateAvailable {
		t.Fatalf("构建时间不可用时应按 tag 提交判断更新：%+v", result)
	}

	result, err = checker.Check(context.Background(), "dev", "bbbbbbbbbbbb", "unknown")
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateAvailable {
		t.Fatalf("同一提交不应提示更新：%+v", result)
	}
}

func TestPrereleaseVersionUsesLatestStable(t *testing.T) {
	releases := []Release{{TagName: "0.0.2", Name: "v0.0.2", PublishedAt: "2026-11-02T00:00:00Z"}}
	server := testServer(t, releases, nil)
	defer server.Close()
	result, err := newTestChecker(t, server).Check(context.Background(), "0.0.1-dev", "aaaaaaa", "2026-11-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if !result.UpdateAvailable || result.LatestVersion != "0.0.2" {
		t.Fatalf("预发布版本应可更新到更高的正式版本：%+v", result)
	}
}

func TestPrereleaseVersionSameBaseUsesBuildTime(t *testing.T) {
	releases := []Release{{TagName: "0.0.2", Name: "v0.0.2", PublishedAt: "2026-11-02T00:00:00Z"}}
	server := testServer(t, releases, nil)
	defer server.Close()
	checker := newTestChecker(t, server)

	result, err := checker.Check(context.Background(), "0.0.2-dev", "aaaaaaa", "2026-11-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if !result.UpdateAvailable {
		t.Fatalf("同基础版本且正式版晚于构建发布时应提示更新：%+v", result)
	}

	result, err = checker.Check(context.Background(), "0.0.2-dev", "aaaaaaa", "2026-11-03T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateAvailable {
		t.Fatalf("正式版早于构建发布时不应提示更新：%+v", result)
	}
}

func TestPrereleaseVersionDoesNotOfferOlderStable(t *testing.T) {
	releases := []Release{{TagName: "0.0.1", Name: "v0.0.1", PublishedAt: "2026-11-02T00:00:00Z"}}
	server := testServer(t, releases, nil)
	defer server.Close()
	result, err := newTestChecker(t, server).Check(context.Background(), "0.0.2-dev", "aaaaaaa", "2026-11-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateAvailable {
		t.Fatalf("较低版本不应提示为更新：%+v", result)
	}
}

func TestPrereleaseReleasesAreIgnored(t *testing.T) {
	releases := []Release{
		{TagName: "0.0.1", Name: "v0.0.1", PublishedAt: "2026-11-01T00:00:00Z"},
		{TagName: "0.0.2", Name: "v0.0.2", PublishedAt: "2026-11-03T00:00:00Z", Prerelease: true},
		{TagName: "dev", Name: "dev", PublishedAt: "2026-11-04T00:00:00Z", Prerelease: true},
	}
	server := testServer(t, releases, nil)
	defer server.Close()
	result, err := newTestChecker(t, server).Check(context.Background(), "0.0.1", "aaaaaaa", "2026-11-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateAvailable || result.LatestVersion != "0.0.1" {
		t.Fatalf("预发布版本不应参与更新判断：%+v", result)
	}
}

func TestNoStableRelease(t *testing.T) {
	releases := []Release{{
		TagName: "dev", Name: "dev", PublishedAt: "2026-11-04T00:00:00Z", Prerelease: true,
	}}
	server := testServer(t, releases, nil)
	defer server.Close()
	_, err := newTestChecker(t, server).Check(context.Background(), "0.0.1", "aaaaaaa", "2026-11-01T00:00:00Z")
	if !errors.Is(err, ErrNoStableRelease) {
		t.Fatalf("没有正式版本时应返回明确错误：%v", err)
	}
}

func TestOutOfOrderStableUsesHighestVersion(t *testing.T) {
	releases := []Release{
		{TagName: "0.0.2", Name: "v0.0.2", PublishedAt: "2026-11-03T00:00:00Z"},
		{TagName: "0.0.10", Name: "v0.0.10", PublishedAt: "2026-10-01T00:00:00Z"},
	}
	server := testServer(t, releases, nil)
	defer server.Close()
	result, err := newTestChecker(t, server).Check(context.Background(), "0.0.2", "aaaaaaa", "2026-11-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if !result.UpdateAvailable || result.LatestVersion != "0.0.10" {
		t.Fatalf("应按语义版本选择最高正式版：%+v", result)
	}
}

func TestGitHubErrorIsReturned(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	defer server.Close()
	_, err := newTestChecker(t, server).Check(context.Background(), "0.0.1", "aaaaaaa", "2026-11-01T00:00:00Z")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("GitHub 错误未返回：%v", err)
	}
}
