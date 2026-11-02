package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dreamstation625/ovpanel/internal/vpnruntime"
)

func TestServerCommandRequiresControlBundle(t *testing.T) {
	srv := newTestServer(t)
	h, cookies := setupAndLogin(t, srv)

	command := func(action string) map[string]string {
		return map[string]string{"action": action}
	}

	start := doJSON(t, h, http.MethodPost, "/api/server/command", "127.0.0.1:5000", command("start"), cookies...)
	if start.Code != http.StatusConflict || !strings.Contains(start.Body.String(), "配置尚未应用") {
		t.Fatalf("缺少控制包时应拒绝启动：%d %s", start.Code, start.Body.String())
	}

	stop := doJSON(t, h, http.MethodPost, "/api/server/command", "127.0.0.1:5000", command("stop"), cookies...)
	if stop.Code != http.StatusOK {
		t.Fatalf("停止命令不应受控制包限制：%d %s", stop.Code, stop.Body.String())
	}

	bundle := filepath.Join(srv.cfg.ControlDir, vpnruntime.BundleFileName)
	if err := os.WriteFile(bundle, []byte("{}"), 0o600); err != nil {
		t.Fatalf("写入控制包失败：%v", err)
	}
	start = doJSON(t, h, http.MethodPost, "/api/server/command", "127.0.0.1:5000", command("start"), cookies...)
	if start.Code != http.StatusOK {
		t.Fatalf("存在控制包时应允许启动：%d %s", start.Code, start.Body.String())
	}
}

func TestAdminPasswordMinimumEight(t *testing.T) {
	srv := newTestServer(t)
	h := srv.Handler()

	rec := doJSON(t, h, http.MethodPost, "/api/setup", "127.0.0.1:5000",
		map[string]string{"username": "admin", "password": "12345678"})
	if rec.Code != http.StatusOK {
		t.Fatalf("8 位管理员密码应被接受：%d %s", rec.Code, rec.Body.String())
	}
	cookies := authCookiesFrom(t, rec)

	change := doJSON(t, h, http.MethodPost, "/api/admin/password", "127.0.0.1:5000", map[string]string{
		"currentPassword": "12345678",
		"newPassword":     "abcdefgh",
	}, cookies...)
	if change.Code != http.StatusOK {
		t.Fatalf("8 位新密码应被接受：%d %s", change.Code, change.Body.String())
	}
}

