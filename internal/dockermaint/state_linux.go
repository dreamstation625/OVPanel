package dockermaint

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// 维护状态承载宿主 Docker 规格。检查整条路径，避免 Web 通过可写父目录替换它。
func secureStateDirectory(path string) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !info.IsDir() || !ok || (stat.Uid != 0 && stat.Uid != uint32(os.Geteuid())) ||
			(info.Mode().Perm()&0o022 != 0 && (current == path || info.Mode()&os.ModeSticky == 0)) {
			return fmt.Errorf("维护状态路径可被其他用户替换：%s", current)
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	// 固定值文件供宿主 Compose 读取；目录仅维护用户可写，任务文件保持 0600。
	return os.Chmod(path, 0o755)
}

func validateStateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("维护状态文件所有者或权限不安全：%s", path)
	}
	return nil
}
