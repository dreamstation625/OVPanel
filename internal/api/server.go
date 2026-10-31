package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dreamstation625/ovpanel/internal/accounts"
	"github.com/dreamstation625/ovpanel/internal/auth"
	"github.com/dreamstation625/ovpanel/internal/backup"
	"github.com/dreamstation625/ovpanel/internal/config"
	"github.com/dreamstation625/ovpanel/internal/maintclient"
	"github.com/dreamstation625/ovpanel/internal/pki"
	"github.com/dreamstation625/ovpanel/internal/statistics"
	"github.com/dreamstation625/ovpanel/internal/store"
	"github.com/dreamstation625/ovpanel/internal/vpnruntime"
)

// sessionCookie 是会话 Cookie 名。CSRF 令牌单独使用非 HttpOnly Cookie。
const (
	sessionCookie = "ovpanel_session"
	csrfCookie    = "ovpanel_csrf"
	csrfHeader    = "X-CSRF-Token"
)

// minPasswordLength 是管理员密码的最小长度。
const minPasswordLength = 8

// BuildInfo 是构建信息，由链接期注入。
type BuildInfo struct {
	Version   string
	Commit    string
	BuildTime string
	GoVersion string
}

// Options 是构造 HTTP 层的依赖。
type Options struct {
	Config         config.Config
	Store          *store.Store
	Accounts       *accounts.Service
	PKI            *pki.Service
	Runtime        *vpnruntime.Service
	Statistics     *statistics.Service
	Backups        *backup.Service
	Maintenance    *maintclient.Client
	Log            *slog.Logger
	Build          BuildInfo
	BootstrapToken string
	WebFS          fs.FS
	// OnSetupComplete 在管理员创建成功后调用，用于删除一次性的引导口令文件。
	// 回调失败不撤销已完成的初始化，由调用方记录日志。
	OnSetupComplete func() error
}

// Server 持有 HTTP 层的全部依赖。
type Server struct {
	cfg             config.Config
	store           *store.Store
	accounts        *accounts.Service
	pki             *pki.Service
	runtime         *vpnruntime.Service
	statistics      *statistics.Service
	backups         *backup.Service
	maintenance     *maintclient.Client
	log             *slog.Logger
	build           BuildInfo
	bootstrapToken  string
	webFS           fs.FS
	onSetupComplete func() error

	passwordLimiter *auth.Limiter
	loginLimiter    *loginLimiter
	startedAt       time.Time

	now func() time.Time
}

// New 创建 HTTP 层。
func New(opts Options) *Server {
	s := &Server{
		cfg:             opts.Config,
		store:           opts.Store,
		accounts:        opts.Accounts,
		pki:             opts.PKI,
		runtime:         opts.Runtime,
		statistics:      opts.Statistics,
		backups:         opts.Backups,
		maintenance:     opts.Maintenance,
		log:             opts.Log,
		build:           opts.Build,
		bootstrapToken:  opts.BootstrapToken,
		webFS:           opts.WebFS,
		onSetupComplete: opts.OnSetupComplete,
		passwordLimiter: auth.NewLimiter(2),
		loginLimiter:    newLoginLimiter(5, 15*time.Minute),
		startedAt:       time.Now().UTC(),
		now:             func() time.Time { return time.Now().UTC() },
	}
	if s.build.GoVersion == "" {
		s.build.GoVersion = "unknown"
	}
	return s
}

