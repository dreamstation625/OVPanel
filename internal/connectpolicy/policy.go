// Package connectpolicy 把认证快照中的配置级路由与内网权限转换成
// OpenVPN client-connect 助手可读取的身份映射。
//
// 路由推送决定客户端拿到哪些路由；真正的访问权限仍由运行代理的
// nftables 规则执行，两者不能互相替代。
package connectpolicy

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/dreamstation625/ovpanel/internal/admission"
)

// Policy 是一份配置的有效网络策略。
type Policy struct {
	GlobalExit      bool     `json:"globalExit"`
	AllowedInternal []string `json:"allowedInternal,omitempty"`
	FixedIP         string   `json:"fixedIp,omitempty"`
	VPNMask         string   `json:"vpnMask,omitempty"`
}

// File 是按证书指纹或登录名索引的有效策略。
type File struct {
	Entries map[string]Policy `json:"entries"`
}

// Effective 计算配置在服务端默认值之上的有效策略。
// 路由方式与内网权限分别继承或覆盖。
func Effective(profile admission.Profile, server Policy) Policy {
	global := server.GlobalExit
	switch profile.RouteMode {
	case "split":
		global = false
	case "global":
		global = true
	}

	allowed := server.AllowedInternal
	switch profile.AccessMode {
	case "allow":
		allowed = profile.AllowedInternal
	case "deny":
		allowed = nil
	}
	return Policy{
		GlobalExit:      global,
		AllowedInternal: append([]string(nil), allowed...),
		FixedIP:         profile.FixedVPNIP,
		VPNMask:         server.VPNMask,
	}
}

// Build 生成身份到策略的映射。证书身份优先使用 SHA-256 指纹，
// 仅密码配置使用登录名。
func Build(snapshot admission.Snapshot, server Policy) (File, error) {
	file := File{Entries: map[string]Policy{}}
	for _, profile := range snapshot.Profiles {
		effective := Effective(profile, server)
		keys := make([]string, 0, 2)
		if fingerprint := normalizeFingerprint(profile.CertFingerprint); fingerprint != "" {
			keys = append(keys, "fp:"+fingerprint)
		}
		if profile.Username != "" {
			keys = append(keys, "user:"+strings.ToLower(profile.Username))
		}
		if len(keys) == 0 {
			// 证书已吊销且没有登录名的配置无法通过准入，也不会产生连接；
			// 跳过即可，不能让整份控制包失败。
			continue
		}
		for _, key := range keys {
			if existing, ok := file.Entries[key]; ok && !samePolicy(existing, effective) {
				return File{}, fmt.Errorf("网络策略身份冲突：%s", key)
			}
			file.Entries[key] = effective
		}
	}
	return file, nil
}

// Lookup 按证书指纹优先、登录名次之查找有效策略。
// 未匹配时返回 false，调用方应按最小权限处理。
func Lookup(file File, fingerprint, username string) (Policy, bool) {
	if key := normalizeFingerprint(fingerprint); key != "" {
		if policy, ok := file.Entries["fp:"+key]; ok {
			return policy, true
		}
	}
	if username = strings.TrimSpace(username); username != "" {
		if policy, ok := file.Entries["user:"+strings.ToLower(username)]; ok {
			return policy, true
		}
	}
	return Policy{}, false
}

// ClientLines 生成 client-connect 文件中的固定地址和推送指令。
func ClientLines(policy Policy) ([]string, error) {
	lines := make([]string, 0, 2)
	if policy.FixedIP != "" {
		if policy.VPNMask == "" {
			return nil, fmt.Errorf("固定 IP 缺少 VPN 掩码")
		}
		lines = append(lines, fmt.Sprintf("ifconfig-push %s %s", policy.FixedIP, policy.VPNMask))
	}
	push, err := PushLines(policy)
	if err != nil {
		return nil, err
	}
	return append(lines, push...), nil
}

// PushLines 生成 client-connect 文件中的推送指令。
func PushLines(policy Policy) ([]string, error) {
	if policy.GlobalExit {
		return []string{`push "redirect-gateway def1 bypass-dhcp"`}, nil
	}
	lines := make([]string, 0, len(policy.AllowedInternal))
	for _, raw := range policy.AllowedInternal {
		_, network, err := net.ParseCIDR(strings.TrimSpace(raw))
		if err != nil || network.IP.To4() == nil {
			return nil, fmt.Errorf("授权内网网段非法：%q", raw)
		}
		lines = append(lines, fmt.Sprintf(`push "route %s %s"`, network.IP.String(), net.IP(network.Mask).String()))
	}
	return lines, nil
}

// Write 原子写入策略文件，权限 0600。
func Write(path string, file File) error {
	payload, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("编码连接策略: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建连接策略目录: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".connect-policy-*")
	if err != nil {
		return fmt.Errorf("创建连接策略临时文件: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return fmt.Errorf("写入连接策略: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("替换连接策略: %w", err)
	}
	return nil
}

// Load 读取连接策略文件。
func Load(path string) (File, error) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return File{}, err
	}
	var file File
	if err := json.Unmarshal(payload, &file); err != nil {
		return File{}, fmt.Errorf("解析连接策略: %w", err)
	}
	if file.Entries == nil {
		file.Entries = map[string]Policy{}
	}
	return file, nil
}

func samePolicy(a, b Policy) bool {
	if a.GlobalExit != b.GlobalExit || len(a.AllowedInternal) != len(b.AllowedInternal) {
		return false
	}
	for i := range a.AllowedInternal {
		if a.AllowedInternal[i] != b.AllowedInternal[i] {
			return false
		}
	}
	return true
}

func normalizeFingerprint(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, ":", "")
	value = strings.ReplaceAll(value, " ", "")
	return value
}
