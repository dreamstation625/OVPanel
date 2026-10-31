package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/dreamstation625/ovpanel/internal/secretbox"
	"github.com/dreamstation625/ovpanel/internal/store"
	"github.com/dreamstation625/ovpanel/internal/telemetry"
)

type stagingPackage struct {
	Dir        string
	Manifest   Manifest
	PanelDB    string
	AgentUsage string
	MasterKey  string
	Passphrase string
	ExpiresAt  time.Time
}

// Preview 是恢复前展示的包内容与影响提示。
type Preview struct {
	Token     string    `json:"token"`
	Manifest  Manifest  `json:"manifest"`
	Warnings  []string  `json:"warnings"`
	ExpiresAt time.Time `json:"expiresAt"`
	SHA256    string    `json:"sha256"`
}

// RestoreResult 是恢复执行结果。
type RestoreResult struct {
	RestoredAt         time.Time `json:"restoredAt"`
	PreRestoreBackupID string    `json:"preRestoreBackupId"`
	VPNStarted         bool      `json:"vpnStarted"`
	ReloginRequired    bool      `json:"reloginRequired"`
	Warnings           []string  `json:"warnings"`
}

var allowedPackageFiles = map[string]int64{
	"panel.db":         MaxDatabaseBytes,
	"agent-usage.json": MaxSmallFileBytes,
	"master.key":       MaxSmallFileBytes,
	"deployment.json":  MaxSmallFileBytes,
	"manifest.json":    MaxSmallFileBytes,
}

// PreviewRestore 解密上传包、校验清单与数据库，并生成恢复预览。
func (s *Service) PreviewRestore(ctx context.Context, reader io.Reader, passphrase string) (Preview, error) {
	s.cleanupStaging()
	passphrase = strings.TrimSpace(passphrase)
	if passphrase == "" {
		return Preview{}, errors.New("请输入备份恢复密码")
	}
	if err := os.MkdirAll(s.backupsDir, 0o700); err != nil {
		return Preview{}, fmt.Errorf("创建备份目录: %w", err)
	}
	upload, err := os.CreateTemp(s.backupsDir, ".upload-*")
	if err != nil {
		return Preview{}, fmt.Errorf("保存上传备份: %w", err)
	}
	uploadPath := upload.Name()
	defer os.Remove(uploadPath)
	if err := upload.Chmod(0o600); err != nil {
		upload.Close()
		return Preview{}, err
	}
	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(upload, hasher), io.LimitReader(reader, MaxPackageBytes+1))
	if err != nil {
		upload.Close()
		return Preview{}, fmt.Errorf("读取上传备份: %w", err)
	}
	if err := upload.Close(); err != nil {
		return Preview{}, err
	}
	if size > MaxPackageBytes {
		return Preview{}, fmt.Errorf("备份包超过 %d 字节上限", int64(MaxPackageBytes))
	}

	stagingDir, err := os.MkdirTemp(s.backupsDir, ".restore-*")
	if err != nil {
		return Preview{}, fmt.Errorf("创建恢复临时目录: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			os.RemoveAll(stagingDir)
		}
	}()

	if err := decryptAndExtract(uploadPath, stagingDir, passphrase); err != nil {
		return Preview{}, err
	}
	manifestData, err := os.ReadFile(filepath.Join(stagingDir, "manifest.json"))
	if err != nil {
		return Preview{}, fmt.Errorf("备份包缺少清单: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return Preview{}, fmt.Errorf("解析备份清单: %w", err)
	}
	if manifest.SchemaVersion != manifestSchemaVersion {
		return Preview{}, fmt.Errorf("备份清单版本不支持：%d", manifest.SchemaVersion)
	}
	if err := verifyManifest(stagingDir, manifest); err != nil {
		return Preview{}, err
	}
	schema, err := store.InspectDatabase(filepath.Join(stagingDir, "panel.db"))
	if err != nil {
		return Preview{}, err
	}
	currentSchema, err := s.store.SchemaVersion(ctx)
	if err != nil {
		return Preview{}, err
	}
	if schema > currentSchema {
		return Preview{}, fmt.Errorf("备份数据库版本 %d 高于当前程序支持的 %d", schema, currentSchema)
	}

	warnings, err := s.restoreWarnings(ctx, manifest, schema, currentSchema, stagingDir)
	if err != nil {
		return Preview{}, err
	}
	token := newToken()
	preview := Preview{
		Token:     token,
		Manifest:  manifest,
		Warnings:  warnings,
		ExpiresAt: s.now().Add(stagingTTL),
		SHA256:    hex.EncodeToString(hasher.Sum(nil)),
	}
	s.stagingMu.Lock()
	s.staging[token] = &stagingPackage{
		Dir:        stagingDir,
		Manifest:   manifest,
		PanelDB:    filepath.Join(stagingDir, "panel.db"),
		AgentUsage: filepath.Join(stagingDir, "agent-usage.json"),
		MasterKey:  filepath.Join(stagingDir, "master.key"),
		Passphrase: passphrase,
		ExpiresAt:  preview.ExpiresAt,
	}
	s.stagingMu.Unlock()
	cleanup = false
	return preview, nil
}

