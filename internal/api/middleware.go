package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

type ctxKey string

const ctxRequestID ctxKey = "request_id"

// maxBodyBytes 限制请求体大小，避免超大 JSON 占用内存。
const maxBodyBytes = 1 << 20 // 1 MiB

// requestIDFrom 返回请求 ID；缺失时为空。
func requestIDFrom(r *http.Request) string {
	if v, ok := r.Context().Value(ctxRequestID).(string); ok {
		return v
	}
	return ""
}

// withRequestID 为每个请求分配 ID 并回写响应头，便于与日志对照。
//
// 外部传入的 ID 只接受安全字符，避免把控制字符写进响应头或日志。
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if !validRequestID(id) {
			id = newRequestID()
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
	})
}

func validRequestID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

func newRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b)
}

// withRecover 捕获处理函数中的 panic，避免单个请求打挂整个进程。
func withRecover(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Error("request panic",
					"path", r.URL.Path, "panic", rec, "request_id", requestIDFrom(r))
				writeError(w, r, http.StatusInternalServerError, CodeInternal, "服务器内部错误")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// contentSecurityPolicy 允许同源资源；Vue 与 Naive UI 会注入内联样式，
// 因此 style-src 必须保留 unsafe-inline，但脚本仍只允许同源文件。
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'none'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

// withSecurityHeaders 设置基础安全响应头。
func withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		next.ServeHTTP(w, r)
	})
}

// withSameOrigin 拒绝跨站写请求。
//
// 覆盖所有公开的写入口（初始化、登录、退出），而不是只在登录后的路由里校验；
// 否则未初始化的面板仍可能被跨站请求抢先设置管理员。
func withSameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sameOrigin(r) {
			writeError(w, r, http.StatusForbidden, CodeForbidden, "跨站请求被拒绝")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withBodyLimit 限制请求体读取大小。
// 备份上传使用独立的流式限制，避免把大文件按 JSON 上限拒绝。
func withBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && !strings.HasPrefix(r.URL.Path, "/api/backups/restore/preview") {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// withAccessLog 记录访问日志。
//
// 只记录方法、路径、状态码与耗时：请求体里的密码与会话令牌
// 不进入日志，避免脱敏规则漏掉新字段时泄露凭据。
func withAccessLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"remote_ip", remoteIP(r),
			"request_id", requestIDFrom(r),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// remoteIP 只使用 TCP 对端地址。
//
// 不读取 X-Forwarded-For：该头可被客户端伪造，
// 而「是否本机访问」决定了初始化入口是否开放，不能建立在可伪造的头上。
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isLoopbackRequest 判断请求是否来自本机。
func isLoopbackRequest(r *http.Request) bool {
	ip := net.ParseIP(remoteIP(r))
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// isLocalSetupRequest 判断是否是可以无口令初始化的本机直连请求。
//
// 容器端口映射和反向代理都会让远端请求以回环地址到达进程；
// 因此还必须要求 Host 指向回环名称，且不存在任何转发头。
// 这样 Docker 部署仍需要引导口令，而直接在本机浏览器打开端口可以免口令。
func isLocalSetupRequest(r *http.Request) bool {
	if !isLoopbackRequest(r) {
		return false
	}
	for _, header := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip"} {
		if r.Header.Get(header) != "" {
			return false
		}
	}

	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// sameOrigin 校验跨站写请求。
//
// SameSite=Strict Cookie 是主要防线；这里再拒绝明显跨站的 Origin，
// 避免浏览器在跨站场景下携带 Cookie 完成写操作。
func sameOrigin(r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead ||
		r.Method == http.MethodOptions {
		return true
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin" || site == "none"
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		// 非浏览器客户端不带 Origin，此时依靠 Cookie 的 SameSite 与令牌本身。
		return true
	}
	return strings.HasSuffix(origin, "://"+r.Host)
}
