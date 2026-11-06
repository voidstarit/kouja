package csi

import (
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
)

func writeTestTLS(t *testing.T) (certFile, keyFile, caFile string) {
	t.Helper()
	directory := t.TempDir()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "kouja-test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "kouja-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile = filepath.Join(directory, "tls.crt")
	keyFile = filepath.Join(directory, "tls.key")
	caFile = filepath.Join(directory, "ca.crt")
	_ = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), 0o600)
	_ = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600)
	_ = os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600)
	return
}

func TestNewKubernetesAttachmentLookup(t *testing.T) {
	kubeconfig := filepath.Join(t.TempDir(), "config")
	content := `apiVersion: v1
kind: Config
clusters:
- name: test
  cluster:
    server: https://127.0.0.1:1
    insecure-skip-tls-verify: true
contexts:
- name: test
  context:
    cluster: test
    user: test
current-context: test
users:
- name: test
  user: {}
`
	if err := os.WriteFile(kubeconfig, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewKubernetesAttachmentLookup(kubeconfig); err != nil {
		t.Fatalf("NewKubernetesAttachmentLookup: %v", err)
	}
	if _, err := NewKubernetesAttachmentLookup(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing kubeconfig did not fail")
	}
	if _, err := NewKubernetesPlacement(kubeconfig); err == nil {
		t.Fatal("placement discovery should fail for an unreachable API server")
	}
}

func TestLoadTLSConfigAndControllerLifecycle(t *testing.T) {
	certFile, keyFile, caFile := writeTestTLS(t)
	tlsConfig, err := LoadTLSConfig(certFile, keyFile, caFile)
	if err != nil {
		t.Fatalf("LoadTLSConfig: %v", err)
	}
	if _, err := NewController(nil, tlsConfig); err == nil {
		t.Fatal("empty controller endpoints did not fail")
	}
	controller, err := NewController([]string{"127.0.0.1:1"}, tlsConfig)
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	controller.SetAttachmentLookup(fakeAttachmentLookup{id: "id"})
	controller.SetPolicyLookup(fakePolicyLookup{policy: "wait"})
	controller.SetPlacement(&fakePlacement{records: map[string]PlacementRecord{}})
	controller.hosts = []*hostClient{{volume: &fakeVolumeClient{}, ready: true, uuid: "host-1"}}
	if !controller.Ready() {
		t.Fatal("controller should be ready with a ready host")
	}
	controller.Close()
	controller.Ready()
	if _, err := LoadTLSConfig(certFile, filepath.Join(t.TempDir(), "missing"), caFile); err == nil {
		t.Fatal("missing key did not fail")
	}
	if _, err := LoadTLSConfig(certFile, keyFile, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing CA did not fail")
	}
	if _, err := dialHost("bad://target", tlsConfig); err == nil {
		t.Fatal("invalid endpoint did not fail")
	}
	host, err := dialHost("127.0.0.1:1", tlsConfig)
	if err != nil {
		t.Fatalf("dialHost: %v", err)
	}
	_ = host.close()
	_ = host.close()
	if _, err := NewKubernetesPlacement(filepath.Join(t.TempDir(), "missing-kubeconfig")); err == nil {
		t.Fatal("missing kubeconfig did not fail")
	}
}
