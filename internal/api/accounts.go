package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dreamstation625/ovpanel/internal/accounts"
	"github.com/dreamstation625/ovpanel/internal/auth"
	"github.com/dreamstation625/ovpanel/internal/pki"
	"github.com/dreamstation625/ovpanel/internal/store"
)

// ---------- 响应结构 ----------

type userDTO struct {
	ID                 string   `json:"id"`
	DisplayName        string   `json:"displayName"`
	Notes              string   `json:"notes"`
	Tags               []string `json:"tags"`
	Status             string   `json:"status"`
	ProfileCount       int      `json:"profileCount"`
	ActiveProfileCount int      `json:"activeProfileCount"`
	CreatedAt          string   `json:"createdAt"`
	UpdatedAt          string   `json:"updatedAt"`
}

type certificateDTO struct {
	ID           string `json:"id"`
	SerialNumber string `json:"serialNumber"`
	Fingerprint  string `json:"fingerprint"`
	CommonName   string `json:"commonName"`
	Status       string `json:"status"`
	IssuedAt     string `json:"issuedAt"`
	ExpiresAt    string `json:"expiresAt"`
	RevokedAt    string `json:"revokedAt,omitempty"`
	RevokeReason string `json:"revokeReason,omitempty"`
}

type profileDTO struct {
	ID              string          `json:"id"`
	UserID          string          `json:"userId"`
	UserDisplayName string          `json:"userDisplayName"`
	Name            string          `json:"name"`
	AuthType        string          `json:"authType"`
	Username        string          `json:"username,omitempty"`
	Status          string          `json:"status"`
	EffectiveStatus string          `json:"effectiveStatus"`
	StatusReason    string          `json:"statusReason"`
	ExpiresAt       string          `json:"expiresAt,omitempty"`
	Certificate     *certificateDTO `json:"certificate,omitempty"`
	CreatedAt       string          `json:"createdAt"`
	UpdatedAt       string          `json:"updatedAt"`
}

type userDetailDTO struct {
	User     userDTO      `json:"user"`
	Profiles []profileDTO `json:"profiles"`
}

// ---------- 请求结构 ----------

