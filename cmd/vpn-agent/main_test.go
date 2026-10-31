package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dreamstation625/ovpanel/internal/runtimeenv"
)

func TestHealthIsLivenessAndNeverClaimsConfigured(t *testing.T) {
	report := runtimeenv.Report{EnvironmentReady: false, Blockers: []string{"缺少 /dev/net/tun"}}
	h := newHandler(report, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("liveness 应返回 200，实际 %d", rec.Code)
	}
	var body struct {
		Status     string `json:"status"`
		Configured bool   `json:"configured"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if body.Status != "ok" {
		t.Fatalf("status = %q", body.Status)
	}
	if body.Configured {
		t.Fatal("P02 代理不得声称 VPN 已配置")
	}
}

func TestReadyzReflectsEnvironment(t *testing.T) {
	h := newHandler(runtimeenv.Report{EnvironmentReady: false, Blockers: []string{"缺少 ifb"}}, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("环境不完整时 readyz 应 503，实际 %d", rec.Code)
	}

	h = newHandler(runtimeenv.Report{EnvironmentReady: true}, nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("环境完整时 readyz 应 200，实际 %d", rec.Code)
	}
}

func TestUnknownRouteReturnsJSON(t *testing.T) {
	h := newHandler(runtimeenv.Report{}, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知路径应 404，实际 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", ct)
	}
}