func (s *Service) restoreWarnings(ctx context.Context, manifest Manifest, schema, currentSchema int, stagingDir string) ([]string, error) {
	warnings := []string{
		"恢复会把面板、证书、额度和统计回退到备份时间点；备份之后的修改将丢失。",
		"恢复会断开全部 VPN 连接，并要求重新登录面板。",
	}
	if schema < currentSchema {
		warnings = append(warnings, fmt.Sprintf("备份数据库版本 %d 将在恢复后迁移到 %d。", schema, currentSchema))
	}
	admin, ok, err := s.store.Admin(ctx)
	if err != nil {
		return nil, err
	}
	if ok && manifest.AdminUpdatedAt != nil && manifest.AdminUpdatedAt.Before(admin.UpdatedAt) {
		warnings = append(warnings, "管理员密码将回退到备份时间点，之后修改的密码失效。")
	}
	_, revoked, _, err := s.store.CertificateStats(ctx, s.now())
	if err != nil {
		return nil, err
	}
	if manifest.RevokedCertificates < revoked {
		warnings = append(warnings, "部分证书吊销记录将回退，备份之后新增的吊销可能失效，请恢复后复核。")
	}
	quotaResets, err := s.store.QuotaResetCount(ctx)
	if err != nil {
		return nil, err
	}
	if manifest.QuotaResets < quotaResets {
		warnings = append(warnings, "部分额度重置记录将回退，恢复后请复核用量与额度。")
	}
	if manifest.AdminUpdatedAt == nil {
		warnings = append(warnings, "备份未记录管理员密码更新时间，无法比较密码回退范围。")
	}
	if !manifest.AgentPresent {
		warnings = append(warnings, "备份未包含运行代理用量检查点，恢复后用量与额度可能滞后。")
	}
	if manifest.MasterKeyFingerprint != "" && manifest.MasterKeyFingerprint != s.masterKey.Fingerprint() {
		warnings = append(warnings, "备份使用不同的主密钥；恢复后将切换为备份中的主密钥。")
	}
	if _, err := os.Stat(filepath.Join(stagingDir, "master.key")); err != nil {
		warnings = append(warnings, "备份包不含主密钥；恢复主机必须使用原主密钥，否则证书材料无法解密。")
	}
	return warnings, nil
}

