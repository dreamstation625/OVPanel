// Package clientroutes 维护客户端共享内网在服务端主路由表中的动态路由。
//
// 共享网段不写入 OpenVPN 配置，因此增删网段不需要重启 VPN。路由在
// VPN 运行时指向 tun，停止时改为 blackhole；A 离线时 OpenVPN 没有
// iroute，流量会在 tun 内被丢弃，不会从默认路由泄漏。
package clientroutes

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Manager 管理一组由 OVPanel 创建的共享内网路由。
type Manager struct {
	statePath string
	run       func(args ...string) ([]byte, error)
	mu        sync.Mutex
	managed   map[string]struct{}
	modes     map[string]bool
}

// New 创建路由管理器并读取上次运行留下的路由清单。
func New(statePath string) *Manager {
	m := &Manager{
		statePath: statePath,
		run: func(args ...string) ([]byte, error) {
			return exec.Command("ip", args...).CombinedOutput()
		},
		managed: map[string]struct{}{},
		modes:   map[string]bool{},
	}
	m.load()
	return m
}

// Sync 把路由同步到期望状态。networks 必须已规范化；viaTun 为 true 时
// 指向 tun，否则使用 blackhole。不再存在的网段会被删除。
func (m *Manager) Sync(networks []string, viaTun bool, tun string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	desired := make([]string, 0, len(networks))
	seen := map[string]struct{}{}
	for _, raw := range networks {
		cidr := strings.TrimSpace(raw)
		if cidr == "" {
			continue
		}
		if ip, _, err := net.ParseCIDR(cidr); err != nil || ip.To4() == nil {
			return fmt.Errorf("共享内网路由非法：%q", raw)
		}
		if _, ok := seen[cidr]; ok {
			continue
		}
		seen[cidr] = struct{}{}
		desired = append(desired, cidr)
	}
	sort.Strings(desired)
	if viaTun && strings.TrimSpace(tun) == "" {
		return errors.New("共享内网路由缺少 tun 设备")
	}

	var firstErr error
	for _, cidr := range desired {
		if _, ok := m.managed[cidr]; ok {
			if mode, exists := m.modes[cidr]; exists && mode == viaTun {
				continue
			}
		}
		var args []string
		if viaTun {
			args = []string{"route", "replace", cidr, "dev", tun}
		} else {
			args = []string{"route", "replace", "blackhole", cidr}
		}
		if out, err := m.run(args...); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("同步共享内网路由 %s 失败：%w（%s）", cidr, err, strings.TrimSpace(string(out)))
			}
			continue
		}
		m.modes[cidr] = viaTun
	}
	// 已移除的共享网段不能再回落到默认路由；保留 blackhole，直到代理停止时清理。
	for cidr := range m.managed {
		if _, ok := seen[cidr]; ok {
			continue
		}
		if mode, ok := m.modes[cidr]; ok && !mode {
			continue
		}
		if out, err := m.run("route", "replace", "blackhole", cidr); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("回收共享内网路由 %s 失败：%w（%s）", cidr, err, strings.TrimSpace(string(out)))
			}
			continue
		}
		m.modes[cidr] = false
	}
	for cidr := range seen {
		m.managed[cidr] = struct{}{}
	}
	if err := m.save(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// Cleanup 删除本进程管理的全部共享内网路由，并清空状态文件。
func (m *Manager) Cleanup() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var firstErr error
	for cidr := range m.managed {
		if out, err := m.run("route", "del", cidr); err != nil && !missingRoute(out) {
			if firstErr == nil {
				firstErr = fmt.Errorf("清理共享内网路由 %s 失败：%w（%s）", cidr, err, strings.TrimSpace(string(out)))
			}
		}
	}
	m.managed = map[string]struct{}{}
	m.modes = map[string]bool{}
	if err := m.save(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func (m *Manager) load() {
	if m.statePath == "" {
		return
	}
	payload, err := os.ReadFile(m.statePath)
	if err != nil {
		return
	}
	var networks []string
	if err := json.Unmarshal(payload, &networks); err != nil {
		return
	}
	for _, cidr := range networks {
		if _, _, err := net.ParseCIDR(cidr); err == nil {
			m.managed[cidr] = struct{}{}
		}
	}
}

func (m *Manager) save() error {
	if m.statePath == "" {
		return nil
	}
	networks := make([]string, 0, len(m.managed))
	for cidr := range m.managed {
		networks = append(networks, cidr)
	}
	sort.Strings(networks)
	payload, err := json.Marshal(networks)
	if err != nil {
		return fmt.Errorf("编码共享内网路由: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(m.statePath), 0o700); err != nil {
		return fmt.Errorf("创建共享内网路由目录: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.statePath), ".client-routes-*")
	if err != nil {
		return fmt.Errorf("创建共享内网路由临时文件: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return fmt.Errorf("写入共享内网路由: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, m.statePath); err != nil {
		return fmt.Errorf("替换共享内网路由: %w", err)
	}
	return nil
}

func missingRoute(output []byte) bool {
	text := strings.ToLower(string(output))
	return strings.Contains(text, "no such process") ||
		strings.Contains(text, "cannot find device") ||
		strings.Contains(text, "no such file or directory")
}
