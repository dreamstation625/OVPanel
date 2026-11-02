package netpolicy

import (
	"fmt"
	"reflect"
	"testing"
)

func TestNormalizeTargets(t *testing.T) {
	cases := []struct {
		name    string
		input   []string
		want    []string
		wantErr bool
	}{
		{name: "cidr", input: []string{"192.168.1.0/24"}, want: []string{"192.168.1.0/24"}},
		{name: "host bits", input: []string{"10.0.0.5/24"}, want: []string{"10.0.0.0/24"}},
		{name: "single ip", input: []string{"1.1.1.1"}, want: []string{"1.1.1.1/32"}},
		{name: "public", input: []string{"203.0.113.5/24"}, want: []string{"203.0.113.0/24"}},
		{name: "cgnat", input: []string{"100.64.1.2/10"}, want: []string{"100.64.0.0/10"}},
		{name: "dedupe", input: []string{"1.1.1.1", "1.1.1.1/32"}, want: []string{"1.1.1.1/32"}},
		{name: "empty", input: nil, want: []string{}},
		{name: "default route", input: []string{"0.0.0.0/0"}, wantErr: true},
		{name: "zero", input: []string{"0.0.0.0/8"}, wantErr: true},
		{name: "zero host", input: []string{"0.0.0.1"}, wantErr: true},
		{name: "loopback", input: []string{"127.0.0.1"}, wantErr: true},
		{name: "link local", input: []string{"169.254.1.1"}, wantErr: true},
		{name: "multicast", input: []string{"224.0.0.1"}, wantErr: true},
		{name: "reserved", input: []string{"240.0.0.1"}, wantErr: true},
		{name: "ipv6", input: []string{"2001:db8::/32"}, wantErr: true},
		{name: "invalid", input: []string{"bad"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeTargets(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("应拒绝 %v", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("规范失败：%v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("结果异常：got=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestNormalizeTargetsLimit(t *testing.T) {
	values := make([]string, 0, maxTargets+1)
	for i := 0; i <= maxTargets; i++ {
		values = append(values, fmt.Sprintf("198.51.%d.0/24", i))
	}
	if _, err := NormalizeTargets(values); err == nil {
		t.Fatal("超过数量上限应被拒绝")
	}
	if _, err := NormalizeTargets(values[:maxTargets]); err != nil {
		t.Fatalf("上限内的目标应被接受：%v", err)
	}
}

func TestPrepareTargetsRejectsPoolOverlap(t *testing.T) {
	if _, err := PrepareTargets("10.8.0.0/16", []string{"203.0.113.10/24"}); err != nil {
		t.Fatalf("公网目标应被接受：%v", err)
	}
	for _, value := range []string{"10.8.0.0/16", "10.8.0.0/24", "10.0.0.0/8"} {
		if _, err := PrepareTargets("10.8.0.0/16", []string{value}); err == nil {
			t.Fatalf("与 VPN 地址池重叠的目标应被拒绝：%s", value)
		}
	}
}
