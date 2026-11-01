package updatecheck

import (
	"context"
	"encoding/json"
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

func TestDevUpdateUsesTagCommit(t *testing.T) {
	releases := []Release{{
		TagName: "dev", Name: "dev", Body: "开发版更新", HTMLURL: "https://example.test/r/dev",
		PublishedAt: "2026-11-02T00:00:00Z", Prerelease: true,
	}}
	server := testServer(t, releases, map[string]string{"dev": "bbbbbbbbbbbb"})
	defer server.Close()
	checker := newTestChecker(t, server)

	result, err := checker.Check(context.Background(), "dev", "aaaaaaaaaaaa", "2026-11-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if !result.UpdateAvailable || result.LatestVersion != "dev" || result.Notes != "开发版更新" {
		t.Fatalf("开发版更新判断异常：%+v", result)
	}

	// 同一构建信息在缓存有效期内直接返回上次结果。
	result, err = checker.Check(context.Background(), "dev", "aaaaaaaaaaaa", "2026-11-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if !result.UpdateAvailable {
		t.Fatal("缓存结果不应在有效期内变化")
	}
}

func TestDevNoUpdateWhenCommitMatches(t *testing.T) {
	releases := []Release{{
		TagName: "dev", PublishedAt: "2026-11-02T00:00:00Z", Prerelease: true,
	}}
	server := testServer(t, releases, map[string]string{"dev": "bbbbbbbbbbbb"})
	defer server.Close()
	result, err := newTestChecker(t, server).Check(context.Background(), "dev", "bbbbbbbbbbbb", "2026-11-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdateAvailable {
		t.Fatalf("相同提交不应提示更新：%+v", result)
	}
}

func TestPrereleaseVersionUsesDevChannel(t *testing.T) {
	releases := []Release{{
		TagName: "dev", Name: "dev", Body: "开发版更新", PublishedAt: "2026-11-02T00:00:00Z", Prerelease: true,
	}}
	server := testServer(t, releases, map[string]string{"dev": "bbbbbbbbbbbb"})
	defer server.Close()
	result, err := newTestChecker(t, server).Check(context.Background(), "0.0.1-dev", "aaaaaaaaaaaa", "2026-11-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if !result.UpdateAvailable || result.LatestVersion != "dev" {
		t.Fatalf("预发布版本应跟随 dev Release：%+v", result)
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
