// Command vpn-connect 是 OpenVPN client-connect 助手。
//
// OpenVPN 按连接调用本程序并传入推送文件路径；程序只读取本机策略文件，
// 按证书指纹或登录名写入该连接的路由推送。推送失败或身份未知时保持
// 最小权限：不写全局路由，也不放行额外内网。
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/dreamstation625/ovpanel/internal/connectpolicy"
)

const defaultPolicyPath = "/var/lib/ovpanel/connect-policy.json"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "vpn-connect: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("缺少 client-connect 推送文件路径")
	}
	output := os.Args[1]
	path := strings.TrimSpace(os.Getenv("OVPANEL_CONNECT_POLICY"))
	if path == "" {
		path = defaultPolicyPath
	}

	policy, found := connectpolicy.Policy{}, false
	if file, err := connectpolicy.Load(path); err == nil {
		policy, found = connectpolicy.Lookup(
			file,
			os.Getenv("tls_digest_sha256_0"),
			os.Getenv("username"),
		)
	}
	lines := []string{}
	if found {
		var err error
		lines, err = connectpolicy.ClientLines(policy)
		if err != nil {
			return err
		}
	}

	payload := ""
	if len(lines) > 0 {
		payload = strings.Join(lines, "\n") + "\n"
	}
	if err := os.WriteFile(output, []byte(payload), 0o600); err != nil {
		return fmt.Errorf("写入推送配置: %w", err)
	}
	fmt.Fprintf(os.Stderr, "vpn-connect: matched=%t global=%t routes=%d shared=%d\n",
		found, policy.GlobalExit, len(policy.AllowedInternal), len(policy.OwnClientNetworks))
	return nil
}
