package dockermaint

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateDirectoryRejectsWritableParentsAndSymlinks(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "state")
	if err := secureStateDirectory(state); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := secureStateDirectory(state); err == nil {
		t.Fatal("Web 用户组可写的父目录必须拒绝")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(state, alias); err != nil {
		t.Fatal(err)
	}
	if err := secureStateDirectory(filepath.Join(alias, "nested")); err == nil {
		t.Fatal("符号链接父目录必须拒绝")
	}
}

func TestStateFileRejectsSharedPermissionsAndSymlinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateStateFile(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		t.Fatal(err)
	}
	if err := validateStateFile(path); err == nil {
		t.Fatal("组可读写的任务文件必须拒绝")
	}
	alias := path + ".link"
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if err := validateStateFile(alias); err == nil {
		t.Fatal("任务文件符号链接必须拒绝")
	}
}
