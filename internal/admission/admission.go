// Package admission 实现证书 / 证书+密码 / 仅密码三种认证的统一准入判定。
//
// 判定表按固定顺序执行：TLS 校验结果 → 唯一配置匹配 → 用户/配置/证书状态。
// A/B/C 不是“任意一条成功即可”的后备路径；证书与账号混搭、B 省略证书、
// 用其他配置密码登录等都必须有明确拒绝结果。
package admission

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/dreamstation625/ovpanel/internal/auth"
)

// AuthType 与 store 中的认证类型一致。
type AuthType string

const (
	AuthCertificate         AuthType = "certificate"
	AuthCertificatePassword AuthType = "certificate_password"
	AuthPassword            AuthType = "password"
)

// 拒绝/允许码。界面和日志使用稳定编码，原因文本可调整。
const (
	CodeOK                       = "ok"
	CodeNoCredentials            = "no_credentials"
	CodeCertNotVerified          = "cert_not_verified"
	CodeUnknownCert              = "unknown_cert"
	CodeAmbiguousIdentity        = "ambiguous_identity"
	CodeCertForPasswordProfile   = "cert_for_password_profile"
	CodeMissingCert              = "missing_cert"
	CodeMissingPassword          = "missing_password"
	CodeWrongUsername            = "wrong_username"
	CodeWrongPassword            = "wrong_password"
	CodeUnknownAccount           = "unknown_account"
	CodeUnexpectedCredentials    = "unexpected_credentials"
	CodeCrossIdentityCredentials = "cross_identity_credentials"
	CodeUserDisabled             = "user_disabled"
	CodeProfileDisabled          = "profile_disabled"
	CodeProfileExpired           = "profile_expired"
	CodeCertificateExpired       = "certificate_expired"
)

// User 是凭据归属人。
type User struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// Profile 是认证快照中的一份客户端配置。PasswordHash 只在运行代理内部使用。
type Profile struct {
	ID           string     `json:"id"`
	UserID       string     `json:"userId"`
	AuthType     AuthType   `json:"authType"`
	Username     string     `json:"username,omitempty"`
	PasswordHash string     `json:"passwordHash,omitempty"`
	Status       string     `json:"status"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`

	CertSerial      string     `json:"certSerial,omitempty"`
	CertFingerprint string     `json:"certFingerprint,omitempty"`
	CertStatus      string     `json:"certStatus,omitempty"`
	CertExpiresAt   *time.Time `json:"certExpiresAt,omitempty"`
}

// Snapshot 是面板同步给运行代理的认证快照。
type Snapshot struct {
	Users    []User    `json:"users"`
	Profiles []Profile `json:"profiles"`
}

// Incoming 是 OpenVPN 管理接口回调中的身份。
type Incoming struct {
	CommonName      string
	Username        string
	Password        string
	CertSerial      string
	CertFingerprint string
	CertPresent     bool
	CertVerified    bool
}

// Decision 是准入结论。
type Decision struct {
	Allow     bool
	ProfileID string
	Code      string
	Reason    string
}

// Validate 在应用快照前检查唯一性与字段一致性。
func (s Snapshot) Validate() error {
	users := map[string]struct{}{}
	for _, user := range s.Users {
		if user.ID == "" {
			return fmt.Errorf("存在空用户 ID")
		}
		if _, dup := users[user.ID]; dup {
			return fmt.Errorf("用户 ID 重复：%s", user.ID)
		}
		users[user.ID] = struct{}{}
	}

	ids := map[string]struct{}{}
	usernames := map[string]string{}
	serials := map[string]string{}
	for _, profile := range s.Profiles {
		if profile.ID == "" {
			return fmt.Errorf("存在空配置 ID")
		}
		if _, dup := ids[profile.ID]; dup {
			return fmt.Errorf("配置 ID 重复：%s", profile.ID)
		}
		ids[profile.ID] = struct{}{}
		if _, ok := users[profile.UserID]; !ok {
			return fmt.Errorf("配置 %s 引用不存在的用户 %s", profile.ID, profile.UserID)
		}

		switch profile.AuthType {
		case AuthCertificate:
			// 证书可能已吊销或尚未签发；这类配置仍保留在快照中，
			// 由运行时代码按“未知证书/缺少证书”拒绝，而不是让整份快照失效。
			if profile.Username != "" || profile.PasswordHash != "" {
				return fmt.Errorf("证书配置 %s 不应携带账号密码", profile.ID)
			}
		case AuthCertificatePassword:
			if profile.Username == "" || profile.PasswordHash == "" {
				return fmt.Errorf("证书+密码配置 %s 账号材料不完整", profile.ID)
			}
		case AuthPassword:
			if profile.Username == "" || profile.PasswordHash == "" {
				return fmt.Errorf("仅密码配置 %s 材料不完整", profile.ID)
			}
			if profile.CertSerial != "" {
				return fmt.Errorf("仅密码配置 %s 不应关联证书", profile.ID)
			}
		default:
			return fmt.Errorf("配置 %s 的认证类型未知：%q", profile.ID, profile.AuthType)
		}

		if profile.Username != "" {
			key := strings.ToLower(profile.Username)
			if other, dup := usernames[key]; dup {
				return fmt.Errorf("登录名同时属于配置 %s 和 %s", other, profile.ID)
			}
			usernames[key] = profile.ID
		}
		if profile.CertSerial != "" {
			key := normalizeSnapshotSerial(profile.CertSerial)
			if key == "" {
				return fmt.Errorf("配置 %s 的证书序列号非法", profile.ID)
			}
			if other, dup := serials[key]; dup {
				return fmt.Errorf("证书序列号同时属于配置 %s 和 %s", other, profile.ID)
			}
			serials[key] = profile.ID
		}
	}
	return nil
}