// ExecuteRestore 执行恢复：恢复前备份 → 停止 VPN → 导入账本 → 替换数据库 → 重新应用配置。
func (s *Service) ExecuteRestore(ctx context.Context, token string) (RestoreResult, error) {
	s.createMu.Lock()
	defer s.createMu.Unlock()

	s.stagingMu.Lock()
	staging, ok := s.staging[token]
	if ok && s.now().After(staging.ExpiresAt) {
		os.RemoveAll(staging.Dir)
		delete(s.staging, token)
		ok = false
	}
	if ok {
		delete(s.staging, token)
	}
	s.stagingMu.Unlock()
	if !ok {
		return RestoreResult{}, ErrRestoreToken
	}
	defer os.RemoveAll(staging.Dir)

	opID := s.startOperation(ctx, "restore")
	fail := func(err error) (RestoreResult, error) {
		s.finishOperation(ctx, opID, false, err.Error())
		return RestoreResult{}, err
	}

	s.updateOperation(ctx, opID, 10, "创建恢复前备份")
	preBackup, err := s.createLocked(ctx, store.BackupPreRestore, "恢复前自动备份", staging.Passphrase)
	if err != nil {
		return fail(fmt.Errorf("创建恢复前备份失败，未执行恢复: %w", err))
	}

	warnings := make([]string, 0)
	agentAvailable := false
	if s.runtime != nil {
		_, agentAvailable = s.runtime.AgentStatus()
	}
	hasAgentUsage := false
	if _, err := os.Stat(staging.AgentUsage); err == nil {
		hasAgentUsage = true
	}
	if hasAgentUsage && !agentAvailable {
		return fail(errors.New("运行代理不可用，无法导入备份中的用量账本；未执行恢复"))
	}

	s.updateOperation(ctx, opID, 25, "停止 VPN 并导入用量检查点")
	if agentAvailable {
		if err := s.runtime.Command("stop"); err != nil {
			return fail(fmt.Errorf("停止 VPN 失败: %w", err))
		}
		if !s.waitAgent(20*time.Second, func(status map[string]any) bool {
			stopped, _ := status["stopped"].(bool)
			running, hasRunning := status["openvpnRunning"].(bool)
			return stopped || (hasRunning && !running)
		}) {
			_ = s.runtime.Command("start")
			return fail(errors.New("运行代理未确认停止，未执行恢复"))
		}
	} else {
		warnings = append(warnings, "运行代理当前不可用，未执行停止与账本导入。")
	}

	if hasAgentUsage && agentAvailable {
		if err := s.importAgentUsage(ctx, staging.AgentUsage); err != nil {
			_ = s.runtime.Command("start")
			return fail(err)
		}
	} else if !hasAgentUsage {
		warnings = append(warnings, "备份未包含代理账本，恢复后沿用当前用量。")
	}

	s.updateOperation(ctx, opID, 50, "替换面板数据库")
	if err := os.MkdirAll(filepath.Join(s.dataDir, "recovery"), 0o700); err != nil {
		return fail(err)
	}
	recoveryPath := filepath.Join(s.dataDir, "recovery",
		"panel.db.pre-restore-"+s.now().Format("20060102T150405Z"))
	version, err := s.store.ReplaceDatabase(ctx, staging.PanelDB, recoveryPath)
	if err != nil {
		_ = s.runtime.Command("start")
		return fail(fmt.Errorf("替换面板数据库失败: %w", err))
	}
	s.log.Warn("panel database restored", "schema", version)
	// 恢复任务记录属于旧数据库；在恢复后的数据库中重新登记同一任务。
	if err := s.store.CreateOperation(ctx, opID, "restore"); err != nil {
		s.log.Warn("recreate restore operation after restore failed", "error", err)
	}

	// 恢复前备份记录写入了旧数据库，需要在恢复后的数据库中重新登记。
	if err := s.store.CreateBackupRecord(ctx, preBackup); err != nil {
		s.log.Warn("register pre-restore backup after restore failed", "error", err)
	}

	s.updateOperation(ctx, opID, 75, "切换主密钥并重新应用配置")
	if err := s.restoreMasterKey(staging.MasterKey, &warnings); err != nil {
		return fail(err)
	}
	vpnStarted := false
	if s.runtime != nil && agentAvailable {
		if _, err := s.runtime.Apply(ctx); err != nil {
			warnings = append(warnings, "恢复后的服务端配置应用失败："+err.Error())
		}
		if !staging.Manifest.AgentStopped {
			if err := s.runtime.Command("start"); err != nil {
				warnings = append(warnings, "启动 VPN 命令失败："+err.Error())
			} else if s.waitAgent(25*time.Second, func(status map[string]any) bool {
				running, _ := status["openvpnRunning"].(bool)
				management, _ := status["managementConnected"].(bool)
				return running && management
			}) {
				vpnStarted = true
			} else {
				warnings = append(warnings, "VPN 未在 25 秒内恢复就绪，请查看服务端页面。")
			}
		}
	}

	result := RestoreResult{
		RestoredAt:         s.now(),
		PreRestoreBackupID: preBackup.ID,
		VPNStarted:         vpnStarted,
		ReloginRequired:    true,
		Warnings:           warnings,
	}
	s.finishOperation(ctx, opID, true, "恢复完成，需要重新登录")
	return result, nil
}

func (s *Service) importAgentUsage(ctx context.Context, path string) error {
	if s.runtime == nil {
		return errors.New("运行控制未配置，无法导入账本")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取备份账本: %w", err)
	}
	if err := os.MkdirAll(s.controlDir, 0o700); err != nil {
		return fmt.Errorf("创建控制目录: %w", err)
	}
	target := filepath.Join(s.controlDir, telemetry.RestoreFile)
	tmp, err := os.CreateTemp(s.controlDir, ".usage-restore-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, target); err != nil {
		return err
	}
	started := s.now()
	if err := s.runtime.Command("restore-usage"); err != nil {
		return fmt.Errorf("导入代理账本命令失败: %w", err)
	}
	if !s.waitAgent(20*time.Second, func(status map[string]any) bool {
		raw, ok := status["usageRestoredAt"].(string)
		if !ok {
			return false
		}
		restored, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return false
		}
		return !restored.Before(started.Add(-time.Second))
	}) {
		return errors.New("运行代理未确认导入用量账本")
	}
	return nil
}

