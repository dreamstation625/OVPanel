// Package runtimeenv 采集 OpenVPN 容器内的数据面能力。
//
// P02 只建立运行骨架：这里的结果用于在部署时说明 TUN、nftables、tc 与
// IFB 是否可用，不能代表 OpenVPN 已启动或客户端已可连接。
package runtimeenv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Report 是一次环境采集结果。
type Report struct {
	CollectedAt time.Time `json:"collectedAt"`
	OS          string    `json:"os"`
	Arch        string    `json:"arch"`
	Kernel      string    `json:"kernel"`
	Root        bool      `json:"root"`
	CapEff      string    `json:"capEff,omitempty"`

	TunDevice bool `json:"tunDevice"`

	Binaries map[string]bool   `json:"binaries"`
	Versions map[string]string `json:"versions"`
	Modules  map[string]bool   `json:"modules"`

	EnvironmentReady bool     `json:"environmentReady"`
	Blockers         []string `json:"blockers"`
}

// binaryNames 是需要探测的固定命令集合。
var binaryNames = []string{"openvpn", "nft", "tc", "ip", "modprobe"}

// moduleNames 是数据面所需的内核模块。
var moduleNames = []string{"ifb", "sch_htb", "act_mirred"}

// Probe 采集当前进程可见的运行环境。命令均以固定 argv 执行，不经过 shell。
func Probe() Report {
	r := Report{
		CollectedAt: time.Now().UTC(),
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		Root:        os.Geteuid() == 0,
		Binaries:    map[string]bool{},
		Versions:    map[string]string{},
		Modules:     map[string]bool{},
	}

	if data, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		r.Kernel = strings.TrimSpace(string(data))
	} else {
		r.Kernel = "unknown"
	}

	if _, err := os.Stat("/dev/net/tun"); err == nil {
		r.TunDevice = true
	}
	if data, err := os.ReadFile("/proc/self/status"); err == nil {
		r.CapEff = fieldValue(string(data), "CapEff")
	}

	for _, name := range binaryNames {
		path, err := exec.LookPath(name)
		r.Binaries[name] = err == nil
		if err != nil {
			continue
		}
		r.Versions[name] = binaryVersion(path, name)
	}

	modules := moduleSet()
	for _, name := range moduleNames {
		r.Modules[name] = modules[name]
	}

	r.Blockers = blockers(r)
	r.EnvironmentReady = len(r.Blockers) == 0
	return r
}

// String 生成便于日志阅读的一行结论。
func (r Report) String() string {
	state := "READY"
	if !r.EnvironmentReady {
		state = "BLOCKED"
	}
	return fmt.Sprintf("env=%s/%s tun=%v openvpn=%v nft=%v tc=%v ifb=%v htb=%v mirred=%v => %s",
		r.OS, r.Arch, r.TunDevice, r.Binaries["openvpn"], r.Binaries["nft"],
		r.Binaries["tc"], r.Modules["ifb"], r.Modules["sch_htb"], r.Modules["act_mirred"], state)
}

func blockers(r Report) []string {
	var out []string
	if !r.Root {
		out = append(out, "当前进程不是 root，无法在容器网络命名空间内配置 nftables 与 tc")
	}
	if !r.TunDevice {
		out = append(out, "缺少 /dev/net/tun，无法运行 TUN 模式的 OpenVPN")
	}
	if !r.Binaries["openvpn"] {
		out = append(out, "镜像内缺少 openvpn 命令")
	}
	if !r.Binaries["nft"] {
		out = append(out, "缺少 nft 命令，无法实施访问控制")
	}
	if !r.Binaries["tc"] {
		out = append(out, "缺少 tc 命令，无法实施按连接限速")
	}
	if !r.Binaries["ip"] {
		out = append(out, "缺少 ip 命令，无法创建 IFB 设备")
	}
	if !r.Modules["ifb"] {
		out = append(out, "内核未加载 ifb 模块，上传方向限速无法实施")
	}
	if !r.Modules["sch_htb"] {
		out = append(out, "内核未加载 sch_htb 模块，HTB 限速无法实施")
	}
	if !r.Modules["act_mirred"] {
		out = append(out, "内核未加载 act_mirred 模块，入方向镜像无法实施")
	}
	return out
}

// moduleSet 合并 /proc/modules 与 /sys/module 的结果：
// 前者包含已加载模块，后者也覆盖内建模块。
func moduleSet() map[string]bool {
	out := map[string]bool{}
	if data, err := os.ReadFile("/proc/modules"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			name := firstField(line)
			if name == "" {
				continue
			}
			out[name] = true
			out[strings.ReplaceAll(name, "-", "_")] = true
		}
	}

	entries, err := os.ReadDir("/sys/module")
	if err == nil {
		for _, entry := range entries {
			out[entry.Name()] = true
			out[strings.ReplaceAll(entry.Name(), "-", "_")] = true
		}
	}
	return out
}

func binaryVersion(path, name string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var args []string
	switch name {
	case "openvpn":
		args = []string{"--version"}
	case "nft":
		args = []string{"--version"}
	case "tc":
		args = []string{"-V"}
	case "ip":
		args = []string{"-V"}
	default:
		return ""
	}

	out, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if err != nil && len(out) == 0 {
		return ""
	}
	line := firstLine(string(out))
	return truncate(line, 160)
}

func firstField(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func fieldValue(status, key string) string {
	for _, line := range strings.Split(status, "\n") {
		if strings.HasPrefix(line, key+":") {
			return strings.TrimSpace(strings.TrimPrefix(line, key+":"))
		}
	}
	return ""
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
