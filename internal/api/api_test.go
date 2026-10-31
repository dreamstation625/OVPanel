package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/dreamstation625/ovpanel/internal/auth"
	"github.com/dreamstation625/ovpanel/internal/config"
	"github.com/dreamstation625/ovpanel/internal/logging"
	"github.com/dreamstation625/ovpanel/internal/store"
)

// weakParams 让测试里的密码哈希保持快速，不影响生产默认值。
func weakParams(t *testing.T) {
	t.Helper()
	old := auth.DefaultParams
	auth.DefaultParams = auth.Params{
		Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 8, KeyLength: 16,
	}
	t.Cleanup(func() { auth.DefaultParams = old })
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	weakParams(t)

	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "panel.db"))
	if err != nil {
		t.Fatalf("打开数据库失败：%v", err)
	}
	t.Cleanup(func() { st.Close() })

	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("迁移失败：%v", err)
	}

	webFS := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><title>OVPanel</title>")},
		"app.js":     {Data: []byte("console.log('app')")},
	}

	srv := New(Options{
		Config: config.Config{
			DataDir:    dir,
			Listen:     ":0",
			LogLevel:   "error",
			SessionTTL: time.Hour,
		},
		Store:          st,
		Log:            logging.New(io.Discard, "error"),
		Build:          BuildInfo{Version: "test"},
		BootstrapToken: "test-bootstrap-token",
		WebFS:          webFS,
	})
	srv.now = func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }
	return srv
}

func doJSON(t *testing.T, h http.Handler, method, path, remoteAddr string, body any, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求失败：%v", err)
		}
		reader = bytes.NewReader(data)
	}

	req := httptest.NewRequest(method, path, reader)
	req.Host = "127.0.0.1"
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
		if !strings.HasPrefix(remoteAddr, "127.0.0.1:") && !strings.HasPrefix(remoteAddr, "[::1]:") {
			req.Host = "panel.example.com"
		}
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) errorDetail {
	t.Helper()
	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应不是合法错误体：%v（%s）", err, rec.Body.String())
	}
	return body.Error
}

func sessionCookieFrom(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			return c
		}
	}
	t.Fatal("响应未设置会话 Cookie")
	return nil
}

