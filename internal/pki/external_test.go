package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voidstarit/kouja/internal/audit"
	"github.com/voidstarit/kouja/internal/config"
	"github.com/voidstarit/kouja/internal/naming"
	"github.com/voidstarit/kouja/internal/observability"
	"github.com/voidstarit/kouja/internal/state"
)

func TestRevokeExternalCertificate(t *testing.T) {
	store, err := state.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	certPath, keyPath := testCA(t)
	certDir := t.TempDir()
	//nolint:gosec // test directory needs execute permission
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
	service, err := New(Options{Config: cfg, Store: store, Audit: audit.NewRecorder(store, nil, observability.New()), Naming: naming.NewValidators()})
	if err != nil {
		t.Fatal(err)
	}
	caPEM, err := os.ReadFile(certPath) //nolint:gosec // test file
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(caPEM)
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM, err := os.ReadFile(keyPath) //nolint:gosec // test file
	if err != nil {
		t.Fatal(err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	keyAny, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	caKey := keyAny.(*ecdsa.PrivateKey)
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(42), Subject: pkix.Name{CommonName: "kouja-admin-old"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:        []*url.URL{{Scheme: "spiffe", Host: "kouja", Path: "/admin/old"}},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := service.RevokeExternal(t.Context(), Caller{Owner: "admin"}, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), "orphan")
	if err != nil {
		t.Fatal(err)
	}
	if record.SerialNumber != "2a" || !record.Revoked {
		t.Fatalf("record = %#v", record)
	}
	if !service.IsRevoked("2a") {
		t.Fatal("external serial is not enforced")
	}
}
