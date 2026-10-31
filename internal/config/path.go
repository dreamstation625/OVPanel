package config

import "path/filepath"

// joinPath 统一使用平台路径分隔符，便于在 Windows 上直接运行。
func joinPath(dir, name string) string {
	return filepath.Join(dir, name)
}
