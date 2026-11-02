// Package statistics 把运行代理的用量快照与事件日志同步到面板数据库。
//
// 面板不是用量事实来源：实时数据来自 usage.json，历史与日汇总来自
// 代理追加的事件日志。同步按「运行代次 + 序号」去重，面板离线期间
// 代理继续记录，恢复后补传不会重复累计。
package statistics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dreamstation625/ovpanel/internal/store"
	"github.com/dreamstation625/ovpanel/internal/telemetry"
)

// Service 同步运行代理统计并读取实时快照。
type Service struct {
	store      *store.Store
	controlDir string
	log        *slog.Logger
	now        func() time.Time

	mu      sync.Mutex
	offsets map[string]int64
}

// SyncResult 描述一次同步。
type SyncResult struct {
	Events    int
	Closed    int
	Snapshot  telemetry.Snapshot
	Present   bool
	Fresh     bool
	SyncedAt  time.Time
	ParseSkip int
}

// New 创建统计同步服务。
func New(st *store.Store, controlDir string, log *slog.Logger) *Service {
	return &Service{
		store:      st,
		controlDir: controlDir,
		log:        log,
		now:        func() time.Time { return time.Now().UTC() },
		offsets:    map[string]int64{},
	}
}

// Live 读取代理最近写出的实时快照；不存在时 present=false。
func (s *Service) Live() (telemetry.Snapshot, bool) {
	return readSnapshot(s.controlDir)
}

// Sync 读取新事件写入数据库，并按实时快照关闭已消失的连接。
func (s *Service) Sync(ctx context.Context) (SyncResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := SyncResult{SyncedAt: s.now()}
	snapshot, present := readSnapshot(s.controlDir)
	result.Snapshot = snapshot
	result.Present = present
	result.Fresh = present && result.SyncedAt.Sub(snapshot.UpdatedAt) < 10*time.Second

	cursors, err := s.store.UsageCursor(ctx)
	if err != nil {
		return result, err
	}
	events, skipped := s.collectEvents(cursors)
	result.ParseSkip = skipped
	if len(events) > 0 {
		applied, err := s.store.ApplyUsageEvents(ctx, events)
		if err != nil {
			return result, err
		}
		result.Events = applied
	}

	if present && result.Fresh {
		liveIDs := make(map[string]struct{}, len(snapshot.Sessions))
		for _, session := range snapshot.Sessions {
			liveIDs[session.ID] = struct{}{}
		}
		closed, err := s.store.CloseStaleSessions(ctx, snapshot.Generation, liveIDs, s.now())
		if err != nil {
			return result, err
		}
		result.Closed = closed
	}
	return result, nil
}

// collectEvents 读取事件文件中尚未同步的记录。
//
// 文件内偏移只在本次进程内缓存，重启后会重读并按数据库游标去重。
// 只处理以换行结尾的完整记录，写到一半的最后一行留待下次同步。
func (s *Service) collectEvents(cursors map[string]uint64) ([]telemetry.Event, int) {
	entries, err := os.ReadDir(s.controlDir)
	if err != nil {
		return nil, 0
	}
	type fileInfo struct {
		path    string
		modTime time.Time
	}
	files := make([]fileInfo, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() ||
			!strings.HasPrefix(name, telemetry.EventFilePrefix) ||
			!strings.HasSuffix(name, telemetry.EventFileSuffix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, fileInfo{path: filepath.Join(s.controlDir, name), modTime: info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.Before(files[j].modTime) })

	events := make([]telemetry.Event, 0, 128)
	skipped := 0
	for _, file := range files {
		offset := s.offsets[file.path]
		data, next, err := readFrom(file.path, offset)
		if err != nil {
			s.log.Warn("read usage event file failed", "file", file.path, "error", err)
			continue
		}
		s.offsets[file.path] = next
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			var event telemetry.Event
			if err := json.Unmarshal(line, &event); err != nil {
				skipped++
				continue
			}
			if event.Version != telemetry.Version || event.Generation == "" || event.Seq == 0 {
				skipped++
				continue
			}
			if event.Seq <= cursors[event.Generation] {
				continue
			}
			events = append(events, event)
		}
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].At.Equal(events[j].At) {
			if events[i].Generation == events[j].Generation {
				return events[i].Seq < events[j].Seq
			}
			return events[i].Generation < events[j].Generation
		}
		return events[i].At.Before(events[j].At)
	})
	return events, skipped
}

func readFrom(path string, offset int64) ([]byte, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, offset, err
	}
	if info.Size() < offset {
		// 文件被替换或截断，从头读取；数据库游标仍会去重。
		offset = 0
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, offset, err
	}
	complete := bytes.LastIndexByte(data, '\n')
	if complete < 0 {
		return nil, offset, nil
	}
	return data[:complete+1], offset + int64(complete+1), nil
}

func readSnapshot(controlDir string) (telemetry.Snapshot, bool) {
	data, err := os.ReadFile(filepath.Join(controlDir, telemetry.SnapshotFile))
	if err != nil {
		return telemetry.Snapshot{}, false
	}
	var snapshot telemetry.Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return telemetry.Snapshot{}, false
	}
	if snapshot.Version != telemetry.Version || snapshot.Generation == "" {
		return telemetry.Snapshot{}, false
	}
	return snapshot, true
}

// String 便于日志输出同步摘要。
func (r SyncResult) String() string {
	return fmt.Sprintf("events=%d closed=%d present=%v fresh=%v", r.Events, r.Closed, r.Present, r.Fresh)
}
