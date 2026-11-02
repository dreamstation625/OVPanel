package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/dreamstation625/ovpanel/internal/auth"
)

func setupAndLogin(t *testing.T, srv *Server) (http.Handler, []*http.Cookie) {
	t.Helper()
	h := srv.Handler()
	rec := doJSON(t, h, http.MethodPost, "/api/setup", "127.0.0.1:5000",
		map[string]string{"username": "admin", "password": "a-long-enough-password"})
	if rec.Code != http.StatusOK {
		t.Fatalf("初始化失败：%d %s", rec.Code, rec.Body.String())
	}
	return h, authCookiesFrom(t, rec)
}

func TestP03UserProfileCertificateFlow(t *testing.T) {
	srv := newTestServer(t)
	h, cookies := setupAndLogin(t, srv)

	// 创建用户与首份仅证书配置。
	create := doJSON(t, h, http.MethodPost, "/api/users", "127.0.0.1:5000", map[string]any{
		"displayName": "张三",
		"notes":       "家庭用户",
		"tags":        []string{"家庭"},
		"profile": map[string]any{
			"name":     "电脑",
			"authType": "certificate",
		},
	}, cookies...)
	if create.Code != http.StatusCreated {
		t.Fatalf("创建用户失败：%d %s", create.Code, create.Body.String())
	}
	var created struct {
		User     userDTO    `json:"user"`
		Profile  profileDTO `json:"profile"`
		Password string     `json:"password"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("解析创建结果失败：%v", err)
	}
	if created.Password != "" {
		t.Fatal("仅证书配置不应返回密码")
	}
	if created.Profile.Certificate == nil || created.Profile.Certificate.Fingerprint == "" {
		t.Fatal("未返回证书信息")
	}
	userID := created.User.ID
	certProfileID := created.Profile.ID
	fingerprint := created.Profile.Certificate.Fingerprint

	// 列表与详情。
	list := doJSON(t, h, http.MethodGet, "/api/users", "127.0.0.1:5000", nil, cookies...)
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "张三") {
		t.Fatalf("用户列表异常：%d %s", list.Code, list.Body.String())
	}
	detail := doJSON(t, h, http.MethodGet, "/api/users/"+userID, "127.0.0.1:5000", nil, cookies...)
	if detail.Code != http.StatusOK {
		t.Fatalf("用户详情失败：%d", detail.Code)
	}

	// 新增证书+密码配置，密码只返回一次。
	add := doJSON(t, h, http.MethodPost, "/api/users/"+userID+"/profiles", "127.0.0.1:5000", map[string]any{
		"name":     "手机",
		"authType": "certificate_password",
		"username": "bob",
	}, cookies...)
	if add.Code != http.StatusCreated {
		t.Fatalf("新增配置失败：%d %s", add.Code, add.Body.String())
	}
	var added struct {
		Profile  profileDTO `json:"profile"`
		Password string     `json:"password"`
	}
	if err := json.Unmarshal(add.Body.Bytes(), &added); err != nil {
		t.Fatalf("解析新增配置失败：%v", err)
	}
	if added.Password == "" || added.Profile.Certificate == nil {
		t.Fatalf("证书+密码配置结果异常：%+v", added)
	}
	hash, has, found, err := srv.store.ProfilePasswordHash(t.Context(), added.Profile.ID)
	if err != nil || !found || !has {
		t.Fatalf("读取密码哈希失败：%v", err)
	}
	if ok, _ := auth.VerifyPassword(hash, added.Password); !ok {
		t.Fatal("返回的一次性密码无法通过校验")
	}

	// 未配置连接地址时导出应失败。
	export := doJSON(t, h, http.MethodPost, "/api/profiles/"+certProfileID+"/export", "127.0.0.1:5000",
		map[string]string{}, cookies...)
	if export.Code != http.StatusConflict || decodeError(t, export).Code != CodeExportNotConfigured {
		t.Fatalf("未配置地址应拒绝导出：%d %s", export.Code, export.Body.String())
	}

	// 保存导出设置。
	save := doJSON(t, h, http.MethodPut, "/api/settings/export", "127.0.0.1:5000", map[string]any{
		"host": "vpn.example.com", "port": 1194, "proto": "udp",
	}, cookies...)
	if save.Code != http.StatusOK {
		t.Fatalf("保存导出设置失败：%d %s", save.Code, save.Body.String())
	}

	// 导出 .ovpn，必须包含证书材料且禁止缓存。
	export = doJSON(t, h, http.MethodPost, "/api/profiles/"+certProfileID+"/export", "127.0.0.1:5000",
		map[string]string{}, cookies...)
	if export.Code != http.StatusOK {
		t.Fatalf("导出失败：%d %s", export.Code, export.Body.String())
	}
	body := export.Body.String()
	for _, want := range []string{"<ca>", "<cert>", "<key>", "<tls-crypt>", "remote vpn.example.com 1194"} {
		if !strings.Contains(body, want) {
			t.Fatalf("导出内容缺少 %q", want)
		}
	}
	if got := export.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("导出 Cache-Control = %q", got)
	}
	if disposition := export.Header().Get("Content-Disposition"); !strings.Contains(disposition, "filename*=UTF-8''") {
		t.Fatalf("导出文件名缺少 UTF-8 编码：%q", disposition)
	}

	// 下发运行控制包（面板侧；代理是否确认由运行代理记录）。
	apply := doJSON(t, h, http.MethodPost, "/api/runtime/apply", "127.0.0.1:5000",
		map[string]string{"adminPassword": "a-long-enough-password"}, cookies...)
	if apply.Code != http.StatusOK {
		t.Fatalf("下发运行控制包失败：%d %s", apply.Code, apply.Body.String())
	}
	if !strings.Contains(apply.Body.String(), `"agentPending":true`) {
		t.Fatalf("下发响应缺少 agentPending：%s", apply.Body.String())
	}

	// 轮换证书后指纹变化。
	rotate := doJSON(t, h, http.MethodPost, "/api/profiles/"+certProfileID+"/certificate/rotate", "127.0.0.1:5000",
		map[string]string{}, cookies...)
	if rotate.Code != http.StatusOK {
		t.Fatalf("轮换失败：%d %s", rotate.Code, rotate.Body.String())
	}
	var rotated struct {
		Profile profileDTO `json:"profile"`
	}
	if err := json.Unmarshal(rotate.Body.Bytes(), &rotated); err != nil {
		t.Fatalf("解析轮换结果失败：%v", err)
	}
	if rotated.Profile.Certificate == nil || rotated.Profile.Certificate.Fingerprint == fingerprint {
		t.Fatal("轮换后证书指纹未变化")
	}

	// 吊销后配置无生效证书。
	revoke := doJSON(t, h, http.MethodPost, "/api/profiles/"+certProfileID+"/certificate/revoke", "127.0.0.1:5000",
		map[string]string{}, cookies...)
	if revoke.Code != http.StatusOK {
		t.Fatalf("吊销失败：%d %s", revoke.Code, revoke.Body.String())
	}
	var revoked struct {
		Profile profileDTO `json:"profile"`
	}
	if err := json.Unmarshal(revoke.Body.Bytes(), &revoked); err != nil {
		t.Fatalf("解析吊销结果失败：%v", err)
	}
	if revoked.Profile.Certificate != nil || revoked.Profile.EffectiveStatus != "certificate_missing" {
		t.Fatalf("吊销后状态异常：%+v", revoked.Profile)
	}

	// 重置密码并校验新密码。
	reset := doJSON(t, h, http.MethodPost, "/api/profiles/"+added.Profile.ID+"/password", "127.0.0.1:5000",
		map[string]string{}, cookies...)
	if reset.Code != http.StatusOK {
		t.Fatalf("重置密码失败：%d %s", reset.Code, reset.Body.String())
	}
	var resetBody struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal(reset.Body.Bytes(), &resetBody); err != nil {
		t.Fatalf("解析重置结果失败：%v", err)
	}
	hash, _, _, _ = srv.store.ProfilePasswordHash(t.Context(), added.Profile.ID)
	if resetBody.Password == "" {
		t.Fatal("重置后应返回一次性密码")
	}
	if ok, _ := auth.VerifyPassword(hash, resetBody.Password); !ok {
		t.Fatal("重置后的密码无法通过校验")
	}

	// 停用用户后所有配置的有效状态变化。
	disable := doJSON(t, h, http.MethodPatch, "/api/users/"+userID, "127.0.0.1:5000",
		map[string]string{"status": "disabled"}, cookies...)
	if disable.Code != http.StatusOK {
		t.Fatalf("停用用户失败：%d %s", disable.Code, disable.Body.String())
	}
	var disabled userDetailDTO
	if err := json.Unmarshal(disable.Body.Bytes(), &disabled); err != nil {
		t.Fatalf("解析停用结果失败：%v", err)
	}
	for _, p := range disabled.Profiles {
		if p.EffectiveStatus != "user_disabled" {
			t.Fatalf("用户停用后配置状态 = %q", p.EffectiveStatus)
		}
	}

	// PKI 状态可读取。
	pkiStatus := doJSON(t, h, http.MethodGet, "/api/pki", "127.0.0.1:5000", nil, cookies...)
	if pkiStatus.Code != http.StatusOK || !strings.Contains(pkiStatus.Body.String(), `"caExists":true`) {
		t.Fatalf("PKI 状态异常：%d %s", pkiStatus.Code, pkiStatus.Body.String())
	}
}

func TestP03CSRFAndAdminPassword(t *testing.T) {
	srv := newTestServer(t)
	h, cookies := setupAndLogin(t, srv)

	// 只有会话 Cookie、没有 CSRF 头的写请求应被拒绝。
	var session *http.Cookie
	for _, c := range cookies {
		if c.Name == sessionCookie {
			session = c
		}
	}
	if session == nil {
		t.Fatal("缺少会话 Cookie")
	}
	rec := doJSON(t, h, http.MethodPost, "/api/users", "127.0.0.1:5000",
		map[string]any{"displayName": "无 CSRF"}, session)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("缺少 CSRF 应 403，实际 %d", rec.Code)
	}

	// 修改管理员密码后旧会话立即失效。
	change := doJSON(t, h, http.MethodPost, "/api/admin/password", "127.0.0.1:5000", map[string]string{
		"currentPassword": "a-long-enough-password",
		"newPassword":     "new-long-enough-password",
	}, cookies...)
	if change.Code != http.StatusOK {
		t.Fatalf("修改密码失败：%d %s", change.Code, change.Body.String())
	}
	if got := doJSON(t, h, http.MethodGet, "/api/users", "127.0.0.1:5000", nil, cookies...).Code; got != http.StatusUnauthorized {
		t.Fatalf("旧会话应失效，实际 %d", got)
	}

	login := doJSON(t, h, http.MethodPost, "/api/login", "127.0.0.1:5000", map[string]string{
		"username": "admin",
		"password": "new-long-enough-password",
	})
	if login.Code != http.StatusOK {
		t.Fatalf("新密码登录失败：%d %s", login.Code, login.Body.String())
	}
	authCookiesFrom(t, login)
}
