package serverconfig

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
)

// AddressPlan 是 VPN 网段内的地址规划。
// 动态池占网段中间约一半，固定 IP 使用首尾两段。
// 固定 IP 不能落在动态池、服务器地址、网络地址或广播地址上。
type AddressPlan struct {
	Network    string
	Mask       string
	Server     string
	PoolStart  string
	PoolEnd    string
	FixedStart string
	FixedEnd   string
}

// PlanAddresses 计算 VPN 网段的服务器地址、中间动态池和固定 IP 可用范围。
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
	size := uint64(1) << (32 - ones)
	// 动态池占中间一半，首尾各约四分之一留给固定 IP。
	// 这样低地址和原有末尾固定段都可用，动态池也有足够容量。
	poolStart := base + uint32(size/4)
	poolEnd := base + uint32(size*3/4-1)
	fixedStart := base + 2
	if poolStart <= base+1 || poolEnd >= broadcast {
		return AddressPlan{}, fmt.Errorf("VPN 网段太小，无法同时容纳动态池和固定 IP")
	}

	return AddressPlan{
		Network:    uint32IP(base).String(),
		Mask:       uint32IP(mask).String(),
		Server:     uint32IP(base + 1).String(),
		PoolStart:  uint32IP(poolStart).String(),
		PoolEnd:    uint32IP(poolEnd).String(),
		FixedStart: uint32IP(fixedStart).String(),
		FixedEnd:   uint32IP(last).String(),
	}, nil
}

// ValidateFixedIP 校验固定地址是否位于 VPN 网段内且避开动态池，并返回规范化地址。
func ValidateFixedIP(vpnNetwork, value string) (string, error) {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil || ip.To4() == nil {
		return "", fmt.Errorf("固定 IP 必须是 IPv4 地址")
	}
	plan, err := PlanAddresses(vpnNetwork)
	if err != nil {
		return "", err
	}
	current := ipToUint32(ip.To4())
	fixedStart := ipToUint32(net.ParseIP(plan.FixedStart).To4())
	fixedEnd := ipToUint32(net.ParseIP(plan.FixedEnd).To4())
	if current < fixedStart || current > fixedEnd {
		return "", fmt.Errorf("固定 IP 必须在 VPN 网段 %s 至 %s 内", plan.FixedStart, plan.FixedEnd)
	}
	poolStart := ipToUint32(net.ParseIP(plan.PoolStart).To4())
	poolEnd := ipToUint32(net.ParseIP(plan.PoolEnd).To4())
	if current >= poolStart && current <= poolEnd {
		return "", fmt.Errorf("固定 IP 不能使用动态池 %s 至 %s", plan.PoolStart, plan.PoolEnd)
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
