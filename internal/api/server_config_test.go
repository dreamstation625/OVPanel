package api

import (
	"encoding/json"
	"net/http"
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
		return map[string]string{"adminPassword": "a-long-enough-password", "action": action}
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

func TestServerConfigDraftAndApply(t *testing.T) {
	srv := newTestServer(t)
	h, cookies := setupAndLogin(t, srv)

	get := doJSON(t, h, http.MethodGet, "/api/server/config", "127.0.0.1:5000", nil, cookies...)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"vpnNetwork":"10.8.0.0/16"`) {
		t.Fatalf("读取服务端配置失败：%d %s", get.Code, get.Body.String())
	}

	config := map[string]any{
		"vpnNetwork":       "10.9.0.0/16",
		"protocol":         "udp",
		"port":             1194,
		"dns":              []string{"1.1.1.1"},
		"routeMode":        "split",
		"allowedInternal":  []string{"192.168.99.0/24"},
		"allowInterClient": false,
		"blockIpv6":        true,
		"extraDirectives":  []string{"tun-mtu 1500"},
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
	if draftBody.Draft.Config.VPNNetwork != "10.9.0.0/16" {
		t.Fatalf("草稿内容异常：%+v", draftBody.Draft.Config)
	}

	settings := doJSON(t, h, http.MethodPut, "/api/settings/export", "127.0.0.1:5000", map[string]any{
		"host": "vpn.example.com", "port": 1194, "proto": "udp",
	}, cookies...)
	if settings.Code != http.StatusOK {
		t.Fatalf("保存导出设置失败：%d %s", settings.Code, settings.Body.String())
	}

	apply := doJSON(t, h, http.MethodPost, "/api/server/config/apply", "127.0.0.1:5000",
		map[string]string{"adminPassword": "a-long-enough-password"}, cookies...)
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
