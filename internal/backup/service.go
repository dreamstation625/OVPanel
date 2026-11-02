// Package backup 生成、校验与恢复面板加密备份包。
//
// 备份包是 tar.gz 外层再套 age 口令加密；包内包含面板数据库一致性快照、
// 运行代理用量检查点、主密钥与部署信息。恢复密码不进入明文清单，
// 自动备份使用主密钥加密保存的恢复口令。
package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dreamstation625/ovpanel/internal/accounts"
	"github.com/dreamstation625/ovpanel/internal/pki"
	"github.com/dreamstation625/ovpanel/internal/secretbox"
	"github.com/dreamstation625/ovpanel/internal/store"
	"github.com/dreamstation625/ovpanel/internal/vpnruntime"
)

const (
	// PackageSuffix 是备份包扩展名。
	PackageSuffix = ".ovpbak"
	// MaxPackageBytes 是上传/下载允许的最大包体。
	MaxPackageBytes = 512 << 20
	// MaxDatabaseBytes 是备份包内数据库上限。
	MaxDatabaseBytes = 512 << 20
	// MaxSmallFileBytes 是清单、账本、主密钥等小文件上限。
	MaxSmallFileBytes = 16 << 20
	// MaxExtractBytes 是解包后总大小上限。
	MaxExtractBytes = 600 << 20

	stagingTTL = 30 * time.Minute

	passphraseAAD = "ovpanel-backup-passphrase-v1"
)

var (
	ErrNotConfigured   = errors.New("尚未设置备份恢复密码")
	ErrInvalidPackage  = errors.New("备份包无效或恢复密码错误")
	ErrRestoreToken    = errors.New("恢复令牌无效或已过期")
	ErrMaintenanceBusy = errors.New("OpenVPN 更新任务进行中，暂不能备份或恢复")
)

var schedulePattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// 中国不实行夏令时，固定 UTC+8 与统计自然日口径一致。
func usageZone() *time.Location {
	return time.FixedZone("Asia/Shanghai", 8*3600)
}

// Options 是备份服务的依赖。
type Options struct {
	Store         *store.Store
	PKI           *pki.Service
	Accounts      *accounts.Service
	Runtime       *vpnruntime.Service
	MasterKey     secretbox.Key
	MasterKeyPath string
	MasterKeyEnv  bool
	DataDir       string
	ControlDir    string
	Version       string
	Commit        string
	Log           *slog.Logger
	// MaintenanceBusy 在 OpenVPN 更新期间返回错误，用于与升级任务互斥。
	MaintenanceBusy func(context.Context) error
}

// Service 生成、管理和恢复备份包。
type Service struct {
	store           *store.Store
	pki             *pki.Service
	accounts        *accounts.Service
	runtime         *vpnruntime.Service
	masterKey       secretbox.Key
	masterKeyPath   string
	masterKeyEnv    bool
	dataDir         string
	controlDir      string
	backupsDir      string
	version         string
	commit          string
	log             *slog.Logger
	now             func() time.Time
	maintenanceBusy func(context.Context) error
	mutating        atomic.Bool

	createMu             sync.Mutex
	scheduleMu           sync.Mutex
	lastScheduledAttempt time.Time
	stagingMu            sync.Mutex
	staging              map[string]*stagingPackage
}

// New 创建备份服务。
func New(opts Options) *Service {
	now := func() time.Time { return time.Now().UTC() }
	return &Service{
		store:           opts.Store,
		pki:             opts.PKI,
		accounts:        opts.Accounts,
		runtime:         opts.Runtime,
		masterKey:       opts.MasterKey,
		masterKeyPath:   opts.MasterKeyPath,
		masterKeyEnv:    opts.MasterKeyEnv,
		dataDir:         opts.DataDir,
		controlDir:      opts.ControlDir,
		backupsDir:      filepath.Join(opts.DataDir, "backups"),
		version:         opts.Version,
		commit:          opts.Commit,
		log:             opts.Log,
		maintenanceBusy: opts.MaintenanceBusy,
		now:             now,
		staging:         map[string]*stagingPackage{},
	}
}

// SettingsInput 是更新备份设置的输入。
type SettingsInput struct {
	Enabled        bool
	ScheduleTime   string
	RetentionCount int
	// BackupPassword 为空表示保留现有恢复密码。
	BackupPassword string
}

// SettingsView 是可返回给界面的备份设置。
type SettingsView struct {
	Configured     bool      `json:"configured"`
	Enabled        bool      `json:"enabled"`
	ScheduleTime   string    `json:"scheduleTime"`
	RetentionCount int       `json:"retentionCount"`
	UpdatedAt      time.Time `json:"updatedAt"`
	NextRunAt      string    `json:"nextRunAt,omitempty"`
}

