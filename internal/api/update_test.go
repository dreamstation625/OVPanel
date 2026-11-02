package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dreamstation625/ovpanel/internal/updatecheck"
)

func TestUpdateCheck(t *testing.T) {
	releases := []updatecheck.Release{{
		TagName: "0.0.2", Name: "v0.0.2", Body: "更新内容", HTMLURL: "https://example.test/release",
		PublishedAt: "2026-11-02T00:00:00Z",
	}}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/test/repo/releases" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(releases)
	}))
	defer fake.Close()

	srv := newTestServer(t)
	srv.updateChecker = updatecheck.NewWithOptions(updatecheck.Options{
		Client: fake.Client(), APIBase: fake.URL, Repository: "test/repo",
		TTL: time.Minute, Now: func() time.Time { return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) },
	})
	srv.build = BuildInfo{Version: "0.0.1", Commit: "aaaaaaa", BuildTime: "2026-11-01T00:00:00Z"}
	h, cookies := setupAndLogin(t, srv)

	rec := doJSON(t, h, http.MethodGet, "/api/update", "127.0.0.1:5000", nil, cookies...)
	if rec.Code != http.StatusOK {
		t.Fatalf("更新检查失败：%d %s", rec.Code, rec.Body.String())
	}
	var body updatecheck.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.UpdateAvailable || body.LatestVersion != "0.0.2" || body.Notes != "更新内容" {
		t.Fatalf("更新结果异常：%+v", body)
	}
}

func TestUpdateCheckNoStableRelease(t *testing.T) {
	releases := []updatecheck.Release{{
		TagName: "dev", Name: "dev", PublishedAt: "2026-11-02T00:00:00Z", Prerelease: true,
	}}
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/test/repo/releases" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(releases)
	}))
	defer fake.Close()

	srv := newTestServer(t)
	srv.updateChecker = updatecheck.NewWithOptions(updatecheck.Options{
		Client: fake.Client(), APIBase: fake.URL, Repository: "test/repo",
		TTL: time.Minute, Now: func() time.Time { return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) },
	})
	srv.build = BuildInfo{Version: "0.0.1-dev", Commit: "aaaaaaa", BuildTime: "2026-11-01T00:00:00Z"}
	h, cookies := setupAndLogin(t, srv)

	rec := doJSON(t, h, http.MethodGet, "/api/update", "127.0.0.1:5000", nil, cookies...)
	if rec.Code != http.StatusOK {
		t.Fatalf("更新检查错误响应码异常：%d %s", rec.Code, rec.Body.String())
	}
	var body updateErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Error, "正式版本") || body.CurrentVersion != "0.0.1-dev" || body.URL == "" {
		t.Fatalf("没有正式版本时响应异常：%+v", body)
	}
}

func TestUpdateCheckReportsGitHubError(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer fake.Close()

	srv := newTestServer(t)
	srv.updateChecker = updatecheck.NewWithOptions(updatecheck.Options{
		Client: fake.Client(), APIBase: fake.URL, Repository: "test/repo",
		TTL: time.Minute, Now: func() time.Time { return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) },
	})
	srv.build = BuildInfo{Version: "0.0.1", Commit: "aaaaaaa", BuildTime: "2026-11-01T00:00:00Z"}
	h, cookies := setupAndLogin(t, srv)

	rec := doJSON(t, h, http.MethodGet, "/api/update", "127.0.0.1:5000", nil, cookies...)
	if rec.Code != http.StatusOK {
		t.Fatalf("更新检查错误响应码异常：%d %s", rec.Code, rec.Body.String())
	}
	var body updateErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error == "" || body.CurrentVersion != "0.0.1" || body.URL == "" {
		t.Fatalf("更新检查错误响应异常：%+v", body)
	}
}
