package netpolicy

import (
	"fmt"
	"net"
	"strings"
)

const maxTargets = 20

// NormalizeTargets 规范化目标网段：支持 IPv4 CIDR 与单个 IPv4，
// 主机位归一到网络地址，单个地址补 /32，去重并限制数量。
func NormalizeTargets(values []string) ([]string, error) {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		cidr, err := normalizeTarget(raw)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[cidr]; ok {
			continue
		}
		seen[cidr] = struct{}{}
		out = append(out, cidr)
	}
	if len(out) > maxTargets {
		return nil, fmt.Errorf("目标网段最多 %d 个", maxTargets)
	}
	return out, nil
}

// PrepareTargets 在规范化目标网段后检查其不能与 VPN 地址池重叠。
func PrepareTargets(vpnNetwork string, values []string) ([]string, error) {
	normalized, err := NormalizeTargets(values)
	if err != nil {
		return nil, err
	}
	_, pool, err := net.ParseCIDR(strings.TrimSpace(vpnNetwork))
	if err != nil || pool.IP.To4() == nil {
		return nil, fmt.Errorf("VPN 网段必须是合法 IPv4 CIDR")
	}
	for _, raw := range normalized {
		_, target, err := net.ParseCIDR(raw)
		if err != nil {
			return nil, fmt.Errorf("目标网段必须是合法 IPv4 CIDR：%q", raw)
		}
		if target.Contains(pool.IP) || pool.Contains(target.IP) {
			return nil, fmt.Errorf("目标网段不能与 VPN 地址池重叠：%q", raw)
		}
	}
	return normalized, nil
}

func normalizeTarget(raw string) (string, error) {
	if strings.Contains(raw, ":") {
		return "", fmt.Errorf("目标网段只支持 IPv4：%q", raw)
	}

	var network *net.IPNet
	if strings.Contains(raw, "/") {
		_, parsed, err := net.ParseCIDR(raw)
		if err != nil {
			return "", fmt.Errorf("目标网段必须是合法 IPv4 CIDR 或 IPv4 地址：%q", raw)
		}
		network = parsed
	} else {
		ip := net.ParseIP(raw)
		if ip == nil || ip.To4() == nil {
			return "", fmt.Errorf("目标网段必须是合法 IPv4 CIDR 或 IPv4 地址：%q", raw)
		}
		network = &net.IPNet{IP: ip.To4(), Mask: net.CIDRMask(32, 32)}
	}
	if network.IP.To4() == nil {
		return "", fmt.Errorf("目标网段只支持 IPv4：%q", raw)
	}

	ones, bits := network.Mask.Size()
	if bits != 32 || ones == 0 {
		return "", fmt.Errorf("目标网段不能是 0.0.0.0/0；需要全部 IPv4 时请使用全局出口")
	}
	network.IP = network.IP.Mask(network.Mask)
	switch {
	case network.IP.To4()[0] == 0:
		return "", fmt.Errorf("目标网段不能使用未指定地址：%q", raw)
	case network.IP.IsLoopback():
		return "", fmt.Errorf("目标网段不能使用回环地址：%q", raw)
	case network.IP.IsLinkLocalUnicast():
		return "", fmt.Errorf("目标网段不能使用链路本地地址：%q", raw)
	case network.IP.IsMulticast():
		return "", fmt.Errorf("目标网段不能使用组播地址：%q", raw)
	case network.IP.To4()[0] >= 240:
		return "", fmt.Errorf("目标网段不能使用保留地址：%q", raw)
	}
	return network.String(), nil
}
