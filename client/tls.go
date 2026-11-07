package client

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// LoadTLSConfig loads an mTLS client configuration from a certificate, a
// private key, and the CA used to verify the agent certificate.
func LoadTLSConfig(certFile, keyFile, caFile string) (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("client: load certificate: %w", err)
	}
	caPEM, err := os.ReadFile(caFile) //nolint:gosec // operator-selected CA path
	if err != nil {
		return nil, fmt.Errorf("client: read CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("client: no certificates found in %s", caFile)
	}
	return &tls.Config{ //nolint:gosec // kouja requires TLS 1.3 and hostname verification
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		RootCAs:      roots,
	}, nil
}