func TestServerNetworkChangeRequiresFixedIPReset(t *testing.T) {
	srv := newTestServer(t)
	h, cookies := setupAndLogin(t, srv)

	settings := doJSON(t, h, http.MethodPut, "/api/settings/export", "127.0.0.1:5000", map[string]any{
		"host": "vpn.example.com", "port": 1194, "proto": "udp",
	}, cookies...)
	if settings.Code != http.StatusOK {
		t.Fatalf("保存导出设置失败：%d %s", settings.Code, settings.Body.String())
	}
	configFor := func(network string) map[string]any {
		return map[string]any{
			"vpnNetwork":        network,
			"protocol":          "udp",
			"port":              1194,
			"dns":               []string{"119.29.29.29"},
			"dataCiphers":       []string{"AES-256-GCM"},
			"authDigest":        "SHA256",
			"tlsMinVersion":     "1.2",
			"keepaliveInterval": 10,
			"keepaliveTimeout":  60,
			"maxClients":        0,
			"routeMode":         "split",
			"allowedInternal":   []string{},
			"blockIpv6":         true,
		}
	}
	saveDraft := func(config map[string]any) {
		t.Helper()
		rec := doJSON(t, h, http.MethodPut, "/api/server/config/draft", "127.0.0.1:5000", config, cookies...)
		if rec.Code != http.StatusOK {
			t.Fatalf("保存服务端配置失败：%d %s", rec.Code, rec.Body.String())
		}
	}
	apply := func(body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		return doJSON(t, h, http.MethodPost, "/api/server/config/apply", "127.0.0.1:5000", body, cookies...)
	}

	saveDraft(configFor("10.8.0.0/16"))
	if rec := apply(map[string]any{}); rec.Code != http.StatusOK {
		t.Fatalf("首次应用失败：%d %s", rec.Code, rec.Body.String())
	}

	create := doJSON(t, h, http.MethodPost, "/api/users", "127.0.0.1:5000", map[string]any{
		"displayName": "固定 IP 用户",
		"profile":     map[string]any{"name": "固定 IP 配置", "authType": "certificate"},
	}, cookies...)
	if create.Code != http.StatusCreated && create.Code != http.StatusAccepted {
		t.Fatalf("创建配置失败：%d %s", create.Code, create.Body.String())
	}
	var created struct {
		Profile profileDTO `json:"profile"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("解析创建结果失败：%v", err)
	}
	patch := doJSON(t, h, http.MethodPatch, "/api/profiles/"+created.Profile.ID, "127.0.0.1:5000",
		map[string]any{"fixedVpnIp": "10.8.0.2"}, cookies...)
	if patch.Code != http.StatusOK && patch.Code != http.StatusAccepted {
		t.Fatalf("设置固定 IP 失败：%d %s", patch.Code, patch.Body.String())
	}

	var configState struct {
		FixedIPProfiles []fixedIPProfileDTO `json:"fixedIpProfiles"`
	}
	raw := doJSON(t, h, http.MethodGet, "/api/server/config", "127.0.0.1:5000", nil, cookies...)
	if err := json.Unmarshal(raw.Body.Bytes(), &configState); err != nil {
		t.Fatalf("解析服务端配置失败：%v", err)
	}
	if len(configState.FixedIPProfiles) != 1 || configState.FixedIPProfiles[0].FixedVPNIP != "10.8.0.2" {
		t.Fatalf("固定 IP 列表异常：%+v", configState.FixedIPProfiles)
	}

	saveDraft(configFor("10.18.0.0/16"))
	denied := apply(map[string]any{})
	if denied.Code != http.StatusConflict || !strings.Contains(denied.Body.String(), "固定 VPN 地址") {
		t.Fatalf("改动网段时应先要求处理固定 IP：%d %s", denied.Code, denied.Body.String())
	}
	reset := apply(map[string]any{"resetFixedIps": true})
	if reset.Code != http.StatusOK {
		t.Fatalf("自动重置固定 IP 后应用失败：%d %s", reset.Code, reset.Body.String())
	}

	detail := doJSON(t, h, http.MethodGet, "/api/profiles/"+created.Profile.ID, "127.0.0.1:5000", nil, cookies...)
	var profile profileDTO
	if err := json.Unmarshal(detail.Body.Bytes(), &profile); err != nil {
		t.Fatalf("解析配置详情失败：%v", err)
	}
	if profile.FixedVPNIP != "" {
		t.Fatalf("固定 IP 应已改为自动分配：%q", profile.FixedVPNIP)
	}

	var after struct {
		Active          *serverConfigRevisionDTO `json:"active"`
		FixedIPProfiles []fixedIPProfileDTO      `json:"fixedIpProfiles"`
	}
	raw = doJSON(t, h, http.MethodGet, "/api/server/config", "127.0.0.1:5000", nil, cookies...)
	if err := json.Unmarshal(raw.Body.Bytes(), &after); err != nil {
		t.Fatalf("解析应用后服务端配置失败：%v", err)
	}
	if after.Active == nil || after.Active.Config.VPNNetwork != "10.18.0.0/16" {
		t.Fatalf("新网段未生效：%+v", after.Active)
	}
	if len(after.FixedIPProfiles) != 0 {
		t.Fatalf("应用后固定 IP 列表应为空：%+v", after.FixedIPProfiles)
	}

	// 后续应用失败时，刚自动清除的固定 IP 必须恢复，避免数据丢失。
	patch = doJSON(t, h, http.MethodPatch, "/api/profiles/"+created.Profile.ID, "127.0.0.1:5000",
		map[string]any{"fixedVpnIp": "10.18.0.2"}, cookies...)
	if patch.Code != http.StatusOK && patch.Code != http.StatusAccepted {
		t.Fatalf("重新设置固定 IP 失败：%d %s", patch.Code, patch.Body.String())
	}
	saveDraft(configFor("10.20.0.0/16"))
	if rec := doJSON(t, h, http.MethodPut, "/api/settings/export", "127.0.0.1:5000", map[string]any{
		"host": "vpn.example.com", "port": 1195, "proto": "udp",
	}, cookies...); rec.Code != http.StatusOK {
		t.Fatalf("修改导出设置失败：%d %s", rec.Code, rec.Body.String())
	}
	if rec := apply(map[string]any{"resetFixedIps": true}); rec.Code == http.StatusOK {
		t.Fatalf("导出设置不一致时不应应用成功：%s", rec.Body.String())
	}
	detail = doJSON(t, h, http.MethodGet, "/api/profiles/"+created.Profile.ID, "127.0.0.1:5000", nil, cookies...)
	if err := json.Unmarshal(detail.Body.Bytes(), &profile); err != nil {
		t.Fatalf("解析失败回退后的配置详情失败：%v", err)
	}
	if profile.FixedVPNIP != "10.18.0.2" {
		t.Fatalf("应用失败后固定 IP 应恢复，实际：%q", profile.FixedVPNIP)
	}
}

func TestServerConfigDraftAndApply(t *testing.T) {
	srv := newTestServer(t)
	h, cookies := setupAndLogin(t, srv)

	get := doJSON(t, h, http.MethodGet, "/api/server/config", "127.0.0.1:5000", nil, cookies...)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"vpnNetwork":"10.8.0.0/16"`) {
		t.Fatalf("读取服务端配置失败：%d %s", get.Code, get.Body.String())
	}

	config := map[string]any{
		"vpnNetwork":        "10.9.0.0/16",
		"protocol":          "udp",
		"port":              1194,
		"dns":               []string{"1.1.1.1"},
		"dataCiphers":       []string{"AES-256-GCM", "CHACHA20-POLY1305"},
		"authDigest":        "SHA512",
		"tlsMinVersion":     "1.3",
		"keepaliveInterval": 15,
		"keepaliveTimeout":  90,
		"maxClients":        100,
		"routeMode":         "split",
		"allowedInternal":   []string{"192.168.99.0/24"},
		"allowInterClient":  false,
		"blockIpv6":         true,
		"extraDirectives":   []string{"tun-mtu 1500"},
	}
	preview := doJSON(t, h, http.MethodPost, "/api/server/config/preview", "127.0.0.1:5000", config, cookies...)
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), `crl-verify`) {
		t.Fatalf("预览失败：%d %s", preview.Code, preview.Body.String())
	}

	draft := doJSON(t, h, http.MethodPut, "/api/server/config/draft", "127.0.0.1:5000", config, cookies...)
	if draft.Code != http.StatusOK {
		t.Fatalf("保存草稿失败：%d %s", draft.Code, draft.Body.String())
	}
	var draftBody struct {
		Draft serverConfigRevisionDTO `json:"draft"`
	}
	if err := json.Unmarshal(draft.Body.Bytes(), &draftBody); err != nil {
		t.Fatalf("解析草稿失败：%v", err)
	}
	if draftBody.Draft.Config.VPNNetwork != "10.9.0.0/16" ||
		draftBody.Draft.Config.AuthDigest != "SHA512" ||
		draftBody.Draft.Config.TLSMinVersion != "1.3" ||
		draftBody.Draft.Config.KeepaliveInterval != 15 ||
		draftBody.Draft.Config.MaxClients != 100 {
		t.Fatalf("草稿内容异常：%+v", draftBody.Draft.Config)
	}

	settings := doJSON(t, h, http.MethodPut, "/api/settings/export", "127.0.0.1:5000", map[string]any{
		"host": "vpn.example.com", "port": 1194, "proto": "udp",
	}, cookies...)
	if settings.Code != http.StatusOK {
		t.Fatalf("保存导出设置失败：%d %s", settings.Code, settings.Body.String())
	}

	apply := doJSON(t, h, http.MethodPost, "/api/server/config/apply", "127.0.0.1:5000",
		map[string]string{}, cookies...)
	if apply.Code != http.StatusOK {
		t.Fatalf("应用配置失败：%d %s", apply.Code, apply.Body.String())
	}
	if !strings.Contains(apply.Body.String(), `"agentPending":true`) {
		t.Fatalf("应用响应缺少 agentPending：%s", apply.Body.String())
	}

	get = doJSON(t, h, http.MethodGet, "/api/server/config", "127.0.0.1:5000", nil, cookies...)
	if get.Code != http.StatusOK {
		t.Fatalf("应用后读取失败：%d", get.Code)
	}
	var after struct {
		Active serverConfigRevisionDTO `json:"active"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &after); err != nil {
		t.Fatalf("解析应用后配置失败：%v", err)
	}
	if after.Active.Config.VPNNetwork != "10.9.0.0/16" || after.Active.State != "active" {
		t.Fatalf("生效修订异常：%+v", after.Active)
	}
}