type profileRequest struct {
	Name      string `json:"name"`
	AuthType  string `json:"authType"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	ExpiresAt string `json:"expiresAt"`
}

type createUserRequest struct {
	DisplayName string          `json:"displayName"`
	Notes       string          `json:"notes"`
	Tags        []string        `json:"tags"`
	Profile     *profileRequest `json:"profile"`
}

type updateUserRequest struct {
	DisplayName *string   `json:"displayName"`
	Notes       *string   `json:"notes"`
	Tags        *[]string `json:"tags"`
	Status      *string   `json:"status"`
}

type updateProfileRequest struct {
	Name   *string `json:"name"`
	Status *string `json:"status"`
}

// ---------- 用户 ----------

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if utf8.RuneCountInString(query) > 100 {
		writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "搜索关键字不能超过 100 个字符")
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && status != store.StatusActive && status != store.StatusDisabled {
		writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "用户状态过滤值不合法")
		return
	}

	users, err := s.store.ListUsers(r.Context(), query, status)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]userDTO, 0, len(users))
	for _, u := range users {
		out = append(out, userToDTO(u.User, u.ProfileCount, u.ActiveProfileCount))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	userInput := accounts.UserInput{DisplayName: req.DisplayName, Notes: req.Notes, Tags: req.Tags}

	if req.Profile == nil {
		user, err := s.accounts.CreateUser(r.Context(), userInput)
		if err != nil {
			s.writeAccountError(w, r, err)
			return
		}
		s.log.Info("user created", "user_id", user.ID)
		writeJSON(w, http.StatusCreated, map[string]any{"user": userToDTO(user, 0, 0)})
		return
	}

	profileInput, err := profileInputFromRequest(*req.Profile)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, err.Error())
		return
	}
	result, err := s.accounts.CreateUserWithProfile(r.Context(), userInput, profileInput)
	if err != nil {
		s.writeAccountError(w, r, err)
		return
	}

	resp := map[string]any{
		"user": userToDTO(result.User, 1, 1),
	}
	if result.Profile != nil {
		dto := profileToDTO(*result.Profile, s.now())
		resp["profile"] = dto
	}
	if result.Password != "" {
		resp["password"] = result.Password
	}
	profileID, authType := "", ""
	if result.Profile != nil {
		profileID = result.Profile.ID
		authType = result.Profile.AuthType
	}
	s.log.Info("user and profile created",
		"user_id", result.User.ID, "profile_id", profileID, "auth_type", authType)
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleUserDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user, ok, err := s.store.User(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !ok {
		writeError(w, r, http.StatusNotFound, CodeNotFound, "用户不存在")
		return
	}
	profiles, err := s.store.ProfilesByUser(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, buildUserDetail(user, profiles, s.now()))
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req updateUserRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if req.DisplayName == nil && req.Notes == nil && req.Tags == nil && req.Status == nil {
		writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "没有需要更新的字段")
		return
	}

	existing, ok, err := s.store.User(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !ok {
		writeError(w, r, http.StatusNotFound, CodeNotFound, "用户不存在")
		return
	}

	if req.DisplayName != nil || req.Notes != nil || req.Tags != nil {
		input := accounts.UserInput{DisplayName: existing.DisplayName, Notes: existing.Notes, Tags: existing.Tags}
		if req.DisplayName != nil {
			input.DisplayName = *req.DisplayName
		}
		if req.Notes != nil {
			input.Notes = *req.Notes
		}
		if req.Tags != nil {
			input.Tags = *req.Tags
		}
		if _, err := s.accounts.UpdateUser(r.Context(), id, input); err != nil {
			s.writeAccountError(w, r, err)
			return
		}
	}
	if req.Status != nil {
		if _, err := s.accounts.SetUserStatus(r.Context(), id, *req.Status); err != nil {
			s.writeAccountError(w, r, err)
			return
		}
	}

	user, ok, err := s.store.User(r.Context(), id)
	if err != nil || !ok {
		s.internalError(w, r, err)
		return
	}
	profiles, err := s.store.ProfilesByUser(r.Context(), id)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.log.Info("user updated", "user_id", id)
	writeJSON(w, http.StatusOK, buildUserDetail(user, profiles, s.now()))
}

// ---------- 客户端配置 ----------

func (s *Server) handleAddProfile(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("id")
	var req profileRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	input, err := profileInputFromRequest(req)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, err.Error())
		return
	}
	result, err := s.accounts.AddProfile(r.Context(), userID, input)
	if err != nil {
		s.writeAccountError(w, r, err)
		return
	}
	resp := map[string]any{"profile": profileToDTO(result.Profile, s.now())}
	if result.Password != "" {
		resp["password"] = result.Password
	}
	s.log.Info("profile created",
		"user_id", userID, "profile_id", result.Profile.ID, "auth_type", result.Profile.AuthType)
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleProfileDetail(w http.ResponseWriter, r *http.Request) {
	detail, ok, err := s.store.Profile(r.Context(), r.PathValue("id"))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !ok {
		writeError(w, r, http.StatusNotFound, CodeNotFound, "客户端配置不存在")
		return
	}
	writeJSON(w, http.StatusOK, profileToDTO(detail, s.now()))
}

func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req updateProfileRequest
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if req.Name == nil && req.Status == nil {
		writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "没有需要更新的字段")
		return
	}

	detail := store.ProfileDetail{}
	var err error
	if req.Name != nil {
		detail, err = s.accounts.UpdateProfileName(r.Context(), id, *req.Name)
		if err != nil {
			s.writeAccountError(w, r, err)
			return
		}
	}
	if req.Status != nil {
		detail, err = s.accounts.SetProfileStatus(r.Context(), id, *req.Status)
		if err != nil {
			s.writeAccountError(w, r, err)
			return
		}
	}
	s.log.Info("profile updated", "profile_id", id)
	writeJSON(w, http.StatusOK, profileToDTO(detail, s.now()))
}

func (s *Server) handleResetProfilePassword(w http.ResponseWriter, r *http.Request) {
	profileID := r.PathValue("id")
	var req struct {
		AdminPassword string `json:"adminPassword"`
		Password      string `json:"password"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if _, ok := s.verifyAdminPassword(w, r, req.AdminPassword); !ok {
		return
	}
	result, err := s.accounts.ResetPassword(r.Context(), profileID, req.Password)
	if err != nil {
		s.writeAccountError(w, r, err)
		return
	}
	s.log.Info("profile password reset", "profile_id", profileID)
	writeJSON(w, http.StatusOK, map[string]any{
		"profile":  profileToDTO(result.Profile, s.now()),
		"password": result.Password,
	})
}

func (s *Server) handleRotateCertificate(w http.ResponseWriter, r *http.Request) {
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
	detail, err := s.accounts.RotateCertificate(r.Context(), profileID)
	if err != nil {
		s.writeAccountError(w, r, err)
		return
	}
	s.log.Info("profile certificate rotated", "profile_id", profileID)
	writeJSON(w, http.StatusOK, map[string]any{"profile": profileToDTO(detail, s.now())})
}

