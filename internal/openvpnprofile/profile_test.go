package openvpnprofile

import (
	"strings"
	"testing"
)

func TestRenderSelfContainedProfile(t *testing.T) {
	content := string(Render(ClientConfig{
		Host:            "vpn.example.com",
		Port:            1194,
		Proto:           "udp",
		CAPEM:           "CA-CONTENT",
		CertPEM:         "CERT-CONTENT",
		KeyPEM:          "KEY-CONTENT",
		TLSCryptKey:     "TA-CONTENT",
		RequireUserPass: true,
	}))
	for _, want := range []string{
		"client\n", "dev tun\n", "proto udp\n", "remote vpn.example.com 1194\n", "pull\n",
		"auth-user-pass\n", "<ca>\nCA-CONTENT\n</ca>", "<cert>\nCERT-CONTENT\n</cert>",
		"<key>\nKEY-CONTENT\n</key>", "<tls-crypt>\nTA-CONTENT\n</tls-crypt>",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("缺少 %q：\n%s", want, content)
		}
	}
	if strings.Contains(content, "password") {
		t.Fatal(".ovpn 不应包含明文密码")
	}
}

func TestRenderCertificateOnlyOmitsAuthUserPass(t *testing.T) {
	content := string(Render(ClientConfig{
		Host: "10.0.0.1", Port: 443, Proto: "tcp",
		CAPEM: "CA", CertPEM: "CERT", KeyPEM: "KEY", TLSCryptKey: "TA",
	}))
	if strings.Contains(content, "auth-user-pass") {
		t.Fatal("仅证书配置不应要求账号密码")
	}
	if !strings.Contains(content, "proto tcp\n") {
		t.Fatal("协议未写入")
	}
}

func TestRenderPasswordOnlyHasNoCertificate(t *testing.T) {
	content := string(Render(ClientConfig{
		Host: "vpn.example.com", Port: 1194, Proto: "udp",
		CAPEM: "CA", TLSCryptKey: "TA", RequireUserPass: true,
	}))
	if strings.Contains(content, "<cert>") || strings.Contains(content, "<key>") {
		t.Fatal("仅密码配置不应包含客户端证书或私钥")
	}
	if !strings.Contains(content, "auth-user-pass\n") {
		t.Fatal("仅密码配置应要求账号密码")
	}
}

func TestGenerateTLSCryptKeyFormat(t *testing.T) {
	key, err := GenerateTLSCryptKey()
	if err != nil {
		t.Fatalf("生成失败：%v", err)
	}
	lines := strings.Split(strings.TrimSpace(key), "\n")
	if len(lines) != 18 {
		t.Fatalf("tls-crypt 行数 = %d，期望 18", len(lines))
	}
	if lines[0] != "-----BEGIN OpenVPN Static key V1-----" {
		t.Fatalf("头部异常：%q", lines[0])
	}
	if lines[len(lines)-1] != "-----END OpenVPN Static key V1-----" {
		t.Fatalf("尾部异常：%q", lines[len(lines)-1])
	}
	for _, line := range lines[1 : len(lines)-1] {
		if len(line) != 32 {
			t.Fatalf("密钥行长度 = %d，期望 32", len(line))
		}
	}
}
