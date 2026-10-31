package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// AuthRecord 是运行代理认证快照所需的用户/配置/生效证书组合。
type AuthRecord struct {
	UserID          string
	UserStatus      string
	ProfileID       string
	AuthType        string
	Username        string
	PasswordHash    string
	ProfileStatus   string
	ExpiresAt       *time.Time
	RouteMode       string
	AccessMode      string
	AllowedInternal []string

	ConcurrentLimit int64
	QuotaBytes      int64
	UploadBps       int64
	DownloadBps     int64
	QuotaEpoch      int64

	CertSerial      string
	CertFingerprint string
	CertStatus      string
	CertExpiresAt   *time.Time
}

// AuthRecords 读取全部用户配置及当前生效证书。
//
// 密码哈希只用于代理内部认证；吊销的证书不会出现（LEFT JOIN 仅匹配 active），
// 因此代理会按“未知证书”拒绝旧证书。
func (s *Store) AuthRecords(ctx context.Context) ([]AuthRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT u.id, u.status,
		        p.id, p.auth_type, COALESCE(p.username, ''), COALESCE(p.password_hash, ''),
		        p.status, p.expires_at, p.route_mode, p.access_mode, p.allowed_internal_json,
		        p.concurrent_limit, p.quota_bytes, p.upload_bps, p.download_bps, p.quota_epoch,
		        c.serial_number, c.fingerprint_sha256, c.status, c.expires_at
		   FROM app_user u
		   JOIN client_profile p ON p.user_id = u.id
		   LEFT JOIN certificate c ON c.profile_id = p.id AND c.status = 'active'
		  ORDER BY u.id, p.id`)
	if err != nil {
		return nil, fmt.Errorf("读取认证记录: %w", err)
	}
	defer rows.Close()

	out := make([]AuthRecord, 0)
	for rows.Next() {
		var (
			record                         AuthRecord
			profileExpires, certExpires    sql.NullString
			allowedInternal                string
			concurrent, quota, up, down    sql.NullInt64
			certSerial, certFP, certStatus sql.NullString
		)
		if err := rows.Scan(
			&record.UserID, &record.UserStatus,
			&record.ProfileID, &record.AuthType, &record.Username, &record.PasswordHash,
			&record.ProfileStatus, &profileExpires,
			&record.RouteMode, &record.AccessMode, &allowedInternal,
			&concurrent, &quota, &up, &down, &record.QuotaEpoch,
			&certSerial, &certFP, &certStatus, &certExpires,
		); err != nil {
			return nil, fmt.Errorf("读取认证记录: %w", err)
		}
		record.ExpiresAt = nullableTime(profileExpires)
		record.AllowedInternal = decodeStringList(allowedInternal)
		record.ConcurrentLimit = concurrent.Int64
		record.QuotaBytes = quota.Int64
		record.UploadBps = up.Int64
		record.DownloadBps = down.Int64
		record.CertSerial = certSerial.String
		record.CertFingerprint = certFP.String
		record.CertStatus = certStatus.String
		record.CertExpiresAt = nullableTime(certExpires)
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取认证记录: %w", err)
	}
	return out, nil
}
