// Package agentusage 在 OpenVPN 容器内维护持久用量账本与事件日志。
//
// 账本以「会话累计高水位」记录用量：重复或乱序采样不重复累计，
// 计数回退只记录断档、不产生负流量。手动重置额度通过代次推进把
// 当前累计设为新基线，历史累计与日汇总不被删除。
//
// 事件日志写入共享控制目录，面板离线期间仍持续追加，恢复后按
// 「运行代次 + 序号」去重补传。
package agentusage

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dreamstation625/ovpanel/internal/admission"
	"github.com/dreamstation625/ovpanel/internal/telemetry"
)

const (
	stateFileName = "usage-state.json"
	activeEvents  = "events-active.jsonl"
	maxEventBytes = 8 << 20
	eventRetain   = 90 * 24 * time.Hour
)

const stateSchema = 1

type sessionState struct {
	Up   uint64 `json:"up"`
	Down uint64 `json:"down"`
}

type profileState struct {
	Epoch    int64                    `json:"epoch"`
	Baseline uint64                   `json:"baseline"`
	Total    uint64                   `json:"total"`
	Gaps     int                      `json:"gaps"`
	Sessions map[string]*sessionState `json:"sessions"`
}

type stateFile struct {
	SchemaVersion int                      `json:"schemaVersion"`
	Seq           uint64                   `json:"seq"`
	Profiles      map[string]*profileState `json:"profiles"`
	UpdatedAt     string                   `json:"updatedAt"`
}

// RecordResult 描述一次采样的累计结果。
type RecordResult struct {
	DeltaUp   uint64
	DeltaDown uint64
	Rollback  bool
	New       bool
}

// Store 是按配置汇总的用量账本。
type Store struct {
	mu         sync.Mutex
	statePath  string
	controlDir string
	generation string
	log        *slog.Logger
	now        func() time.Time

	seq      uint64
	profiles map[string]*profileState
}

// New 创建账本；generation 是本次运行代理代次，不能为空。
func New(stateDir, controlDir, generation string, log *slog.Logger) *Store {
	return &Store{
		statePath:  filepath.Join(stateDir, stateFileName),
		controlDir: controlDir,
		generation: generation,
		log:        log,
		now:        func() time.Time { return time.Now().UTC() },
		profiles:   map[string]*profileState{},
	}
}

// Load 读取持久检查点。文件不存在视为首次运行。
// 旧代次的会话高水位不会沿用：新代次的会话 ID 不同，重新从 0 开始。
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.statePath)
	if errors.Is(err, os.ErrNotExist) {
		s.pruneEventFilesLocked()
		if err := s.writeCheckpointLocked(); err != nil {
			s.log.Warn("write initial usage checkpoint failed", "error", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取用量账本: %w", err)
	}
	var state stateFile
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("解析用量账本: %w", err)
	}
	if state.SchemaVersion != stateSchema {
		return fmt.Errorf("用量账本版本不兼容：%d", state.SchemaVersion)
	}
	s.seq = state.Seq
	s.profiles = state.Profiles
	if s.profiles == nil {
		s.profiles = map[string]*profileState{}
	}
	for _, profile := range s.profiles {
		profile.Sessions = map[string]*sessionState{}
	}
	s.pruneEventFilesLocked()
	if err := s.writeCheckpointLocked(); err != nil {
		s.log.Warn("write usage checkpoint on load failed", "error", err)
	}
	return nil
}

// Save 原子写入本地检查点，并导出可被面板备份的副本。
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.saveLocked(); err != nil {
		return err
	}
	return s.writeCheckpointLocked()
}

// Import 用备份中的账本替换当前状态；会话高水位清空，VPN 应处于停止状态。
func (s *Store) Import(data []byte) error {
	var state stateFile
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("解析恢复账本: %w", err)
	}
	if state.SchemaVersion != stateSchema {
		return fmt.Errorf("恢复账本版本不兼容：%d", state.SchemaVersion)
	}
	if state.Profiles == nil {
		state.Profiles = map[string]*profileState{}
	}
	for _, profile := range state.Profiles {
		profile.Sessions = map[string]*sessionState{}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq = state.Seq
	s.profiles = state.Profiles
	// 恢复点之后的事件属于已回退的状态，必须清理；否则面板恢复旧游标后
	// 会把备份之后的用量再次补传，覆盖恢复结果。
	s.clearEventFilesLocked()
	if err := s.saveLocked(); err != nil {
		return err
	}
	return s.writeCheckpointLocked()
}

