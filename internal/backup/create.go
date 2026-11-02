package backup

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dreamstation625/ovpanel/internal/store"
	"github.com/dreamstation625/ovpanel/internal/telemetry"
	"github.com/google/uuid"
)

const manifestSchemaVersion = 1

// FileEntry 是备份包内文件的校验信息。
type FileEntry struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest 是备份包内的明文清单；包外层由 age 认证加密。
type Manifest struct {
	SchemaVersion        int         `json:"schemaVersion"`
	Kind                 string      `json:"kind"`
	CreatedAt            time.Time   `json:"createdAt"`
	AppVersion           string      `json:"appVersion"`
	Commit               string      `json:"commit,omitempty"`
	PanelSchema          int         `json:"panelSchema"`
	ConfigRevision       int64       `json:"configRevision"`
	ConfigHash           string      `json:"configHash,omitempty"`
	Users                int         `json:"users"`
	Profiles             int         `json:"profiles"`
	ValidCertificates    int         `json:"validCertificates"`
	RevokedCertificates  int         `json:"revokedCertificates"`
	ExpiredCertificates  int         `json:"expiredCertificates"`
	QuotaResets          int         `json:"quotaResets"`
	AdminUpdatedAt       *time.Time  `json:"adminUpdatedAt,omitempty"`
	AgentPresent         bool        `json:"agentPresent"`
	AgentStopped         bool        `json:"agentStopped"`
	AgentGeneration      string      `json:"agentGeneration,omitempty"`
	UsageSeq             uint64      `json:"usageSeq"`
	CheckpointAt         string      `json:"checkpointAt,omitempty"`
	MasterKeyFingerprint string      `json:"masterKeyFingerprint"`
	Files                []FileEntry `json:"files"`
}

// Create 生成加密备份包并登记；kind 为 manual/scheduled/pre_restore。
func (s *Service) Create(ctx context.Context, kind, note, passphrase string) (store.BackupRecord, error) {
	s.createMu.Lock()
	defer s.createMu.Unlock()
	if err := s.beginMutation(ctx); err != nil {
		return store.BackupRecord{}, err
	}
	defer s.mutating.Store(false)
	return s.createLocked(ctx, kind, note, passphrase)
}

