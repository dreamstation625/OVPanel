package clientroutes

import (
	"path/filepath"
	"strings"
	"testing"
)

func testManager(t *testing.T, commands *[][]string) *Manager {
	t.Helper()
	m := New(filepath.Join(t.TempDir(), "client-routes.json"))
	m.run = func(args ...string) ([]byte, error) {
		*commands = append(*commands, append([]string(nil), args...))
		return nil, nil
	}
	return m
}

func TestSyncBlackholeAndCleanup(t *testing.T) {
	var commands [][]string
	m := testManager(t, &commands)
	if err := m.Sync([]string{"192.168.50.0/24"}, false, "tun0"); err != nil {
		t.Fatalf("同步失败：%v", err)
	}
	if len(commands) != 1 ||
		strings.Join(commands[0], " ") != "route replace blackhole 192.168.50.0/24" {
		t.Fatalf("blackhole 命令异常：%v", commands)
	}
	if err := m.Sync([]string{"192.168.50.0/24"}, true, "tun0"); err != nil {
		t.Fatalf("切换 tun 失败：%v", err)
	}
	if len(commands) != 2 ||
		strings.Join(commands[1], " ") != "route replace 192.168.50.0/24 dev tun0" {
		t.Fatalf("tun 命令异常：%v", commands)
	}
	if err := m.Cleanup(); err != nil {
		t.Fatalf("清理失败：%v", err)
	}
	if len(commands) != 3 || strings.Join(commands[2], " ") != "route del 192.168.50.0/24" {
		t.Fatalf("清理命令异常：%v", commands)
	}
}

func TestSyncRetiresStaleNetworks(t *testing.T) {
	var commands [][]string
	m := testManager(t, &commands)
	if err := m.Sync([]string{"192.168.50.0/24", "192.168.60.0/24"}, true, "tun0"); err != nil {
		t.Fatalf("同步失败：%v", err)
	}
	if err := m.Sync([]string{"192.168.50.0/24"}, true, "tun0"); err != nil {
		t.Fatalf("同步失败：%v", err)
	}
	found := false
	for _, args := range commands {
		if strings.Join(args, " ") == "route replace blackhole 192.168.60.0/24" {
			found = true
		}
	}
	if !found {
		t.Fatalf("已移除的共享网段应改为 blackhole：%v", commands)
	}
	if err := m.Cleanup(); err != nil {
		t.Fatalf("清理失败：%v", err)
	}
	deleted := 0
	for _, args := range commands {
		if strings.Join(args, " ") == "route del 192.168.50.0/24" ||
			strings.Join(args, " ") == "route del 192.168.60.0/24" {
			deleted++
		}
	}
	if deleted != 2 {
		t.Fatalf("代理停止时应清理全部共享路由：%v", commands)
	}
}

func TestSyncRequiresTun(t *testing.T) {
	var commands [][]string
	m := testManager(t, &commands)
	if err := m.Sync([]string{"192.168.50.0/24"}, true, ""); err == nil {
		t.Fatal("viaTun 缺少设备应失败")
	}
	if len(commands) != 0 {
		t.Fatalf("失败前不应执行命令：%v", commands)
	}
}
