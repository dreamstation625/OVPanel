package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 用户与配置状态。
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"

	AuthCertificate         = "certificate"
	AuthCertificatePassword = "certificate_password"
	AuthPassword            = "password"

	CertificateActive  = "active"
	CertificateRevoked = "revoked"
)

// 数据层错误。调用方据此返回明确响应，不依赖驱动错误文本。
var (
	ErrNotFound      = errors.New("记录不存在")
	ErrUsernameTaken = errors.New("登录名已被使用")
)

// User 是一个凭据归属人，不是面板登录账号。
type User struct {
	ID          string
	DisplayName string
	Notes       string
	Tags        []string
	Status      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// UserSummary 是用户列表行，附带配置数量。
type UserSummary struct {
	User
	ProfileCount       int
	ActiveProfileCount int
}

// 配置级路由与内网权限取值。
const (
	RouteInherit = "inherit"
	RouteSplit   = "split"
	RouteGlobal  = "global"

	AccessInherit = "inherit"
	AccessAllow   = "allow"
	AccessDeny    = "deny"
)

// Profile 是一份独立的认证与授权单位。
type Profile struct {
	ID       string
	UserID   string
	Name     string
	AuthType string
	Username string

	// RouteMode 与 AccessMode 默认继承服务端；AllowedInternal 只在
	// AccessMode = AccessAllow 时生效。路由推送与访问权限分别执行。
	RouteMode       string
	AccessMode      string
	AllowedInternal []string

	// HasPassword 用于详情展示；PasswordHash 只在创建或改密时写入，
	// 读取详情时不会加载哈希。
	HasPassword  bool
	PasswordHash string

	Status string

	ExpiresAt       *time.Time
	ConcurrentLimit *int64
	QuotaBytes      *int64
	UploadBps       *int64
	DownloadBps     *int64
	// QuotaEpoch 是手动重置额度的代次，运行代理据此切换用量基线。
	QuotaEpoch int64

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Certificate 是证书元信息；私钥密文不进入列表与详情响应。
type Certificate struct {
	ID           string
	ProfileID    string
	SerialNumber string
	Fingerprint  string
	CommonName   string
	Status       string
	IssuedAt     time.Time
	ExpiresAt    time.Time
	RevokedAt    *time.Time
	RevokeReason string
}

// CertificateRecord 是写入数据库所需的完整证书材料。
type CertificateRecord struct {
	Certificate
	CertPEM       string
	KeyCiphertext []byte
}

// ProfileDetail 是配置详情，附带所属用户与当前生效证书。
type ProfileDetail struct {
	Profile
	UserDisplayName string
	UserStatus      string
	Certificate     *Certificate
}

// CA 是唯一的项目 CA。KeyCiphertext 只在签发证书时解密。
type CA struct {
	CertPEM       string
	KeyCiphertext []byte
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

// ExportSetting 是生成客户端 .ovpn 所需的连接信息。
type ExportSetting struct {
	Host               string
	Port               int
	Proto              string
	TLSCryptCiphertext []byte
	UpdatedAt          time.Time
}

type dbExec interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type rowScanner interface {
	Scan(dest ...any) error
}

// CreateUser 创建一个没有配置的用户。
func (s *Store) CreateUser(ctx context.Context, u User) error {
	if err := insertUser(ctx, s.db, u); err != nil {
		return fmt.Errorf("创建用户: %w", err)
	}
	return nil
}

// CreateUserWithProfile 在同一事务内创建用户、首份配置与证书。
func (s *Store) CreateUserWithProfile(ctx context.Context, u User, p Profile, cert *CertificateRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始创建用户事务: %w", err)
	}
	defer tx.Rollback()

	if err := insertUser(ctx, tx, u); err != nil {
		return err
	}
	if err := insertProfile(ctx, tx, p); err != nil {
		return err
	}
	if cert != nil {
		if err := insertCertificate(ctx, tx, *cert); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交创建用户事务: %w", err)
	}
	return nil
}

// CreateProfileWithCertificate 为已有用户创建配置及证书。
func (s *Store) CreateProfileWithCertificate(ctx context.Context, p Profile, cert *CertificateRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始创建配置事务: %w", err)
	}
	defer tx.Rollback()

	if err := insertProfile(ctx, tx, p); err != nil {
		return err
	}
	if cert != nil {
		if err := insertCertificate(ctx, tx, *cert); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交创建配置事务: %w", err)
	}
	return nil
}

func insertUser(ctx context.Context, ex dbExec, u User) error {
	tags, err := encodeTags(u.Tags)
	if err != nil {
		return err
	}
	_, err = ex.ExecContext(ctx,
		`INSERT INTO app_user (id, display_name, notes, tags_json, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		u.ID, u.DisplayName, u.Notes, tags, u.Status, formatTime(u.CreatedAt), formatTime(u.UpdatedAt))
	if err != nil {
		return fmt.Errorf("写入用户 %s: %w", u.ID, err)
	}
	return nil
}

func insertProfile(ctx context.Context, ex dbExec, p Profile) error {
	routeMode, accessMode := p.RouteMode, p.AccessMode
	if routeMode == "" {
		routeMode = RouteInherit
	}
	if accessMode == "" {
		accessMode = AccessInherit
	}
	allowed, err := encodeStringList(p.AllowedInternal)
	if err != nil {
		return err
	}
	_, err = ex.ExecContext(ctx,
		`INSERT INTO client_profile (
			id, user_id, name, auth_type, username, password_hash, status, expires_at,
			concurrent_limit, quota_bytes, upload_bps, download_bps,
			route_mode, access_mode, allowed_internal_json, created_at, updated_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.UserID, p.Name, p.AuthType,
		nullableString(p.Username), nullableString(p.PasswordHash),
		p.Status, nullableTimeValue(p.ExpiresAt),
		nullableInt(p.ConcurrentLimit), nullableInt(p.QuotaBytes),
		nullableInt(p.UploadBps), nullableInt(p.DownloadBps),
		routeMode, accessMode, allowed,
		formatTime(p.CreatedAt), formatTime(p.UpdatedAt))
	if err != nil {
		if isUsernameConflict(err) {
			return ErrUsernameTaken
		}
		return fmt.Errorf("写入客户端配置 %s: %w", p.ID, err)
	}
	return nil
}

func insertCertificate(ctx context.Context, ex dbExec, c CertificateRecord) error {
	_, err := ex.ExecContext(ctx,
		`INSERT INTO certificate (
			id, profile_id, serial_number, fingerprint_sha256, common_name,
			cert_pem, key_ciphertext, status, issued_at, expires_at, revoked_at, revoke_reason
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.ProfileID, c.SerialNumber, c.Fingerprint, c.CommonName,
		c.CertPEM, c.KeyCiphertext, c.Status,
		formatTime(c.IssuedAt), formatTime(c.ExpiresAt),
		nullableTimeValue(c.RevokedAt), c.RevokeReason)
	if err != nil {
		return fmt.Errorf("写入证书 %s: %w", c.ID, err)
	}
	return nil
}

// UpdateUser 更新用户资料。
func (s *Store) UpdateUser(ctx context.Context, id, displayName, notes string, tags []string) error {
	encoded, err := encodeTags(tags)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE app_user SET display_name = ?, notes = ?, tags_json = ?, updated_at = ? WHERE id = ?`,
		displayName, notes, encoded, formatTime(nowUTC()), id)
	if err != nil {
		return fmt.Errorf("更新用户 %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateUserStatus 启用或停用用户。
func (s *Store) UpdateUserStatus(ctx context.Context, id, status string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE app_user SET status = ?, updated_at = ? WHERE id = ?`,
		status, formatTime(nowUTC()), id)
	if err != nil {
		return fmt.Errorf("更新用户状态 %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateProfileName 更新配置名称。
func (s *Store) UpdateProfileName(ctx context.Context, id, name string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE client_profile SET name = ?, updated_at = ? WHERE id = ?`,
		name, formatTime(nowUTC()), id)
	if err != nil {
		return fmt.Errorf("更新配置名称 %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateProfilePolicy 更新配置级路由方式与内网权限。
func (s *Store) UpdateProfilePolicy(ctx context.Context, id, routeMode, accessMode string, allowedInternal []string) error {
	allowed, err := encodeStringList(allowedInternal)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE client_profile
		    SET route_mode = ?, access_mode = ?, allowed_internal_json = ?, updated_at = ?
		  WHERE id = ?`,
		routeMode, accessMode, allowed, formatTime(nowUTC()), id)
	if err != nil {
		return fmt.Errorf("更新配置网络策略 %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateProfileStatus 启用或停用配置。
func (s *Store) UpdateProfileStatus(ctx context.Context, id, status string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE client_profile SET status = ?, updated_at = ? WHERE id = ?`,
		status, formatTime(nowUTC()), id)
	if err != nil {
		return fmt.Errorf("更新配置状态 %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateProfilePassword 更新配置的密码哈希。
func (s *Store) UpdateProfilePassword(ctx context.Context, id, passwordHash string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE client_profile SET password_hash = ?, updated_at = ? WHERE id = ?`,
		passwordHash, formatTime(nowUTC()), id)
	if err != nil {
		return fmt.Errorf("更新配置密码 %s: %w", id, err)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// ReplaceActiveCertificate 原子吊销当前证书并写入新证书。
func (s *Store) ReplaceActiveCertificate(ctx context.Context, profileID string, cert CertificateRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始轮换证书事务: %w", err)
	}
	defer tx.Rollback()

	now := formatTime(nowUTC())
	if _, err := tx.ExecContext(ctx,
		`UPDATE certificate SET status = ?, revoked_at = ?, revoke_reason = ?
		  WHERE profile_id = ? AND status = ?`,
		CertificateRevoked, now, "superseded", profileID, CertificateActive); err != nil {
		return fmt.Errorf("吊销旧证书: %w", err)
	}
	if cert.ProfileID == "" {
		cert.ProfileID = profileID
	}
	if err := insertCertificate(ctx, tx, cert); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交轮换证书事务: %w", err)
	}
	return nil
}

// RevokeActiveCertificate 吊销当前生效证书，返回是否存在被吊销的证书。
func (s *Store) RevokeActiveCertificate(ctx context.Context, profileID, reason string, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE certificate SET status = ?, revoked_at = ?, revoke_reason = ?
		  WHERE profile_id = ? AND status = ?`,
		CertificateRevoked, formatTime(at), reason, profileID, CertificateActive)
	if err != nil {
		return false, fmt.Errorf("吊销证书: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("统计吊销结果: %w", err)
	}
	return n > 0, nil
}

// DeleteAllSessions 使全部管理员会话失效。
func (s *Store) DeleteAllSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM admin_session`)
	if err != nil {
		return 0, fmt.Errorf("删除全部会话: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("统计会话删除: %w", err)
	}
	return n, nil
}

// User 读取单个用户。
func (s *Store) User(ctx context.Context, id string) (User, bool, error) {
	var (
		u       User
		tags    string
		created string
		updated string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, display_name, notes, tags_json, status, created_at, updated_at
		   FROM app_user WHERE id = ?`, id,
	).Scan(&u.ID, &u.DisplayName, &u.Notes, &tags, &u.Status, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("读取用户: %w", err)
	}
	u.Tags = decodeTags(tags)
	u.CreatedAt = parseTime(created)
	u.UpdatedAt = parseTime(updated)
	return u, true, nil
}

// ListUsers 返回用户列表，可按关键字与状态过滤。
func (s *Store) ListUsers(ctx context.Context, query, status string) ([]UserSummary, error) {
	like := "%" + query + "%"
	rows, err := s.db.QueryContext(ctx,
		`SELECT u.id, u.display_name, u.notes, u.tags_json, u.status, u.created_at, u.updated_at,
		        COUNT(p.id),
		        COALESCE(SUM(CASE WHEN p.status = 'active' THEN 1 ELSE 0 END), 0)
		   FROM app_user u
		   LEFT JOIN client_profile p ON p.user_id = u.id
		  WHERE (? = '' OR u.status = ?)
		    AND (? = '' OR u.display_name LIKE ? OR u.notes LIKE ?)
		  GROUP BY u.id
		  ORDER BY u.created_at DESC, u.id DESC`,
		status, status, query, like, like)
	if err != nil {
		return nil, fmt.Errorf("列出用户: %w", err)
	}
	defer rows.Close()

	out := make([]UserSummary, 0)
	for rows.Next() {
		var (
			u       UserSummary
			tags    string
			created string
			updated string
		)
		if err := rows.Scan(&u.ID, &u.DisplayName, &u.Notes, &tags, &u.Status,
			&created, &updated, &u.ProfileCount, &u.ActiveProfileCount); err != nil {
			return nil, fmt.Errorf("读取用户列表: %w", err)
		}
		u.Tags = decodeTags(tags)
		u.CreatedAt = parseTime(created)
		u.UpdatedAt = parseTime(updated)
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取用户列表: %w", err)
	}
	return out, nil
}

// ProfilesByUser 返回用户的全部配置。
func (s *Store) ProfilesByUser(ctx context.Context, userID string) ([]ProfileDetail, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+profileDetailSelect+`
		   FROM client_profile p
		   JOIN app_user u ON u.id = p.user_id
		   LEFT JOIN certificate c ON c.profile_id = p.id AND c.status = 'active'
		  WHERE p.user_id = ?
		  ORDER BY p.created_at, p.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("列出用户配置: %w", err)
	}
	defer rows.Close()

	out := make([]ProfileDetail, 0)
	for rows.Next() {
		d, err := scanProfileDetail(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取用户配置: %w", err)
	}
	return out, nil
}

// Profile 读取单份配置。
func (s *Store) Profile(ctx context.Context, id string) (ProfileDetail, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+profileDetailSelect+`
		   FROM client_profile p
		   JOIN app_user u ON u.id = p.user_id
		   LEFT JOIN certificate c ON c.profile_id = p.id AND c.status = 'active'
		  WHERE p.id = ?`, id)
	d, err := scanProfileDetail(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ProfileDetail{}, false, nil
	}
	if err != nil {
		return ProfileDetail{}, false, err
	}
	return d, true, nil
}

// ProfilePasswordHash 读取配置密码哈希，仅供认证匹配内部使用。
func (s *Store) ProfilePasswordHash(ctx context.Context, id string) (hash string, hasPassword bool, found bool, err error) {
	var value sql.NullString
	err = s.db.QueryRowContext(ctx,
		`SELECT password_hash FROM client_profile WHERE id = ?`, id).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, false, nil
	}
	if err != nil {
		return "", false, false, fmt.Errorf("读取配置密码: %w", err)
	}
	return value.String, value.Valid, true, nil
}

// ProfileByUsername 按登录名查找配置，供后续运行代理认证匹配复用。
func (s *Store) ProfileByUsername(ctx context.Context, username string) (ProfileDetail, bool, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+profileDetailSelect+`
		   FROM client_profile p
		   JOIN app_user u ON u.id = p.user_id
		   LEFT JOIN certificate c ON c.profile_id = p.id AND c.status = 'active'
		  WHERE p.username = ?`, username)
	d, err := scanProfileDetail(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ProfileDetail{}, false, nil
	}
	if err != nil {
		return ProfileDetail{}, false, err
	}
	return d, true, nil
}

const profileDetailSelect = `p.id, p.user_id, p.name, p.auth_type, COALESCE(p.username, ''),
	CASE WHEN p.password_hash IS NULL THEN 0 ELSE 1 END, p.status, p.expires_at,
	p.concurrent_limit, p.quota_bytes, p.upload_bps, p.download_bps, p.quota_epoch,
	p.route_mode, p.access_mode, p.allowed_internal_json, p.created_at, p.updated_at,
	u.display_name, u.status,
	c.id, c.serial_number, c.fingerprint_sha256, c.common_name, c.status,
	c.issued_at, c.expires_at, c.revoked_at, c.revoke_reason`

func scanProfileDetail(row rowScanner) (ProfileDetail, error) {
	var (
		d                                ProfileDetail
		username                         string
		hasPassword                      int
		expiresAt                        sql.NullString
		concurrent, quota, upload, down  sql.NullInt64
		quotaEpoch                       int64
		routeMode, accessMode, allowed   string
		createdAt, updatedAt             string
		certID, certSerial, certFP       sql.NullString
		certCN, certStatus, certIssued   sql.NullString
		certExpires, certRevoked, reason sql.NullString
	)
	err := row.Scan(
		&d.ID, &d.UserID, &d.Name, &d.AuthType, &username, &hasPassword, &d.Status, &expiresAt,
		&concurrent, &quota, &upload, &down, &quotaEpoch,
		&routeMode, &accessMode, &allowed, &createdAt, &updatedAt,
		&d.UserDisplayName, &d.UserStatus,
		&certID, &certSerial, &certFP, &certCN, &certStatus, &certIssued, &certExpires, &certRevoked, &reason,
	)
	if err != nil {
		return ProfileDetail{}, err
	}
	d.Username = username
	d.HasPassword = hasPassword == 1
	d.ExpiresAt = nullableTime(expiresAt)
	d.ConcurrentLimit = nullableIntPtr(concurrent)
	d.QuotaBytes = nullableIntPtr(quota)
	d.UploadBps = nullableIntPtr(upload)
	d.DownloadBps = nullableIntPtr(down)
	d.QuotaEpoch = quotaEpoch
	d.RouteMode = routeMode
	d.AccessMode = accessMode
	d.AllowedInternal = decodeStringList(allowed)
	d.CreatedAt = parseTime(createdAt)
	d.UpdatedAt = parseTime(updatedAt)

	if certID.Valid {
		cert := &Certificate{
			ID:           certID.String,
			ProfileID:    d.ID,
			SerialNumber: certSerial.String,
			Fingerprint:  certFP.String,
			CommonName:   certCN.String,
			Status:       certStatus.String,
			IssuedAt:     parseTime(certIssued.String),
			ExpiresAt:    parseTime(certExpires.String),
			RevokedAt:    nullableTime(certRevoked),
			RevokeReason: reason.String,
		}
		d.Certificate = cert
	}
	return d, nil
}

// ActiveCertificate 读取配置当前生效证书的元信息。
func (s *Store) ActiveCertificate(ctx context.Context, profileID string) (Certificate, bool, error) {
	var (
		c               Certificate
		issued, expires string
		revoked         sql.NullString
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, profile_id, serial_number, fingerprint_sha256, common_name, status,
		        issued_at, expires_at, revoked_at, revoke_reason
		   FROM certificate WHERE profile_id = ? AND status = ?`, profileID, CertificateActive,
	).Scan(&c.ID, &c.ProfileID, &c.SerialNumber, &c.Fingerprint, &c.CommonName, &c.Status,
		&issued, &expires, &revoked, &c.RevokeReason)
	if errors.Is(err, sql.ErrNoRows) {
		return Certificate{}, false, nil
	}
	if err != nil {
		return Certificate{}, false, fmt.Errorf("读取生效证书: %w", err)
	}
	c.IssuedAt = parseTime(issued)
	c.ExpiresAt = parseTime(expires)
	c.RevokedAt = nullableTime(revoked)
	return c, true, nil
}

// ActiveCertificateMaterial 读取导出所需的证书 PEM 与私钥密文。
func (s *Store) ActiveCertificateMaterial(ctx context.Context, profileID string) (CertificateRecord, bool, error) {
	var (
		c               CertificateRecord
		issued, expires string
		revoked         sql.NullString
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, profile_id, serial_number, fingerprint_sha256, common_name, cert_pem,
		        key_ciphertext, status, issued_at, expires_at, revoked_at, revoke_reason
		   FROM certificate WHERE profile_id = ? AND status = ?`, profileID, CertificateActive,
	).Scan(&c.ID, &c.ProfileID, &c.SerialNumber, &c.Fingerprint, &c.CommonName, &c.CertPEM,
		&c.KeyCiphertext, &c.Status, &issued, &expires, &revoked, &c.RevokeReason)
	if errors.Is(err, sql.ErrNoRows) {
		return CertificateRecord{}, false, nil
	}
	if err != nil {
		return CertificateRecord{}, false, fmt.Errorf("读取证书材料: %w", err)
	}
	c.IssuedAt = parseTime(issued)
	c.ExpiresAt = parseTime(expires)
	c.RevokedAt = nullableTime(revoked)
	return c, true, nil
}

// CertificateHistory 返回配置的证书历史，包含已吊销记录。
func (s *Store) CertificateHistory(ctx context.Context, profileID string) ([]Certificate, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, profile_id, serial_number, fingerprint_sha256, common_name, status,
		        issued_at, expires_at, revoked_at, revoke_reason
		   FROM certificate WHERE profile_id = ? ORDER BY issued_at DESC, id DESC`, profileID)
	if err != nil {
		return nil, fmt.Errorf("列出证书历史: %w", err)
	}
	defer rows.Close()

	out := make([]Certificate, 0)
	for rows.Next() {
		var (
			c               Certificate
			issued, expires string
			revoked         sql.NullString
		)
		if err := rows.Scan(&c.ID, &c.ProfileID, &c.SerialNumber, &c.Fingerprint, &c.CommonName,
			&c.Status, &issued, &expires, &revoked, &c.RevokeReason); err != nil {
			return nil, fmt.Errorf("读取证书历史: %w", err)
		}
		c.IssuedAt = parseTime(issued)
		c.ExpiresAt = parseTime(expires)
		c.RevokedAt = nullableTime(revoked)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取证书历史: %w", err)
	}
	return out, nil
}

// CA 读取项目 CA。
func (s *Store) CA(ctx context.Context) (CA, bool, error) {
	var (
		c                CA
		created, expires string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT cert_pem, key_ciphertext, created_at, expires_at FROM pki_ca WHERE id = 1`,
	).Scan(&c.CertPEM, &c.KeyCiphertext, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return CA{}, false, nil
	}
	if err != nil {
		return CA{}, false, fmt.Errorf("读取 CA: %w", err)
	}
	c.CreatedAt = parseTime(created)
	c.ExpiresAt = parseTime(expires)
	return c, true, nil
}

// CreateCA 保存项目 CA。
func (s *Store) CreateCA(ctx context.Context, ca CA) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO pki_ca (id, cert_pem, key_ciphertext, created_at, expires_at)
		 VALUES (1, ?, ?, ?, ?)`,
		ca.CertPEM, ca.KeyCiphertext, formatTime(ca.CreatedAt), formatTime(ca.ExpiresAt))
	if err != nil {
		return fmt.Errorf("写入 CA: %w", err)
	}
	return nil
}

// ExportSetting 读取导出连接设置。
func (s *Store) ExportSetting(ctx context.Context) (ExportSetting, bool, error) {
	var (
		e       ExportSetting
		updated string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT vpn_host, vpn_port, vpn_proto, tls_crypt_ciphertext, updated_at
		   FROM vpn_export_setting WHERE id = 1`,
	).Scan(&e.Host, &e.Port, &e.Proto, &e.TLSCryptCiphertext, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return ExportSetting{}, false, nil
	}
	if err != nil {
		return ExportSetting{}, false, fmt.Errorf("读取导出设置: %w", err)
	}
	e.UpdatedAt = parseTime(updated)
	return e, true, nil
}

// SaveExportSetting 新增或更新导出连接设置。
func (s *Store) SaveExportSetting(ctx context.Context, e ExportSetting) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO vpn_export_setting (id, vpn_host, vpn_port, vpn_proto, tls_crypt_ciphertext, updated_at)
		 VALUES (1, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   vpn_host = excluded.vpn_host,
		   vpn_port = excluded.vpn_port,
		   vpn_proto = excluded.vpn_proto,
		   tls_crypt_ciphertext = excluded.tls_crypt_ciphertext,
		   updated_at = excluded.updated_at`,
		e.Host, e.Port, e.Proto, e.TLSCryptCiphertext, formatTime(e.UpdatedAt))
	if err != nil {
		return fmt.Errorf("保存导出设置: %w", err)
	}
	return nil
}

// CertificateStats 返回证书数量统计。
func (s *Store) CertificateStats(ctx context.Context, now time.Time) (active, revoked, expired int, err error) {
	var activeN, revokedN, expiredN sql.NullInt64
	err = s.db.QueryRowContext(ctx,
		`SELECT
		   SUM(CASE WHEN status = 'active' THEN 1 ELSE 0 END),
		   SUM(CASE WHEN status = 'revoked' THEN 1 ELSE 0 END),
		   SUM(CASE WHEN status = 'active' AND expires_at <= ? THEN 1 ELSE 0 END)
		 FROM certificate`, formatTime(now)).Scan(&activeN, &revokedN, &expiredN)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("统计证书: %w", err)
	}
	return int(activeN.Int64), int(revokedN.Int64), int(expiredN.Int64), nil
}

// AccountCounts 返回用户与配置总数。
func (s *Store) AccountCounts(ctx context.Context) (users, profiles int, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT (SELECT COUNT(*) FROM app_user), (SELECT COUNT(*) FROM client_profile)`,
	).Scan(&users, &profiles)
	if err != nil {
		return 0, 0, fmt.Errorf("统计用户与配置: %w", err)
	}
	return users, profiles, nil
}

func encodeTags(tags []string) (string, error) {
	if tags == nil {
		tags = []string{}
	}
	data, err := json.Marshal(tags)
	if err != nil {
		return "", fmt.Errorf("编码标签: %w", err)
	}
	return string(data), nil
}

func decodeTags(raw string) []string {
	var tags []string
	if err := json.Unmarshal([]byte(raw), &tags); err != nil || tags == nil {
		return []string{}
	}
	return tags
}

func encodeStringList(values []string) (string, error) {
	if values == nil {
		values = []string{}
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("编码字符串列表: %w", err)
	}
	return string(data), nil
}

func decodeStringList(raw string) []string {
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil || values == nil {
		return []string{}
	}
	return values
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableTimeValue(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}

func nullableTime(v sql.NullString) *time.Time {
	if !v.Valid || v.String == "" {
		return nil
	}
	t := parseTime(v.String)
	if t.IsZero() {
		return nil
	}
	return &t
}

func nullableIntPtr(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	value := v.Int64
	return &value
}

func isUsernameConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "client_profile.username")
}