func (s *Service) createLocked(ctx context.Context, kind, note, passphrase string) (store.BackupRecord, error) {
	if passphrase == "" {
		loaded, err := s.passphrase(ctx)
		if err != nil {
			return store.BackupRecord{}, err
		}
		passphrase = loaded
	}
	if strings.TrimSpace(passphrase) == "" {
		return store.BackupRecord{}, ErrNotConfigured
	}

	opID := s.startOperation(ctx, "backup")
	fail := func(err error) (store.BackupRecord, error) {
		s.finishOperation(ctx, opID, false, err.Error())
		return store.BackupRecord{}, err
	}

	if err := os.MkdirAll(s.backupsDir, 0o700); err != nil {
		return fail(fmt.Errorf("创建备份目录: %w", err))
	}
	s.updateOperation(ctx, opID, 10, "生成数据库快照")
	s.cleanupStaging()

	tempDir, err := os.MkdirTemp(s.backupsDir, ".build-*")
	if err != nil {
		return fail(fmt.Errorf("创建备份临时目录: %w", err))
	}
	defer os.RemoveAll(tempDir)

	now := s.now()
	dbPath := filepath.Join(tempDir, "panel.db")
	if err := s.store.SnapshotDatabase(ctx, dbPath); err != nil {
		return fail(err)
	}

	// 请求代理写出最新账本检查点；代理离线时保留现有检查点并标注时间。
	s.updateOperation(ctx, opID, 30, "读取用量记录")
	checkpointAt := s.requestCheckpoint(ctx, 3*time.Second)
	agentPath := filepath.Join(s.controlDir, telemetry.CheckpointFile)
	agentMeta := agentCheckpointMeta{}
	agentPresent := false
	if data, err := os.ReadFile(agentPath); err == nil {
		agentPresent = true
		if err := json.Unmarshal(data, &agentMeta); err != nil {
			s.log.Warn("agent checkpoint is malformed; backup continues without usage seq", "error", err)
		}
	}

	s.updateOperation(ctx, opID, 45, "写入备份内容")
	masterKeyPath := filepath.Join(tempDir, "master.key")
	if err := writeFile(masterKeyPath, []byte(s.masterKey.Encode()+"\n"), 0o600); err != nil {
		return fail(err)
	}
	deployment, err := json.MarshalIndent(map[string]any{
		"version":      s.version,
		"commit":       s.commit,
		"panelSchema":  s.currentSchema(ctx),
		"backupSchema": manifestSchemaVersion,
		"panelImage":   strings.TrimSpace(os.Getenv("OVPANEL_PANEL_IMAGE_REF")),
		"openvpnImage": strings.TrimSpace(os.Getenv("OVPANEL_OPENVPN_IMAGE_REF")),
		"createdAt":    now.Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return fail(err)
	}
	deploymentPath := filepath.Join(tempDir, "deployment.json")
	if err := writeFile(deploymentPath, deployment, 0o600); err != nil {
		return fail(err)
	}

	manifest, err := s.buildManifest(ctx, kind, now, dbPath, agentPath, agentPresent,
		agentMeta, checkpointAt, masterKeyPath, deploymentPath)
	if err != nil {
		return fail(err)
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fail(err)
	}
	manifestPath := filepath.Join(tempDir, "manifest.json")
	if err := writeFile(manifestPath, manifestData, 0o600); err != nil {
		return fail(err)
	}

	s.updateOperation(ctx, opID, 65, "加密备份包")
	id := uuid.NewString()
	filename := fmt.Sprintf("ovpanel-%s-%s-%s%s", kind,
		now.Format("20060102T150405Z"), id[:8], PackageSuffix)
	target := filepath.Join(s.backupsDir, filename)
	sum, size, err := writeEncryptedPackage(target, passphrase, func(writer *tar.Writer) error {
		if err := addFile(writer, "agent-usage.json", agentPath); err != nil && agentPresent {
			return err
		}
		if err := addFile(writer, "panel.db", dbPath); err != nil {
			return err
		}
		if err := addFile(writer, "master.key", masterKeyPath); err != nil {
			return err
		}
		if err := addFile(writer, "deployment.json", deploymentPath); err != nil {
			return err
		}
		return addFile(writer, "manifest.json", manifestPath)
	})
	if err != nil {
		os.Remove(target)
		return fail(err)
	}

	record := store.BackupRecord{
		ID:              id,
		Filename:        filename,
		Kind:            kind,
		Note:            strings.TrimSpace(note),
		SizeBytes:       size,
		SHA256:          sum,
		AppVersion:      s.version,
		ConfigRevision:  manifest.ConfigRevision,
		AgentGeneration: manifest.AgentGeneration,
		UsageSeq:        manifest.UsageSeq,
		ManifestJSON:    string(manifestData),
		CreatedAt:       now,
	}
	if err := s.store.CreateBackupRecord(ctx, record); err != nil {
		os.Remove(target)
		return fail(err)
	}
	if kind == store.BackupScheduled {
		s.pruneScheduled(ctx)
	}
	s.finishOperation(ctx, opID, true, fmt.Sprintf("备份完成：%s（%d 字节）", filename, size))
	return record, nil
}

func (s *Service) buildManifest(ctx context.Context, kind string, now time.Time,
	dbPath, agentPath string, agentPresent bool, agentMeta agentCheckpointMeta,
	checkpointAt time.Time, masterKeyPath, deploymentPath string) (Manifest, error) {
	users, profiles, err := s.store.AccountCounts(ctx)
	if err != nil {
		return Manifest{}, err
	}
	active, revoked, expired, err := s.store.CertificateStats(ctx, now)
	if err != nil {
		return Manifest{}, err
	}
	quotaResets, err := s.store.QuotaResetCount(ctx)
	if err != nil {
		return Manifest{}, err
	}
	schema, err := s.store.SchemaVersion(ctx)
	if err != nil {
		return Manifest{}, err
	}
	state, err := s.store.ServerConfigState(ctx)
	if err != nil {
		return Manifest{}, err
	}
	var revision int64
	var hash string
	if state.Active != nil {
		revision = state.Active.Revision
		hash = state.Active.RenderedHash
	}
	admin, ok, err := s.store.Admin(ctx)
	if err != nil {
		return Manifest{}, err
	}
	var adminUpdated *time.Time
	if ok {
		value := admin.UpdatedAt
		adminUpdated = &value
	}

	manifest := Manifest{
		SchemaVersion:        manifestSchemaVersion,
		Kind:                 kind,
		CreatedAt:            now,
		AppVersion:           s.version,
		Commit:               s.commit,
		PanelSchema:          schema,
		ConfigRevision:       revision,
		ConfigHash:           hash,
		Users:                users,
		Profiles:             profiles,
		ValidCertificates:    active - expired,
		RevokedCertificates:  revoked,
		ExpiredCertificates:  expired,
		QuotaResets:          quotaResets,
		AdminUpdatedAt:       adminUpdated,
		AgentPresent:         agentPresent,
		AgentGeneration:      strings.TrimSpace(agentMeta.Generation),
		UsageSeq:             agentMeta.Seq,
		MasterKeyFingerprint: s.masterKey.Fingerprint(),
	}
	if !checkpointAt.IsZero() {
		manifest.CheckpointAt = checkpointAt.UTC().Format(time.RFC3339)
	}
	status, hasStatus := map[string]any{}, false
	if s.runtime != nil {
		status, hasStatus = s.runtime.AgentStatus()
	}
	if hasStatus {
		if stopped, ok := status["stopped"].(bool); ok {
			manifest.AgentStopped = stopped
		}
		if generation, ok := status["usageGeneration"].(string); ok && generation != "" {
			manifest.AgentGeneration = generation
		}
	} else {
		manifest.AgentPresent = false
	}

	entries := make([]FileEntry, 0, 5)
	for _, item := range []struct{ name, path string }{
		{"panel.db", dbPath},
	} {
		sum, size, err := hashFile(item.path)
		if err != nil {
			return Manifest{}, err
		}
		entries = append(entries, FileEntry{Name: item.name, Size: size, SHA256: sum})
	}
	if agentPresent {
		sum, size, err := hashFile(agentPath)
		if err != nil {
			return Manifest{}, err
		}
		entries = append(entries, FileEntry{Name: "agent-usage.json", Size: size, SHA256: sum})
	}
	for _, item := range []struct{ name, path string }{
		{"master.key", masterKeyPath},
		{"deployment.json", deploymentPath},
	} {
		sum, size, err := hashFile(item.path)
		if err != nil {
			return Manifest{}, err
		}
		entries = append(entries, FileEntry{Name: item.name, Size: size, SHA256: sum})
	}
	manifest.Files = entries
	return manifest, nil
}

type agentCheckpointMeta struct {
	SchemaVersion int    `json:"schemaVersion"`
	Seq           uint64 `json:"seq"`
	Generation    string `json:"generation"`
	UpdatedAt     string `json:"updatedAt"`
}

// requestCheckpoint 请求代理立即写账本，返回检查点时间；离线时返回零值。
func (s *Service) requestCheckpoint(ctx context.Context, timeout time.Duration) time.Time {
	started := s.now()
	if s.runtime == nil {
		return time.Time{}
	}
	if _, ok := s.runtime.AgentStatus(); !ok {
		// 代理从未运行：没有可刷新的检查点，不等待超时。
		return time.Time{}
	}
	if err := s.runtime.Command("checkpoint"); err != nil {
		s.log.Warn("request agent checkpoint failed", "error", err)
	}
	path := filepath.Join(s.controlDir, telemetry.CheckpointFile)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil && info.ModTime().After(started.Add(-time.Second)) {
			return info.ModTime().UTC()
		}
		select {
		case <-ctx.Done():
			return time.Time{}
		case <-time.After(150 * time.Millisecond):
		}
	}
	return time.Time{}
}

