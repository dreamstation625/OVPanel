package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ServerCertificate 是 OpenVPN 服务端证书及加密私钥。
type ServerCertificate struct {
	CertPEM       string
	KeyCiphertext []byte
	IssuedAt      time.Time
	ExpiresAt     time.Time
}

// ServerCertificate 读取服务端证书；尚未生成时返回 ok=false。
func (s *Store) ServerCertificate(ctx context.Context) (ServerCertificate, bool, error) {
	var (
		cert            ServerCertificate
		issued, expires string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT cert_pem, key_ciphertext, issued_at, expires_at
		   FROM server_certificate WHERE id = 1`,
	).Scan(&cert.CertPEM, &cert.KeyCiphertext, &issued, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return ServerCertificate{}, false, nil
	}
	if err != nil {
		return ServerCertificate{}, false, fmt.Errorf("读取服务端证书: %w", err)
	}
	cert.IssuedAt = parseTime(issued)
	cert.ExpiresAt = parseTime(expires)
	return cert, true, nil
}

// SaveServerCertificate 新增或更新服务端证书。
func (s *Store) SaveServerCertificate(ctx context.Context, cert ServerCertificate) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO server_certificate (id, cert_pem, key_ciphertext, issued_at, expires_at)
		 VALUES (1, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   cert_pem = excluded.cert_pem,
		   key_ciphertext = excluded.key_ciphertext,
		   issued_at = excluded.issued_at,
		   expires_at = excluded.expires_at`,
		cert.CertPEM, cert.KeyCiphertext, formatTime(cert.IssuedAt), formatTime(cert.ExpiresAt))
	if err != nil {
		return fmt.Errorf("保存服务端证书: %w", err)
	}
	return nil
}
