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
	"169.254.169.254/32",
	"169.254.0.0/16",
}

// Policy 是计算后的有效网络策略。
type Policy struct {
	VPNPool          string   `json:"vpnPool"`
	TunDevice        string   `json:"tunDevice"`
	AllowInterClient bool     `json:"allowInterClient"`
	GlobalExit       bool     `json:"globalExit"`
	BlockIPv6        bool     `json:"blockIpv6"`
	AllowedInternal  []string `json:"allowedInternal,omitempty"`
	ProtectTargets   []string `json:"protectTargets,omitempty"`
}

// Render 生成完整规则脚本。
func Render(p Policy) (string, error) {
	pool, err := parseCIDR("VPN 地址池", p.VPNPool)
	if err != nil {
		return "", err
	}
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
	allowed := make([]string, 0, len(p.AllowedInternal))
	for _, raw := range p.AllowedInternal {
		cidr, err := parseCIDR("授权内网网段", raw)
		if err != nil {
			return "", err
		}
		allowed = append(allowed, cidr)
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
		fmt.Fprintf(&b, "\t\tip saddr %s ip daddr %s counter accept\n", pool, pool)
	} else {
		fmt.Fprintf(&b, "\t\tip saddr %s ip daddr %s counter drop\n", pool, pool)
	}

	b.WriteString("\n\t\t# 4) 内网权限：只放行授权网段，其余私有网段拒绝\n")
	for _, cidr := range allowed {
		fmt.Fprintf(&b, "\t\tip saddr %s ip daddr %s counter accept\n", pool, cidr)
	}
	for _, cidr := range []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"} {
		fmt.Fprintf(&b, "\t\tip saddr %s ip daddr %s counter drop\n", pool, cidr)
	}

	b.WriteString("\n\t\t# 5) 出口策略\n")
	if p.GlobalExit {
		fmt.Fprintf(&b, "\t\tip saddr %s counter accept\n", pool)
	} else {
		fmt.Fprintf(&b, "\t\tip saddr %s counter drop\n", pool)
	}
	b.WriteString("\n\t\t# 6) 回程流量：放在所有收紧规则之后\n")
	b.WriteString("\t\tct state established,related counter accept\n")
	b.WriteString("\t}\n")

	b.WriteString("\n\tchain postrouting {\n")
	b.WriteString("\t\ttype nat hook postrouting priority srcnat; policy accept;\n")
	fmt.Fprintf(&b, "\t\tip saddr %s ip daddr != %s counter masquerade\n", pool, pool)
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
