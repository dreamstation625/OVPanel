package netpolicy

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadHostNetworks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host-networks.txt")
	content := "# 宿主网段\n192.168.31.0/24\n10.42.42.0/24\n192.168.31.0/24\nnot-a-cidr\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入测试文件失败：%v", err)
	}
	got, err := LoadHostNetworks(path)
	if err != nil {
		t.Fatalf("读取宿主网段失败：%v", err)
	}
	want := []string{"10.42.42.0/24", "192.168.31.0/24"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("宿主网段解析异常：got=%v want=%v", got, want)
	}
}

func TestLoadHostNetworksMissing(t *testing.T) {
	got, err := LoadHostNetworks(filepath.Join(t.TempDir(), "missing.txt"))
	if err != nil || got != nil {
		t.Fatalf("缺失文件应返回空列表：got=%v err=%v", got, err)
	}
}