func (s *Service) restoreMasterKey(path string, warnings *[]string) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		*warnings = append(*warnings, "备份不含主密钥，继续使用当前主密钥。")
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取备份主密钥: %w", err)
	}
	key, err := secretbox.Parse(string(data))
	if err != nil {
		return fmt.Errorf("解析备份主密钥: %w", err)
	}
	if key.Fingerprint() == s.masterKey.Fingerprint() {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.masterKeyPath), 0o700); err != nil {
		return err
	}
	tmp := s.masterKeyPath + ".restore.tmp"
	if err := os.WriteFile(tmp, []byte(key.Encode()+"\n"), 0o600); err != nil {
		return fmt.Errorf("写入恢复主密钥: %w", err)
	}
	if err := os.Rename(tmp, s.masterKeyPath); err != nil {
		return fmt.Errorf("替换主密钥文件: %w", err)
	}
	s.masterKey = key
	s.pki.SetKey(key)
	s.accounts.SetKey(key)
	if s.masterKeyEnv {
		*warnings = append(*warnings, "当前通过 OVPANEL_MASTER_KEY 注入主密钥；请把环境变量更新为备份主密钥，否则重启会再次不匹配。")
	}
	return nil
}

func (s *Service) waitAgent(timeout time.Duration, ready func(map[string]any) bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.runtime != nil {
			if status, ok := s.runtime.AgentStatus(); ok && ready(status) {
				return true
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

func decryptAndExtract(packagePath, stagingDir, passphrase string) error {
	file, err := os.Open(packagePath)
	if err != nil {
		return fmt.Errorf("打开备份包: %w", err)
	}
	defer file.Close()
	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return fmt.Errorf("解析恢复密码: %w", err)
	}
	decrypted, err := age.Decrypt(file, identity)
	if err != nil {
		return ErrInvalidPackage
	}
	gzipReader, err := gzip.NewReader(decrypted)
	if err != nil {
		return ErrInvalidPackage
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)

	total := int64(0)
	seen := map[string]struct{}{}
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ErrInvalidPackage
		}
		name := header.Name
		limit, ok := allowedPackageFiles[name]
		if !ok || header.Typeflag != tar.TypeReg {
			return fmt.Errorf("备份包包含不允许的文件：%q", name)
		}
		if _, dup := seen[name]; dup {
			return fmt.Errorf("备份包文件重复：%q", name)
		}
		seen[name] = struct{}{}
		if header.Size < 0 || header.Size > limit {
			return fmt.Errorf("备份文件 %s 大小非法", name)
		}
		total += header.Size
		if total > MaxExtractBytes {
			return errors.New("备份包解压后超过大小上限")
		}
		path := filepath.Join(stagingDir, name)
		out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("写入恢复文件 %s: %w", name, err)
		}
		written, copyErr := io.CopyN(out, tarReader, header.Size)
		closeErr := out.Close()
		if copyErr != nil || written != header.Size {
			return fmt.Errorf("恢复文件 %s 不完整", name)
		}
		if closeErr != nil {
			return closeErr
		}
	}
	if _, ok := seen["manifest.json"]; !ok {
		return errors.New("备份包缺少清单")
	}
	if _, ok := seen["panel.db"]; !ok {
		return errors.New("备份包缺少面板数据库")
	}
	return nil
}

func verifyManifest(stagingDir string, manifest Manifest) error {
	for _, entry := range manifest.Files {
		if _, ok := allowedPackageFiles[entry.Name]; !ok {
			return fmt.Errorf("清单包含未知文件：%q", entry.Name)
		}
		path := filepath.Join(stagingDir, entry.Name)
		sum, size, err := hashFile(path)
		if err != nil {
			return fmt.Errorf("校验备份文件 %s: %w", entry.Name, err)
		}
		if size != entry.Size {
			return fmt.Errorf("备份文件 %s 大小与清单不一致", entry.Name)
		}
		if !strings.EqualFold(sum, entry.SHA256) {
			return fmt.Errorf("备份文件 %s 校验和不一致", entry.Name)
		}
	}
	if _, err := os.Stat(filepath.Join(stagingDir, "panel.db")); err != nil {
		return errors.New("备份缺少面板数据库")
	}
	return nil
}

func newToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