// Handler 组装完整的 HTTP 处理链。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/setup", s.handleSetupStatus)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)

	mux.Handle("GET /api/me", s.requireSession(http.HandlerFunc(s.handleMe)))
	mux.Handle("GET /api/system", s.requireSession(http.HandlerFunc(s.handleSystem)))
	mux.Handle("GET /api/overview", s.requireSession(http.HandlerFunc(s.handleOverview)))
	mux.Handle("GET /api/connections", s.requireSession(http.HandlerFunc(s.handleConnections)))
	mux.Handle("GET /api/connections/history", s.requireSession(http.HandlerFunc(s.handleConnectionHistory)))
	mux.Handle("POST /api/connections/{id}/disconnect", s.requireSession(http.HandlerFunc(s.handleDisconnect)))
	mux.Handle("GET /api/usage/trend", s.requireSession(http.HandlerFunc(s.handleUsageTrend)))
	mux.Handle("GET /api/operations", s.requireSession(http.HandlerFunc(s.handleOperations)))

	mux.Handle("GET /api/users", s.requireSession(http.HandlerFunc(s.handleListUsers)))
	mux.Handle("POST /api/users", s.requireSession(http.HandlerFunc(s.handleCreateUser)))
	mux.Handle("GET /api/users/{id}", s.requireSession(http.HandlerFunc(s.handleUserDetail)))
	mux.Handle("PATCH /api/users/{id}", s.requireSession(http.HandlerFunc(s.handleUpdateUser)))
	mux.Handle("POST /api/users/{id}/profiles", s.requireSession(http.HandlerFunc(s.handleAddProfile)))

	mux.Handle("GET /api/profiles/{id}", s.requireSession(http.HandlerFunc(s.handleProfileDetail)))
	mux.Handle("PATCH /api/profiles/{id}", s.requireSession(http.HandlerFunc(s.handleUpdateProfile)))
	mux.Handle("PUT /api/profiles/{id}/limits", s.requireSession(http.HandlerFunc(s.handleUpdateProfileLimits)))
	mux.Handle("POST /api/profiles/{id}/quota/reset", s.requireSession(http.HandlerFunc(s.handleResetQuota)))
	mux.Handle("POST /api/profiles/{id}/password", s.requireSession(http.HandlerFunc(s.handleResetProfilePassword)))
	mux.Handle("POST /api/profiles/{id}/certificate/rotate", s.requireSession(http.HandlerFunc(s.handleRotateCertificate)))
	mux.Handle("POST /api/profiles/{id}/certificate/revoke", s.requireSession(http.HandlerFunc(s.handleRevokeCertificate)))
	mux.Handle("POST /api/profiles/{id}/export", s.requireSession(http.HandlerFunc(s.handleExportProfile)))

	mux.Handle("GET /api/pki", s.requireSession(http.HandlerFunc(s.handlePKIStatus)))
	mux.Handle("GET /api/settings/export", s.requireSession(http.HandlerFunc(s.handleGetExportSettings)))
	mux.Handle("PUT /api/settings/export", s.requireSession(http.HandlerFunc(s.handleSaveExportSettings)))
	mux.Handle("POST /api/admin/password", s.requireSession(http.HandlerFunc(s.handleChangeAdminPassword)))
	mux.Handle("POST /api/runtime/apply", s.requireSession(http.HandlerFunc(s.handleRuntimeApply)))
	mux.Handle("GET /api/server/config", s.requireSession(http.HandlerFunc(s.handleServerConfig)))
	mux.Handle("POST /api/server/config/preview", s.requireSession(http.HandlerFunc(s.handleServerConfigPreview)))
	mux.Handle("PUT /api/server/config/draft", s.requireSession(http.HandlerFunc(s.handleSaveServerDraft)))
	mux.Handle("POST /api/server/config/apply", s.requireSession(http.HandlerFunc(s.handleApplyServerConfig)))
	mux.Handle("POST /api/server/command", s.requireSession(http.HandlerFunc(s.handleServerCommand)))

	mux.Handle("GET /api/backups", s.requireSession(http.HandlerFunc(s.handleListBackups)))
	mux.Handle("PUT /api/backups/settings", s.requireSession(http.HandlerFunc(s.handleSaveBackupSettings)))
	mux.Handle("POST /api/backups", s.requireSession(http.HandlerFunc(s.handleCreateBackup)))
	mux.Handle("POST /api/backups/{id}/download", s.requireSession(http.HandlerFunc(s.handleDownloadBackup)))
	mux.Handle("POST /api/backups/{id}/delete", s.requireSession(http.HandlerFunc(s.handleDeleteBackup)))
	mux.Handle("POST /api/backups/restore/preview", s.requireSession(http.HandlerFunc(s.handleRestorePreview)))
	mux.Handle("POST /api/backups/restore/execute", s.requireSession(http.HandlerFunc(s.handleRestoreExecute)))

	mux.Handle("GET /api/maintenance/openvpn", s.requireSession(http.HandlerFunc(s.handleOpenVPNMaintenance)))
	mux.Handle("POST /api/maintenance/openvpn/update", s.requireSession(http.HandlerFunc(s.handleOpenVPNUpdate)))

	// 未匹配的 API 路径必须返回 JSON 404，不能落到单页应用回退。
	mux.HandleFunc("/api/", s.handleAPINotFound)

	// 静态资源与单页应用回退。注册为不限方法，才能让 /api/ 的 JSON 404
	// 覆盖所有方法；其他非 GET 请求由静态处理器返回 405。
	mux.Handle("/", s.staticHandler())

	// 处理顺序：请求 ID → 访问日志 → panic 恢复 → 安全头 → 同源校验 → 请求体限制。
	// 请求 ID 与访问日志必须在外层，panic 日志和访问日志才能带上同一个 ID。
	var h http.Handler = mux
	h = withBodyLimit(h)
	h = withSameOrigin(h)
	h = withSecurityHeaders(h)
	h = withRecover(s.log, h)
	h = withAccessLog(s.log, h)
	h = withRequestID(h)
	return h
}

