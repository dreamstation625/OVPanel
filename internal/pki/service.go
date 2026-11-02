// Package pki 管理项目 CA、客户端与服务端证书生命周期。
//
// CA、客户端与服务端私钥都以密文保存；本包只在签发或导出时短暂解密。
// 证书序列号、指纹与 CN 是可信标识，CN 仅用于展示，不作为唯一身份。
package pki

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/dreamstation625/ovpanel/internal/secretbox"
	"github.com/dreamstation625/ovpanel/internal/store"
	"github.com/google/uuid"
)

const (
	caKeyAAD            = "ovpanel-ca-key-v1"
	certKeyAAD          = "ovpanel-cert-key:"
	serverKeyAAD        = "ovpanel-server-key-v1"
	caValidityYears     = 20
	clientValidityYears = 15
	serverValidityYears = 15
	rsaBits             = 2048
)

// ErrNoActiveCertificate 表示配置没有可导出的生效证书。
var ErrNoActiveCertificate = errors.New("配置没有生效证书")

// ErrCANotCreated 表示项目 CA 尚未生成。
var ErrCANotCreated = errors.New("CA 尚未创建")

func certificateNotAfter(now time.Time, years int, ca *x509.Certificate) time.Time {
	notAfter := now.AddDate(years, 0, 0)
	if ca != nil {
		limit := ca.NotAfter.Add(-24 * time.Hour)
		if notAfter.After(limit) {
			return limit
		}
	}
	return notAfter
}

// CAInfo 是可展示的 CA 信息，不包含私钥。
type CAInfo struct {
	Exists      bool
	Subject     string
	Serial      string
	Fingerprint string
	NotBefore   time.Time
	NotAfter    time.Time
}

// ServerCertInfo 是可展示的服务端证书信息，不包含私钥。
type ServerCertInfo struct {
	Exists      bool
	Subject     string
	Serial      string
	Fingerprint string
	NotBefore   time.Time
	NotAfter    time.Time
}

// Service 负责 CA、客户端与服务端证书。
type Service struct {
	store *store.Store
	keyMu sync.RWMutex
	key   secretbox.Key
	now   func() time.Time

	mu sync.Mutex
}

// New 创建 PKI 服务。
func New(st *store.Store, key secretbox.Key) *Service {
	return &Service{store: st, key: key, now: func() time.Time { return time.Now().UTC() }}
}

// SetKey 在恢复备份后替换内存中的主密钥；磁盘主密钥由恢复流程同步更新。
func (s *Service) SetKey(key secretbox.Key) {
	s.keyMu.Lock()
	s.key = key
	s.keyMu.Unlock()
}

func (s *Service) seal(plaintext, aad []byte) ([]byte, error) {
	s.keyMu.RLock()
	key := s.key
	s.keyMu.RUnlock()
	return key.Seal(plaintext, aad)
}

func (s *Service) open(blob, aad []byte) ([]byte, error) {
	s.keyMu.RLock()
	key := s.key
	s.keyMu.RUnlock()
	return key.Open(blob, aad)
}