// SaveSettings 更新备份计划与恢复密码。恢复密码只保存哈希与主密钥密文。
func (s *Service) SaveSettings(ctx context.Context, input SettingsInput) (SettingsView, error) {
	input.ScheduleTime = strings.TrimSpace(input.ScheduleTime)
	if !schedulePattern.MatchString(input.ScheduleTime) {
		return SettingsView{}, errors.New("备份时间必须为 HH:MM")
	}
	if input.RetentionCount < 1 || input.RetentionCount > 90 {
		return SettingsView{}, errors.New("保留数量需在 1 到 90 之间")
	}

	current, err := s.store.BackupSettings(ctx)
	if err != nil {
		return SettingsView{}, err
	}
	ciphertext := current.PassphraseCipher
	if input.BackupPassword != "" {
		password := strings.TrimSpace(input.BackupPassword)
		if n := len([]rune(password)); n < 12 || n > 200 {
			return SettingsView{}, errors.New("恢复密码长度需在 12 到 200 个字符之间")
		}
		ciphertext, err = s.masterKey.Seal([]byte(password), []byte(passphraseAAD))
		if err != nil {
			return SettingsView{}, fmt.Errorf("加密备份恢复密码: %w", err)
		}
	}
	if len(ciphertext) == 0 {
		return SettingsView{}, ErrNotConfigured
	}
	now := s.now()
	if err := s.store.SaveBackupSettings(ctx, store.BackupSettings{
		Enabled:          input.Enabled,
		ScheduleTime:     input.ScheduleTime,
		RetentionCount:   input.RetentionCount,
		PassphraseCipher: ciphertext,
	}, now); err != nil {
		return SettingsView{}, err
	}
	return s.SettingsView(ctx)
}

// SettingsView 返回公开设置。
func (s *Service) SettingsView(ctx context.Context) (SettingsView, error) {
	settings, err := s.store.BackupSettings(ctx)
	if err != nil {
		return SettingsView{}, err
	}
	view := SettingsView{
		Configured:     len(settings.PassphraseCipher) > 0,
		Enabled:        settings.Enabled,
		ScheduleTime:   settings.ScheduleTime,
		RetentionCount: settings.RetentionCount,
		UpdatedAt:      settings.UpdatedAt,
	}
	if settings.Enabled {
		view.NextRunAt = s.nextRun(settings.ScheduleTime).Format(time.RFC3339)
	}
	return view, nil
}

func (s *Service) passphrase(ctx context.Context) (string, error) {
	settings, err := s.store.BackupSettings(ctx)
	if err != nil {
		return "", err
	}
	if len(settings.PassphraseCipher) == 0 {
		return "", ErrNotConfigured
	}
	plaintext, err := s.masterKey.Open(settings.PassphraseCipher, []byte(passphraseAAD))
	if err != nil {
		return "", fmt.Errorf("解密备份恢复密码: %w", err)
	}
	return strings.TrimSpace(string(plaintext)), nil
}

// Mutating 表示备份或恢复正在修改状态。
func (s *Service) Mutating() bool {
	return s.mutating.Load()
}

func (s *Service) beginMutation(ctx context.Context) error {
	if s.maintenanceBusy != nil {
		if err := s.maintenanceBusy(ctx); err != nil {
			return ErrMaintenanceBusy
		}
	}
	s.mutating.Store(true)
	return nil
}

// List 返回备份列表与存储占用。
func (s *Service) List(ctx context.Context) ([]store.BackupRecord, int64, error) {
	records, err := s.store.ListBackups(ctx, 200)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.store.BackupStorageBytes(ctx)
	if err != nil {
		return nil, 0, err
	}
	return records, total, nil
}

// PackagePath 返回备份包文件路径。
func (s *Service) PackagePath(record store.BackupRecord) string {
	return filepath.Join(s.backupsDir, filepath.Base(record.Filename))
}

// Delete 删除备份包与记录。
func (s *Service) Delete(ctx context.Context, id string) error {
	record, ok, err := s.store.BackupRecord(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return store.ErrNotFound
	}
	path := s.PackagePath(record)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除备份文件: %w", err)
	}
	return s.store.DeleteBackupRecord(ctx, id)
}

// RunScheduled 在到达计划时间且今天尚未自动备份时执行一次。
func (s *Service) RunScheduled(ctx context.Context) error {
	settings, err := s.store.BackupSettings(ctx)
	if err != nil {
		return err
	}
	if !settings.Enabled || len(settings.PassphraseCipher) == 0 {
		return nil
	}
	now := s.now()
	due, ok := scheduleDue(settings.ScheduleTime, now)
	if !ok {
		return nil
	}
	records, err := s.store.ListBackups(ctx, 100)
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.Kind == store.BackupScheduled && sameDay(record.CreatedAt, now) {
			return nil
		}
	}
	if !due {
		return nil
	}
	s.scheduleMu.Lock()
	if !s.lastScheduledAttempt.IsZero() && now.Sub(s.lastScheduledAttempt) < 10*time.Minute {
		s.scheduleMu.Unlock()
		return nil
	}
	s.lastScheduledAttempt = now
	s.scheduleMu.Unlock()
	_, err = s.Create(ctx, store.BackupScheduled, "定时备份", "")
	return err
}

// scheduleDue 判断当前是否已过计划时间；跨日判断由调用方检查当日记录。
func scheduleDue(schedule string, now time.Time) (bool, bool) {
	parts := strings.Split(schedule, ":")
	if len(parts) != 2 {
		return false, false
	}
	local := now.In(usageZone())
	hour := 0
	minute := 0
	if _, err := fmt.Sscanf(parts[0], "%d", &hour); err != nil {
		return false, false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &minute); err != nil {
		return false, false
	}
	scheduled := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, local.Location())
	return !now.Before(scheduled), true
}

func (s *Service) nextRun(schedule string) time.Time {
	parts := strings.Split(schedule, ":")
	local := s.now().In(usageZone())
	hour, minute := 3, 0
	if len(parts) == 2 {
		fmt.Sscanf(parts[0], "%d", &hour)
		fmt.Sscanf(parts[1], "%d", &minute)
	}
	next := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, local.Location())
	if !next.After(s.now()) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

func sameDay(a, b time.Time) bool {
	left := a.In(usageZone())
	right := b.In(usageZone())
	return left.Year() == right.Year() && left.YearDay() == right.YearDay()
}
