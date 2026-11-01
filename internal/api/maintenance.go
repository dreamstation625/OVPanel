package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/dreamstation625/ovpanel/internal/backup"
	"github.com/dreamstation625/ovpanel/internal/maintapi"
	"github.com/dreamstation625/ovpanel/internal/store"
)

type openVPNMaintenanceDTO struct {
	Available         bool                `json:"available"`
	Reason            string              `json:"reason,omitempty"`
	AllowedRepository string              `json:"allowedRepository,omitempty"`
	Container         *maintapi.Container `json:"container,omitempty"`
	Task              *maintapi.Task      `json:"task,omitempty"`
	PinImage          string              `json:"pinImage,omitempty"`
	PanelVersion      string              `json:"panelVersion"`
	PanelManualSteps  []string            `json:"panelManualSteps"`
	ImageSwapWarning  string              `json:"imageSwapWarning"`
}

func (s *Server) handleOpenVPNMaintenance(w http.ResponseWriter, r *http.Request) {
	response := openVPNMaintenanceDTO{
		PanelVersion: s.build.Version,
		PanelManualSteps: []string{
			"备份当前数据并确认恢复密码可用。",
			"拉取目标面板镜像并固定版本。",
			"仅重建 panel 容器：./deploy/compose.sh up -d --no-deps panel。",
			"检查面板健康、登录和数据；OpenVPN 容器不应被联动重启。",
		},
		ImageSwapWarning: "OpenVPN 更新会短暂中断全部 VPN 连接；任何时刻只保留两个项目容器。",
	}
	if s.maintenance == nil {
		response.Reason = "维护进程未配置"
		writeJSON(w, http.StatusOK, response)
		return
	}
	state, err := s.maintenance.State(r.Context())
	if err != nil {
		response.Reason = "维护进程不可用：" + err.Error()
		writeJSON(w, http.StatusOK, response)
		return
	}
	response.Available = state.Available
	response.Reason = state.Reason
	response.AllowedRepository = state.AllowedRepository
	response.Container = state.Container
	response.Task = state.Task
	response.PinImage = state.PinImage
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleOpenVPNUpdate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AdminPassword string `json:"adminPassword"`
		TargetTag     string `json:"targetTag"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if _, ok := s.verifyAdminPassword(w, r, req.AdminPassword); !ok {
		return
	}
	if s.maintenance == nil {
		writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "维护进程未配置")
		return
	}
	if s.backups == nil {
		writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "备份服务未配置")
		return
	}
	if s.backups.Mutating() {
		writeError(w, r, http.StatusConflict, CodeConflict, "备份或恢复正在进行，暂不能更新 OpenVPN")
		return
	}
	state, err := s.maintenance.State(r.Context())
	if err != nil || !state.Available {
		reason := "维护进程不可用"
		if err != nil {
			reason += "：" + err.Error()
		} else if state.Reason != "" {
			reason += "：" + state.Reason
		}
		writeError(w, r, http.StatusServiceUnavailable, CodeInternal, reason)
		return
	}
	if state.Task != nil && (state.Task.State == maintapi.TaskPending || state.Task.State == maintapi.TaskRunning) {
		writeError(w, r, http.StatusConflict, CodeConflict, "已有 OpenVPN 更新任务在进行")
		return
	}
	settings, err := s.backups.SettingsView(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !settings.Configured {
		writeError(w, r, http.StatusConflict, CodeConflict, "请先在备份页设置恢复密码，升级前必须创建可用备份")
		return
	}
	tag := strings.TrimSpace(req.TargetTag)
	if tag == "" {
		writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, "请填写目标版本标签")
		return
	}

	backupRecord, err := s.backups.Create(r.Context(), store.BackupPreRestore, "OpenVPN 升级前备份", "")
	if err != nil {
		if errors.Is(err, backup.ErrNotConfigured) {
			writeError(w, r, http.StatusConflict, CodeConflict, "请先设置备份恢复密码")
			return
		}
		s.internalError(w, r, err)
		return
	}
	task, err := s.maintenance.StartUpdate(r.Context(), tag, backupRecord.ID)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, CodeInternal, "启动更新失败："+err.Error())
		return
	}
	s.log.Warn("openvpn update started",
		"task_id", task.ID, "target", task.TargetImage, "backup_id", backupRecord.ID)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"task":   task,
		"backup": backupToDTO(backupRecord),
	})
}