func (s *Service) pruneScheduled(ctx context.Context) {
	settings, err := s.store.BackupSettings(ctx)
	if err != nil {
		s.log.Warn("read backup settings for retention failed", "error", err)
		return
	}
	records, err := s.store.ListBackups(ctx, 500)
	if err != nil {
		s.log.Warn("list backups for retention failed", "error", err)
		return
	}
	scheduled := make([]store.BackupRecord, 0, len(records))
	for _, record := range records {
		if record.Kind == store.BackupScheduled {
			scheduled = append(scheduled, record)
		}
	}
	for i := settings.RetentionCount; i < len(scheduled); i++ {
		if err := s.Delete(ctx, scheduled[i].ID); err != nil {
			s.log.Warn("prune scheduled backup failed", "backup_id", scheduled[i].ID, "error", err)
		}
	}
}

func (s *Service) currentSchema(ctx context.Context) int {
	version, err := s.store.SchemaVersion(ctx)
	if err != nil {
		return 0
	}
	return version
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		return fmt.Errorf("写入 %s: %w", path, err)
	}
	return nil
}

func (s *Service) startOperation(ctx context.Context, kind string) string {
	id := uuid.NewString()
	if err := s.store.CreateOperation(ctx, id, kind); err != nil {
		s.log.Warn("create backup operation failed", "error", err)
		return ""
	}
	s.updateOperation(ctx, id, 1, "准备备份")
	return id
}

func (s *Service) updateOperation(ctx context.Context, id string, progress int, message string) {
	if id == "" {
		return
	}
	op := store.Operation{ID: id, State: store.OperationRunning, Progress: progress, Message: message}
	if err := s.store.UpdateOperation(ctx, op); err != nil {
		s.log.Warn("update backup operation failed", "error", err)
	}
}

func (s *Service) finishOperation(ctx context.Context, id string, ok bool, message string) {
	if id == "" {
		return
	}
	state := store.OperationSucceeded
	code := ""
	if !ok {
		state = store.OperationFailed
		code = "backup_failed"
	}
	op := store.Operation{
		ID: id, State: state, Progress: 100, Message: message,
		ErrorCode: code, FinishedAt: s.now(),
	}
	if err := s.store.UpdateOperation(ctx, op); err != nil {
		s.log.Warn("finish backup operation failed", "error", err)
	}
}

func (s *Service) cleanupStaging() {
	s.muStaging(func(staging map[string]*stagingPackage) {
		now := s.now()
		for token, item := range staging {
			if now.After(item.ExpiresAt) {
				os.RemoveAll(item.Dir)
				delete(staging, token)
			}
		}
	})
}

func (s *Service) muStaging(fn func(map[string]*stagingPackage)) {
	// 单管理员、低频操作；使用独立锁避免与创建互斥。
	s.stagingMu.Lock()
	defer s.stagingMu.Unlock()
	fn(s.staging)
}
