package serverconfig

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// directiveSpec 描述额外指令兼容清单中的一个条目。
type directiveSpec struct {
	MinArgs int
	MaxArgs int
	Min     int
	Max     int
}

// directiveSpecs 是允许的额外指令白名单。所有参数都必须是整数，
// 避免把网页输入拼成任意 OpenVPN 参数。
var directiveSpecs = map[string]directiveSpec{
	"max-routes-per-client": {1, 1, 1, 10000},
	"sndbuf":                {1, 1, 1024, 268435456},
	"rcvbuf":                {1, 1, 1024, 268435456},
	"txqueuelen":            {1, 1, 1, 10000},
	"tun-mtu":               {1, 1, 576, 9000},
	"mssfix":                {0, 1, 0, 9000},
	"reneg-sec":             {1, 1, 0, 604800},
	"tls-timeout":           {1, 1, 1, 60},
	"hand-window":           {1, 1, 1, 600},
	"tran-window":           {1, 1, 1, 3600},
}

// managedDirectives 是面板托管或明确禁止从额外指令覆盖的项。
var managedDirectives = map[string]struct{}{
	"port": {}, "proto": {}, "dev": {}, "dev-type": {}, "topology": {},
	"server": {}, "server-bridge": {}, "server-ipv6": {}, "push": {},
	"route": {}, "route-gateway": {}, "redirect-gateway": {}, "route-nopull": {},
	"ca": {}, "cert": {}, "key": {}, "dh": {}, "ecdh-curve": {},
	"tls-crypt": {}, "tls-auth": {}, "tls-version-min": {}, "data-ciphers": {}, "auth": {},
	"management": {}, "management-client-auth": {}, "management-client-pf": {},
	"client-to-client": {}, "duplicate-cn": {}, "verify-client-cert": {},
	"auth-user-pass-optional": {}, "auth-user-pass-verify": {}, "auth-user-pass": {},
	"plugin": {}, "script-security": {}, "up": {}, "down": {},
	"client-connect": {}, "client-disconnect": {}, "learn-address": {},
	"ifconfig-pool-persist": {}, "user": {}, "group": {}, "chroot": {},
	"daemon": {}, "log": {}, "status": {}, "status-version": {},
	"management-log-cache": {}, "max-clients": {}, "block-ipv6": {},
	"keepalive": {}, "ping": {}, "ping-restart": {}, "ping-timer-rem": {},
	"verb": {}, "mute": {}, "mute-replay-warnings": {},
}

// normalizeExtraDirectives 解析并规范化额外指令。
func normalizeExtraDirectives(lines []string) ([]string, error) {
	if len(lines) > 50 {
		return nil, fmt.Errorf("额外指令不能超过 50 条")
	}
	total := 0
	out := make([]string, 0, len(lines))
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		total += len(line)
		if total > 8192 {
			return nil, fmt.Errorf("额外指令总长度不能超过 8 KiB")
		}
		if len(line) > 200 {
			return nil, fmt.Errorf("单条额外指令不能超过 200 个字符")
		}

		tokens, err := tokenizeDirective(line)
		if err != nil {
			return nil, fmt.Errorf("额外指令格式不正确：%q", line)
		}
		name := strings.ToLower(tokens[0])
		if _, denied := managedDirectives[name]; denied {
			return nil, fmt.Errorf("指令 %q 由面板托管，不能写入额外指令", name)
		}
		spec, ok := directiveSpecs[name]
		if !ok {
			return nil, fmt.Errorf("额外指令 %q 不在兼容清单中", name)
		}
		args := tokens[1:]
		if len(args) < spec.MinArgs || len(args) > spec.MaxArgs {
			return nil, fmt.Errorf("指令 %q 的参数数量不合法", name)
		}
		for _, arg := range args {
			value, err := strconv.Atoi(arg)
			if err != nil || value < spec.Min || value > spec.Max {
				return nil, fmt.Errorf("指令 %q 的参数 %q 超出允许范围", name, arg)
			}
		}
		out = append(out, strings.Join(tokens, " "))
	}
	return out, nil
}

// tokenizeDirective 支持单引号、双引号与反斜杠转义，但只允许单行文本。
func tokenizeDirective(line string) ([]string, error) {
	var (
		tokens []string
		cur    strings.Builder
		quote  rune
		escape bool
	)
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}

	for _, r := range line {
		switch {
		case escape:
			cur.WriteRune(r)
			escape = false
		case r == '\\' && quote != '\'':
			escape = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case unicode.IsSpace(r):
			flush()
		case unicode.IsControl(r):
			return nil, fmt.Errorf("包含控制字符")
		default:
			cur.WriteRune(r)
		}
	}
	if escape {
		return nil, fmt.Errorf("反斜杠后缺少字符")
	}
	if quote != 0 {
		return nil, fmt.Errorf("引号未闭合")
	}
	flush()
	if len(tokens) == 0 {
		return nil, fmt.Errorf("空指令")
	}
	return tokens, nil
}
