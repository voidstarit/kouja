package client

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"testing"
)

func bddTestLoadTLSConfig(t testing.TB) {
	certFile, keyFile, caFile := writeTestCertificates(t)
	config, err := LoadTLSConfig(certFile, keyFile, caFile)
	if err != nil {
		t.Fatalf("LoadTLSConfig: %v", err)
	}
	if config.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion = %x, want TLS 1.3", config.MinVersion)
	}
	if len(config.Certificates) != 1 {
		t.Fatalf("Certificates = %d, want 1", len(config.Certificates))
	}
	if config.RootCAs == nil {
		t.Fatal("RootCAs is nil")
	}

	missing := filepath.Join(t.TempDir(), "missing.crt")
	if _, err := LoadTLSConfig(missing, keyFile, caFile); err == nil {
		t.Fatal("LoadTLSConfig accepted a missing certificate")
	}
	if _, err := LoadTLSConfig(certFile, missing, caFile); err == nil {
		t.Fatal("LoadTLSConfig accepted a missing key")
	}

	badCA := filepath.Join(t.TempDir(), "bad-ca.crt")
	if err := os.WriteFile(badCA, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadTLSConfig(certFile, keyFile, badCA); err == nil {
		t.Fatal("LoadTLSConfig accepted a CA file without certificates")
	}
}