// Decide 按固定顺序给出准入结论。
func Decide(snapshot Snapshot, incoming Incoming, now time.Time) Decision {
	hasCredentials := incoming.Username != "" || incoming.Password != ""
	if !incoming.CertPresent && !hasCredentials {
		return deny(CodeNoCredentials, "未提供任何凭据")
	}
	if incoming.CertPresent && !incoming.CertVerified {
		return deny(CodeCertNotVerified, "客户端证书未通过 TLS 校验")
	}

	var certMatch *Profile
	if incoming.CertPresent {
		fingerprint := normalizeFingerprint(incoming.CertFingerprint)
		if strings.TrimSpace(incoming.CertSerial) == "" && fingerprint == "" {
			return deny(CodeUnknownCert, "证书序列号缺失")
		}
		hits := 0
		for i := range snapshot.Profiles {
			profile := &snapshot.Profiles[i]
			if profile.CertSerial == "" {
				continue
			}
			if incoming.CertSerial != "" && serialMatches(profile.CertSerial, incoming.CertSerial) {
				hits++
				certMatch = profile
				continue
			}
			if fingerprint != "" && normalizeFingerprint(profile.CertFingerprint) == fingerprint {
				hits++
				certMatch = profile
			}
		}
		if hits > 1 {
			return deny(CodeAmbiguousIdentity, "证书匹配到多份配置")
		}
		if hits == 0 {
			return deny(CodeUnknownCert, "证书未登记或已吊销")
		}
	}

	var accountMatch *Profile
	if incoming.Username != "" {
		hits := 0
		for i := range snapshot.Profiles {
			profile := &snapshot.Profiles[i]
			if profile.Username == "" {
				continue
			}
			if strings.EqualFold(profile.Username, incoming.Username) {
				hits++
				accountMatch = profile
			}
		}
		if hits > 1 {
			return deny(CodeAmbiguousIdentity, "登录名匹配到多份配置")
		}
	}

	if incoming.CertPresent {
		switch certMatch.AuthType {
		case AuthPassword:
			return deny(CodeCertForPasswordProfile, "仅密码配置不接受客户端证书")
		case AuthCertificate:
			if hasCredentials {
				if accountMatch != nil && accountMatch.ID != certMatch.ID {
					return deny(CodeCrossIdentityCredentials, "账号密码属于其他配置")
				}
				return deny(CodeUnexpectedCredentials, "证书配置不接受额外账号密码")
			}
			return checkState(snapshot, *certMatch, now)
		case AuthCertificatePassword:
			if incoming.Username == "" || incoming.Password == "" {
				return deny(CodeMissingPassword, "该配置需要同时提供账号密码")
			}
			if !strings.EqualFold(certMatch.Username, incoming.Username) {
				return deny(CodeWrongUsername, "账号与证书不属于同一配置")
			}
			if !passwordMatches(certMatch.PasswordHash, incoming.Password) {
				return deny(CodeWrongPassword, "账号密码错误")
			}
			return checkState(snapshot, *certMatch, now)
		}
		return deny(CodeAmbiguousIdentity, "无法匹配证书配置")
	}

	if incoming.Username == "" {
		return deny(CodeNoCredentials, "缺少用户名")
	}
	if incoming.Password == "" {
		return deny(CodeMissingPassword, "缺少密码")
	}
	if accountMatch == nil {
		return deny(CodeUnknownAccount, "账号密码错误或账号不存在")
	}
	switch accountMatch.AuthType {
	case AuthCertificatePassword:
		return deny(CodeMissingCert, "该账号必须同时提供客户端证书")
	case AuthPassword:
		if !passwordMatches(accountMatch.PasswordHash, incoming.Password) {
			return deny(CodeWrongPassword, "账号密码错误")
		}
		return checkState(snapshot, *accountMatch, now)
	case AuthCertificate:
		return deny(CodeUnexpectedCredentials, "证书配置不接受账号密码")
	default:
		return deny(CodeAmbiguousIdentity, "无法匹配账号配置")
	}
}