func (s *Server) handleAPINotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotFound, CodeNotFound, "接口不存在")
}

// ---------- 健康与初始化 ----------

type healthResponse struct {
	Status      string `json:"status"`
	Version     string `json:"version"`
	Initialized bool   `json:"initialized"`
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	initialized, err := s.store.AdminExists(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, healthResponse{
		Status:      "ok",
		Version:     s.build.Version,
		Initialized: initialized,
	})
}

type setupStatusResponse struct {
	Initialized   bool `json:"initialized"`
	SetupAllowed  bool `json:"setupAllowed"`
	RequiresToken bool `json:"requiresToken"`
	TLSEnabled    bool `json:"tlsEnabled"`
	LocalRequest  bool `json:"localRequest"`
}

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	initialized, err := s.store.AdminExists(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	local := isLocalSetupRequest(r)
	writeJSON(w, http.StatusOK, setupStatusResponse{
		Initialized:   initialized,
		SetupAllowed:  !initialized,
		RequiresToken: !local,
		TLSEnabled:    s.cfg.TLSEnabled(),
		LocalRequest:  local,
	})
}

type setupRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Token    string `json:"token"`
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	initialized, err := s.store.AdminExists(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	// 初始化完成后入口永久关闭：即使携带正确口令也不再接受。
	if initialized {
		writeError(w, r, http.StatusConflict, CodeSetupClosed, "面板已完成初始化")
		return
	}

	var req setupRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}

	// 容器或反向代理下，远端请求经 Docker/代理转发后对端也是「本机」。
	// 只有同时满足回环地址、回环 Host 且没有转发头，才视为安全的首次初始化。
	if !isLocalSetupRequest(r) {
		if !s.tokenMatches(req.Token) {
			s.log.Warn("setup rejected", "reason", "bad_bootstrap_token", "remote_ip", remoteIP(r))
			writeError(w, r, http.StatusForbidden, CodeSetupForbidden, "初始化口令不正确")
			return
		}
	}

	if msg := validateCredentials(req.Username, req.Password); msg != "" {
		writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, msg)
		return
	}
	s.completeSetup(w, r, req.Username, req.Password)
}

// completeSetup 创建管理员、建立会话并返回登录态。
func (s *Server) completeSetup(w http.ResponseWriter, r *http.Request, username, password string) {
	ctx := r.Context()

	release, err := s.passwordLimiter.Acquire(ctx)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "服务器繁忙，请稍后重试")
		return
	}
	hash, err := auth.HashPassword(password)
	release()
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	if err := s.store.CreateAdmin(ctx, username, hash); err != nil {
		if errors.Is(err, store.ErrAdminExists) {
			writeError(w, r, http.StatusConflict, CodeSetupClosed, "面板已完成初始化")
			return
		}
		s.internalError(w, r, err)
		return
	}

	// 管理员已经落库，引导口令随即失效；删除失败不回滚初始化，只记录。
	if s.onSetupComplete != nil {
		if err := s.onSetupComplete(); err != nil {
			s.log.Warn("remove bootstrap token failed", "error", err)
		}
	}

	// 初始化完成后立即登录，避免用户刚设完密码又被弹回登录页。
	if !s.startSession(w, r, username) {
		return
	}
	s.log.Info("panel initialized", "username", username, "remote_ip", remoteIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"username": username})
}

