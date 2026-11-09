package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voidstarit/kouja/internal/audit"
	"github.com/voidstarit/kouja/internal/config"
	"github.com/voidstarit/kouja/internal/naming"
	"github.com/voidstarit/kouja/internal/observability"
	"github.com/voidstarit/kouja/internal/state"
	"io"
	"log/slog"
)

func testCA(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath = filepath.Join(dir, "client-ca.crt")
	keyPath = filepath.Join(dir, "ca.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func TestIssueAndRevoke(t *testing.T) {
	store, err := state.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	certPath, keyPath := testCA(t)
	certDir := t.TempDir()
	if err := os.Chmod(certDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Listen: config.ListenConfig{TLS: config.TLSConfig{ClientCAFile: certPath}},
		CertificateAuthority: config.CertificateAuthorityConfig{
			Enabled: true, KeyFile: keyPath, CertificateDir: certDir,
			RevocationLogFile: filepath.Join(t.TempDir(), "revoked.log"),
			DefaultTTLDays:    30, MaxTTLDays: 365, MaxActiveCertificates: 10,
		},
		State: config.StateConfig{AuditRetentionDays: 90},
	}
	recorder := audit.NewRecorder(store, slog.New(slog.NewTextHandler(io.Discard, nil)), observability.New())
	service, err := New(Options{Config: cfg, Store: store, Audit: recorder, Naming: naming.NewValidators()})
	if err != nil {
		t.Fatal(err)
	}
	if !service.Ready() {
		t.Fatalf("service is not ready: %v", service.CAState())
	}
	issued, err := service.Issue(t.Context(), Caller{Owner: "admin", SerialNumber: "issuer"}, IssueRequest{Role: "admin", Name: "bootstrap", TTLDays: 1})
	if err != nil {
		t.Fatal(err)
	}
	if issued.SerialNumber == "" || issued.CertificatePEM == "" || issued.PrivateKeyPEM == "" {
		t.Fatalf("issued = %#v", issued)
	}
	record, err := service.Get(t.Context(), issued.SerialNumber)
	if err != nil || record.SerialNumber != issued.SerialNumber {
		t.Fatalf("get = %#v, %v", record, err)
	}
	page, err := service.List(t.Context(), ListOptions{PageSize: 10})
	if err != nil || len(page.Certificates) != 1 {
		t.Fatalf("list = %#v, %v", page, err)
	}
	revoked, verdicts, err := service.Revoke(t.Context(), Caller{Owner: "admin"}, issued.SerialNumber, "test")
	if err != nil || !revoked.Revoked {
		t.Fatalf("revoke = %#v, %#v, %v", revoked, verdicts, err)
	}
	if !service.IsRevoked(issued.SerialNumber) {
		t.Fatal("revoked serial is not enforced")
	}
	if _, err := os.Stat(record.CertFile); !os.IsNotExist(err) {
		t.Fatalf("certificate file still exists: %v", err)
	}
}
