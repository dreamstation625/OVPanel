package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dreamstation625/ovpanel/internal/backup"
	"github.com/dreamstation625/ovpanel/internal/store"
)

type backupDTO struct {
	ID              string `json:"id"`
	Filename        string `json:"filename"`
	Kind            string `json:"kind"`
	Note            string `json:"note"`
	SizeBytes       int64  `json:"sizeBytes"`
	SHA256          string `json:"sha256"`
	AppVersion      string `json:"appVersion"`
	ConfigRevision  int64  `json:"configRevision"`
	AgentGeneration string `json:"agentGeneration,omitempty"`
	UsageSeq        uint64 `json:"usageSeq"`
	CreatedAt       string `json:"createdAt"`
}

func backupToDTO(record store.BackupRecord) backupDTO {
	return backupDTO{
		ID:              record.ID,
		Filename:        record.Filename,
		Kind:            record.Kind,
		Note:            record.Note,
		SizeBytes:       record.SizeBytes,
		SHA256:          record.SHA256,
		AppVersion:      record.AppVersion,
		ConfigRevision:  record.ConfigRevision,
		AgentGeneration: record.AgentGeneration,
		UsageSeq:        record.UsageSeq,
		CreatedAt:       record.CreatedAt.Format(time.RFC3339),
	}
}

func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	if s.backups == nil {
		writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "备份服务未配置")
		return
	}
	settings, err := s.backups.SettingsView(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	records, total, err := s.backups.List(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	out := make([]backupDTO, 0, len(records))
	for _, record := range records {
		out = append(out, backupToDTO(record))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings":       settings,
		"backups":        out,
		"storageBytes":   total,
		"maxPackageSize": int64(backup.MaxPackageBytes),
	})
}

func (s *Server) handleSaveBackupSettings(w http.ResponseWriter, r *http.Request) {
	if s.backups == nil {
		writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "备份服务未配置")
		return
	}
	var req struct {
		BackupPassword string `json:"backupPassword"`
		Enabled        bool   `json:"enabled"`
		ScheduleTime   string `json:"scheduleTime"`
		RetentionCount int    `json:"retentionCount"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	settings, err := s.backups.SaveSettings(r.Context(), backup.SettingsInput{
		Enabled:        req.Enabled,
		ScheduleTime:   req.ScheduleTime,
		RetentionCount: req.RetentionCount,
		BackupPassword: req.BackupPassword,
	})
	if err != nil {
		if errors.Is(err, backup.ErrNotConfigured) {
			writeError(w, r, http.StatusConflict, CodeConflict, "请先设置备份恢复密码")
			return
		}
		writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, err.Error())
		return
	}
	s.log.Info("backup settings saved", "enabled", settings.Enabled, "schedule", settings.ScheduleTime)
	writeJSON(w, http.StatusOK, map[string]any{"settings": settings})
}

func (s *Server) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	if s.backups == nil {
		writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "备份服务未配置")
		return
	}
	var req struct {
		Note string `json:"note"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	record, err := s.backups.Create(r.Context(), store.BackupManual, req.Note, "")
	if err != nil {
		if errors.Is(err, backup.ErrNotConfigured) {
			writeError(w, r, http.StatusConflict, CodeConflict, "请先设置备份恢复密码")
			return
		}
		if errors.Is(err, backup.ErrMaintenanceBusy) {
			writeError(w, r, http.StatusConflict, CodeConflict, "OpenVPN 更新任务进行中，暂不能创建备份")
			return
		}
		s.internalError(w, r, err)
		return
	}
	s.log.Info("manual backup created", "backup_id", record.ID, "size", record.SizeBytes)
	writeJSON(w, http.StatusCreated, map[string]any{"backup": backupToDTO(record)})
}

func (s *Server) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	if s.backups == nil {
		writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "备份服务未配置")
		return
	}
	record, ok, err := s.store.BackupRecord(r.Context(), r.PathValue("id"))
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !ok {
		writeError(w, r, http.StatusNotFound, CodeNotFound, "备份不存在")
		return
	}
	path := s.backups.PackagePath(record)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeError(w, r, http.StatusNotFound, CodeNotFound, "备份文件不存在")
			return
		}
		s.internalError(w, r, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filepath.Base(record.Filename)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, file); err != nil {
		s.log.Warn("write backup download failed", "backup_id", record.ID, "error", err)
	}
	s.log.Info("backup downloaded", "backup_id", record.ID)
}