// ---------- 登录与退出 ----------

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ip := remoteIP(r)

	if retry, ok := s.loginLimiter.blocked(ip, s.now()); ok {
		w.Header().Set("Retry-After", itoa(int(retry.Seconds())))
		writeError(w, r, http.StatusTooManyRequests, CodeRateLimited, "登录尝试过于频繁，请稍后再试")
		return
	}

	var req loginRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}

	admin, exists, err := s.store.Admin(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !exists {
		writeError(w, r, http.StatusConflict, CodeSetupRequired, "面板尚未初始化")
		return
	}

	release, err := s.passwordLimiter.Acquire(ctx)
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "服务器繁忙，请稍后重试")
		return
	}
	ok, verifyErr := auth.VerifyPassword(admin.PasswordHash, req.Password)
	release()

	// 用户名错误与密码错误返回同一提示，避免被用来枚举账号。
	if verifyErr != nil || !ok || subtle.ConstantTimeCompare([]byte(admin.Username), []byte(req.Username)) != 1 {
		s.loginLimiter.fail(ip, s.now())
		s.log.Warn("login failed", "remote_ip", ip, "username", req.Username)
		writeError(w, r, http.StatusUnauthorized, CodeUnauthorized, "用户名或密码错误")
		return
	}

	s.loginLimiter.reset(ip)
	if !s.startSession(w, r, admin.Username) {
		return
	}
	s.log.Info("login succeeded", "username", admin.Username, "remote_ip", ip)
	writeJSON(w, http.StatusOK, map[string]any{"username": admin.Username})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token := s.sessionToken(r); token != "" {
		if !s.csrfValid(r) {
			writeError(w, r, http.StatusForbidden, CodeForbidden, "CSRF 校验失败")
			return
		}
		if err := s.store.DeleteSession(r.Context(), auth.HashToken(token)); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	s.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---------- 需要登录的接口 ----------

type meResponse struct {
	Username  string `json:"username"`
	CreatedAt string `json:"createdAt"`
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	admin, ok, err := s.store.Admin(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !ok {
		writeError(w, r, http.StatusConflict, CodeSetupRequired, "面板尚未初始化")
		return
	}
	writeJSON(w, http.StatusOK, meResponse{
		Username:  admin.Username,
		CreatedAt: admin.CreatedAt.Format(time.RFC3339),
	})
}

type systemResponse struct {
	Version             string `json:"version"`
	Commit              string `json:"commit"`
	BuildTime           string `json:"buildTime"`
	GoVersion           string `json:"goVersion"`
	TLSEnabled          bool   `json:"tlsEnabled"`
	SchemaVersion       int    `json:"schemaVersion"`
	Sessions            int    `json:"sessions"`
	Users               int    `json:"users"`
	Profiles            int    `json:"profiles"`
	ActiveCertificates  int    `json:"activeCertificates"`
	RevokedCertificates int    `json:"revokedCertificates"`
	UptimeSeconds       int64  `json:"uptimeSeconds"`
	DataStore           string `json:"dataStore"`
}

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	schema, err := s.store.SchemaVersion(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	sessions, err := s.store.CountSessions(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	users, profiles, err := s.store.AccountCounts(ctx)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	activeCerts, revokedCerts, expiredCerts, err := s.store.CertificateStats(ctx, s.now())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	validCerts := activeCerts - expiredCerts

	writeJSON(w, http.StatusOK, systemResponse{
		Version:             s.build.Version,
		Commit:              s.build.Commit,
		BuildTime:           s.build.BuildTime,
		GoVersion:           s.build.GoVersion,
		TLSEnabled:          s.cfg.TLSEnabled(),
		SchemaVersion:       schema,
		Sessions:            sessions,
		Users:               users,
		Profiles:            profiles,
		ActiveCertificates:  validCerts,
		RevokedCertificates: revokedCerts,
		UptimeSeconds:       int64(s.now().Sub(s.startedAt).Seconds()),
		DataStore:           "sqlite",
	})
}

type operationItem struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	State      string `json:"state"`
	Progress   int    `json:"progress"`
	Message    string `json:"message"`
	ErrorCode  string `json:"errorCode,omitempty"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

func (s *Server) handleOperations(w http.ResponseWriter, r *http.Request) {
	ops, err := s.store.ListOperations(r.Context(), 20)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]operationItem, 0, len(ops))
	for _, op := range ops {
		item := operationItem{
			ID: op.ID, Kind: op.Kind, State: op.State, Progress: op.Progress,
			Message: op.Message, ErrorCode: op.ErrorCode,
			StartedAt: op.StartedAt.Format(time.RFC3339),
		}
		if !op.FinishedAt.IsZero() {
			item.FinishedAt = op.FinishedAt.Format(time.RFC3339)
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"operations": out})
}

// ---------- 会话与鉴权 ----------

// requireSession 校验会话 Cookie，并把用户名放入上下文。
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := s.sessionToken(r)
		if token == "" {
			writeError(w, r, http.StatusUnauthorized, CodeUnauthorized, "请先登录")
			return
		}

		hash := auth.HashToken(token)
		valid, err := s.store.SessionValid(r.Context(), hash, s.now())
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if !valid {
			s.clearSessionCookie(w)
			writeError(w, r, http.StatusUnauthorized, CodeUnauthorized, "会话已失效，请重新登录")
			return
		}
		if !safeMethod(r.Method) && !s.csrfValid(r) {
			writeError(w, r, http.StatusForbidden, CodeForbidden, "CSRF 校验失败")
			return
		}

		if err := s.store.TouchSession(r.Context(), hash, s.now()); err != nil {
			s.log.Warn("touch session failed", "error", err)
		}

		ctx := context.WithValue(r.Context(), ctxAdmin, true)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type ctxKeyAdmin struct{}

var ctxAdmin ctxKeyAdmin

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, username string) bool {
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		s.internalError(w, r, err)
		return false
	}

	csrf, err := auth.NewCSRFToken()
	if err != nil {
		s.internalError(w, r, err)
		return false
	}

	expires := s.now().Add(s.cfg.SessionTTL)
	if err := s.store.CreateSession(r.Context(), hash, expires, remoteIP(r), r.UserAgent()); err != nil {
		s.internalError(w, r, err)
		return false
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.TLSEnabled(),
		SameSite: http.SameSiteStrictMode,
		Expires:  expires,
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
	})
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookie,
		Value:    csrf,
		Path:     "/",
		HttpOnly: false,
		Secure:   s.cfg.TLSEnabled(),
		SameSite: http.SameSiteStrictMode,
		Expires:  expires,
		MaxAge:   int(s.cfg.SessionTTL.Seconds()),
	})
	return true
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	for _, name := range []string{sessionCookie, csrfCookie} {
		http.SetCookie(w, &http.Cookie{
			Name:     name,
			Value:    "",
			Path:     "/",
			HttpOnly: name == sessionCookie,
			Secure:   s.cfg.TLSEnabled(),
			SameSite: http.SameSiteStrictMode,
			MaxAge:   -1,
		})
	}
}

func (s *Server) sessionToken(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// csrfValid 使用双提交 Cookie 校验写请求：攻击者无法在跨站请求中读取 Cookie。
func (s *Server) csrfValid(r *http.Request) bool {
	cookie, err := r.Cookie(csrfCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	header := r.Header.Get(csrfHeader)
	if header == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(header)) == 1
}

func safeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// tokenMatches 常数时间比较引导口令。
func (s *Server) tokenMatches(provided string) bool {
	if s.bootstrapToken == "" || provided == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(s.bootstrapToken), []byte(provided)) == 1
}

// ---------- 辅助 ----------

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("internal error", "path", r.URL.Path, "error", err, "request_id", requestIDFrom(r))
	writeError(w, r, http.StatusInternalServerError, CodeInternal, "服务器内部错误")
}

func (s *Server) decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if ct := strings.ToLower(r.Header.Get("Content-Type")); !strings.HasPrefix(ct, "application/json") {
		writeError(w, r, http.StatusUnsupportedMediaType, CodeInvalidRequest, "请求格式必须是 JSON")
		return false
	}

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, r, http.StatusRequestEntityTooLarge, CodeInvalidRequest, "请求体过大")
			return false
		}
		writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "请求格式不正确")
		return false
	}

	// 一个请求只能包含一个 JSON 值；尾随内容通常意味着客户端拼接出了错误请求。
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "请求体只能包含一个 JSON 对象")
		return false
	}
	return true
}

// validateCredentials 校验管理员账号与密码，返回中文错误说明。
func validateCredentials(username, password string) string {
	username = strings.TrimSpace(username)
	if len(username) < 3 || len(username) > 64 {
		return "用户名长度需在 3 到 64 个字符之间"
	}
	if len(password) < minPasswordLength {
		return "密码至少需要 8 个字符"
	}
	if len(password) > 200 {
		return "密码过长"
	}
	if strings.EqualFold(username, password) {
		return "密码不能与用户名相同"
	}
	return ""
}

func itoa(v int) string {
	if v <= 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// ---------- 登录限流 ----------

type loginLimiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	attempts map[string]*loginAttempt
}

type loginAttempt struct {
	failures int
	resetAt  time.Time
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{max: max, window: window, attempts: map[string]*loginAttempt{}}
}

// blocked 判断该来源是否已被限流，返回需等待的时长。
func (l *loginLimiter) blocked(key string, now time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	a, ok := l.attempts[key]
	if !ok {
		return 0, false
	}
	if now.After(a.resetAt) {
		delete(l.attempts, key)
		return 0, false
	}
	if a.failures >= l.max {
		return a.resetAt.Sub(now), true
	}
	return 0, false
}

func (l *loginLimiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	a, ok := l.attempts[key]
	if !ok || now.After(a.resetAt) {
		l.attempts[key] = &loginAttempt{failures: 1, resetAt: now.Add(l.window)}
		return
	}
	a.failures++
}

func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}
