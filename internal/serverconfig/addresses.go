package serverconfig

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
)

// AddressPlan 是 VPN 网段内的地址规划。
// 动态池从网段第二个地址开始，网段末尾的保留段留给固定 IP。
type AddressPlan struct {
	Network     string
	Mask        string
	Server      string
	PoolStart   string
	PoolEnd     string
	StaticStart string
	StaticEnd   string
}

// PlanAddresses 计算 VPN 网段的服务器地址、动态池和固定 IP 保留段。
func PlanAddresses(vpnNetwork string) (AddressPlan, error) {
	ip, network, err := net.ParseCIDR(strings.TrimSpace(vpnNetwork))
	if err != nil || network.IP.To4() == nil {
		return AddressPlan{}, fmt.Errorf("VPN 网段必须是合法 IPv4 CIDR")
	}
	if !ip.Equal(network.IP) {
		return AddressPlan{}, fmt.Errorf("VPN 网段必须使用网络地址")
	}
	ones, bits := network.Mask.Size()
	if bits != 32 || ones < 16 || ones > 24 {
		return AddressPlan{}, fmt.Errorf("VPN 网段前缀需在 /16 到 /24 之间")
	}

	mask := binary.BigEndian.Uint32(network.Mask)
	base := binary.BigEndian.Uint32(network.IP.To4())
	broadcast := base | ^mask
	last := broadcast - 1
	staticStart := last - 62
	poolStart := base + 2
	poolEnd := staticStart - 1
	if poolStart > poolEnd {
		return AddressPlan{}, fmt.Errorf("VPN 网段太小，无法同时容纳动态池和固定 IP")
	}

	return AddressPlan{
		Network:     uint32IP(base).String(),
		Mask:        uint32IP(mask).String(),
		Server:      uint32IP(base + 1).String(),
		PoolStart:   uint32IP(poolStart).String(),
		PoolEnd:     uint32IP(poolEnd).String(),
		StaticStart: uint32IP(staticStart).String(),
		StaticEnd:   uint32IP(last).String(),
	}, nil
}

// ValidateFixedIP 校验固定地址是否位于保留段，并返回规范化地址。
func ValidateFixedIP(vpnNetwork, value string) (string, error) {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil || ip.To4() == nil {
		return "", fmt.Errorf("固定 IP 必须是 IPv4 地址")
	}
	plan, err := PlanAddresses(vpnNetwork)
	if err != nil {
		return "", err
	}
	start := ipToUint32(net.ParseIP(plan.StaticStart).To4())
	end := ipToUint32(net.ParseIP(plan.StaticEnd).To4())
	current := ipToUint32(ip.To4())
	if current < start || current > end {
		return "", fmt.Errorf("固定 IP 必须使用保留地址段 %s 至 %s", plan.StaticStart, plan.StaticEnd)
	}
	return ip.To4().String(), nil
}

func uint32IP(value uint32) net.IP {
	ip := make(net.IP, net.IPv4len)
	binary.BigEndian.PutUint32(ip, value)
	return ip
}

func ipToUint32(ip net.IP) uint32 {
	return binary.BigEndian.Uint32(ip)
}
