// Package accounts 编排用户、客户端配置、证书与客户端配置导出。
//
// 输入在这里统一校验并收窄为内部类型；证书与密码材料只在创建/重置时
// 返回一次，详情与列表不包含私钥或密码哈希。
package accounts

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dreamstation625/ovpanel/internal/auth"
	"github.com/dreamstation625/ovpanel/internal/openvpnprofile"
	"github.com/dreamstation625/ovpanel/internal/pki"
	"github.com/dreamstation625/ovpanel/internal/secretbox"
	"github.com/dreamstation625/ovpanel/internal/serverconfig"
	"github.com/dreamstation625/ovpanel/internal/store"
	"github.com/google/uuid"
)

const (
	minPasswordLength       = 12
	maxPasswordLength       = 200
	generatedPasswordLength = 20
	tlsCryptAAD             = "ovpanel-tls-crypt-v1"
)

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{3,64}$`)
var hostPattern = regexp.MustCompile(`^[A-Za-z0-9.:\[\]_-]+$`)

// 业务错误。API 根据 errors.Is 选择响应码与稳定错误码。
var (
	ErrValidation          = errors.New("输入不合法")
	ErrNoPasswordAuth      = errors.New("该配置不使用密码认证")
	ErrNoCertificateAuth   = errors.New("该配置不使用证书认证")
	ErrExportNotConfigured = errors.New("导出连接地址尚未配置")
)

// UserInput 是创建或更新用户的输入。
type UserInput struct {
	DisplayName string
	Notes       string
	Tags        []string
}

// ProfileInput 是创建配置的输入；密码留空时由面板生成。
type ProfileInput struct {
	Name      string
	AuthType  string
	Username  string
	Password  string
	ExpiresAt *time.Time
}

// CreateUserResult 返回新用户、首份配置与一次性密码。
type CreateUserResult struct {
	User     store.User
	Profile  *store.ProfileDetail
	Password string
}

// ProfileResult 返回配置与一次性密码。
type ProfileResult struct {
	Profile  store.ProfileDetail
	Password string
}

// ExportSettings 是客户端连接设置的公开部分。
type ExportSettings struct {
	Host  string
	Port  int
	Proto string
}

// ExportResult 是生成好的 .ovpn 文件。
type ExportResult struct {
	Filename string
	Content  []byte
}

// Service 编排账号、证书与导出操作。
type Service struct {
	store *store.Store
	pki   *pki.Service
	key   secretbox.Key
	now   func() time.Time
}

// New 创建账号服务。
func New(st *store.Store, pkiService *pki.Service, key secretbox.Key) *Service {
	return &Service{store: st, pki: pkiService, key: key, now: func() time.Time { return time.Now().UTC() }}
}

// CreateUser 创建没有配置的用户。
func (s *Service) CreateUser(ctx context.Context, input UserInput) (store.User, error) {
	normalized, err := normalizeUserInput(input)
	if err != nil {
		return store.User{}, err
	}
	now := s.now()
	user := store.User{
		ID:          uuid.NewString(),
		DisplayName: normalized.DisplayName,
		Notes:       normalized.Notes,
		Tags:        normalized.Tags,
		Status:      store.StatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.store.CreateUser(ctx, user); err != nil {
		return store.User{}, err
	}
	return user, nil
}

// CreateUserWithProfile 创建用户与首份配置，证书/密码只在本次返回。
func (s *Service) CreateUserWithProfile(ctx context.Context, userInput UserInput, profileInput ProfileInput) (CreateUserResult, error) {
	normalizedUser, err := normalizeUserInput(userInput)
	if err != nil {
		return CreateUserResult{}, err
	}
	normalizedProfile, err := normalizeProfileInput(profileInput, s.now())
	if err != nil {
		return CreateUserResult{}, err
	}
	if err := s.ensureUsernameAvailable(ctx, normalizedProfile.Username); err != nil {
		return CreateUserResult{}, err
	}

	now := s.now()
	user := store.User{
		ID:          uuid.NewString(),
		DisplayName: normalizedUser.DisplayName,
		Notes:       normalizedUser.Notes,
		Tags:        normalizedUser.Tags,
		Status:      store.StatusActive,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	profile, cert, password, err := s.buildProfile(ctx, user.ID, normalizedProfile, now)
	if err != nil {
		return CreateUserResult{}, err
	}
	if err := s.store.CreateUserWithProfile(ctx, user, profile, cert); err != nil {
		return CreateUserResult{}, err
	}

	detail := buildDetail(profile, user, cert)
	return CreateUserResult{User: user, Profile: &detail, Password: password}, nil
}

// AddProfile 为已有用户创建配置。
func (s *Service) AddProfile(ctx context.Context, userID string, input ProfileInput) (ProfileResult, error) {
	normalized, err := normalizeProfileInput(input, s.now())
	if err != nil {
		return ProfileResult{}, err
	}
	user, ok, err := s.store.User(ctx, userID)
	if err != nil {
		return ProfileResult{}, err
	}
	if !ok {
		return ProfileResult{}, store.ErrNotFound
	}
	if err := s.ensureUsernameAvailable(ctx, normalized.Username); err != nil {
		return ProfileResult{}, err
	}

	profile, cert, password, err := s.buildProfile(ctx, userID, normalized, s.now())
	if err != nil {
		return ProfileResult{}, err
	}
	if err := s.store.CreateProfileWithCertificate(ctx, profile, cert); err != nil {
		return ProfileResult{}, err
	}
	return ProfileResult{Profile: buildDetail(profile, user, cert), Password: password}, nil
}

// UpdateUser 更新显示名称、备注与标签。
func (s *Service) UpdateUser(ctx context.Context, id string, input UserInput) (store.User, error) {
	normalized, err := normalizeUserInput(input)
	if err != nil {
		return store.User{}, err
	}
	if err := s.store.UpdateUser(ctx, id, normalized.DisplayName, normalized.Notes, normalized.Tags); err != nil {
		return store.User{}, err
	}
	user, ok, err := s.store.User(ctx, id)
	if err != nil {
		return store.User{}, err
	}
	if !ok {
		return store.User{}, store.ErrNotFound
	}
	return user, nil
}

// SetUserStatus 启用或停用用户。
func (s *Service) SetUserStatus(ctx context.Context, id, status string) (store.User, error) {
	if status != store.StatusActive && status != store.StatusDisabled {
		return store.User{}, validation("用户状态只能是 active 或 disabled")
	}
	if err := s.store.UpdateUserStatus(ctx, id, status); err != nil {
		return store.User{}, err
	}
	user, ok, err := s.store.User(ctx, id)
	if err != nil {
		return store.User{}, err
	}
	if !ok {
		return store.User{}, store.ErrNotFound
	}
	return user, nil
}

// SetProfileStatus 启用或停用配置。
func (s *Service) SetProfileStatus(ctx context.Context, id, status string) (store.ProfileDetail, error) {
	if status != store.StatusActive && status != store.StatusDisabled {
		return store.ProfileDetail{}, validation("配置状态只能是 active 或 disabled")
	}
	if err := s.store.UpdateProfileStatus(ctx, id, status); err != nil {
		return store.ProfileDetail{}, err
	}
	return s.profileDetail(ctx, id)
}

// UpdateProfileName 更新配置名称。
func (s *Service) UpdateProfileName(ctx context.Context, id, name string) (store.ProfileDetail, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 80 {
		return store.ProfileDetail{}, validation("配置名称需在 1 到 80 个字符之间")
	}
	if err := s.store.UpdateProfileName(ctx, id, name); err != nil {
		return store.ProfileDetail{}, err
	}
	return s.profileDetail(ctx, id)
}

// UpdateProfilePolicy 更新配置级路由方式与内网权限。
// 路由推送与访问权限分别生效：前者决定客户端拿到哪些路由，
// 后者由运行代理的 nftables 规则执行。
func (s *Service) UpdateProfilePolicy(ctx context.Context, id, routeMode, accessMode string, allowedInternal []string) (store.ProfileDetail, error) {
	routeMode = strings.TrimSpace(routeMode)
	accessMode = strings.TrimSpace(accessMode)
	switch routeMode {
	case store.RouteInherit, store.RouteSplit, store.RouteGlobal:
	default:
		return store.ProfileDetail{}, validation("路由方式不合法")
	}
	switch accessMode {
	case store.AccessInherit, store.AccessAllow, store.AccessDeny:
	default:
		return store.ProfileDetail{}, validation("内网权限不合法")
	}
	allowedInternal = normalizeStringList(allowedInternal)
	if accessMode != store.AccessAllow {
		allowedInternal = []string{}
	}
	if accessMode == store.AccessAllow && len(allowedInternal) == 0 {
		return store.ProfileDetail{}, validation("仅允许指定网段时至少填写一个网段")
	}
	vpnNetwork, err := s.activeVPNNetwork(ctx)
	if err != nil {
		return store.ProfileDetail{}, err
	}
	if err := serverconfig.ValidateInternalCIDRs(vpnNetwork, allowedInternal); err != nil {
		return store.ProfileDetail{}, validation(err.Error())
	}
	if err := s.store.UpdateProfilePolicy(ctx, id, routeMode, accessMode, allowedInternal); err != nil {
		return store.ProfileDetail{}, err
	}
	return s.profileDetail(ctx, id)
}

func (s *Service) activeVPNNetwork(ctx context.Context) (string, error) {
	state, err := s.store.ServerConfigState(ctx)
	if err != nil {
		return "", err
	}
	if state.Active == nil {
		return serverconfig.Defaults().VPNNetwork, nil
	}
	var cfg serverconfig.Config
	if err := json.Unmarshal([]byte(state.Active.ConfigJSON), &cfg); err != nil {
		return "", fmt.Errorf("解析生效服务端配置: %w", err)
	}
	prepared, err := serverconfig.Prepare(cfg)
	if err != nil {
		return "", err
	}
	return prepared.VPNNetwork, nil
}

// ResetPassword 为证书+密码或仅密码配置生成新密码，只返回一次。
func (s *Service) ResetPassword(ctx context.Context, profileID, password string) (ProfileResult, error) {
	profile, ok, err := s.store.Profile(ctx, profileID)
	if err != nil {
		return ProfileResult{}, err
	}
	if !ok {
		return ProfileResult{}, store.ErrNotFound
	}
	if profile.AuthType == store.AuthCertificate {
		return ProfileResult{}, ErrNoPasswordAuth
	}

	password = strings.TrimSpace(password)
	if password == "" {
		password, err = GeneratePassword(generatedPasswordLength)
		if err != nil {
			return ProfileResult{}, err
		}
	} else if err := validatePassword(password, profile.Username); err != nil {
		return ProfileResult{}, err
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return ProfileResult{}, err
	}
	if err := s.store.UpdateProfilePassword(ctx, profileID, hash); err != nil {
		return ProfileResult{}, err
	}
	profile.HasPassword = true
	profile.UpdatedAt = s.now()
	return ProfileResult{Profile: profile, Password: password}, nil
}

// RotateCertificate 为证书类配置签发新证书并吊销旧证书。
func (s *Service) RotateCertificate(ctx context.Context, profileID string) (store.ProfileDetail, error) {
	profile, ok, err := s.store.Profile(ctx, profileID)
	if err != nil {
		return store.ProfileDetail{}, err
	}
	if !ok {
		return store.ProfileDetail{}, store.ErrNotFound
	}
	if profile.AuthType == store.AuthPassword {
		return store.ProfileDetail{}, ErrNoCertificateAuth
	}

	cert, _, err := s.pki.BuildClientCertificate(ctx, profileID, "ovp-"+profileID)
	if err != nil {
		return store.ProfileDetail{}, err
	}
	if err := s.store.ReplaceActiveCertificate(ctx, profileID, cert); err != nil {
		return store.ProfileDetail{}, err
	}
	return s.profileDetail(ctx, profileID)
}

// RevokeCertificate 吊销配置当前证书；已吊销的配置可再次轮换恢复。
func (s *Service) RevokeCertificate(ctx context.Context, profileID, reason string) (store.ProfileDetail, error) {
	profile, ok, err := s.store.Profile(ctx, profileID)
	if err != nil {
		return store.ProfileDetail{}, err
	}
	if !ok {
		return store.ProfileDetail{}, store.ErrNotFound
	}
	if profile.AuthType == store.AuthPassword {
		return store.ProfileDetail{}, ErrNoCertificateAuth
	}
	if reason != "manual" && reason != "key_compromise" {
		reason = "manual"
	}

	revoked, err := s.store.RevokeActiveCertificate(ctx, profileID, reason, s.now())
	if err != nil {
		return store.ProfileDetail{}, err
	}
	if !revoked {
		return store.ProfileDetail{}, pki.ErrNoActiveCertificate
	}
	return s.profileDetail(ctx, profileID)
}

// Export 生成自包含 .ovpn。调用方必须先复核管理员密码。
func (s *Service) Export(ctx context.Context, profileID string) (ExportResult, error) {
	profile, ok, err := s.store.Profile(ctx, profileID)
	if err != nil {
		return ExportResult{}, err
	}
	if !ok {
		return ExportResult{}, store.ErrNotFound
	}

	setting, ok, err := s.store.ExportSetting(ctx)
	if err != nil {
		return ExportResult{}, err
	}
	if !ok {
		return ExportResult{}, ErrExportNotConfigured
	}
	tlsKey, err := s.key.Open(setting.TLSCryptCiphertext, []byte(tlsCryptAAD))
	if err != nil {
		return ExportResult{}, fmt.Errorf("解密 tls-crypt 密钥: %w", err)
	}

	config := openvpnprofile.ClientConfig{
		Host:            setting.Host,
		Port:            setting.Port,
		Proto:           setting.Proto,
		TLSCryptKey:     string(tlsKey),
		RequireUserPass: profile.AuthType != store.AuthCertificate,
	}
	if profile.AuthType != store.AuthPassword {
		caPEM, certPEM, keyPEM, err := s.pki.ExportMaterial(ctx, profileID)
		if err != nil {
			return ExportResult{}, err
		}
		config.CAPEM = caPEM
		config.CertPEM = certPEM
		config.KeyPEM = keyPEM
	} else {
		caPEM, err := s.pki.EnsureCAPEM(ctx)
		if err != nil {
			return ExportResult{}, err
		}
		config.CAPEM = caPEM
	}

	return ExportResult{
		Filename: "ovpanel-" + profileID + ".ovpn",
		Content:  openvpnprofile.Render(config),
	}, nil
}

// TLSCryptKey 返回服务端与客户端共用的 tls-crypt 密钥明文。
// 运行代理应用服务端配置时使用；未配置导出地址时返回 ErrExportNotConfigured。
func (s *Service) TLSCryptKey(ctx context.Context) (string, error) {
	setting, ok, err := s.store.ExportSetting(ctx)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrExportNotConfigured
	}
	key, err := s.key.Open(setting.TLSCryptCiphertext, []byte(tlsCryptAAD))
	if err != nil {
		return "", fmt.Errorf("解密 tls-crypt 密钥: %w", err)
	}
	return string(key), nil
}

// GetExportSettings 返回连接设置；未配置时 configured=false。
func (s *Service) GetExportSettings(ctx context.Context) (ExportSettings, bool, error) {
	setting, ok, err := s.store.ExportSetting(ctx)
	if err != nil || !ok {
		return ExportSettings{}, false, err
	}
	return ExportSettings{Host: setting.Host, Port: setting.Port, Proto: setting.Proto}, true, nil
}

// SaveExportSettings 保存连接设置；首次保存时生成并加密 tls-crypt 密钥。
func (s *Service) SaveExportSettings(ctx context.Context, input ExportSettings) (ExportSettings, error) {
	normalized, err := normalizeExportSettings(input)
	if err != nil {
		return ExportSettings{}, err
	}

	existing, ok, err := s.store.ExportSetting(ctx)
	if err != nil {
		return ExportSettings{}, err
	}
	var ciphertext []byte
	if ok && len(existing.TLSCryptCiphertext) > 0 {
		ciphertext = existing.TLSCryptCiphertext
	} else {
		tlsKey, err := openvpnprofile.GenerateTLSCryptKey()
		if err != nil {
			return ExportSettings{}, err
		}
		ciphertext, err = s.key.Seal([]byte(tlsKey), []byte(tlsCryptAAD))
		if err != nil {
			return ExportSettings{}, err
		}
	}

	now := s.now()
	if err := s.store.SaveExportSetting(ctx, store.ExportSetting{
		Host:               normalized.Host,
		Port:               normalized.Port,
		Proto:              normalized.Proto,
		TLSCryptCiphertext: ciphertext,
		UpdatedAt:          now,
	}); err != nil {
		return ExportSettings{}, err
	}
	return normalized, nil
}

// GeneratePassword 生成指定长度的无歧义随机密码。
func GeneratePassword(length int) (string, error) {
	if length < 1 {
		return "", errors.New("密码长度必须大于 0")
	}
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	// 拒绝采样，避免 256 取模造成字符分布偏差。
	limit := byte(256 - (256 % len(alphabet)))
	out := make([]byte, 0, length)
	buf := make([]byte, length)
	for len(out) < length {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("生成密码: %w", err)
		}
		for _, b := range buf {
			if b >= limit {
				continue
			}
			out = append(out, alphabet[int(b)%len(alphabet)])
			if len(out) == length {
				break
			}
		}
	}
	return string(out), nil
}

// EffectiveStatus 计算配置当前是否可连接。运行代理的最终准入仍由 P04/P05 执行。
func EffectiveStatus(detail store.ProfileDetail, now time.Time) (code, reason string) {
	switch {
	case detail.UserStatus == store.StatusDisabled:
		return "user_disabled", "用户已停用"
	case detail.Status == store.StatusDisabled:
		return "profile_disabled", "配置已停用"
	case detail.AuthType != store.AuthPassword && detail.Certificate == nil:
		return "certificate_missing", "证书已吊销或不存在"
	case detail.Certificate != nil && detail.Certificate.Status == store.CertificateRevoked:
		return "certificate_revoked", "证书已吊销"
	case detail.Certificate != nil && !now.Before(detail.Certificate.ExpiresAt):
		return "certificate_expired", "证书已过期"
	case detail.ExpiresAt != nil && !now.Before(*detail.ExpiresAt):
		return "expired", "已到期"
	default:
		return "active", "可连接"
	}
}

func (s *Service) buildProfile(ctx context.Context, userID string, input ProfileInput, now time.Time) (store.Profile, *store.CertificateRecord, string, error) {
	profileID := uuid.NewString()
	profile := store.Profile{
		ID:              profileID,
		UserID:          userID,
		Name:            input.Name,
		AuthType:        input.AuthType,
		Username:        input.Username,
		Status:          store.StatusActive,
		ExpiresAt:       input.ExpiresAt,
		RouteMode:       store.RouteInherit,
		AccessMode:      store.AccessInherit,
		AllowedInternal: []string{},
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	password := ""
	if input.AuthType != store.AuthCertificate {
		password = strings.TrimSpace(input.Password)
		if password == "" {
			generated, err := GeneratePassword(generatedPasswordLength)
			if err != nil {
				return store.Profile{}, nil, "", err
			}
			password = generated
		}
		hash, err := auth.HashPassword(password)
		if err != nil {
			return store.Profile{}, nil, "", err
		}
		profile.PasswordHash = hash
		profile.HasPassword = true
	}

	var cert *store.CertificateRecord
	if input.AuthType != store.AuthPassword {
		record, _, err := s.pki.BuildClientCertificate(ctx, profileID, "ovp-"+profileID)
		if err != nil {
			return store.Profile{}, nil, "", err
		}
		cert = &record
	}
	return profile, cert, password, nil
}

func (s *Service) ensureUsernameAvailable(ctx context.Context, username string) error {
	if username == "" {
		return nil
	}
	_, exists, err := s.store.ProfileByUsername(ctx, username)
	if err != nil {
		return err
	}
	if exists {
		return store.ErrUsernameTaken
	}
	return nil
}

func (s *Service) profileDetail(ctx context.Context, id string) (store.ProfileDetail, error) {
	detail, ok, err := s.store.Profile(ctx, id)
	if err != nil {
		return store.ProfileDetail{}, err
	}
	if !ok {
		return store.ProfileDetail{}, store.ErrNotFound
	}
	return detail, nil
}

func buildDetail(profile store.Profile, user store.User, cert *store.CertificateRecord) store.ProfileDetail {
	detail := store.ProfileDetail{
		Profile:         profile,
		UserDisplayName: user.DisplayName,
		UserStatus:      user.Status,
	}
	if cert != nil {
		c := cert.Certificate
		detail.Certificate = &c
	}
	return detail
}

func normalizeUserInput(input UserInput) (UserInput, error) {
	displayName := strings.TrimSpace(input.DisplayName)
	if n := utf8.RuneCountInString(displayName); n < 1 || n > 80 {
		return UserInput{}, validation("用户名称需在 1 到 80 个字符之间")
	}
	notes := strings.TrimSpace(input.Notes)
	if utf8.RuneCountInString(notes) > 2000 {
		return UserInput{}, validation("备注不能超过 2000 个字符")
	}

	seen := map[string]struct{}{}
	tags := make([]string, 0, len(input.Tags))
	for _, raw := range input.Tags {
		tag := strings.TrimSpace(raw)
		if tag == "" {
			continue
		}
		if utf8.RuneCountInString(tag) > 32 {
			return UserInput{}, validation("单个标签不能超过 32 个字符")
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	if len(tags) > 10 {
		return UserInput{}, validation("标签不能超过 10 个")
	}

	return UserInput{DisplayName: displayName, Notes: notes, Tags: tags}, nil
}

func normalizeProfileInput(input ProfileInput, now time.Time) (ProfileInput, error) {
	name := strings.TrimSpace(input.Name)
	if n := utf8.RuneCountInString(name); n < 1 || n > 80 {
		return ProfileInput{}, validation("配置名称需在 1 到 80 个字符之间")
	}
	authType := strings.TrimSpace(input.AuthType)
	switch authType {
	case store.AuthCertificate, store.AuthCertificatePassword, store.AuthPassword:
	default:
		return ProfileInput{}, validation("认证类型不合法")
	}

	username := strings.TrimSpace(input.Username)
	password := strings.TrimSpace(input.Password)
	if authType == store.AuthCertificate {
		if username != "" || password != "" {
			return ProfileInput{}, validation("仅证书认证不需要登录名或密码")
		}
	} else {
		if !usernamePattern.MatchString(username) {
			return ProfileInput{}, validation("登录名需为 3 到 64 位字母、数字、点、下划线或连字符")
		}
		if password != "" {
			if err := validatePassword(password, username); err != nil {
				return ProfileInput{}, err
			}
		}
	}

	if input.ExpiresAt != nil && !now.Before(*input.ExpiresAt) {
		return ProfileInput{}, validation("到期时间必须晚于当前时间")
	}
	return ProfileInput{
		Name:      name,
		AuthType:  authType,
		Username:  username,
		Password:  password,
		ExpiresAt: input.ExpiresAt,
	}, nil
}

func validatePassword(password, username string) error {
	if n := utf8.RuneCountInString(password); n < minPasswordLength || n > maxPasswordLength {
		return validation(fmt.Sprintf("密码长度需在 %d 到 %d 个字符之间", minPasswordLength, maxPasswordLength))
	}
	if strings.EqualFold(password, username) {
		return validation("密码不能与登录名相同")
	}
	return nil
}

func normalizeStringList(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func normalizeExportSettings(input ExportSettings) (ExportSettings, error) {
	host := strings.TrimSpace(input.Host)
	if len(host) == 0 || len(host) > 255 || !hostPattern.MatchString(host) {
		return ExportSettings{}, validation("VPN 地址只能包含字母、数字、点、冒号、方括号、下划线或连字符")
	}
	if input.Port < 1 || input.Port > 65535 {
		return ExportSettings{}, validation("端口需在 1 到 65535 之间")
	}
	proto := strings.ToLower(strings.TrimSpace(input.Proto))
	if proto != "udp" && proto != "tcp" {
		return ExportSettings{}, validation("协议只能是 udp 或 tcp")
	}
	return ExportSettings{Host: host, Port: input.Port, Proto: proto}, nil
}

func validation(message string) error {
	return fmt.Errorf("%w：%s", ErrValidation, message)
}