func TestHealthBeforeInitialization(t *testing.T) {
	srv := newTestServer(t)
	rec := doJSON(t, srv.Handler(), http.MethodGet, "/api/health", "", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	var body healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if body.Initialized {
		t.Fatal("新实例不应已初始化")
	}
	if body.Status != "ok" {
		t.Fatalf("status = %q", body.Status)
	}
}

// TestSetupFromLoopbackNeedsNoToken 确认本机初始化不需要口令。
func TestSetupFromLoopbackNeedsNoToken(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Handler()

	rec := doJSON(t, h, http.MethodPost, "/api/setup", "127.0.0.1:5000",
		map[string]string{"username": "admin", "password": "a-long-enough-password"})
	if rec.Code != http.StatusOK {
		t.Fatalf("本机初始化应成功，状态码 = %d，响应 %s", rec.Code, rec.Body.String())
	}
	cookie := sessionCookieFrom(t, rec)

	// 初始化后立即登录态可用。
	me := doJSON(t, h, http.MethodGet, "/api/me", "127.0.0.1:5000", nil, cookie)
	if me.Code != http.StatusOK {
		t.Fatalf("/api/me 应返回 200，实际 %d", me.Code)
	}
}

// TestSetupFromRemoteWithoutTokenRejected 确认公网来源不能抢先接管。
func TestSetupFromRemoteWithoutTokenRejected(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Handler()
	remote := "203.0.113.9:40000"

	rec := doJSON(t, h, http.MethodPost, "/api/setup", remote,
		map[string]string{"username": "attacker", "password": "a-long-enough-password"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("远端无口令应被拒绝，状态码 = %d", rec.Code)
	}
	if got := decodeError(t, rec).Code; got != CodeSetupForbidden {
		t.Fatalf("错误码 = %q", got)
	}

	// 被拒绝后仍未初始化。
	status := doJSON(t, h, http.MethodGet, "/api/setup", remote, nil)
	var body setupStatusResponse
	if err := json.Unmarshal(status.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if body.Initialized {
		t.Fatal("拒绝后不应创建管理员")
	}
}

func TestSetupFromRemoteWithTokenSucceeds(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Handler()
	remote := "203.0.113.9:40000"

	rec := doJSON(t, h, http.MethodPost, "/api/setup", remote, map[string]string{
		"username": "admin",
		"password": "a-long-enough-password",
		"token":    "test-bootstrap-token",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("携带口令应成功，状态码 = %d，响应 %s", rec.Code, rec.Body.String())
	}
}

// TestSetupClosedAfterInitialization 确认初始化入口不可重复使用。
func TestSetupClosedAfterInitialization(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Handler()

	first := doJSON(t, h, http.MethodPost, "/api/setup", "127.0.0.1:5000",
		map[string]string{"username": "admin", "password": "a-long-enough-password"})
	if first.Code != http.StatusOK {
		t.Fatalf("首次初始化失败：%d", first.Code)
	}

	second := doJSON(t, h, http.MethodPost, "/api/setup", "127.0.0.1:5000",
		map[string]string{"username": "second", "password": "another-long-password"})
	if second.Code != http.StatusConflict {
		t.Fatalf("重复初始化应返回 409，实际 %d", second.Code)
	}
	if got := decodeError(t, second).Code; got != CodeSetupClosed {
		t.Fatalf("错误码 = %q", got)
	}
}

func TestSetupRejectsShortPassword(t *testing.T) {
	srv := newTestServer(t)
	rec := doJSON(t, srv.Handler(), http.MethodPost, "/api/setup", "127.0.0.1:5000",
		map[string]string{"username": "admin", "password": "short"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("短密码应被拒绝，状态码 = %d", rec.Code)
	}
}

func TestLoginFlow(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Handler()

	if rec := doJSON(t, h, http.MethodPost, "/api/setup", "127.0.0.1:5000",
		map[string]string{"username": "admin", "password": "a-long-enough-password"}); rec.Code != http.StatusOK {
		t.Fatalf("初始化失败：%d", rec.Code)
	}

	// 退出后 /api/me 应失效。
	logoutSetup := doJSON(t, h, http.MethodPost, "/api/login", "127.0.0.1:5001",
		map[string]string{"username": "admin", "password": "a-long-enough-password"})
	if logoutSetup.Code != http.StatusOK {
		t.Fatalf("登录失败：%d %s", logoutSetup.Code, logoutSetup.Body.String())
	}
	cookie := sessionCookieFrom(t, logoutSetup)

	if rec := doJSON(t, h, http.MethodPost, "/api/logout", "127.0.0.1:5001", nil, cookie); rec.Code != http.StatusOK {
		t.Fatalf("退出失败：%d", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodGet, "/api/me", "127.0.0.1:5001", nil, cookie); rec.Code != http.StatusUnauthorized {
		t.Fatalf("退出后应 401，实际 %d", rec.Code)
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Handler()

	doJSON(t, h, http.MethodPost, "/api/setup", "127.0.0.1:5000",
		map[string]string{"username": "admin", "password": "a-long-enough-password"})

	rec := doJSON(t, h, http.MethodPost, "/api/login", "203.0.113.50:1234",
		map[string]string{"username": "admin", "password": "wrong-password-here"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("错误密码应 401，实际 %d", rec.Code)
	}
	// 不应泄露具体原因。
	if msg := decodeError(t, rec).Message; !strings.Contains(msg, "用户名或密码错误") {
		t.Fatalf("提示文案异常：%q", msg)
	}
}

// TestLoginRateLimited 确认连续失败会触发限流。
func TestLoginRateLimited(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Handler()

	doJSON(t, h, http.MethodPost, "/api/setup", "127.0.0.1:5000",
		map[string]string{"username": "admin", "password": "a-long-enough-password"})

	remote := "203.0.113.77:1234"
	for i := 0; i < 5; i++ {
		rec := doJSON(t, h, http.MethodPost, "/api/login", remote,
			map[string]string{"username": "admin", "password": "wrong-password-here"})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次失败应 401，实际 %d", i+1, rec.Code)
		}
	}

	rec := doJSON(t, h, http.MethodPost, "/api/login", remote,
		map[string]string{"username": "admin", "password": "a-long-enough-password"})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("超限后应 429，实际 %d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("限流响应应带 Retry-After")
	}
}

func TestProtectedRouteRequiresSession(t *testing.T) {
	srv := newTestServer(t)
	rec := doJSON(t, srv.Handler(), http.MethodGet, "/api/me", "127.0.0.1:5000", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401，实际 %d", rec.Code)
	}
}

// TestCrossSiteWriteRejected 确认跨站写请求被拒绝。
func TestCrossSiteWriteRejected(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Handler()

	setup := doJSON(t, h, http.MethodPost, "/api/setup", "127.0.0.1:5000",
		map[string]string{"username": "admin", "password": "a-long-enough-password"})
	cookie := sessionCookieFrom(t, setup)

	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.AddCookie(cookie)
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("跨站写请求应被拒绝，实际 %d", rec.Code)
	}
}

func TestSystemRequiresAuthAndReportsSchema(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Handler()

	if rec := doJSON(t, h, http.MethodGet, "/api/system", "127.0.0.1:5000", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("未登录应 401，实际 %d", rec.Code)
	}

	setup := doJSON(t, h, http.MethodPost, "/api/setup", "127.0.0.1:5000",
		map[string]string{"username": "admin", "password": "a-long-enough-password"})
	cookie := sessionCookieFrom(t, setup)

	rec := doJSON(t, h, http.MethodGet, "/api/system", "127.0.0.1:5000", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	var body systemResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if body.SchemaVersion < 1 {
		t.Fatalf("schema 版本 = %d", body.SchemaVersion)
	}
	if body.DataStore != "sqlite" {
		t.Fatalf("dataStore = %q", body.DataStore)
	}
}

// TestSPAFallback 确认未知路径回退到入口页，缺失静态资源返回 404。
func TestSPAFallback(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Handler()

	page := doJSON(t, h, http.MethodGet, "/users/123", "", nil)
	if page.Code != http.StatusOK {
		t.Fatalf("前端路由应回退到入口页，实际 %d", page.Code)
	}
	if !strings.Contains(page.Body.String(), "OVPanel") {
		t.Fatalf("回退内容异常：%s", page.Body.String())
	}

	missing := doJSON(t, h, http.MethodGet, "/missing.js", "", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("缺失的静态资源应 404，实际 %d", missing.Code)
	}
}

// TestUnknownAPIReturnsJSON404 确认未知接口不会被单页应用回退成 200 HTML。
func TestUnknownAPIReturnsJSON404(t *testing.T) {
	srv := newTestServer(t)
	rec := doJSON(t, srv.Handler(), http.MethodGet, "/api/does-not-exist", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知接口应 404，实际 %d", rec.Code)
	}
	if got := decodeError(t, rec).Code; got != CodeNotFound {
		t.Fatalf("错误码 = %q", got)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("错误响应应为 JSON，实际 %q", ct)
	}
}

// TestCrossSiteSetupRejected 确认未初始化面板不能被跨站请求抢先接管。
func TestCrossSiteSetupRejected(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/setup",
		strings.NewReader(`{"username":"attacker","password":"a-long-enough-password"}`))
	req.RemoteAddr = "127.0.0.1:5000"
	req.Host = "127.0.0.1:8080"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Origin", "https://evil.example")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("跨站初始化应 403，实际 %d", rec.Code)
	}
}

// TestLoopbackProxyStillRequiresToken 确认容器或反向代理转发的请求不能绕过引导口令。
func TestLoopbackProxyStillRequiresToken(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/setup",
		strings.NewReader(`{"username":"admin","password":"a-long-enough-password"}`))
	req.RemoteAddr = "127.0.0.1:5000"
	req.Host = "panel.example.com"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "203.0.113.9")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("经代理转发的初始化应要求口令，实际 %d", rec.Code)
	}
}

func TestSetupStatusReportsTokenRequirement(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Handler()

	local := doJSON(t, h, http.MethodGet, "/api/setup", "127.0.0.1:5000", nil)
	var localBody setupStatusResponse
	if err := json.Unmarshal(local.Body.Bytes(), &localBody); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if localBody.RequiresToken || !localBody.LocalRequest {
		t.Fatalf("本机直连不应要求口令：%+v", localBody)
	}

	// 同一个进程内，Host 不是回环名称时按代理场景处理。
	req := httptest.NewRequest(http.MethodGet, "/api/setup", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Host = "panel.example.com"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	var proxiedBody setupStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &proxiedBody); err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if !proxiedBody.RequiresToken || proxiedBody.LocalRequest {
		t.Fatalf("代理转发的请求应要求口令：%+v", proxiedBody)
	}
}

func TestSecurityHeadersAndRequestID(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("X-Request-Id", "bad\r\nInjected: yes")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got == "" {
		t.Fatal("缺少 CSP")
	}
	if got := rec.Header().Get("X-Request-Id"); got == "" || strings.ContainsAny(got, "\r\n") {
		t.Fatalf("请求 ID 未安全生成：%q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("X-Request-Id", "trace-123")
	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if got := rec.Header().Get("X-Request-Id"); got != "trace-123" {
		t.Fatalf("合法请求 ID 应保留：%q", got)
	}
}

func TestRejectsNonJSONAndTrailingData(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodPost, "/api/setup",
		strings.NewReader(`{"username":"admin","password":"a-long-enough-password"}`))
	req.RemoteAddr = "127.0.0.1:5000"
	req.Host = "127.0.0.1"
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("非 JSON 请求应 415，实际 %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/setup",
		strings.NewReader(`{"username":"admin","password":"a-long-enough-password"}{"extra":1}`))
	req.RemoteAddr = "127.0.0.1:5000"
	req.Host = "127.0.0.1"
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("尾随 JSON 应 400，实际 %d", rec.Code)
	}
}

func TestStaticWithoutIndexExplainsBuild(t *testing.T) {
	srv := newTestServer(t)
	srv.webFS = fstest.MapFS{}
	rec := doJSON(t, srv.Handler(), http.MethodGet, "/", "", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("未构建前端时应 503，实际 %d", rec.Code)
	}
}

func TestHashedAssetDetection(t *testing.T) {
	cases := map[string]bool{
		"assets/index-a1b2c3d4.js":  true,
		"assets/index-a1b2c3d4.css": true,
		"assets/logo-a1b2c3d4.svg":  true,
		"assets/logo.svg":           false,
		"favicon.svg":               false,
		"assets/index.js":           false,
		"assets/index-xyz.js":       false,
	}
	for name, want := range cases {
		if got := isHashedAsset(name); got != want {
			t.Fatalf("isHashedAsset(%q) = %v，期望 %v", name, got, want)
		}
	}
}

func TestSystemRequestBodyTooLarge(t *testing.T) {
	srv := newTestServer(t)
	big := strings.Repeat("a", maxBodyBytes+1024)
	req := httptest.NewRequest(http.MethodPost, "/api/setup",
		strings.NewReader(`{"username":"admin","password":"`+big+`"}`))
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge && rec.Code != http.StatusBadRequest {
		t.Fatalf("超大请求体应被拒绝，实际 %d", rec.Code)
	}
}
