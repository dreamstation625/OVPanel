// Package runtimeauth 把面板数据库中的用户、配置与生效证书转换成
// 运行代理可使用的认证快照。它不保存数据，只做一次只读映射与校验。
package runtimeauth

import (
	"context"

	"github.com/dreamstation625/ovpanel/internal/admission"
	"github.com/dreamstation625/ovpanel/internal/store"
)

// Build 从数据库构建认证快照。
func Build(ctx context.Context, st *store.Store) (admission.Snapshot, error) {
	records, err := st.AuthRecords(ctx)
	if err != nil {
		return admission.Snapshot{}, err
	}

	users := make([]admission.User, 0)
	seenUsers := map[string]struct{}{}
	profiles := make([]admission.Profile, 0, len(records))
	for _, record := range records {
		if _, ok := seenUsers[record.UserID]; !ok {
			seenUsers[record.UserID] = struct{}{}
			users = append(users, admission.User{
				ID:     record.UserID,
				Status: record.UserStatus,
			})
		}
		profile := admission.Profile{
			ID:              record.ProfileID,
			UserID:          record.UserID,
			AuthType:        admission.AuthType(record.AuthType),
			Username:        record.Username,
			PasswordHash:    record.PasswordHash,
			Status:          record.ProfileStatus,
			ExpiresAt:       record.ExpiresAt,
			RouteMode:       record.RouteMode,
			AccessMode:      record.AccessMode,
			AllowedInternal: record.AllowedInternal,
			ConcurrentLimit: record.ConcurrentLimit,
			QuotaBytes:      uint64(maxInt64(record.QuotaBytes)),
			UploadBps:       record.UploadBps,
			DownloadBps:     record.DownloadBps,
			QuotaEpoch:      record.QuotaEpoch,
		}
		if record.CertSerial != "" {
			profile.CertSerial = record.CertSerial
			profile.CertFingerprint = record.CertFingerprint
			profile.CertStatus = record.CertStatus
			profile.CertExpiresAt = record.CertExpiresAt
		}
		profiles = append(profiles, profile)
	}

	snapshot := admission.Snapshot{Users: users, Profiles: profiles}
	if err := snapshot.Validate(); err != nil {
		return admission.Snapshot{}, err
	}
	return snapshot, nil
}

func maxInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}
