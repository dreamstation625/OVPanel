package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/dreamstation625/ovpanel/internal/accounts"
	"github.com/dreamstation625/ovpanel/internal/serverconfig"
	"github.com/dreamstation625/ovpanel/internal/store"
)

type serverConfigRevisionDTO struct {
	Revision  int64               `json:"revision"`
	State     string              `json:"state"`
	Config    serverconfig.Config `json:"config"`
	Rendered  string              `json:"rendered"`
	Hash      string              `json:"hash"`
	CreatedAt string              `json:"createdAt"`
	AppliedAt string              `json:"appliedAt,omitempty"`
}

func (s *Server) handleServerConfig(w http.ResponseWriter, r *http.Request) {
	state, err := s.store.ServerConfigState(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	active, err := s.serverConfigDTO(r, state.Active)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	draft, err := s.serverConfigDTO(r, state.Draft)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	agent, hasAgent := s.runtime.AgentStatus()
	resp := map[string]any{
		"defaults":            serverconfig.Defaults(),
		"supportedDirectives": serverconfig.SupportedDirectives(),
		"active":              active,
		"draft":               draft,
		"agentStatus":         agent,
		"agentStatusPresent":  hasAgent,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) serverConfigDTO(r *http.Request, revision *store.ServerConfigRevision) (*serverConfigRevisionDTO, error) {
	if revision == nil {
		return nil, nil
	}
	var config serverconfig.Config
	if err := json.Unmarshal([]byte(revision.ConfigJSON), &config); err != nil {
		return nil, fmt.Errorf("解析服务端配置修订 %d: %w", revision.Revision, err)
	}
	prepared, err := serverconfig.Prepare(config)
	if err != nil {
		return nil, fmt.Errorf("校验服务端配置修订 %d: %w", revision.Revision, err)
	}
	rendered, hash, err := serverconfig.Render(prepared)
	if err != nil {
		return nil, err
	}
	dto := &serverConfigRevisionDTO{
		Revision:  revision.Revision,
		State:     revision.State,
		Config:    prepared,
		Rendered:  rendered,
		Hash:      hash,
		CreatedAt: revision.CreatedAt.Format(time.RFC3339),
	}
	if revision.AppliedAt != nil {
		dto.AppliedAt = revision.AppliedAt.Format(time.RFC3339)
	}
	return dto, nil
}

func (s *Server) handleServerConfigPreview(w http.ResponseWriter, r *http.Request) {
	var config serverconfig.Config
	if !s.decodeJSON(w, r, &config) {
		return
	}
	prepared, rendered, hash, err := prepareServerConfig(config)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"config":   prepared,
		"rendered": rendered,
		"hash":     hash,
	})
}

func (s *Server) handleSaveServerDraft(w http.ResponseWriter, r *http.Request) {
	var config serverconfig.Config
	if !s.decodeJSON(w, r, &config) {
		return
	}
	prepared, _, hash, err := prepareServerConfig(config)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, err.Error())
		return
	}
	payload, err := json.Marshal(prepared)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	revision, err := s.store.SaveServerConfigDraft(r.Context(), string(payload), hash, s.now())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.log.Info("server config draft saved", "revision", revision.Revision, "hash", hash)
	dto, err := s.serverConfigDTO(r, &revision)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"draft": dto})
}

func (s *Server) handleApplyServerConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AdminPassword string `json:"adminPassword"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if _, ok := s.verifyAdminPassword(w, r, req.AdminPassword); !ok {
		return
	}

	state, err := s.store.ServerConfigState(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	var result map[string]any
	switch {
	case state.Draft != nil:
		var config serverconfig.Config
		if err := json.Unmarshal([]byte(state.Draft.ConfigJSON), &config); err != nil {
			s.internalError(w, r, err)
			return
		}
		applied, err := s.runtime.ApplyConfig(r.Context(), config, state.Draft.Revision)
		if err != nil {
			s.writeRuntimeApplyError(w, r, err)
			return
		}
		if _, err := s.store.ActivateServerConfig(r.Context(), state.Draft.Revision, s.now()); err != nil {
			// 控制包已经写成新配置；激活失败时把控制包恢复到仍生效的旧修订，
			// 避免面板显示旧版本而代理运行新配置。
			if _, restoreErr := s.runtime.Apply(r.Context()); restoreErr != nil {
				s.log.Error("restore control bundle after activation failure failed", "error", restoreErr)
			}
			s.internalError(w, r, err)
			return
		}
		result = runtimeResultMap(applied)
	case state.Active != nil:
		applied, err := s.runtime.Apply(r.Context())
		if err != nil {
			s.writeRuntimeApplyError(w, r, err)
			return
		}
		result = runtimeResultMap(applied)
	default:
		writeError(w, r, http.StatusConflict, CodeConflict, "请先保存服务端配置草稿")
		return
	}

	agent, _ := s.runtime.AgentStatus()
	s.log.Info("server config applied to control bundle")
	writeJSON(w, http.StatusOK, map[string]any{
		"result":      result,
		"agentStatus": agent,
	})
}

func (s *Server) handleServerCommand(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AdminPassword string `json:"adminPassword"`
		Action        string `json:"action"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if _, ok := s.verifyAdminPassword(w, r, req.AdminPassword); !ok {
		return
	}
	// 代理没有控制包时不会启动 OpenVPN，却仍按“已启动”记录日志；
	// 这里先拦住，避免页面下发一条不会生效的命令。
	if (req.Action == "start" || req.Action == "restart") && !s.runtime.BundlePresent() {
		writeError(w, r, http.StatusConflict, CodeConflict, "控制包尚未下发，请先保存并应用服务端配置")
		return
	}
	if err := s.runtime.Command(req.Action); err != nil {
		writeError(w, r, http.StatusBadRequest, CodeInvalidRequest, err.Error())
		return
	}
	s.log.Info("server command issued", "action", req.Action)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "action": req.Action})
}

func (s *Server) writeRuntimeApplyError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, accounts.ErrExportNotConfigured) {
		s.writeAccountError(w, r, err)
		return
	}
	s.internalError(w, r, err)
}

func prepareServerConfig(input serverconfig.Config) (serverconfig.Config, string, string, error) {
	prepared, err := serverconfig.Prepare(input)
	if err != nil {
		return serverconfig.Config{}, "", "", err
	}
	rendered, hash, err := serverconfig.Render(prepared)
	if err != nil {
		return serverconfig.Config{}, "", "", err
	}
	return prepared, rendered, hash, nil
}
