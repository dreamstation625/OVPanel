// Package devtls 生成仅用于本机开发的短期自签名证书。
//
// 生产部署应挂载正式证书；本包生成的材料只适合在 localhost 上调试 HTTPS，
// 不写入磁盘，也不作为任何信任链的一部分。
package devtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"os"
	"time"
)

// validFor 是开发证书的有效期。
const validFor = 7 * 24 * time.Hour

// Certificate 生成覆盖 localhost、回环地址与本机主机名的自签名证书。
func Certificate() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("生成开发证书私钥: %w", err)
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("生成开发证书序列号: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "OVPanel Development"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(validFor),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}
	if hostname, err := os.Hostname(); err == nil && hostname != "" && hostname != "localhost" {
		tmpl.DNSNames = append(tmpl.DNSNames, hostname)
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("签发开发证书: %w", err)
	}

	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("解析开发证书: %w", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
		Leaf:        leaf,
	}, nil
}
