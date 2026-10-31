package store

import (
	"context"
	"fmt"
	"time"
)

// RevokedCertificate 是生成 CRL 所需的已吊销证书。
type RevokedCertificate struct {
	SerialNumber string
	CertPEM      string
	RevokedAt    time.Time
}

// RevokedCertificates 返回全部已吊销证书。
func (s *Store) RevokedCertificates(ctx context.Context) ([]RevokedCertificate, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT serial_number, cert_pem, revoked_at
		   FROM certificate
		  WHERE status = ? AND revoked_at IS NOT NULL
		  ORDER BY revoked_at`,
		CertificateRevoked)
	if err != nil {
		return nil, fmt.Errorf("读取已吊销证书: %w", err)
	}
	defer rows.Close()

	out := make([]RevokedCertificate, 0)
	for rows.Next() {
		var (
			cert    RevokedCertificate
			revoked string
		)
		if err := rows.Scan(&cert.SerialNumber, &cert.CertPEM, &revoked); err != nil {
			return nil, fmt.Errorf("读取已吊销证书: %w", err)
		}
		cert.RevokedAt = parseTime(revoked)
		out = append(out, cert)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取已吊销证书: %w", err)
	}
	return out, nil
}
