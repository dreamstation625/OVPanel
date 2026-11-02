package traffic

import (
	"strings"
	"testing"
)

func TestRateArg(t *testing.T) {
	cases := map[int64]string{
		10_000_000: "10mbit",
		1_500_000:  "1500kbit",
		100_000:    "100kbit",
		8_000:      "8kbit",
		7_999:      "7999bit",
	}
	for bps, want := range cases {
		if got := RateArg(bps); got != want {
			t.Fatalf("RateArg(%d) = %q，期望 %q", bps, got, want)
		}
	}
}

func TestRenderCreatesIndependentClassesPerSession(t *testing.T) {
	cmds, err := Render("tun0", "ifb0", []Session{
		{CID: "1", IP: "10.8.0.2", UploadBps: 5_000_000, DownloadBps: 10_000_000},
		{CID: "2", IP: "10.8.0.3", UploadBps: 5_000_000, DownloadBps: 10_000_000},
		{CID: "3", IP: "10.8.0.4"}, // 不限速，不生成 class
	})
	if err != nil {
		t.Fatalf("生成限速命令失败：%v", err)
	}
	classes := map[string]bool{}
	filters := 0
	for _, cmd := range cmds {
		line := strings.Join(cmd.Args, " ")
		if strings.Contains(line, "class replace") {
			for i, arg := range cmd.Args {
				if arg == "classid" && i+1 < len(cmd.Args) {
					classes[cmd.Args[i+1]] = true
				}
			}
		}
		if strings.Contains(line, " filter replace ") {
			filters++
		}
	}
	// 两条受限会话各自拥有上下行 class，共 4 个独立 classid（另含默认 class）。
	if len(classes) != 6 {
		t.Fatalf("classid 数量 = %d，期望 6：%v", len(classes), classes)
	}
	// 每条会话：下载分类、上传镜像、IFB 分类各一个 filter。
	if filters != 6 {
		t.Fatalf("filter 数量 = %d，期望 6", filters)
	}
	for _, line := range []string{
		"htb rate 10mbit",
		"htb rate 5mbit",
		"match ip dst 10.8.0.2/32",
		"match ip src 10.8.0.2/32",
		"match ip dst 10.8.0.3/32",
		"match ip src 10.8.0.3/32",
	} {
		found := false
		for _, cmd := range cmds {
			if strings.Contains(strings.Join(cmd.Args, " "), line) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("缺少命令片段：%q", line)
		}
	}
}

func TestRenderSkipsSessionsWithoutAddressOrSpeed(t *testing.T) {
	cmds, err := Render("tun0", "ifb0", []Session{
		{CID: "1", IP: "", UploadBps: 5_000_000},
		{CID: "2", IP: "10.8.0.2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 0 {
		t.Fatalf("无限速会话不应生成命令：%v", cmds)
	}
	if _, err := Render("tun0", "ifb0", []Session{{CID: "1", IP: "not-an-ip", UploadBps: 1}}); err == nil {
		t.Fatal("非法地址应返回错误")
	}
}

func TestClearTargetsOnlyProjectQueues(t *testing.T) {
	cmds := Clear("tun0", "ifb0")
	if len(cmds) != 3 {
		t.Fatalf("清理命令数 = %d", len(cmds))
	}
	for _, cmd := range cmds {
		line := strings.Join(cmd.Args, " ")
		if !strings.HasPrefix(line, "tc qdisc del dev ") {
			t.Fatalf("不应清理其他规则：%q", line)
		}
	}
}
