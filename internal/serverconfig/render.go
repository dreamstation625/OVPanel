package serverconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
)

// 运行代理在容器内使用的固定路径。
const (
	PathCA           = "/etc/openvpn/ovpanel/ca.crt"
	PathServerCert   = "/etc/openvpn/ovpanel/server.crt"
	PathServerKey    = "/etc/openvpn/ovpanel/server.key"
	PathTLSCrypt     = "/etc/openvpn/ovpanel/ta.key"
	PathCRL          = "/etc/openvpn/ovpanel/crl.pem"
	PathPoolState    = "/var/lib/ovpanel/ipp.txt"
	ManagementSocket = "/run/ovpanel/mgmt.sock"
	// PathConnectHelper 按登录身份为该连接写入路由推送。
	PathConnectHelper = "/usr/local/bin/vpn-connect"
)

// Render 生成服务端配置文本与 SHA-256 哈希。
// ipv4MaskString 以点分十进制输出 IPv4 掩码；IPMask.String 对 4 字节掩码
// 可能输出十六进制，不适合直接写入 OpenVPN 配置。
func ipv4MaskString(mask net.IPMask) string {
	if len(mask) != 4 {
		return mask.String()
	}
	return net.IP(mask).String()
}

func Render(input Config) (string, string, error) {
	cfg, err := Prepare(input)
	if err != nil {
		return "", "", err
	}
	_, network, err := net.ParseCIDR(cfg.VPNNetwork)
	if err != nil {
		return "", "", err
	}
	mask := ipv4MaskString(network.Mask)

	var b strings.Builder
	b.WriteString("# OVPanel 生成的服务端配置；托管项不要手工修改\n")
	fmt.Fprintf(&b, "port %d\n", cfg.Port)
	fmt.Fprintf(&b, "proto %s\n", cfg.Protocol)
	b.WriteString("dev tun\n")
	b.WriteString("topology subnet\n")
	fmt.Fprintf(&b, "server %s %s\n", network.IP.String(), mask)
	fmt.Fprintf(&b, "ifconfig-pool-persist %s\n", PathPoolState)
	fmt.Fprintf(&b, "ca %s\n", PathCA)
	fmt.Fprintf(&b, "cert %s\n", PathServerCert)
	fmt.Fprintf(&b, "key %s\n", PathServerKey)
	fmt.Fprintf(&b, "tls-crypt %s\n", PathTLSCrypt)
	fmt.Fprintf(&b, "crl-verify %s\n", PathCRL)
	b.WriteString("dh none\n")
	b.WriteString("ecdh-curve prime256v1\n")
	b.WriteString("tls-version-min 1.2\n")
	b.WriteString("data-ciphers AES-256-GCM:AES-128-GCM\n")
	b.WriteString("auth SHA256\n")
	b.WriteString("verify-client-cert optional\n")
	b.WriteString("auth-user-pass-optional\n")
	b.WriteString("duplicate-cn\n")
	fmt.Fprintf(&b, "management %s unix\n", ManagementSocket)
	b.WriteString("management-client-auth\n")
	b.WriteString("keepalive 10 60\n")
	b.WriteString("persist-key\n")
	b.WriteString("persist-tun\n")

	// 路由与内网权限按配置在 client-connect 中逐连接下发，
	// 不能在服务端全局 push，否则配置级覆盖无法生效。
	b.WriteString("script-security 2\n")
	fmt.Fprintf(&b, "client-connect %s\n", PathConnectHelper)
	for _, dns := range cfg.DNS {
		fmt.Fprintf(&b, "push \"dhcp-option DNS %s\"\n", dns)
	}
	if cfg.BlockIPv6 {
		b.WriteString("push \"block-ipv6\"\n")
	}
	b.WriteString("# 客户端互访由 nftables 统一控制，不使用 OpenVPN 内置转发\n")
	b.WriteString("verb 3\n")
	b.WriteString("mute-replay-warnings\n")
	for _, extra := range cfg.ExtraDirectives {
		b.WriteString(extra)
		b.WriteString("\n")
	}

	text := b.String()
	sum := sha256.Sum256([]byte(text))
	return text, hex.EncodeToString(sum[:]), nil
}
