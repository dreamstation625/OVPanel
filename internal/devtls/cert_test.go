package devtls

import (
	"net"
	"testing"
)

func TestCertificateCoversLoopbackAndLocalhost(t *testing.T) {
	cert, err := Certificate()
	if err != nil {
		t.Fatalf("生成证书失败：%v", err)
	}
	if cert.Leaf == nil {
		t.Fatal("应返回已解析的证书")
	}
	if cert.Leaf.Subject.CommonName != "OVPanel Development" {
		t.Fatalf("CommonName = %q", cert.Leaf.Subject.CommonName)
	}

	if err := cert.Leaf.VerifyHostname("localhost"); err != nil {
		t.Fatalf("localhost 校验失败：%v", err)
	}
	for _, ip := range []string{"127.0.0.1", "::1"} {
		if err := cert.Leaf.VerifyHostname(ip); err != nil {
			t.Fatalf("%s 校验失败：%v", ip, err)
		}
	}

	addrs := map[string]bool{}
	for _, ip := range cert.Leaf.IPAddresses {
		addrs[ip.String()] = true
	}
	if !addrs[net.IPv4(127, 0, 0, 1).String()] {
		t.Fatal("缺少 127.0.0.1")
	}

	if !cert.Leaf.NotAfter.After(cert.Leaf.NotBefore) {
		t.Fatal("有效期顺序异常")
	}
}
