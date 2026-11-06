package agent

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voidstarit/kouja/gen/kouja/v1"
	"github.com/voidstarit/kouja/internal/config"
	"github.com/voidstarit/kouja/internal/observability"
)

func agentTLSFiles(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "agent-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "agent"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	certPath := filepath.Join(dir, "server.crt")
	keyPath := filepath.Join(dir, "server.key")
	caPath := filepath.Join(dir, "ca.crt")
	_ = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), 0o600)
	_ = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600)
	_ = os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600)
	return certPath, keyPath, caPath
}

func TestNewServerLifecycleAndErrors(t *testing.T) {
	cert, key, ca := agentTLSFiles(t)
	cfg := config.Default()
	cfg.Listen.Address = "127.0.0.1:0"
	cfg.Listen.TLS.CertFile = cert
	cfg.Listen.TLS.KeyFile = key
	cfg.Listen.TLS.ClientCAFile = ca
	cfg.Targets.ISCSI.Enabled = false
	cfg.Targets.NVMeTCP.Enabled = false
	metrics := observability.New()
	server, err := NewServer(&cfg,
		&koujav1.UnimplementedVolumeServiceServer{},
		&koujav1.UnimplementedAdminServiceServer{},
		WithReadiness(func() bool { return true }),
		WithMetrics(metrics),
	)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if server.Address() == nil {
		t.Fatal("server has no address")
	}
	server.Stop()
	server.GracefulStop()

	bad := config.Default()
	bad.Listen.Address = "127.0.0.1:0"
	bad.Listen.TLS.CertFile = filepath.Join(t.TempDir(), "missing")
	if _, err := NewServer(&bad, &koujav1.UnimplementedVolumeServiceServer{}, &koujav1.UnimplementedAdminServiceServer{}); err == nil {
		t.Fatal("missing TLS did not fail")
	}
	bad = config.Default()
	bad.Listen.Address = "not-an-address"
	bad.Listen.TLS.CertFile = cert
	bad.Listen.TLS.KeyFile = key
	bad.Listen.TLS.ClientCAFile = ca
	if _, err := NewServer(&bad, &koujav1.UnimplementedVolumeServiceServer{}, &koujav1.UnimplementedAdminServiceServer{}); err == nil {
		t.Fatal("invalid listen address did not fail")
	}
}

func TestDependencyChecksAndReconcileLoop(t *testing.T) {
	previous := os.Getenv("PATH")
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty"))
	cfg := config.Default()
	if err := CheckDependencies(context.Background(), &cfg); err == nil {
		t.Fatal("missing host tools did not fail")
	}
	t.Setenv("PATH", previous)
	if err := requirePath("/definitely/missing/kouja", "test path"); err == nil {
		t.Fatal("missing dependency path did not fail")
	}
	store := newAgentStore(t)
	reconciler := NewReconciler(store, &fakeLVM{}, nil, 0)
	if reconciler.interval <= 0 {
		t.Fatal("default reconcile interval is not positive")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reconciler.Run(ctx)
	_ = WithReadiness(func() bool { return true })
	_ = WithDriftRetrier(nil)
}
