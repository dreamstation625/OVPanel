package runtimeenv

import "testing"

func TestBlockersEmptyWhenReady(t *testing.T) {
	r := Report{
		Root:      true,
		TunDevice: true,
		Binaries:  map[string]bool{"openvpn": true, "nft": true, "tc": true, "ip": true},
		Modules:   map[string]bool{"ifb": true, "sch_htb": true, "act_mirred": true},
	}
	if got := blockers(r); len(got) != 0 {
		t.Fatalf("完整环境不应有阻断项：%v", got)
	}
}

func TestBlockersListMissingCapabilities(t *testing.T) {
	r := Report{
		Root:      false,
		TunDevice: false,
		Binaries:  map[string]bool{},
		Modules:   map[string]bool{},
	}
	got := blockers(r)
	if len(got) < 7 {
		t.Fatalf("缺失项应逐条列出，实际 %d 条：%v", len(got), got)
	}
}

func TestFirstLineSkipsBlankLines(t *testing.T) {
	if got := firstLine("\n  \nOpenVPN 2.7.7 x86_64\nsecond"); got != "OpenVPN 2.7.7 x86_64" {
		t.Fatalf("firstLine = %q", got)
	}
}