// Check 在启动时验证已有 CA 可用；不存在时不做任何事。
// 主密钥不匹配时返回错误，避免面板静默进入无法签发/导出的状态。
func (s *Service) Check(ctx context.Context) error {
	ca, ok, err := s.store.CA(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	_, _, err = s.loadCA(ca)
	return err
}

// CAInfo 返回当前 CA 的公开信息；尚未创建时 Exists=false。
func (s *Service) CAInfo(ctx context.Context) (CAInfo, error) {
	ca, ok, err := s.store.CA(ctx)
	if err != nil {
		return CAInfo{}, err
	}
	if !ok {
		return CAInfo{Exists: false}, nil
	}
	cert, err := parseCertificate(ca.CertPEM)
	if err != nil {
		return CAInfo{}, fmt.Errorf("解析 CA 证书: %w", err)
	}
	return CAInfo{
		Exists:      true,
		Subject:     cert.Subject.String(),
		Serial:      strings.ToUpper(cert.SerialNumber.Text(16)),
		Fingerprint: fingerprint(cert),
		NotBefore:   cert.NotBefore.UTC(),
		NotAfter:    cert.NotAfter.UTC(),
	}, nil
}

// BuildClientCertificate 生成客户端证书与私钥，但不写入数据库。
// 调用方在同一业务事务中把返回的记录与 profile 一起保存。
func (s *Service) BuildClientCertificate(ctx context.Context, profileID, commonName string) (store.CertificateRecord, string, error) {
	caCert, signer, caPEM, err := s.ensureCA(ctx)
	if err != nil {
		return store.CertificateRecord{}, "", err
	}

	clientKey, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		return store.CertificateRecord{}, "", fmt.Errorf("生成客户端私钥: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return store.CertificateRecord{}, "", err
	}
	now := s.now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{"OVPanel"}},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              certificateNotAfter(now, clientValidityYears, caCert),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &clientKey.PublicKey, signer)
	if err != nil {
		return store.CertificateRecord{}, "", fmt.Errorf("签发客户端证书: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return store.CertificateRecord{}, "", fmt.Errorf("解析客户端证书: %w", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(clientKey)
	if err != nil {
		return store.CertificateRecord{}, "", fmt.Errorf("编码客户端私钥: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	certID := uuid.NewString()
	ciphertext, err := s.seal(keyPEM, []byte(certKeyAAD+certID))
	if err != nil {
		return store.CertificateRecord{}, "", err
	}

	record := store.CertificateRecord{
		Certificate: store.Certificate{
			ID:           certID,
			ProfileID:    profileID,
			SerialNumber: strings.ToUpper(leaf.SerialNumber.Text(16)),
			Fingerprint:  fingerprint(leaf),
			CommonName:   leaf.Subject.CommonName,
			Status:       store.CertificateActive,
			IssuedAt:     now,
			ExpiresAt:    leaf.NotAfter.UTC(),
		},
		CertPEM:       string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		KeyCiphertext: ciphertext,
	}
	return record, caPEM, nil
}

// CAPEM 返回项目 CA 证书 PEM，不涉及私钥。
func (s *Service) CAPEM(ctx context.Context) (string, error) {
	ca, ok, err := s.store.CA(ctx)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrCANotCreated
	}
	return ca.CertPEM, nil
}

// CRLPEM 生成当前吊销列表的 PEM。
func (s *Service) CRLPEM(ctx context.Context) (string, error) {
	caCert, signer, _, err := s.ensureCA(ctx)
	if err != nil {
		return "", err
	}
	revoked, err := s.store.RevokedCertificates(ctx)
	if err != nil {
		return "", err
	}
	entries := make([]x509.RevocationListEntry, 0, len(revoked))
	for _, item := range revoked {
		serial := new(big.Int)
		value := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(item.SerialNumber)), "0x")
		if _, ok := serial.SetString(value, 16); !ok {
			continue
		}
		when := item.RevokedAt
		if when.IsZero() {
			when = s.now()
		}
		entries = append(entries, x509.RevocationListEntry{
			SerialNumber:   serial,
			RevocationTime: when.UTC(),
		})
	}
	number, err := randomSerial()
	if err != nil {
		return "", err
	}
	now := s.now()
	template := &x509.RevocationList{
		Number:                    number,
		ThisUpdate:                now.Add(-5 * time.Minute),
		NextUpdate:                now.Add(7 * 24 * time.Hour),
		RevokedCertificateEntries: entries,
	}
	der, err := x509.CreateRevocationList(rand.Reader, template, caCert, signer)
	if err != nil {
		return "", fmt.Errorf("签发 CRL: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der})), nil
}

// EnsureCAPEM 在需要导出仅密码配置时也保证 CA 存在。
func (s *Service) EnsureCAPEM(ctx context.Context) (string, error) {
	_, _, caPEM, err := s.ensureCA(ctx)
	if err != nil {
		return "", err
	}
	return caPEM, nil
}

// ExportMaterial 返回导出 .ovpn 所需的 CA、客户端证书与私钥明文。
// 私钥只在调用方内存中短暂存在，不落盘、不写日志。
func (s *Service) ExportMaterial(ctx context.Context, profileID string) (caPEM, certPEM, keyPEM string, err error) {
	caPEM, err = s.CAPEM(ctx)
	if err != nil {
		return "", "", "", err
	}

	cert, ok, err := s.store.ActiveCertificateMaterial(ctx, profileID)
	if err != nil {
		return "", "", "", err
	}
	if !ok {
		return "", "", "", ErrNoActiveCertificate
	}

	keyDER, err := s.open(cert.KeyCiphertext, []byte(certKeyAAD+cert.ID))
	if err != nil {
		return "", "", "", fmt.Errorf("解密客户端私钥: %w", err)
	}
	return caPEM, cert.CertPEM, string(keyDER), nil
}

// ServerCertificateInfo 返回当前服务端证书的公开信息；尚未签发时 Exists=false。
func (s *Service) ServerCertificateInfo(ctx context.Context) (ServerCertInfo, error) {
	record, ok, err := s.store.ServerCertificate(ctx)
	if err != nil {
		return ServerCertInfo{}, err
	}
	if !ok {
		return ServerCertInfo{}, nil
	}
	cert, err := parseCertificate(record.CertPEM)
	if err != nil {
		return ServerCertInfo{}, fmt.Errorf("解析服务端证书: %w", err)
	}
	return ServerCertInfo{
		Exists:      true,
		Subject:     cert.Subject.String(),
		Serial:      strings.ToUpper(cert.SerialNumber.Text(16)),
		Fingerprint: fingerprint(cert),
		NotBefore:   cert.NotBefore.UTC(),
		NotAfter:    cert.NotAfter.UTC(),
	}, nil
}

// RenewServerCertificate 用现有私钥重新签发服务端证书并更新有效期。
// 客户端只信任 CA，续签后不需要重新下载 .ovpn。
func (s *Service) RenewServerCertificate(ctx context.Context) (ServerCertInfo, error) {
	var existingKey []byte
	if record, ok, err := s.store.ServerCertificate(ctx); err != nil {
		return ServerCertInfo{}, err
	} else if ok {
		if keyDER, err := s.open(record.KeyCiphertext, []byte(serverKeyAAD)); err == nil {
			if _, parseErr := x509.ParsePKCS8PrivateKey(keyDER); parseErr == nil {
				existingKey = keyDER
			}
		}
		// 旧私钥无法解密或解析时生成新私钥，证书仍由同一个 CA 签发。
	}
	if _, _, err := s.issueServerCertificate(ctx, existingKey); err != nil {
		return ServerCertInfo{}, err
	}
	return s.ServerCertificateInfo(ctx)
}

// EnsureServerCertificate 返回 OpenVPN 服务端证书与私钥明文。
// 已有证书仍有效时复用；否则生成新的 serverAuth 证书。
func (s *Service) EnsureServerCertificate(ctx context.Context) (certPEM, keyPEM string, err error) {
	now := s.now()
	if existing, ok, err := s.store.ServerCertificate(ctx); err != nil {
		return "", "", err
	} else if ok && existing.ExpiresAt.After(now.Add(24*time.Hour)) {
		keyDER, err := s.open(existing.KeyCiphertext, []byte(serverKeyAAD))
		if err == nil {
			if _, parseErr := x509.ParsePKCS8PrivateKey(keyDER); parseErr == nil {
				// 复用已有证书时也必须输出 PEM。直接返回 DER 会让 OpenVPN
				// 把私钥当作无法解析的加密材料并拒绝启动。
				keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
				return existing.CertPEM, string(keyPEM), nil
			}
		}
		// 旧材料无法解密或解析时生成新证书，避免静默使用坏材料。
	}
	return s.issueServerCertificate(ctx, nil)
}

func (s *Service) issueServerCertificate(ctx context.Context, existingKey []byte) (certPEM, keyPEM string, err error) {
	caCert, signer, _, err := s.ensureCA(ctx)
	if err != nil {
		return "", "", err
	}

	var key *rsa.PrivateKey
	if len(existingKey) > 0 {
		if parsed, err := x509.ParsePKCS8PrivateKey(existingKey); err == nil {
			if rsaKey, ok := parsed.(*rsa.PrivateKey); ok {
				key = rsaKey
			}
		}
	}
	if key == nil {
		key, err = rsa.GenerateKey(rand.Reader, rsaBits)
		if err != nil {
			return "", "", fmt.Errorf("生成服务端私钥: %w", err)
		}
	}

	now := s.now()
	notAfter := certificateNotAfter(now, serverValidityYears, caCert)
	if !notAfter.After(now) {
		return "", "", errors.New("CA 已过期，无法签发服务端证书")
	}
	serial, err := randomSerial()
	if err != nil {
		return "", "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "OVPanel Server", Organization: []string{"OVPanel"}},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, signer)
	if err != nil {
		return "", "", fmt.Errorf("签发服务端证书: %w", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return "", "", fmt.Errorf("解析服务端证书: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", fmt.Errorf("编码服务端私钥: %w", err)
	}
	ciphertext, err := s.seal(keyDER, []byte(serverKeyAAD))
	if err != nil {
		return "", "", err
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	if err := s.store.SaveServerCertificate(ctx, store.ServerCertificate{
		CertPEM:       certPEM,
		KeyCiphertext: ciphertext,
		IssuedAt:      now,
		ExpiresAt:     leaf.NotAfter.UTC(),
	}); err != nil {
		return "", "", err
	}
	return certPEM, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})), nil
}

func (s *Service) ensureCA(ctx context.Context) (*x509.Certificate, crypto.Signer, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ca, ok, err := s.store.CA(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	if ok {
		cert, signer, err := s.loadCA(ca)
		if err != nil {
			return nil, nil, "", err
		}
		return cert, signer, ca.CertPEM, nil
	}

	key, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		return nil, nil, "", fmt.Errorf("生成 CA 私钥: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, "", err
	}
	now := s.now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "OVPanel Local CA",
			Organization: []string{"OVPanel"},
		},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.AddDate(caValidityYears, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, "", fmt.Errorf("签发 CA: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, "", fmt.Errorf("解析 CA: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, "", fmt.Errorf("编码 CA 私钥: %w", err)
	}
	ciphertext, err := s.seal(keyDER, []byte(caKeyAAD))
	if err != nil {
		return nil, nil, "", err
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))

	record := store.CA{
		CertPEM:       certPEM,
		KeyCiphertext: ciphertext,
		CreatedAt:     now,
		ExpiresAt:     cert.NotAfter.UTC(),
	}
	if err := s.store.CreateCA(ctx, record); err != nil {
		return nil, nil, "", err
	}
	return cert, key, certPEM, nil
}

func (s *Service) loadCA(ca store.CA) (*x509.Certificate, crypto.Signer, error) {
	keyDER, err := s.open(ca.KeyCiphertext, []byte(caKeyAAD))
	if err != nil {
		return nil, nil, fmt.Errorf("解密 CA 私钥（主密钥可能不匹配）: %w", err)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(keyDER)
	if err != nil {
		return nil, nil, fmt.Errorf("解析 CA 私钥: %w", err)
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, nil, errors.New("CA 私钥不支持签名")
	}
	cert, err := parseCertificate(ca.CertPEM)
	if err != nil {
		return nil, nil, err
	}
	if !cert.IsCA {
		return nil, nil, errors.New("保存的 CA 证书未标记为 CA")
	}
	return cert, signer, nil
}

func parseCertificate(certPEM string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("证书 PEM 格式非法")
	}
	return x509.ParseCertificate(block.Bytes)
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("生成证书序列号: %w", err)
	}
	if serial.Sign() == 0 {
		return nil, errors.New("生成的证书序列号为零")
	}
	return serial, nil
}

func fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}