func (s *Server) handleDeleteBackup(w http.ResponseWriter, r *http.Request) {
	if s.backups == nil {
		writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "备份服务未配置")
		return
	}
	id := r.PathValue("id")
	if err := s.backups.Delete(r.Context(), id); err != nil {
		s.writeAccountError(w, r, err)
		return
	}
	s.log.Info("backup deleted", "backup_id", id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRestorePreview(w http.ResponseWriter, r *http.Request) {
	if s.backups == nil {
		writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "备份服务未配置")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, backup.MaxPackageBytes+(4<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "恢复包必须使用 multipart 上传")
		return
	}
	uploadDir := filepath.Join(s.cfg.DataDir, "backups")
	if err := os.MkdirAll(uploadDir, 0o700); err != nil {
		s.internalError(w, r, err)
		return
	}
	upload, err := os.CreateTemp(uploadDir, ".upload-*")
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	uploadPath := upload.Name()
	defer os.Remove(uploadPath)
	if err := upload.Chmod(0o600); err != nil {
		upload.Close()
		s.internalError(w, r, err)
		return
	}

	var (
		backupPassword string
		fileReceived   bool
	)
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			upload.Close()
			writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "读取上传内容失败")
			return
		}
		switch part.FormName() {
		case "file":
			if fileReceived {
				upload.Close()
				writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "只能上传一个备份包")
				return
			}
			fileReceived = true
			if _, err := io.Copy(upload, io.LimitReader(part, backup.MaxPackageBytes+1)); err != nil {
				upload.Close()
				writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "读取备份包失败")
				return
			}
		case "backupPassword":
			backupPassword = readSmallPart(part)
		default:
			io.Copy(io.Discard, io.LimitReader(part, 4096))
		}
		part.Close()
	}
	if err := upload.Close(); err != nil {
		s.internalError(w, r, err)
		return
	}
	if !fileReceived {
		writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "缺少备份包文件")
		return
	}
	if info, err := os.Stat(uploadPath); err == nil && info.Size() > backup.MaxPackageBytes {
		writeError(w, r, http.StatusRequestEntityTooLarge, CodeInvalidRequest, "备份包过大")
		return
	}
	file, err := os.Open(uploadPath)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	defer file.Close()
	preview, err := s.backups.PreviewRestore(r.Context(), file, backupPassword)
	if err != nil {
		if errors.Is(err, backup.ErrInvalidPackage) {
			writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "备份包无效或恢复密码错误")
			return
		}
		writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, err.Error())
		return
	}
	s.log.Warn("backup restore previewed", "token", preview.Token[:8], "created_at", preview.Manifest.CreatedAt)
	writeJSON(w, http.StatusOK, preview)
}

func (s *Server) handleRestoreExecute(w http.ResponseWriter, r *http.Request) {
	if s.backups == nil {
		writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "备份服务未配置")
		return
	}
	var req struct {
		Token string `json:"token"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	result, err := s.backups.ExecuteRestore(r.Context(), strings.TrimSpace(req.Token))
	if err != nil {
		if errors.Is(err, backup.ErrMaintenanceBusy) {
			writeError(w, r, http.StatusConflict, CodeConflict, "OpenVPN 更新任务进行中，暂不能恢复")
			return
		}
		if errors.Is(err, backup.ErrRestoreToken) {
			writeError(w, r, http.StatusConflict, CodeConflict, "恢复令牌无效或已过期，请重新上传备份包")
			return
		}
		s.log.Error("backup restore failed", "error", err)
		writeError(w, r, http.StatusInternalServerError, CodeInternal, "恢复失败："+err.Error())
		return
	}
	s.log.Warn("backup restore completed", "pre_restore_backup", result.PreRestoreBackupID)
	writeJSON(w, http.StatusOK, result)
}

func readSmallPart(part io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(part, 1024))
	return strings.TrimSpace(string(data))
}