// ProfileUsable 判断配置当前是否仍可用于既有会话。
func ProfileUsable(snapshot Snapshot, profileID string, now time.Time) bool {
	var profile *Profile
	for i := range snapshot.Profiles {
		if snapshot.Profiles[i].ID == profileID {
			profile = &snapshot.Profiles[i]
			break
		}
	}
	if profile == nil {
		return false
	}
	return checkState(snapshot, *profile, now).Allow
}

// SerialMatches 供运行代理比较会话证书与最新快照。
func SerialMatches(snapshotSerial, incoming string) bool {
	return serialMatches(snapshotSerial, incoming)
}

func checkState(snapshot Snapshot, profile Profile, now time.Time) Decision {
	var user *User
	for i := range snapshot.Users {
		if snapshot.Users[i].ID == profile.UserID {
			user = &snapshot.Users[i]
			break
		}
	}
	if user == nil {
		return deny(CodeUserDisabled, "所属用户不存在")
	}
	if user.Status != "active" {
		return deny(CodeUserDisabled, "所属用户已停用")
	}
	if profile.Status != "active" {
		return deny(CodeProfileDisabled, "配置已停用")
	}
	if profile.ExpiresAt != nil && !now.Before(*profile.ExpiresAt) {
		return deny(CodeProfileExpired, "配置已到期")
	}
	if profile.AuthType != AuthPassword {
		if profile.CertStatus != "active" {
			return deny(CodeUnknownCert, "证书已吊销")
		}
		if profile.CertExpiresAt != nil && !now.Before(*profile.CertExpiresAt) {
			return deny(CodeCertificateExpired, "证书已过期")
		}
	}
	return Decision{Allow: true, ProfileID: profile.ID, Code: CodeOK, Reason: "准入通过"}
}

func passwordMatches(hash, password string) bool {
	if hash == "" || password == "" {
		return false
	}
	ok, err := auth.VerifyPassword(hash, password)
	return err == nil && ok
}

// normalizeSnapshotSerial 把十六进制序列号归一化为十进制字符串。
// OpenVPN 的 tls_serial_hex_0 以冒号分组（如 5f:aa:...），这里一并去掉。
func normalizeSnapshotSerial(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimPrefix(value, "0x")
	value = strings.ReplaceAll(value, ":", "")
	value = strings.ReplaceAll(value, " ", "")
	if value == "" {
		return ""
	}
	n := new(big.Int)
	if _, ok := n.SetString(value, 16); !ok {
		return ""
	}
	return n.String()
}

// serialMatches 比较面板十六进制序列号与管理接口回报的序列号。
// 管理接口可能给十进制或十六进制；纯数字输入会同时按两种进制比较，
// 避免 1234 这类全数字序列号产生歧义。
func serialMatches(snapshotHex, incoming string) bool {
	expected := normalizeSnapshotSerial(snapshotHex)
	if expected == "" {
		return false
	}
	incoming = strings.TrimSpace(strings.ToLower(incoming))
	if incoming == "" {
		return false
	}
	if strings.HasPrefix(incoming, "0x") || strings.Contains(incoming, ":") {
		return normalizeSnapshotSerial(incoming) == expected
	}
	for _, base := range []int{10, 16} {
		n := new(big.Int)
		if _, ok := n.SetString(incoming, base); ok && n.String() == expected {
			return true
		}
	}
	return false
}

func normalizeFingerprint(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, ":", "")
	value = strings.ReplaceAll(value, " ", "")
	return value
}

func deny(code, reason string) Decision {
	return Decision{Allow: false, Code: code, Reason: reason}
}
