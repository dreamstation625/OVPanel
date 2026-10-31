package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestProfilePolicyUpdate(t *testing.T) {
	srv := newTestServer(t)
	h, cookies := setupAndLogin(t, srv)

	create := doJSON(t, h, http.MethodPost, "/api/users", "127.0.0.1:5000", map[string]any{
		"displayName": "策略用户",
		"profile": map[string]any{
			"name":     "全局出口配置",
			"authType": "certificate",
		},
	}, cookies...)
	if create.Code != http.StatusCreated {
		t.Fatalf("创建配置失败：%d %s", create.Code, create.Body.String())
	}
	var created struct {
		Profile profileDTO `json:"profile"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("解析创建结果失败：%v", err)
	}
	if created.Profile.RouteMode != "inherit" || created.Profile.AccessMode != "inherit" {
		t.Fatalf("新配置应默认继承：%+v", created.Profile)
	}
	id := created.Profile.ID

	update := doJSON(t, h, http.MethodPatch, "/api/profiles/"+id, "127.0.0.1:5000", map[string]any{
		"routeMode":       "global",
		"accessMode":      "allow",
		"allowedInternal": []string{"192.168.99.0/24"},
	}, cookies...)
	if update.Code != http.StatusOK {
		t.Fatalf("更新策略失败：%d %s", update.Code, update.Body.String())
	}
	var updated profileDTO
	if err := json.Unmarshal(update.Body.Bytes(), &updated); err != nil {
		t.Fatalf("解析更新结果失败：%v", err)
	}
	if updated.RouteMode != "global" || updated.AccessMode != "allow" ||
		len(updated.AllowedInternal) != 1 || updated.AllowedInternal[0] != "192.168.99.0/24" {
		t.Fatalf("策略未保存：%+v", updated)
	}

	invalid := doJSON(t, h, http.MethodPatch, "/api/profiles/"+id, "127.0.0.1:5000", map[string]any{
		"accessMode":      "allow",
		"allowedInternal": []string{},
	}, cookies...)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("允许模式缺少网段应被拒绝：%d %s", invalid.Code, invalid.Body.String())
	}

	overlap := doJSON(t, h, http.MethodPatch, "/api/profiles/"+id, "127.0.0.1:5000", map[string]any{
		"accessMode":      "allow",
		"allowedInternal": []string{"10.8.0.0/24"},
	}, cookies...)
	if overlap.Code != http.StatusBadRequest {
		t.Fatalf("与 VPN 地址池重叠的网段应被拒绝：%d %s", overlap.Code, overlap.Body.String())
	}

	detail := doJSON(t, h, http.MethodGet, "/api/profiles/"+id, "127.0.0.1:5000", nil, cookies...)
	if detail.Code != http.StatusOK ||
		!strings.Contains(detail.Body.String(), `"routeMode":"global"`) ||
		!strings.Contains(detail.Body.String(), `"accessMode":"allow"`) {
		t.Fatalf("详情未返回策略：%d %s", detail.Code, detail.Body.String())
	}
}
