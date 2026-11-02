package api

import (
	"encoding/json"
	"net/http"
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

	public := doJSON(t, h, http.MethodPatch, "/api/profiles/"+id, "127.0.0.1:5000", map[string]any{
		"routeMode":       "split",
		"accessMode":      "allow",
		"allowedInternal": []string{"1.1.1.1", "203.0.113.5/24"},
	}, cookies...)
	if public.Code != http.StatusOK {
		t.Fatalf("公网目标应被接受：%d %s", public.Code, public.Body.String())
	}
	var publicProfile profileDTO
	if err := json.Unmarshal(public.Body.Bytes(), &publicProfile); err != nil {
		t.Fatalf("解析公网目标结果失败：%v", err)
	}
	if len(publicProfile.AllowedInternal) != 2 ||
		publicProfile.AllowedInternal[0] != "1.1.1.1/32" ||
		publicProfile.AllowedInternal[1] != "203.0.113.0/24" {
		t.Fatalf("目标网段未规范化：%+v", publicProfile.AllowedInternal)
	}

	for _, bad := range [][]string{{"127.0.0.1"}, {"0.0.0.0/0"}, {"2001:db8::/32"}} {
		invalid := doJSON(t, h, http.MethodPatch, "/api/profiles/"+id, "127.0.0.1:5000", map[string]any{
			"accessMode":      "allow",
			"allowedInternal": bad,
		}, cookies...)
		if invalid.Code != http.StatusBadRequest {
			t.Fatalf("非法目标网段应被拒绝：%v %d %s", bad, invalid.Code, invalid.Body.String())
		}
	}

	shared := doJSON(t, h, http.MethodPatch, "/api/profiles/"+id, "127.0.0.1:5000", map[string]any{
		"clientLanAccess": "allow",
		"clientNetworks":  []string{"192.168.50.5/24", "10.20.30.40"},
	}, cookies...)
	if shared.Code != http.StatusOK {
		t.Fatalf("共享内网应被接受：%d %s", shared.Code, shared.Body.String())
	}
	var sharedProfile profileDTO
	if err := json.Unmarshal(shared.Body.Bytes(), &sharedProfile); err != nil {
		t.Fatalf("解析共享内网结果失败：%v", err)
	}
	if sharedProfile.ClientLANAccess != "allow" ||
		len(sharedProfile.ClientNetworks) != 2 ||
		sharedProfile.ClientNetworks[0] != "192.168.50.0/24" ||
		sharedProfile.ClientNetworks[1] != "10.20.30.40/32" {
		t.Fatalf("共享内网未规范化：%+v", sharedProfile)
	}
	for _, bad := range [][]string{{"8.8.8.0/24"}, {"10.8.1.0/24"}} {
		invalid := doJSON(t, h, http.MethodPatch, "/api/profiles/"+id, "127.0.0.1:5000", map[string]any{
			"clientNetworks": bad,
		}, cookies...)
		if invalid.Code != http.StatusBadRequest {
			t.Fatalf("非法共享内网应被拒绝：%v %d %s", bad, invalid.Code, invalid.Body.String())
		}
	}

	detail := doJSON(t, h, http.MethodGet, "/api/profiles/"+id, "127.0.0.1:5000", nil, cookies...)
	if detail.Code != http.StatusOK {
		t.Fatalf("读取详情失败：%d %s", detail.Code, detail.Body.String())
	}
	var detailProfile profileDTO
	if err := json.Unmarshal(detail.Body.Bytes(), &detailProfile); err != nil {
		t.Fatalf("解析详情失败：%v", err)
	}
	if detailProfile.RouteMode != "split" || detailProfile.AccessMode != "allow" ||
		len(detailProfile.AllowedInternal) != 2 ||
		detailProfile.AllowedInternal[0] != "1.1.1.1/32" ||
		detailProfile.AllowedInternal[1] != "203.0.113.0/24" {
		t.Fatalf("详情未返回目标网段策略：%+v", detailProfile)
	}
}
