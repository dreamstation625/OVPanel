package netpolicy

import (
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
)

// HostNetworksFileName 是宿主网段清单在控制目录中的文件名。
const HostNetworksFileName = "host-networks.txt"

// LoadHostNetworks 读取宿主网段清单。文件不存在时返回空列表；
// 空行、注释和非法行会被忽略，避免宿主辅助脚本的临时内容阻塞面板。
func LoadHostNetworks(path string) ([]string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("读取宿主网段清单: %w", err)
	}

	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, raw := range strings.Split(string(payload), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		_, network, err := net.ParseCIDR(line)
		if err != nil || network.IP.To4() == nil {
			continue
		}
		cidr := network.String()
		if _, ok := seen[cidr]; ok {
			continue
		}
		seen[cidr] = struct{}{}
		out = append(out, cidr)
	}
	sort.Strings(out)
	return out, nil
}
