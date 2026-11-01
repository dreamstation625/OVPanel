package accounts

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxExportNameRunes = 100

// exportFilename 生成“用户名-配置名.ovpn”；非法文件名字符替换为短横线。
func exportFilename(displayName, profileName string) string {
	parts := make([]string, 0, 2)
	for _, value := range []string{displayName, profileName} {
		if part := sanitizeFilePart(value); part != "" {
			parts = append(parts, part)
		}
	}
	base := strings.Join(parts, "-")
	if base == "" {
		base = "ovpanel"
	}
	return base + ".ovpn"
}

func sanitizeFilePart(value string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.TrimSpace(value) {
		if r < 32 || r == 127 || strings.ContainsRune(`<>:"/\|?*`, r) || unicode.IsSpace(r) {
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
			continue
		}
		b.WriteRune(r)
		lastDash = false
	}
	out := strings.Trim(b.String(), "-. ")
	if utf8.RuneCountInString(out) > maxExportNameRunes {
		out = string([]rune(out)[:maxExportNameRunes])
		out = strings.Trim(out, "-. ")
	}
	return out
}
