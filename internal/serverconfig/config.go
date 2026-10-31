// Package serverconfig 定义面板托管的 OpenVPN 服务端配置。
//
// 配置在这里统一校验并生成固定文本；网页只能编辑已知字段和兼容清单中的
// 额外指令，不能覆盖认证、管理接口、证书、路由执行等托管项。
package serverconfig

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

// RouteMode 是客户端默认路由方式。
type RouteMode string

const (
	RouteSplit  RouteMode = "split"
	RouteGlobal RouteMode = "global"
)

// Config 是一份完整的服务端期望配置。
type Config struct {
	VPNNetwork       string    `json:"vpnNetwork"`
	Protocol         string    `json:"protocol"`
	Port             int       `json:"port"`
	DNS              []string  `json:"dns"`
	RouteMode        RouteMode `json:"routeMode"`
	AllowedInternal  []string  `json:"allowedInternal"`
	AllowInterClient bool      `json:"allowInterClient"`
	BlockIPv6        bool      `json:"blockIpv6"`
	ExtraDirectives  []string  `json:"extraDirectives"`
}

// Defaults 返回默认配置：UDP/1194、10.8.0.0/16、内网分流、互访关闭、阻断 IPv6。
func Defaults() Config {
	return Config{
		VPNNetwork:       "10.8.0.0/16",
		Protocol:         "udp",
		Port:             1194,
		DNS:              []string{},
		RouteMode:        RouteSplit,
		AllowedInternal:  []string{},
		AllowInterClient: false,
		BlockIPv6:        true,
		ExtraDirectives:  []string{},
	}
}

// Prepare 规范化并校验配置，返回可直接保存/渲染的版本。
func Prepare(input Config) (Config, error) {
	cfg := normalize(input)

	ip, network, err := net.ParseCIDR(cfg.VPNNetwork)
	if err != nil || network.IP.To4() == nil {
		return Config{}, fmt.Errorf("VPN 网段必须是合法 IPv4 CIDR")
	}
	if !ip.Equal(network.IP) {
		return Config{}, fmt.Errorf("VPN 网段必须使用网络地址，不能包含主机位")
	}
	ones, bits := network.Mask.Size()
	if bits != 32 || ones < 16 || ones > 24 {
		return Config{}, fmt.Errorf("VPN 网段前缀需在 /16 到 /24 之间")
	}
	if network.IP.IsLoopback() || network.IP.IsLinkLocalUnicast() || network.IP.IsMulticast() || network.IP.IsUnspecified() {
		return Config{}, fmt.Errorf("VPN 网段不能使用回环、链路本地、组播或未指定地址")
	}

	switch cfg.Protocol {
	case "udp", "tcp":
	default:
		return Config{}, fmt.Errorf("协议只能是 udp 或 tcp")
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return Config{}, fmt.Errorf("端口需在 1 到 65535 之间")
	}
	switch cfg.RouteMode {
	case RouteSplit, RouteGlobal:
	default:
		return Config{}, fmt.Errorf("路由方式只能是 split 或 global")
	}

	if len(cfg.DNS) > 4 {
		return Config{}, fmt.Errorf("DNS 最多 4 个")
	}
	for _, entry := range cfg.DNS {
		ip := net.ParseIP(entry)
		if ip == nil || ip.To4() == nil {
			return Config{}, fmt.Errorf("DNS 必须是 IPv4 地址：%q", entry)
		}
	}

	if len(cfg.AllowedInternal) > 20 {
		return Config{}, fmt.Errorf("授权内网网段最多 20 个")
	}
	for _, raw := range cfg.AllowedInternal {
		ip, cidr, err := net.ParseCIDR(raw)
		if err != nil || ip.To4() == nil {
			return Config{}, fmt.Errorf("授权内网网段必须是合法 IPv4 CIDR：%q", raw)
		}
		if ones, bits := cidr.Mask.Size(); bits != 32 || ones == 0 {
			return Config{}, fmt.Errorf("授权内网网段不能是 0.0.0.0/0：%q", raw)
		}
		if !ip.Equal(cidr.IP) {
			return Config{}, fmt.Errorf("授权内网网段必须使用网络地址：%q", raw)
		}
		if cidr.Contains(network.IP) || network.Contains(cidr.IP) {
			return Config{}, fmt.Errorf("授权内网网段不能与 VPN 地址池重叠：%q", raw)
		}
	}

	extras, err := normalizeExtraDirectives(cfg.ExtraDirectives)
	if err != nil {
		return Config{}, err
	}
	cfg.ExtraDirectives = extras
	return cfg, nil
}

// SupportedDirectives 返回额外指令兼容清单，供界面提示。
func SupportedDirectives() []string {
	names := make([]string, 0, len(directiveSpecs))
	for name := range directiveSpecs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func normalize(input Config) Config {
	cfg := input
	cfg.VPNNetwork = strings.TrimSpace(cfg.VPNNetwork)
	cfg.Protocol = strings.ToLower(strings.TrimSpace(cfg.Protocol))
	cfg.Port = input.Port
	cfg.RouteMode = RouteMode(strings.ToLower(strings.TrimSpace(string(input.RouteMode))))
	cfg.DNS = uniqueStrings(input.DNS)
	cfg.AllowedInternal = uniqueStrings(input.AllowedInternal)
	if cfg.DNS == nil {
		cfg.DNS = []string{}
	}
	if cfg.AllowedInternal == nil {
		cfg.AllowedInternal = []string{}
	}
	if cfg.ExtraDirectives == nil {
		cfg.ExtraDirectives = []string{}
	}
	return cfg
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