func (s *Server) handleRevokeCertificate(w http.ResponseWriter, r *http.Request) {
	profileID := r.PathValue("id")
	var req struct {
		AdminPassword string `json:"adminPassword"`
		Reason        string `json:"reason"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if _, ok := s.verifyAdminPassword(w, r, req.AdminPassword); !ok {
		return
	}
	detail, err := s.accounts.RevokeCertificate(r.Context(), profileID, req.Reason)
	if err != nil {
		s.writeAccountError(w, r, err)
		return
	}
	s.log.Warn("profile certificate revoked", "profile_id", profileID)
	writeJSON(w, http.StatusOK, map[string]any{"profile": profileToDTO(detail, s.now())})
}

func (s *Server) handleExportProfile(w http.ResponseWriter, r *http.Request) {
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
	result, err := s.accounts.Export(r.Context(), profileID)
	if err != nil {
		s.writeAccountError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-openvpn-profile")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, result.Filename))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(result.Content); err != nil {
		s.log.Warn("write export failed", "profile_id", profileID, "error", err)
	}
	s.log.Info("profile exported", "profile_id", profileID)
}

// ---------- PKI 与导出设置 ----------

func (s *Server) handlePKIStatus(w http.ResponseWriter, r *http.Request) {
	info, err := s.pki.CAInfo(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	active, revoked, expired, err := s.store.CertificateStats(r.Context(), s.now())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	users, profiles, err := s.store.AccountCounts(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	resp := map[string]any{
		"caExists":            info.Exists,
		"activeCertificates":  active - expired,
		"revokedCertificates": revoked,
		"expiredCertificates": expired,
		"users":               users,
		"profiles":            profiles,
	}
	if info.Exists {
		resp["ca"] = map[string]any{
			"subject":     info.Subject,
			"serial":      info.Serial,
			"fingerprint": info.Fingerprint,
			"notBefore":   info.NotBefore.Format(time.RFC3339),
			"notAfter":    info.NotAfter.Format(time.RFC3339),
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetExportSettings(w http.ResponseWriter, r *http.Request) {
	setting, configured, err := s.accounts.GetExportSettings(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, exportSettingsDTO(setting, configured))
}

func (s *Server) handleSaveExportSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host  string `json:"host"`
		Port  int    `json:"port"`
		Proto string `json:"proto"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	setting, err := s.accounts.SaveExportSettings(r.Context(), accounts.ExportSettings{
		Host: req.Host, Port: req.Port, Proto: req.Proto,
	})
	if err != nil {
		s.writeAccountError(w, r, err)
		return
	}
	s.log.Info("export settings saved", "host", setting.Host, "port", setting.Port, "proto", setting.Proto)
	writeJSON(w, http.StatusOK, exportSettingsDTO(setting, true))
}

// ---------- 管理员密码 ----------

func (s *Server) handleChangeAdminPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	admin, ok := s.verifyAdminPassword(w, r, req.CurrentPassword)
	if !ok {
		return
	}
	if msg := validateNewAdminPassword(admin.Username, req.NewPassword); msg != "" {
		writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, msg)
		return
	}

	same, err := auth.VerifyPassword(admin.PasswordHash, req.NewPassword)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if same {
		writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "新密码不能与当前密码相同")
		return
	}

	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := s.store.UpdateAdminPassword(r.Context(), hash); err != nil {
		s.internalError(w, r, err)
		return
	}
	removed, err := s.store.DeleteAllSessions(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.clearSessionCookie(w)
	s.log.Warn("admin password changed; all sessions invalidated", "sessions_removed", removed)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// verifyAdminPassword 复核管理员密码，失败时已写出响应。
// 复用登录限流，避免敏感操作成为密码爆破入口。
func (s *Server) verifyAdminPassword(w http.ResponseWriter, r *http.Request, password string) (store.Admin, bool) {
	ip := remoteIP(r)
	if retry, blocked := s.loginLimiter.blocked(ip, s.now()); blocked {
		w.Header().Set("Retry-After", itoa(int(retry.Seconds())))
		writeError(w, r, http.StatusTooManyRequests, CodeRateLimited, "验证尝试过于频繁，请稍后再试")
		return store.Admin{}, false
	}

	admin, exists, err := s.store.Admin(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return store.Admin{}, false
	}
	if !exists {
		writeError(w, r, http.StatusConflict, CodeSetupRequired, "面板尚未初始化")
		return store.Admin{}, false
	}

	release, err := s.passwordLimiter.Acquire(r.Context())
	if err != nil {
		writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "服务器繁忙，请稍后重试")
		return store.Admin{}, false
	}
	valid, verifyErr := auth.VerifyPassword(admin.PasswordHash, password)
	release()

	if verifyErr != nil || !valid {
		s.loginLimiter.fail(ip, s.now())
		s.log.Warn("admin reauth failed", "remote_ip", ip, "path", r.URL.Path)
		writeError(w, r, http.StatusForbidden, CodeReauthFailed, "管理员密码不正确")
		return store.Admin{}, false
	}
	s.loginLimiter.reset(ip)
	return admin, true
}

// ---------- 映射与校验 ----------

