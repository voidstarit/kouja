package adminui

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestCertificateIssueHelpers(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	server.options.Config.AdminUI.ClientCAKeyFile = ""
	server.options.Config.Listen.TLS.ClientCAFile = "/tmp/kouja/client-ca.crt"
	if path := server.clientCAKeyPath(); path != "/tmp/kouja/ca.key" {
		t.Fatalf("default CA key path = %q", path)
	}

	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalECPrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := parsePrivateKey(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ecDER})
	if err != nil || signer == nil {
		t.Fatalf("parse EC key = %v, %v", signer, err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parsePrivateKey(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}); err != nil {
		t.Fatalf("parse PKCS8 key: %v", err)
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parsePrivateKey(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)}); err != nil {
		t.Fatalf("parse RSA key: %v", err)
	}
	if _, err := parsePrivateKey(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("bad")}); err == nil {
		t.Fatal("invalid PKCS8 key accepted")
	}
	if _, err := parsePrivateKey(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("bad")}); err == nil {
		t.Fatal("unsupported PEM block accepted")
	}

	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // test directory must be private
		t.Fatal(err)
	}
	server.options.Config.AdminUI.ClientCertDir = dir
	if got, err := server.ensureClientCertDir(); err != nil || got != dir {
		t.Fatalf("ensure private dir = %q, %v", got, err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // deliberately unsafe test directory
		t.Fatal(err)
	}
	if _, err := server.ensureClientCertDir(); !errors.Is(err, errUnsafePath) {
		t.Fatalf("world-readable dir error = %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // test directory must be private
		t.Fatal(err)
	}
	linkDir := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, linkDir); err != nil {
		t.Fatal(err)
	}
	server.options.Config.AdminUI.ClientCertDir = linkDir
	if _, err := server.ensureClientCertDir(); !errors.Is(err, errUnsafePath) {
		t.Fatalf("symlink dir error = %v", err)
	}

	file, err := os.CreateTemp(dir, "exclusive-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	path := file.Name()
	if err := writeExclusive(path, []byte("x"), 0o600); !errors.Is(err, errConflict) {
		t.Fatalf("existing exclusive write error = %v", err)
	}
	if err := ensureMissing(path); !errors.Is(err, errConflict) {
		t.Fatalf("existing missing check error = %v", err)
	}
	if _, err := certificateFile(filepath.Join(dir, "missing"), "admin", "certificate"); !errors.Is(err, errInternal) {
		t.Fatalf("missing certificate file error = %v", err)
	}
	if _, err := certificateFile(path, "admin", "certificate"); err != nil {
		t.Fatalf("regular certificate file: %v", err)
	}
	if err := os.Symlink(path, filepath.Join(dir, "link.crt")); err != nil {
		t.Fatal(err)
	}
	if _, err := certificateFile(filepath.Join(dir, "link.crt"), "admin", "certificate"); !errors.Is(err, errInternal) {
		t.Fatalf("symlink certificate file error = %v", err)
	}
}

func TestClientCAKeyInsideCertificateDirectoryIsNotServed(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // test directory must be private
		t.Fatal(err)
	}
	caPath, caKeyPath := writeTestClientCA(t)
	keyData, err := os.ReadFile(caKeyPath) //nolint:gosec // test fixture
	if err != nil {
		t.Fatal(err)
	}
	customKey := filepath.Join(dir, "custom-ca.key")
	if err := os.WriteFile(customKey, keyData, 0o600); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	server.options.Config.AdminUI.ClientCertDir = dir
	server.options.Config.Listen.TLS.ClientCAFile = caPath
	server.options.Config.AdminUI.ClientCAKeyFile = customKey
	response := doRequest(t, server, http.MethodGet, "/api/certificates", nil, nil)
	var listing CertificatesResponse
	if err := jsonUnmarshal(response.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	for _, file := range listing.Files {
		if file.Name == "custom-ca.key" {
			t.Fatal("CA key was listed")
		}
	}
	response = doRequest(t, server, http.MethodGet, "/api/certificates/custom-ca.key", nil, nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("CA key download = %d", response.Code)
	}
}

func TestAdminUICreateCertificateErrors(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // test directory must be private
		t.Fatal(err)
	}
	caPath, caKeyPath := writeTestClientCA(t)
	server.options.Config.AdminUI.ClientCertDir = dir
	server.options.Config.Listen.TLS.ClientCAFile = caPath
	server.options.Config.AdminUI.ClientCAKeyFile = caKeyPath
	response := doRequest(t, server, http.MethodPost, "/api/certificates", []byte(`{}`), nil)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("missing content type = %d", response.Code)
	}
	response = doRequest(t, server, http.MethodPost, "/api/certificates", []byte(`{`), map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON = %d", response.Code)
	}
	response = doRequest(t, server, http.MethodPost, "/api/certificates", []byte(`{"role":"caller"}`), map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("missing name = %d", response.Code)
	}
	response = doRequest(t, server, http.MethodPost, "/api/certificates", []byte(`{"role":"admin","name":"operator"}`), map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusCreated {
		t.Fatalf("admin create = %d %s", response.Code, response.Body.String())
	}
	certData, err := os.ReadFile(filepath.Join(dir, "admin-operator.crt")) //nolint:gosec // test fixture
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certData)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.URIs) != 1 || cert.URIs[0].String() != "spiffe://kouja/admin/operator" {
		t.Fatalf("admin URI SANs = %#v", cert.URIs)
	}
}

func TestClientCAValidationErrors(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	caPath, caKeyPath := writeTestClientCA(t)
	server.options.Config.Listen.TLS.ClientCAFile = caPath
	server.options.Config.AdminUI.ClientCAKeyFile = caKeyPath
	if _, _, err := server.loadClientCA(); err != nil {
		t.Fatalf("valid CA = %v", err)
	}
	if err := os.Chmod(caKeyPath, 0o644); err != nil { //nolint:gosec // deliberately unsafe test key mode
		t.Fatal(err)
	}
	if _, _, err := server.loadClientCA(); !errors.Is(err, errInvalidConfig) {
		t.Fatalf("permissive CA key error = %v", err)
	}
	if err := os.Chmod(caKeyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	server.options.Config.Listen.TLS.ClientCAFile = filepath.Join(t.TempDir(), "missing")
	if _, _, err := server.loadClientCA(); !errors.Is(err, errInvalidConfig) {
		t.Fatalf("missing CA error = %v", err)
	}
	server.options.Config.Listen.TLS.ClientCAFile = caPath
	badKey := filepath.Join(t.TempDir(), "bad.key")
	if err := os.WriteFile(badKey, []byte("not pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	server.options.Config.AdminUI.ClientCAKeyFile = badKey
	if _, _, err := server.loadClientCA(); !errors.Is(err, errInvalidConfig) {
		t.Fatalf("bad CA key error = %v", err)
	}
}
