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
	plan, err := PlanAddresses(cfg.VPNNetwork)
	if err != nil {
		return "", "", err
	}

	var b strings.Builder
	b.WriteString("# OVPanel 生成的服务端配置；托管项不要手工修改\n")
	fmt.Fprintf(&b, "port %d\n", cfg.Port)
	proto := cfg.Protocol
	if proto == "tcp" {
		// 服务端模式不能只写 tcp，OpenVPN 需要明确 tcp-server。
		proto = "tcp-server"
	}
	fmt.Fprintf(&b, "proto %s\n", proto)
	b.WriteString("dev tun\n")
	b.WriteString("topology subnet\n")
	// 手动展开 server 指令，才能把动态池限制在网段末尾，其余地址开放给固定 IP。
	b.WriteString("mode server\n")
	b.WriteString("tls-server\n")
	fmt.Fprintf(&b, "ifconfig %s %s\n", plan.Server, plan.Mask)
	fmt.Fprintf(&b, "ifconfig-pool %s %s %s\n", plan.PoolStart, plan.PoolEnd, plan.Mask)
	fmt.Fprintf(&b, "ifconfig-pool-persist %s\n", PathPoolState)
	b.WriteString("push \"topology subnet\"\n")
	fmt.Fprintf(&b, "push \"route-gateway %s\"\n", plan.Server)
	fmt.Fprintf(&b, "ca %s\n", PathCA)
	fmt.Fprintf(&b, "cert %s\n", PathServerCert)
	fmt.Fprintf(&b, "key %s\n", PathServerKey)
	fmt.Fprintf(&b, "tls-crypt %s\n", PathTLSCrypt)
	fmt.Fprintf(&b, "crl-verify %s\n", PathCRL)
	b.WriteString("dh none\n")
	b.WriteString("ecdh-curve prime256v1\n")
	fmt.Fprintf(&b, "tls-version-min %s\n", cfg.TLSMinVersion)
	fmt.Fprintf(&b, "data-ciphers %s\n", JoinDataCiphers(cfg.DataCiphers))
	fmt.Fprintf(&b, "auth %s\n", cfg.AuthDigest)
	b.WriteString("verify-client-cert optional\n")
	b.WriteString("auth-user-pass-optional\n")
	b.WriteString("duplicate-cn\n")
	if cfg.MaxClients > 0 {
		fmt.Fprintf(&b, "max-clients %d\n", cfg.MaxClients)
	}
	fmt.Fprintf(&b, "management %s unix\n", ManagementSocket)
	b.WriteString("management-client-auth\n")
	fmt.Fprintf(&b, "keepalive %d %d\n", cfg.KeepaliveInterval, cfg.KeepaliveTimeout)
	b.WriteString("persist-key\n")
	b.WriteString("persist-tun\n")

	// 路由与目标网段权限按配置在 client-connect 中逐连接下发，
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
