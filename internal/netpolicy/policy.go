// Package netpolicy 把服务端有效网络策略渲染成项目专用 nftables 表。
//
// 规则只操作 inet ovpanel 这一张表，不清空宿主或容器的其他规则。
// 访问权限始终由服务端执行；客户端自行添加路由不能绕过。
package netpolicy

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
)

// TableName 是项目专用 nftables 表。
const TableName = "ovpanel"

// defaultProtect 是即使“统一开放”也不放行的目标。
var defaultProtect = []string{
	"0.0.0.0/8",
	"127.0.0.0/8",
	"169.254.169.254/32",
	"169.254.0.0/16",
	"224.0.0.0/4",
	"240.0.0.0/4",
}

// defaultPrivateTargets 是未显式放行时拒绝的私有/特殊网段。
// 显式目标放行规则在它们之前，因此列表中的私有目标仍然可达。
var defaultPrivateTargets = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"100.64.0.0/10",
}

// Session 是一个已取得 VPN 地址的在线连接及其有效策略。
type Session struct {
	IP              string   `json:"ip"`
	AllowedInternal []string `json:"allowedInternal,omitempty"`
	GlobalExit      bool     `json:"globalExit"`
}

// Policy 是计算后的有效网络策略。
// GlobalExit 与 AllowedInternal 是服务端默认值，用于生成连接策略；
// 真正的逐连接规则来自 Sessions，未登记地址默认拒绝。
type Policy struct {
	VPNPool          string    `json:"vpnPool"`
	TunDevice        string    `json:"tunDevice"`
	AllowInterClient bool      `json:"allowInterClient"`
	BlockIPv6        bool      `json:"blockIpv6"`
	GlobalExit       bool      `json:"globalExit"`
	AllowedInternal  []string  `json:"allowedInternal,omitempty"`
	ProtectTargets   []string  `json:"protectTargets,omitempty"`
	Sessions         []Session `json:"sessions,omitempty"`
}

type renderedSession struct {
	ip      string
	allowed []string
	global  bool
}