func buildUserDetail(user store.User, profiles []store.ProfileDetail, now time.Time) userDetailDTO {
	active := 0
	out := make([]profileDTO, 0, len(profiles))
	for _, p := range profiles {
		if p.Status == store.StatusActive {
			active++
		}
		out = append(out, profileToDTO(p, now))
	}
	return userDetailDTO{
		User:     userToDTO(user, len(profiles), active),
		Profiles: out,
	}
}

func userToDTO(user store.User, profileCount, activeCount int) userDTO {
	return userDTO{
		ID:                 user.ID,
		DisplayName:        user.DisplayName,
		Notes:              user.Notes,
		Tags:               user.Tags,
		Status:             user.Status,
		ProfileCount:       profileCount,
		ActiveProfileCount: activeCount,
		CreatedAt:          user.CreatedAt.Format(time.RFC3339),
		UpdatedAt:          user.UpdatedAt.Format(time.RFC3339),
	}
}

func profileToDTO(detail store.ProfileDetail, now time.Time) profileDTO {
	effective, reason := accounts.EffectiveStatus(detail, now)
	dto := profileDTO{
		ID:              detail.ID,
		UserID:          detail.UserID,
		UserDisplayName: detail.UserDisplayName,
		Name:            detail.Name,
		AuthType:        detail.AuthType,
		Username:        detail.Username,
		Status:          detail.Status,
		EffectiveStatus: effective,
		StatusReason:    reason,
		CreatedAt:       detail.CreatedAt.Format(time.RFC3339),
		UpdatedAt:       detail.UpdatedAt.Format(time.RFC3339),
	}
	if detail.ExpiresAt != nil {
		dto.ExpiresAt = detail.ExpiresAt.Format(time.RFC3339)
	}
	if detail.Certificate != nil {
		c := detail.Certificate
		dto.Certificate = &certificateDTO{
			ID:           c.ID,
			SerialNumber: c.SerialNumber,
			Fingerprint:  c.Fingerprint,
			CommonName:   c.CommonName,
			Status:       c.Status,
			IssuedAt:     c.IssuedAt.Format(time.RFC3339),
			ExpiresAt:    c.ExpiresAt.Format(time.RFC3339),
			RevokeReason: c.RevokeReason,
		}
		if c.RevokedAt != nil {
			dto.Certificate.RevokedAt = c.RevokedAt.Format(time.RFC3339)
		}
	}
	return dto
}

func profileInputFromRequest(req profileRequest) (accounts.ProfileInput, error) {
	expires, err := parseOptionalTime(req.ExpiresAt)
	if err != nil {
		return accounts.ProfileInput{}, err
	}
	return accounts.ProfileInput{
		Name:      req.Name,
		AuthType:  req.AuthType,
		Username:  req.Username,
		Password:  req.Password,
		ExpiresAt: expires,
	}, nil
}

func parseOptionalTime(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, errors.New("时间格式必须为 RFC3339")
	}
	utc := parsed.UTC()
	return &utc, nil
}

func exportSettingsDTO(setting accounts.ExportSettings, configured bool) map[string]any {
	return map[string]any{
		"configured": configured,
		"host":       setting.Host,
		"port":       setting.Port,
		"proto":      setting.Proto,
	}
}

func validateNewAdminPassword(username, password string) string {
	if n := utf8.RuneCountInString(password); n < minPasswordLength {
		return "新密码至少需要 12 个字符"
	}
	if utf8.RuneCountInString(password) > 200 {
		return "新密码过长"
	}
	if strings.EqualFold(strings.TrimSpace(username), strings.TrimSpace(password)) {
		return "新密码不能与用户名相同"
	}
	return ""
}

func (s *Server) writeAccountError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, r, http.StatusNotFound, CodeNotFound, "记录不存在")
	case errors.Is(err, store.ErrUsernameTaken):
		writeError(w, r, http.StatusConflict, CodeUsernameTaken, "登录名已被其他配置使用")
	case errors.Is(err, accounts.ErrValidation):
		writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, err.Error())
	case errors.Is(err, accounts.ErrNoPasswordAuth):
		writeError(w, r, http.StatusConflict, CodeNoPasswordAuth, "该配置不使用密码认证")
	case errors.Is(err, accounts.ErrNoCertificateAuth):
		writeError(w, r, http.StatusConflict, CodeNoCertificateAuth, "该配置不使用证书认证")
	case errors.Is(err, accounts.ErrExportNotConfigured):
		writeError(w, r, http.StatusConflict, CodeExportNotConfigured, "请先在系统设置中填写 VPN 连接地址")
	case errors.Is(err, pki.ErrNoActiveCertificate):
		writeError(w, r, http.StatusConflict, CodeNoCertificate, "该配置没有生效证书")
	default:
		s.internalError(w, r, err)
	}
}
