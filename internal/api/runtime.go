package api

import (
	"errors"
	"net/http"

	"github.com/dreamstation625/ovpanel/internal/accounts"
	"github.com/dreamstation625/ovpanel/internal/vpnruntime"
)

func (s *Server) handleRuntimeSync(w http.ResponseWriter, r *http.Request) {
	if s.runtime == nil {
		writeJSON(w, http.StatusOK, map[string]any{"pending": false})
		return
	}
	pending, err := s.runtime.AccountsPending(r.Context())
	if err != nil {
		s.log.Warn("read account sync state failed", "error", err)
		pending = true
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": pending})
}

func (s *Server) handleRuntimeApply(w http.ResponseWriter, r *http.Request) {
	if s.runtime == nil {
		writeError(w, r, http.StatusServiceUnavailable, CodeInternal, "运行控制未配置")
		return
	}
	var req struct {
		AdminPassword string `json:"adminPassword"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if _, ok := s.verifyAdminPassword(w, r, req.AdminPassword); !ok {
		return
	}
	result, err := s.runtime.Apply(r.Context())
	if err != nil {
		if errors.Is(err, accounts.ErrExportNotConfigured) {
			s.writeAccountError(w, r, err)
			return
		}
		s.internalError(w, r, err)
		return
	}
	s.log.Info("runtime bundle written",
		"config_revision", result.ConfigRevision,
		"config_hash", result.ConfigHash,
		"users", result.Users,
		"profiles", result.Profiles,
		"agent_pending", result.AgentPending)
	writeJSON(w, http.StatusOK, runtimeResultMap(result))
}

func runtimeResultMap(result vpnruntime.Result) map[string]any {
	return map[string]any{
		"configRevision": result.ConfigRevision,
		"configHash":     result.ConfigHash,
		"users":          result.Users,
		"profiles":       result.Profiles,
		"agentPending":   result.AgentPending,
	}
}
