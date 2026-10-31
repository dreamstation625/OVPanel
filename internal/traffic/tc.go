// Package traffic 按会话生成 tc/IFB 限速命令。
//
// 下载在 TUN 出方向按目标 VPN 地址分类；上传在 TUN 入方向镜像到 IFB 后
// 按来源 VPN 地址分类。每条连接各自拥有 HTB class，因此同一配置的多个
// 连接各自享有配置速度，而不是合计速度。
//
// 命令以 argv 形式执行，不经过 shell；参数只来自已校验的地址与整数限速值。
package traffic

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// Session 是一条在线连接的限速参数；UploadBps/DownloadBps 为 0 表示不限速。
type Session struct {
	CID         string
	IP          string
	UploadBps   int64
	DownloadBps int64
}

// Command 是一条待执行命令。
type Command struct {
	Args           []string
	IgnoreIfErrors []string
}

// RateArg 把 bit/s 转换为 tc 速率参数。
func RateArg(bps int64) string {
	switch {
	case bps <= 0:
		return "0bit"
	case bps%1_000_000 == 0:
		return fmt.Sprintf("%dmbit", bps/1_000_000)
	case bps%1000 == 0:
		return fmt.Sprintf("%dkbit", bps/1000)
	default:
		return fmt.Sprintf("%dbit", bps)
	}
}

// Render 生成完整限速命令；没有限速会话时返回 nil。
func Render(tun, ifb string, sessions []Session) ([]Command, error) {
	tun = strings.TrimSpace(tun)
	if tun == "" {
		return nil, fmt.Errorf("TUN 设备名为空")
	}
	if strings.TrimSpace(ifb) == "" {
		ifb = "ifb0"
	}

	type limited struct {
		ip string
		up int64
		dl int64
	}
	items := make([]limited, 0, len(sessions))
	seen := map[string]struct{}{}
	for _, session := range sessions {
		if session.IP == "" || (session.UploadBps <= 0 && session.DownloadBps <= 0) {
			continue
		}
		ip := net.ParseIP(strings.TrimSpace(session.IP))
		if ip == nil || ip.To4() == nil {
			return nil, fmt.Errorf("会话地址不是合法 IPv4：%q", session.IP)
		}
		if _, dup := seen[ip.String()]; dup {
			return nil, fmt.Errorf("会话地址重复：%q", session.IP)
		}
		seen[ip.String()] = struct{}{}
		items = append(items, limited{ip: ip.String(), up: session.UploadBps, dl: session.DownloadBps})
	}
	if len(items) == 0 {
		return nil, nil
	}

	cmds := make([]Command, 0, 10+len(items)*4)
	// 下载：TUN 根队列 + 每条连接一个 HTB class。
	cmds = append(cmds,
		Command{Args: []string{"tc", "qdisc", "replace", "dev", tun, "root", "handle", "1:", "htb", "default", "10"}},
		Command{Args: []string{"tc", "class", "replace", "dev", tun, "parent", "1:", "classid", "1:10", "htb", "rate", "1000mbit"}},
	)
	// 上传：TUN 入方向镜像到 IFB，再在 IFB 上分类。
	cmds = append(cmds,
		Command{
			Args:           []string{"ip", "link", "add", ifb, "type", "ifb"},
			IgnoreIfErrors: []string{"File exists", "already exists"},
		},
		Command{Args: []string{"ip", "link", "set", ifb, "up"}},
		Command{Args: []string{"tc", "qdisc", "replace", "dev", tun, "handle", "ffff:", "ingress"}},
		Command{Args: []string{"tc", "qdisc", "replace", "dev", ifb, "root", "handle", "2:", "htb", "default", "20"}},
		Command{Args: []string{"tc", "class", "replace", "dev", ifb, "parent", "2:", "classid", "2:20", "htb", "rate", "1000mbit"}},
	)

	for i, item := range items {
		dlClass := fmt.Sprintf("1:%d", 100+i)
		ulClass := fmt.Sprintf("2:%d", 100+i)
		if item.dl > 0 {
			cmds = append(cmds,
				Command{Args: []string{"tc", "class", "replace", "dev", tun, "parent", "1:",
					"classid", dlClass, "htb", "rate", RateArg(item.dl)}},
				Command{Args: []string{"tc", "filter", "replace", "dev", tun, "parent", "1:",
					"protocol", "ip", "prio", "1", "u32", "match", "ip", "dst", item.ip + "/32", "flowid", dlClass}},
			)
		}
		if item.up > 0 {
			cmds = append(cmds,
				Command{Args: []string{"tc", "filter", "replace", "dev", tun, "parent", "ffff:",
					"protocol", "ip", "prio", "1", "u32", "match", "ip", "src", item.ip + "/32",
					"action", "mirred", "egress", "redirect", "dev", ifb}},
				Command{Args: []string{"tc", "class", "replace", "dev", ifb, "parent", "2:",
					"classid", ulClass, "htb", "rate", RateArg(item.up)}},
				Command{Args: []string{"tc", "filter", "replace", "dev", ifb, "parent", "2:",
					"protocol", "ip", "prio", "1", "u32", "match", "ip", "src", item.ip + "/32", "flowid", ulClass}},
			)
		}
	}
	return cmds, nil
}

// Clear 生成清理项目队列的命令；只删除 root/ingress 队列，不清空其他配置。
func Clear(tun, ifb string) []Command {
	tun = strings.TrimSpace(tun)
	if tun == "" {
		return nil
	}
	if strings.TrimSpace(ifb) == "" {
		ifb = "ifb0"
	}
	// 没有 ingress 队列时，tc 在部分版本返回 "Invalid handle"；
	// 默认 fq_codel 根队列的 handle 为 0，也不能直接删除。
	ignore := []string{"No such file", "Cannot delete", "Cannot find", "not found",
		"Invalid handle", "handle of zero"}
	return []Command{
		{Args: []string{"tc", "qdisc", "del", "dev", tun, "root"}, IgnoreIfErrors: ignore},
		{Args: []string{"tc", "qdisc", "del", "dev", tun, "ingress"}, IgnoreIfErrors: ignore},
		{Args: []string{"tc", "qdisc", "del", "dev", ifb, "root"}, IgnoreIfErrors: ignore},
	}
}

// Apply 依次执行命令；返回第一处真实错误。
func Apply(cmds []Command) error {
	for _, cmd := range cmds {
		out, err := exec.Command(cmd.Args[0], cmd.Args[1:]...).CombinedOutput()
		if err == nil {
			continue
		}
		text := string(out)
		ignored := false
		for _, marker := range cmd.IgnoreIfErrors {
			if strings.Contains(text, marker) {
				ignored = true
				break
			}
		}
		if ignored {
			continue
		}
		return fmt.Errorf("执行 %s 失败：%w（%s）", strings.Join(cmd.Args, " "), err, strings.TrimSpace(text))
	}
	return nil
}
