package api

import (
	"net/http"

	"github.com/dreamstation625/ovpanel/internal/updatecheck"
)

type updateErrorResponse struct {
	CurrentVersion string `json:"currentVersion"`
	CurrentCommit  string `json:"currentCommit"`
	URL            string `json:"url"`
	Error          string `json:"error"`
}

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if s.updateChecker == nil {
		writeJSON(w, http.StatusOK, updateErrorResponse{
			CurrentVersion: s.build.Version,
			CurrentCommit:  s.build.Commit,
			URL:            releasePageURL(),
			Error:          "更新检查未配置",
		})
		return
	}
	result, err := s.updateChecker.Check(r.Context(), s.build.Version, s.build.Commit, s.build.BuildTime)
	if err != nil {
		s.log.Warn("check release update failed", "error", err)
		writeJSON(w, http.StatusOK, updateErrorResponse{
			CurrentVersion: s.build.Version,
			CurrentCommit:  s.build.Commit,
			URL:            releasePageURL(),
			Error:          "暂时无法检查更新，请稍后重试或打开 Release 页面。",
		})
		return
	}
	if result.URL == "" {
		result.URL = releasePageURL()
	}
	writeJSON(w, http.StatusOK, result)
}

func releasePageURL() string {
	return "https://github.com/" + updatecheck.DefaultRepository + "/releases"
}
