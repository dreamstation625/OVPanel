//go:build !linux

package dockermaint

import (
	"fmt"
	"os"
)

// 非 Linux 仅用于本地测试；生产部署的所有者与路径校验在 state_linux.go。
func secureStateDirectory(path string) error {
	return os.MkdirAll(path, 0o700)
}

func validateStateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("维护状态必须是普通文件：%s", path)
	}
	return nil
}
