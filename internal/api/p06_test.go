package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func doMultipart(t *testing.T, h http.Handler, path string, cookies []*http.Cookie,
	fields map[string]string, fileField, fileName string, fileData []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile(fileField, fileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(fileData); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, path, &body)
	req.Host = "127.0.0.1"
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("Content-Type", writer.FormDataContentType())
	for _, cookie := range cookies {
		req.AddCookie(cookie)
		if cookie.Name == csrfCookie {
			req.Header.Set(csrfHeader, cookie.Value)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestP06BackupAndRestoreFlow(t *testing.T) {
	srv := newTestServer(t)
	h, cookies := setupAndLogin(t, srv)
	const adminPassword = "a-long-enough-password"
	const backupPassword = "correct-horse-backup-password"

	// 备份设置。
	settings := doJSON(t, h, http.MethodPut, "/api/backups/settings", "127.0.0.1:5000", map[string]any{
		"adminPassword":  adminPassword,
		"backupPassword": backupPassword,
		"enabled":        false,
		"scheduleTime":   "03:00",
		"retentionCount": 2,
	}, cookies...)
	if settings.Code != http.StatusOK || !strings.Contains(settings.Body.String(), `"configured":true`) {
		t.Fatalf("保存备份设置失败：%d %s", settings.Code, settings.Body.String())
	}

	// 创建原始数据。
	create := doJSON(t, h, http.MethodPost, "/api/users", "127.0.0.1:5000", map[string]any{
		"displayName": "备份用户",
		"profile":     map[string]any{"name": "原始配置", "authType": "certificate"},
	}, cookies...)
	if create.Code != http.StatusCreated {
		t.Fatalf("创建用户失败：%d %s", create.Code, create.Body.String())
	}

	// 手动备份。
	backupResponse := doJSON(t, h, http.MethodPost, "/api/backups", "127.0.0.1:5000", map[string]any{
		"adminPassword": adminPassword,
		"note":          "P06 测试备份",
	}, cookies...)
	if backupResponse.Code != http.StatusCreated {
		t.Fatalf("创建备份失败：%d %s", backupResponse.Code, backupResponse.Body.String())
	}
	var created struct {
		Backup backupDTO `json:"backup"`
	}
	if err := json.Unmarshal(backupResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Backup.ID == "" || created.Backup.SizeBytes == 0 {
		t.Fatalf("备份元数据异常：%+v", created.Backup)
	}
	// 下载需要管理员密码，返回 age 加密包。
	wrong := doJSON(t, h, http.MethodPost, "/api/backups/"+created.Backup.ID+"/download", "127.0.0.1:5000",
		map[string]string{"adminPassword": "wrong-password"}, cookies...)
	if wrong.Code != http.StatusForbidden {
		t.Fatalf("错误管理员密码应被拒绝：%d", wrong.Code)
	}
	download := doJSON(t, h, http.MethodPost, "/api/backups/"+created.Backup.ID+"/download", "127.0.0.1:5000",
		map[string]string{"adminPassword": adminPassword}, cookies...)
	if download.Code != http.StatusOK {
		t.Fatalf("下载备份失败：%d %s", download.Code, download.Body.String())
	}
	packageBytes := download.Body.Bytes()
	if !bytes.HasPrefix(packageBytes, []byte("age-encryption.org/v1")) {
		t.Fatalf("备份包不是 age 格式：%q", packageBytes[:min(32, len(packageBytes))])
	}

	// 备份之后新增用户，恢复后应消失。
	extra := doJSON(t, h, http.MethodPost, "/api/users", "127.0.0.1:5000", map[string]any{
		"displayName": "备份后新增",
	}, cookies...)
	if extra.Code != http.StatusCreated {
		t.Fatalf("新增用户失败：%d", extra.Code)
	}

	// 错误恢复密码不能预览。
	badPreview := doMultipart(t, h, "/api/backups/restore/preview", cookies, map[string]string{
		"adminPassword":  adminPassword,
		"backupPassword": "not-the-right-password",
	}, "file", created.Backup.Filename, packageBytes)
	if badPreview.Code != http.StatusBadRequest {
		t.Fatalf("错误恢复密码应被拒绝：%d %s", badPreview.Code, badPreview.Body.String())
	}

	preview := doMultipart(t, h, "/api/backups/restore/preview", cookies, map[string]string{
		"adminPassword":  adminPassword,
		"backupPassword": backupPassword,
	}, "file", created.Backup.Filename, packageBytes)
	if preview.Code != http.StatusOK {
		t.Fatalf("恢复预览失败：%d %s", preview.Code, preview.Body.String())
	}
	var previewBody struct {
		Token    string   `json:"token"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &previewBody); err != nil {
		t.Fatal(err)
	}
	if previewBody.Token == "" || len(previewBody.Warnings) == 0 {
		t.Fatalf("恢复预览内容异常：%s", preview.Body.String())
	}

	execute := doJSON(t, h, http.MethodPost, "/api/backups/restore/execute", "127.0.0.1:5000", map[string]any{
		"token":         previewBody.Token,
		"adminPassword": adminPassword,
	}, cookies...)
	if execute.Code != http.StatusOK {
		t.Fatalf("恢复失败：%d %s", execute.Code, execute.Body.String())
	}
	var result struct {
		PreRestoreBackupID string `json:"preRestoreBackupId"`
		ReloginRequired    bool   `json:"reloginRequired"`
	}
	if err := json.Unmarshal(execute.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.PreRestoreBackupID == "" || !result.ReloginRequired {
		t.Fatalf("恢复结果异常：%s", execute.Body.String())
	}

	// 旧会话失效，需要重新登录。
	if me := doJSON(t, h, http.MethodGet, "/api/me", "127.0.0.1:5000", nil, cookies...); me.Code != http.StatusUnauthorized {
		t.Fatalf("恢复后旧会话应失效：%d %s", me.Code, me.Body.String())
	}
	login := doJSON(t, h, http.MethodPost, "/api/login", "127.0.0.1:5000",
		map[string]string{"username": "admin", "password": adminPassword})
	if login.Code != http.StatusOK {
		t.Fatalf("恢复后登录失败：%d %s", login.Code, login.Body.String())
	}
	newCookies := authCookiesFrom(t, login)

	users := doJSON(t, h, http.MethodGet, "/api/users", "127.0.0.1:5000", nil, newCookies...)
	if users.Code != http.StatusOK {
		t.Fatalf("读取用户失败：%d", users.Code)
	}
	if !strings.Contains(users.Body.String(), "备份用户") {
		t.Fatalf("恢复后缺少备份数据：%s", users.Body.String())
	}
	if strings.Contains(users.Body.String(), "备份后新增") {
		t.Fatalf("恢复后仍存在备份之后的用户：%s", users.Body.String())
	}

	// 恢复前备份在恢复后的数据库中仍可见。
	list := doJSON(t, h, http.MethodGet, "/api/backups", "127.0.0.1:5000", nil, newCookies...)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), result.PreRestoreBackupID) {
		t.Fatalf("恢复前备份未保留：%d %s", list.Code, list.Body.String())
	}
}
