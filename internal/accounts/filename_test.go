package accounts

import "testing"

func TestExportFilename(t *testing.T) {
	cases := []struct {
		displayName string
		profileName string
		want        string
	}{
		{"aaa", "默认配置", "aaa-默认配置.ovpn"},
		{"张三", "手机/平板", "张三-手机-平板.ovpn"},
		{"", "", "ovpanel.ovpn"},
		{"  ", "  ", "ovpanel.ovpn"},
	}
	for _, tc := range cases {
		if got := exportFilename(tc.displayName, tc.profileName); got != tc.want {
			t.Fatalf("文件名异常：got=%q want=%q", got, tc.want)
		}
	}
}
