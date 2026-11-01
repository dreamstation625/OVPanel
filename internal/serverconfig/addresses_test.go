package serverconfig

import "testing"

func TestPlanAddresses(t *testing.T) {
	plan, err := PlanAddresses("10.8.0.0/16")
	if err != nil {
		t.Fatalf("规划地址失败：%v", err)
	}
	if plan.Server != "10.8.0.1" || plan.PoolStart != "10.8.64.0" ||
		plan.PoolEnd != "10.8.191.255" || plan.FixedStart != "10.8.0.2" ||
		plan.FixedEnd != "10.8.255.254" || plan.Mask != "255.255.0.0" {
		t.Fatalf("地址规划异常：%+v", plan)
	}

	plan, err = PlanAddresses("10.20.0.0/24")
	if err != nil {
		t.Fatalf("规划 /24 失败：%v", err)
	}
	if plan.PoolStart != "10.20.0.64" || plan.PoolEnd != "10.20.0.191" ||
		plan.FixedStart != "10.20.0.2" || plan.FixedEnd != "10.20.0.254" {
		t.Fatalf("/24 地址规划异常：%+v", plan)
	}
}

func TestValidateFixedIP(t *testing.T) {
	for _, value := range []string{
		"10.8.0.50", "10.8.1.2", "10.8.63.255",
		"10.8.192.0", "10.8.255.200", "10.8.255.254",
	} {
		if got, err := ValidateFixedIP("10.8.0.0/16", value); err != nil || got != value {
			t.Fatalf("固定 IP %s 应通过：%q %v", value, got, err)
		}
	}
	for _, value := range []string{
		"", "bad", "10.8.0.0", "10.8.0.1", "10.8.64.0", "10.8.100.10",
		"10.8.191.255", "10.8.255.255", "11.0.0.1",
	} {
		if _, err := ValidateFixedIP("10.8.0.0/16", value); err == nil {
			t.Fatalf("固定 IP %q 应被拒绝", value)
		}
	}
}

func TestRenderReservesDynamicPool(t *testing.T) {
	text, _, err := Render(Defaults())
	if err != nil {
		t.Fatalf("渲染失败：%v", err)
	}
	want := "ifconfig-pool 10.8.64.0 10.8.191.255 255.255.0.0"
	if !containsLine(text, want) {
		t.Fatalf("渲染缺少动态池：%s", text)
	}
}

func containsLine(text, want string) bool {
	for _, line := range splitLines(text) {
		if line == want {
			return true
		}
	}
	return false
}

func splitLines(text string) []string {
	out := []string{}
	start := 0
	for i, r := range text {
		if r == '\n' {
			out = append(out, text[start:i])
			start = i + 1
		}
	}
	return append(out, text[start:])
}