func (s *Store) clearEventFilesLocked() {
	entries, err := os.ReadDir(s.controlDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() ||
			!strings.HasPrefix(name, telemetry.EventFilePrefix) ||
			!strings.HasSuffix(name, telemetry.EventFileSuffix) {
			continue
		}
		if err := os.Remove(filepath.Join(s.controlDir, name)); err != nil {
			s.log.Warn("remove replaced usage event file failed", "file", name, "error", err)
		}
	}
}

// writeCheckpointLocked 把完整账本写到控制目录，供面板备份；不含凭据材料。
func (s *Store) writeCheckpointLocked() error {
	if err := os.MkdirAll(s.controlDir, 0o700); err != nil {
		return fmt.Errorf("创建控制目录: %w", err)
	}
	payload, err := json.MarshalIndent(stateFile{
		SchemaVersion: stateSchema,
		Seq:           s.seq,
		Profiles:      s.profiles,
		UpdatedAt:     s.now().Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("编码用量检查点: %w", err)
	}
	path := filepath.Join(s.controlDir, telemetry.CheckpointFile)
	tmp, err := os.CreateTemp(s.controlDir, ".checkpoint-*.tmp")
	if err != nil {
		return fmt.Errorf("创建用量检查点临时文件: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return fmt.Errorf("写入用量检查点: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("替换用量检查点: %w", err)
	}
	return os.Chmod(path, 0o644)
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.statePath), 0o700); err != nil {
		return fmt.Errorf("创建用量账本目录: %w", err)
	}
	payload, err := json.MarshalIndent(stateFile{
		SchemaVersion: stateSchema,
		Seq:           s.seq,
		Profiles:      s.profiles,
		UpdatedAt:     s.now().Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("编码用量账本: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.statePath), ".usage-*.tmp")
	if err != nil {
		return fmt.Errorf("创建用量账本临时文件: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return fmt.Errorf("写入用量账本: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, s.statePath); err != nil {
		return fmt.Errorf("替换用量账本: %w", err)
	}
	return os.Chmod(s.statePath, 0o600)
}

// Close 目前没有常驻文件句柄；保留方法供生命周期调用。
func (s *Store) Close() {}

// SyncEpochs 按认证快照应用手动重置。面板代次更大时，把当前累计设为基线。
func (s *Store) SyncEpochs(snapshot admission.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, profile := range snapshot.Profiles {
		state := s.profileLocked(profile.ID)
		if profile.QuotaEpoch > state.Epoch {
			state.Epoch = profile.QuotaEpoch
			state.Baseline = state.Total
			s.log.Info("quota epoch reset applied",
				"profile_id", profile.ID, "epoch", state.Epoch, "baseline", state.Baseline)
		}
	}
}

// Record 写入一次会话累计采样。
func (s *Store) Record(profileID, sessionID string, up, down uint64) RecordResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	profile := s.profileLocked(profileID)
	session, ok := profile.Sessions[sessionID]
	if !ok {
		session = &sessionState{}
		profile.Sessions[sessionID] = session
	}
	result := RecordResult{New: !ok}

	if up < session.Up || down < session.Down {
		// 计数回退通常来自会话重建；不回退高水位、不产生负流量，只记录断档。
		profile.Gaps++
		result.Rollback = true
	}
	if up > session.Up {
		result.DeltaUp = up - session.Up
		session.Up = up
	}
	if down > session.Down {
		result.DeltaDown = down - session.Down
		session.Down = down
	}
	profile.Total += result.DeltaUp + result.DeltaDown
	return result
}

// RemoveSession 在连接结束后移除会话高水位；已计入的累计保留。
func (s *Store) RemoveSession(profileID, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if profile, ok := s.profiles[profileID]; ok {
		delete(profile.Sessions, sessionID)
	}
}

// Usage 返回当前额度代次已用字节。
func (s *Store) Usage(profileID string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	profile := s.profileLocked(profileID)
	if profile.Total <= profile.Baseline {
		return 0
	}
	return profile.Total - profile.Baseline
}

// Exhausted 判断当前代次是否达到额度；quota 为 0 表示不限量。
func (s *Store) Exhausted(profileID string, quota uint64) bool {
	if quota == 0 {
		return false
	}
	return s.Usage(profileID) >= quota
}

// ProfileStats 返回全部配置的账本快照。
func (s *Store) ProfileStats() []telemetry.ProfileUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]telemetry.ProfileUsage, 0, len(s.profiles))
	for id, profile := range s.profiles {
		usage := uint64(0)
		if profile.Total > profile.Baseline {
			usage = profile.Total - profile.Baseline
		}
		out = append(out, telemetry.ProfileUsage{
			ProfileID:  id,
			QuotaEpoch: profile.Epoch,
			UsageBytes: usage,
			TotalBytes: profile.Total,
			Gaps:       profile.Gaps,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProfileID < out[j].ProfileID })
	return out
}

// AppendEvent 分配序号并追加事件；事件不含凭据材料。
func (s *Store) AppendEvent(event telemetry.Event) (telemetry.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.seq++
	event.Version = telemetry.Version
	event.Seq = s.seq
	event.Generation = s.generation
	if event.At.IsZero() {
		event.At = s.now()
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return event, fmt.Errorf("编码用量事件: %w", err)
	}
	payload = append(payload, '\n')

	file, err := s.openEventFileLocked(int64(len(payload)))
	if err != nil {
		return event, err
	}
	if _, err := file.Write(payload); err != nil {
		file.Close()
		return event, fmt.Errorf("写入用量事件: %w", err)
	}
	if err := file.Close(); err != nil {
		return event, fmt.Errorf("关闭用量事件日志: %w", err)
	}
	return event, nil
}

// CurrentSeq 返回已分配的最大事件序号。
func (s *Store) CurrentSeq() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seq
}

