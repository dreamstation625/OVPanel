package netpolicy

import (
	"fmt"
	"net"
	"strings"
)

const (
	// MaxClientNetworks 是单个配置最多可共享的内网数量。
	MaxClientNetworks = 8
	// MaxSharedNetworks 是所有配置合计最多可共享的内网数量。
	MaxSharedNetworks = 64
)

// privateClientNetworks 是客户端共享内网允许的地址范围。
var privateClientNetworks = []*net.IPNet{
	mustCIDR("10.0.0.0/8"),
	mustCIDR("172.16.0.0/12"),
	mustCIDR("192.168.0.0/16"),
	mustCIDR("100.64.0.0/10"),
}

func mustCIDR(raw string) *net.IPNet {
	_, network, err := net.ParseCIDR(raw)
	if err != nil {
		panic(err)
	}
	return network
}

// NormalizeClientNetworks 规范化客户端共享内网：只允许私网/CGNAT IPv4，
// 支持 CIDR 和单个 IP，单个 IP 规范化为 /32。
func NormalizeClientNetworks(values []string) ([]string, error) {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		network, err := parseClientNetwork(raw)
		if err != nil {
			return nil, err
		}
		cidr := network.String()
		if _, ok := seen[cidr]; ok {
			continue
		}
		seen[cidr] = struct{}{}
		out = append(out, cidr)
	}
	if len(out) > MaxClientNetworks {
		return nil, fmt.Errorf("共享内网最多 %d 个", MaxClientNetworks)
	}
	return out, nil
}

// PrepareClientNetworks 在规范化后检查共享内网不能与 VPN 地址池重叠。
// 跨配置重叠由面板和认证快照校验。
func PrepareClientNetworks(vpnNetwork string, values []string) ([]string, error) {
	normalized, err := NormalizeClientNetworks(values)
	if err != nil {
		return nil, err
	}
	_, pool, err := net.ParseCIDR(strings.TrimSpace(vpnNetwork))
	if err != nil || pool.IP.To4() == nil {
		return nil, fmt.Errorf("VPN 网段必须是合法 IPv4 CIDR")
	}
	for _, raw := range normalized {
		_, network, err := net.ParseCIDR(raw)
		if err != nil {
			return nil, fmt.Errorf("共享内网必须是合法 IPv4 网段：%q", raw)
		}
		if network.Contains(pool.IP) || pool.Contains(network.IP) {
			return nil, fmt.Errorf("共享内网不能与 VPN 地址池重叠：%q", raw)
		}
	}
	return normalized, nil
}

// NetworksOverlap 判断两个已规范化的 IPv4 网段是否重叠。
func NetworksOverlap(a, b string) bool {
	_, left, err := net.ParseCIDR(strings.TrimSpace(a))
	if err != nil || left.IP.To4() == nil {
		return false
	}
	_, right, err := net.ParseCIDR(strings.TrimSpace(b))
	if err != nil || right.IP.To4() == nil {
		return false
	}
	return left.Contains(right.IP) || right.Contains(left.IP)
}

func parseClientNetwork(raw string) (*net.IPNet, error) {
	if strings.Contains(raw, ":") {
		return nil, fmt.Errorf("共享内网只支持 IPv4：%q", raw)
	}
	var network *net.IPNet
	if strings.Contains(raw, "/") {
		_, parsed, err := net.ParseCIDR(raw)
		if err != nil {
			return nil, fmt.Errorf("共享内网必须是合法 IPv4 网段或地址：%q", raw)
		}
		network = parsed
	} else {
		ip := net.ParseIP(raw)
		if ip == nil || ip.To4() == nil {
			return nil, fmt.Errorf("共享内网必须是合法 IPv4 网段或地址：%q", raw)
		}
		network = &net.IPNet{IP: ip.To4(), Mask: net.CIDRMask(32, 32)}
	}
	if network.IP.To4() == nil {
		return nil, fmt.Errorf("共享内网只支持 IPv4：%q", raw)
	}
	ones, bits := network.Mask.Size()
	if bits != 32 || ones < 8 {
		return nil, fmt.Errorf("共享内网前缀需在 /8 到 /32 之间：%q", raw)
	}
	network.IP = network.IP.Mask(network.Mask)
	if !withinPrivateClientRange(network) {
		return nil, fmt.Errorf("共享内网只支持私网或 CGNAT 网段：%q", raw)
	}
	return network, nil
}

func withinPrivateClientRange(network *net.IPNet) bool {
	first := network.IP.To4()
	if first == nil {
		return false
	}
	last := make(net.IP, 4)
	for i := 0; i < 4; i++ {
		last[i] = first[i] | ^network.Mask[i]
	}
	for _, private := range privateClientNetworks {
		if private.Contains(first) && private.Contains(last) {
			return true
		}
	}
	return false
}
