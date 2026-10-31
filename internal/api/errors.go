// Package api 提供面板的 HTTP 接口层。
//
// 错误响应统一为 {"error":{"code","message","requestId"}}：
// 界面按 code 分支处理，message 只用于展示，不参与逻辑判断。
// 内部错误细节只进日志，不出现在响应里。
package api

import (
	"encoding/json"
	"net/http"
)

// 错误码。界面据此做可处理提示，不解析 message 文本。
const (
	CodeInvalidRequest      = "invalid_request"
	CodeUnauthorized        = "unauthorized"
	CodeForbidden           = "forbidden"
	CodeNotFound            = "not_found"
	CodeConflict            = "conflict"
	CodeSetupClosed         = "setup_closed"
	CodeSetupForbidden      = "setup_forbidden"
	CodeSetupRequired       = "setup_required"
	CodeRateLimited         = "rate_limited"
	CodeReauthFailed        = "reauth_failed"
	CodeUsernameTaken       = "username_taken"
	CodeExportNotConfigured = "export_not_configured"
	CodeNoPasswordAuth      = "no_password_auth"
	CodeNoCertificateAuth   = "no_certificate_auth"
	CodeNoCertificate       = "no_certificate"
	CodeInternal            = "internal_error"
	CodeMethodNotAllowed    = "method_not_allowed"
)

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId,omitempty"`
}

// writeJSON 写出 JSON 响应。
//
// 编码失败时无法再改响应头，因此只记录到日志由调用方决定；
// 这里不返回错误，避免调用点写一堆无用分支。
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// writeError 写出统一错误体。
func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{
		Code:      code,
		Message:   message,
		RequestID: requestIDFrom(r),
	}})
}
