package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func createTestProfile(t *testing.T, h http.Handler, name string, cookies ...*http.Cookie) profileDTO {
	t.Helper()
	create := doJSON(t, h, http.MethodPost, "/api/users", "127.0.0.1:5000", map[string]any{
		"displayName": name,
		"profile": map[string]any{
			"name":     name + " 配置",
			"authType": "certificate",
		},
	}, cookies...)
	if create.Code != http.StatusCreated {
		t.Fatalf("创建 %s 失败：%d %s", name, create.Code, create.Body.String())
	}
	var created struct {
		Profile profileDTO `json:"profile"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("解析创建结果失败：%v", err)
	}
	return created.Profile
}

func TestProfileFixedVPNIP(t *testing.T) {
	srv := newTestServer(t)
	h, cookies := setupAndLogin(t, srv)
	profile := createTestProfile(t, h, "固定地址用户", cookies...)

	update := doJSON(t, h, http.MethodPatch, "/api/profiles/"+profile.ID, "127.0.0.1:5000", map[string]any{
		"fixedVpnIp": "10.8.255.200",
	}, cookies...)
	if update.Code != http.StatusOK {
		t.Fatalf("设置固定 IP 失败：%d %s", update.Code, update.Body.String())
	}
	var updated profileDTO
	if err := json.Unmarshal(update.Body.Bytes(), &updated); err != nil {
		t.Fatalf("解析固定 IP 结果失败：%v", err)
	}
	if updated.FixedVPNIP != "10.8.255.200" || updated.ConcurrentLimit != 1 {
		t.Fatalf("固定 IP 或并发上限异常：%+v", updated)
	}
	if updated.FixedIPRangeStart != "10.8.0.2" || updated.FixedIPRangeEnd != "10.8.255.254" {
		t.Fatalf("固定 IP 可用范围异常：%+v", updated)
	}
	if updated.FixedIPPoolStart != "10.8.64.0" || updated.FixedIPPoolEnd != "10.8.191.255" {
		t.Fatalf("固定 IP 动态池范围异常：%+v", updated)
	}

	// 动态池地址不能作为固定地址。
	invalid := doJSON(t, h, http.MethodPatch, "/api/profiles/"+profile.ID, "127.0.0.1:5000", map[string]any{
		"fixedVpnIp": "10.8.100.10",
	}, cookies...)
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), "动态池") {
		t.Fatalf("动态池地址应被拒绝：%d %s", invalid.Code, invalid.Body.String())
	}

	// 同一固定地址不能分配给第二份配置。
	other := createTestProfile(t, h, "另一用户", cookies...)
	duplicate := doJSON(t, h, http.MethodPatch, "/api/profiles/"+other.ID, "127.0.0.1:5000", map[string]any{
		"fixedVpnIp": "10.8.255.200",
	}, cookies...)
	if duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), "固定 IP") {
		t.Fatalf("重复固定 IP 应被拒绝：%d %s", duplicate.Code, duplicate.Body.String())
	}

	// 固定地址存在时不能再提高并发上限。
	limitsTooHigh := doJSON(t, h, http.MethodPut, "/api/profiles/"+profile.ID+"/limits", "127.0.0.1:5000", map[string]any{
		"concurrentLimit": 2,
	}, cookies...)
	if limitsTooHigh.Code != http.StatusBadRequest || !strings.Contains(limitsTooHigh.Body.String(), "并发上限只能为 1") {
		t.Fatalf("固定 IP 的并发上限应被锁定：%d %s", limitsTooHigh.Code, limitsTooHigh.Body.String())
	}
	limitsOK := doJSON(t, h, http.MethodPut, "/api/profiles/"+profile.ID+"/limits", "127.0.0.1:5000", map[string]any{
		"concurrentLimit": 1,
	}, cookies...)
	if limitsOK.Code != http.StatusOK {
		t.Fatalf("并发上限 1 应被接受：%d %s", limitsOK.Code, limitsOK.Body.String())
	}

	// 清空固定地址后恢复自动分配，不再锁定并发上限。
	clear := doJSON(t, h, http.MethodPatch, "/api/profiles/"+profile.ID, "127.0.0.1:5000", map[string]any{
		"fixedVpnIp": "",
	}, cookies...)
	if clear.Code != http.StatusOK {
		t.Fatalf("清除固定 IP 失败：%d %s", clear.Code, clear.Body.String())
	}
	var cleared profileDTO
	if err := json.Unmarshal(clear.Body.Bytes(), &cleared); err != nil {
		t.Fatalf("解析清除结果失败：%v", err)
	}
	if cleared.FixedVPNIP != "" {
		t.Fatalf("固定 IP 未清除：%+v", cleared)
	}
}
