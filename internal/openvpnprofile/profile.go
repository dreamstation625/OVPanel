// Package openvpnprofile 生成自包含的客户端 .ovpn 配置。
//
// 私钥、客户端证书、CA 与 tls-crypt 密钥都内联在文件中；
// 账号密码认证只写入 auth-user-pass，不写入任何明文密码。
package openvpnprofile

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// ClientConfig 是渲染客户端配置所需的材料。
type ClientConfig struct {
	Host            string
	Port            int
	Proto           string
	DataCiphers     string
	AuthDigest      string
	TLSMinVersion   string
	CAPEM           string
	CertPEM         string
	KeyPEM          string
	TLSCryptKey     string
	RequireUserPass bool
}

// Render 生成 .ovpn 内容。证书类字段为空时不写入对应块。
func Render(c ClientConfig) []byte {
	tlsMinVersion := c.TLSMinVersion
	if tlsMinVersion == "" {
		tlsMinVersion = "1.2"
	}
	dataCiphers := c.DataCiphers
	if dataCiphers == "" {
		dataCiphers = "AES-256-GCM:AES-128-GCM"
	}
	authDigest := c.AuthDigest
	if authDigest == "" {
		authDigest = "SHA256"
	}

	var b strings.Builder
	b.WriteString("client\n")
	b.WriteString("dev tun\n")
	b.WriteString("proto " + c.Proto + "\n")
	b.WriteString(fmt.Sprintf("remote %s %d\n", c.Host, c.Port))
	b.WriteString("pull\n")
	b.WriteString("resolv-retry infinite\n")
	b.WriteString("nobind\n")
	b.WriteString("persist-key\n")
	b.WriteString("persist-tun\n")
	b.WriteString("remote-cert-tls server\n")
	b.WriteString("tls-version-min " + tlsMinVersion + "\n")
	b.WriteString("data-ciphers " + dataCiphers + "\n")
	b.WriteString("auth " + authDigest + "\n")
	b.WriteString("verb 3\n")
	b.WriteString("mute-replay-warnings\n")
	if c.RequireUserPass {
		b.WriteString("auth-user-pass\n")
	}
	b.WriteString("\n")

	writeBlock(&b, "ca", c.CAPEM)
	writeBlock(&b, "cert", c.CertPEM)
	writeBlock(&b, "key", c.KeyPEM)
	writeBlock(&b, "tls-crypt", c.TLSCryptKey)
	return []byte(b.String())
}

// GenerateTLSCryptKey 生成 OpenVPN tls-crypt static key 文本。
func GenerateTLSCryptKey() (string, error) {
	raw := make([]byte, 256)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("生成 tls-crypt 密钥: %w", err)
	}
	encoded := hex.EncodeToString(raw)

	var b strings.Builder
	b.WriteString("-----BEGIN OpenVPN Static key V1-----\n")
	for i := 0; i < len(encoded); i += 32 {
		b.WriteString(encoded[i : i+32])
		b.WriteString("\n")
	}
	b.WriteString("-----END OpenVPN Static key V1-----\n")
	return b.String(), nil
}

func writeBlock(b *strings.Builder, tag, content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	b.WriteString("<" + tag + ">\n")
	b.WriteString(content)
	b.WriteString("\n</" + tag + ">\n\n")
}