// Render 生成完整规则脚本。
func Render(p Policy) (string, error) {
	_, pool, err := net.ParseCIDR(strings.TrimSpace(p.VPNPool))
	if err != nil || pool.IP.To4() == nil {
		return "", fmt.Errorf("VPN 地址池不是合法 IPv4 CIDR：%q", p.VPNPool)
	}
	poolText := pool.String()

	protect := append([]string{}, defaultProtect...)
	protect = append(protect, p.ProtectTargets...)
	protectCIDRs := make([]string, 0, len(protect))
	for _, raw := range protect {
		cidr, err := parseCIDR("保护目标", raw)
		if err != nil {
			return "", err
		}
		protectCIDRs = append(protectCIDRs, cidr)
	}

	sessions := make([]renderedSession, 0, len(p.Sessions))
	seenIPs := map[string]struct{}{}
	for _, session := range p.Sessions {
		raw := strings.TrimSpace(session.IP)
		if raw == "" {
			continue
		}
		ip := net.ParseIP(raw)
		if ip == nil || ip.To4() == nil || !pool.Contains(ip) {
			return "", fmt.Errorf("会话地址不在 VPN 地址池内：%q", raw)
		}
		if _, dup := seenIPs[ip.String()]; dup {
			return "", fmt.Errorf("会话地址重复：%q", raw)
		}
		seenIPs[ip.String()] = struct{}{}
		allowed, err := NormalizeTargets(session.AllowedInternal)
		if err != nil {
			return "", fmt.Errorf("会话 %s 的目标网段非法：%w", ip, err)
		}
		sessions = append(sessions, renderedSession{ip: ip.String(), allowed: allowed, global: session.GlobalExit})
	}

	var b strings.Builder
	b.WriteString("# OVPanel 网络策略：仅操作项目专用表\n")
	// 每次应用先重建本项目自己的表；否则 nft -f 会把新规则追加到旧链，
	// 多次应用后规则重复、计数器叠加。
	b.WriteString("add table inet ovpanel\n")
	b.WriteString("flush table inet ovpanel\n")
	fmt.Fprintf(&b, "table inet %s {\n", TableName)
	b.WriteString("\tchain forward {\n")
	b.WriteString("\t\ttype filter hook forward priority filter; policy accept;\n")

	b.WriteString("\n\t\t# 1) 控制面、元数据与链路本地保护\n")
	for _, cidr := range protectCIDRs {
		fmt.Fprintf(&b, "\t\tip daddr %s counter drop\n", cidr)
	}
	if p.BlockIPv6 {
		b.WriteString("\n\t\t# 2) 第一版不转发 IPv6\n")
		b.WriteString("\t\tmeta nfproto ipv6 counter drop\n")
	}

	b.WriteString("\n\t\t# 3) VPN 地址池互访\n")
	if p.AllowInterClient {
		fmt.Fprintf(&b, "\t\tip saddr %s ip daddr %s counter accept\n", poolText, poolText)
	} else {
		fmt.Fprintf(&b, "\t\tip saddr %s ip daddr %s counter drop\n", poolText, poolText)
	}

	b.WriteString("\n\t\t# 4) 目标网段权限：只放行每份配置的授权目标\n")
	for _, session := range sessions {
		for _, cidr := range session.allowed {
			fmt.Fprintf(&b, "\t\tip saddr %s ip daddr %s counter accept\n", session.ip, cidr)
		}
	}
	for _, cidr := range defaultPrivateTargets {
		fmt.Fprintf(&b, "\t\tip saddr %s ip daddr %s counter drop\n", poolText, cidr)
	}

	b.WriteString("\n\t\t# 5) 出口策略：逐连接决定是否允许其余公网\n")
	for _, session := range sessions {
		if session.global {
			fmt.Fprintf(&b, "\t\tip saddr %s counter accept\n", session.ip)
		}
	}
	b.WriteString("\n\t\t# 6) 未开启全局出口或未登记的会话默认拒绝\n")
	fmt.Fprintf(&b, "\t\tip saddr %s counter drop\n", poolText)
	b.WriteString("\n\t\t# 7) 回程流量：放在所有收紧规则之后\n")
	b.WriteString("\t\tct state established,related counter accept\n")
	b.WriteString("\t}\n")

	b.WriteString("\n\tchain postrouting {\n")
	b.WriteString("\t\ttype nat hook postrouting priority srcnat; policy accept;\n")
	fmt.Fprintf(&b, "\t\tip saddr %s ip daddr != %s counter masquerade\n", poolText, poolText)
	b.WriteString("\t}\n")
	b.WriteString("}\n")
	return b.String(), nil
}

// Apply 通过临时文件原子加载规则脚本。
func Apply(script string) error {
	file, err := os.CreateTemp("/run", "ovpanel-*.nft")
	if err != nil {
		return fmt.Errorf("创建 nft 临时文件: %w", err)
	}
	path := file.Name()
	defer os.Remove(path)
	if _, err := file.WriteString(script); err != nil {
		file.Close()
		return fmt.Errorf("写入 nft 规则: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭 nft 规则: %w", err)
	}
	out, err := exec.Command("nft", "-f", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("加载 nft 规则失败：%w（%s）", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Delete 删除项目专用表；表不存在视为成功。
func Delete() error {
	out, err := exec.Command("nft", "delete", "table", "inet", TableName).CombinedOutput()
	if err == nil {
		return nil
	}
	if strings.Contains(string(out), "No such file or directory") {
		return nil
	}
	return fmt.Errorf("删除 nft 规则表失败：%w（%s）", err, strings.TrimSpace(string(out)))
}

func parseCIDR(label, raw string) (string, error) {
	ip, network, err := net.ParseCIDR(strings.TrimSpace(raw))
	if err != nil || ip.To4() == nil {
		return "", fmt.Errorf("%s 不是合法 IPv4 CIDR：%q", label, raw)
	}
	return network.String(), nil
}