// Generation 返回本次运行代次。
func (s *Store) Generation() string {
	return s.generation
}

// openEventFileLocked 打开活动事件文件；超过大小上限时先轮转。
// 每次追加单独打开和关闭，避免 Windows 测试与容器重建时占用文件。
func (s *Store) openEventFileLocked(add int64) (*os.File, error) {
	if err := os.MkdirAll(s.controlDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建控制目录: %w", err)
	}
	activePath := filepath.Join(s.controlDir, activeEvents)
	size := int64(0)
	if info, err := os.Stat(activePath); err == nil {
		size = info.Size()
		// 上次进程在事件写到一半时退出：先隔离不完整末尾，避免下一条事件
		// 被拼进同一行而一起无法解析。
		if size > 0 && !endsWithNewline(activePath, size) {
			target := filepath.Join(s.controlDir,
				fmt.Sprintf("events-partial-%s-%d.jsonl", s.generation, s.seq))
			if err := os.Rename(activePath, target); err != nil {
				return nil, fmt.Errorf("隔离不完整用量事件: %w", err)
			}
			size = 0
		}
	}
	if size+add > maxEventBytes {
		// 轮转活动文件，面板会读取目录中的所有事件文件并按序号去重。
		target := filepath.Join(s.controlDir,
			fmt.Sprintf("events-%s-%d.jsonl", s.generation, s.seq))
		if err := os.Rename(activePath, target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("轮转用量事件日志: %w", err)
		}
	}
	file, err := os.OpenFile(activePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("打开用量事件日志: %w", err)
	}
	return file, nil
}

func endsWithNewline(path string, size int64) bool {
	file, err := os.Open(path)
	if err != nil {
		return true
	}
	defer file.Close()
	var last [1]byte
	if _, err := file.ReadAt(last[:], size-1); err != nil {
		return true
	}
	return last[0] == '\n'
}

func (s *Store) pruneEventFilesLocked() {
	entries, err := os.ReadDir(s.controlDir)
	if err != nil {
		return
	}
	cutoff := s.now().Add(-eventRetain)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name == activeEvents ||
			!strings.HasPrefix(name, telemetry.EventFilePrefix) ||
			!strings.HasSuffix(name, telemetry.EventFileSuffix) {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(s.controlDir, name)); err != nil {
			s.log.Warn("remove expired usage event file failed", "file", name, "error", err)
		}
	}
}

func (s *Store) profileLocked(profileID string) *profileState {
	profile, ok := s.profiles[profileID]
	if !ok {
		profile = &profileState{Sessions: map[string]*sessionState{}}
		s.profiles[profileID] = profile
	}
	if profile.Sessions == nil {
		profile.Sessions = map[string]*sessionState{}
	}
	return profile
}
