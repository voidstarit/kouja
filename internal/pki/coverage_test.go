package pki

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voidstarit/kouja/internal/config"
	"github.com/voidstarit/kouja/internal/state"
)

func TestLoadSigningKeyBranches(t *testing.T) {
	_, keyPath := testCA(t)
	valid, err := os.ReadFile(keyPath) //nolint:gosec // test key
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := loadSigningKey(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing key did not fail")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(keyPath, link); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSigningKey(link); err == nil {
		t.Fatal("symlink key did not fail")
	}
	if _, err := loadSigningKey(dir); err == nil {
		t.Fatal("directory key did not fail")
	}
	permissive := filepath.Join(dir, "permissive")
	if err := os.WriteFile(permissive, valid, 0o644); err != nil { //nolint:gosec // deliberate mode test
		t.Fatal(err)
	}
	if _, err := loadSigningKey(permissive); err == nil {
		t.Fatal("permissive key did not fail")
	}
	badPEM := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(badPEM, []byte("not-pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSigningKey(badPEM); err == nil {
		t.Fatal("bad PEM did not fail")
	}
	encrypted := filepath.Join(dir, "encrypted.pem")
	encryptedBlock := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Headers: map[string]string{"Proc-Type": "4,ENCRYPTED"}, Bytes: []byte("ciphertext")})
	if err := os.WriteFile(encrypted, encryptedBlock, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSigningKey(encrypted); err == nil {
		t.Fatal("encrypted key did not fail")
	}
	unsupported := filepath.Join(dir, "unsupported.pem")
	if err := os.WriteFile(unsupported, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("invalid")}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSigningKey(unsupported); err == nil {
		t.Fatal("unsupported key did not fail")
	}
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	rsaPath := filepath.Join(dir, "rsa.pem")
	if err := os.WriteFile(rsaPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSigningKey(rsaPath); err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8Path := filepath.Join(dir, "pkcs8.pem")
	if err := os.WriteFile(pkcs8Path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSigningKey(pkcs8Path); err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalECPrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	ecPath := filepath.Join(dir, "ec.pem")
	if err := os.WriteFile(ecPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: ecDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadSigningKey(ecPath); err != nil {
		t.Fatal(err)
	}
}

func TestPKIHelpersAndDirectoryValidation(t *testing.T) {
	if _, _, err := certificateIdentity(&x509.Certificate{}); err == nil {
		t.Fatal("certificate without identity did not fail")
	}
	valid := &x509.Certificate{URIs: []*url.URL{{Scheme: "spiffe", Host: "kouja", Path: "/admin/name"}}}
	role, name, err := certificateIdentity(valid)
	if err != nil || role != "admin" || name != "name" {
		t.Fatalf("identity = %q %q %v", role, name, err)
	}
	for _, uri := range []*url.URL{
		{Scheme: "https", Host: "kouja", Path: "/admin/name"},
		{Scheme: "spiffe", Host: "other", Path: "/admin/name"},
		{Scheme: "spiffe", Host: "kouja", Path: "/wrong/name"},
		{Scheme: "spiffe", Host: "kouja", Path: "/admin/name/extra"},
	} {
		if _, _, err := certificateIdentity(&x509.Certificate{URIs: []*url.URL{uri}}); err == nil {
			t.Fatalf("identity %s did not fail", uri)
		}
	}
	if publicKeysEqual(struct{}{}, struct{}{}) {
		t.Fatal("invalid keys compared equal")
	}
	if maxInt(1, 2) != 2 || maxInt(2, 1) != 2 || boolFloat(true) != 1 || boolFloat(false) != 0 {
		t.Fatal("helper values are wrong")
	}
	if (*Service)(nil).Ready() || (*Service)(nil).CAState() == nil || (*Service)(nil).IsRevoked("x") {
		t.Fatal("nil service state is wrong")
	}
	if err := (*Service)(nil).SnapshotJournal(context.Background(), "x"); err == nil {
		t.Fatal("nil snapshot did not fail")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // test directory must be private
		t.Fatal(err)
	}
	service := &Service{certDir: dir}
	if err := service.validateCertificateDir(); err != nil {
		t.Fatal(err)
	}
	service.certDir = ""
	if err := service.validateCertificateDir(); err == nil {
		t.Fatal("empty certificate directory did not fail")
	}
	service.certDir = "relative"
	if err := service.validateCertificateDir(); err == nil {
		t.Fatal("relative certificate directory did not fail")
	}
	parentFile := filepath.Join(dir, "parent-file")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	service.certDir = filepath.Join(parentFile, "child")
	if err := service.validateCertificateDir(); err == nil {
		t.Fatal("blocked certificate directory did not fail")
	}
	linkTarget := filepath.Join(dir, "target")
	if err := os.Mkdir(linkTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(linkTarget, link); err != nil {
		t.Fatal(err)
	}
	service.certDir = link
	if err := service.validateCertificateDir(); err == nil {
		t.Fatal("symlink certificate directory did not fail")
	}
	permissive := filepath.Join(dir, "permissive")
	if err := os.Mkdir(permissive, 0o755); err != nil { //nolint:gosec // deliberate mode test
		t.Fatal(err)
	}
	service.certDir = permissive
	if err := service.validateCertificateDir(); err == nil {
		t.Fatal("permissive certificate directory did not fail")
	}
}

func TestJournalErrorAndTailBranches(t *testing.T) {
	dir := t.TempDir()
	if _, err := openJournal(dir); err == nil {
		t.Fatal("directory journal did not fail")
	}
	noNewline := filepath.Join(dir, "no-newline.log")
	if err := os.WriteFile(noNewline, []byte(`{"serial":"abc"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	j, err := openJournal(noNewline)
	if err != nil || j.healthy {
		t.Fatalf("no-newline journal = %#v, %v", j, err)
	}
	invalidTail := filepath.Join(dir, "invalid-tail.log")
	if err := os.WriteFile(invalidTail, []byte(`{"serial":"abc"}`+"\n"+`broken`), 0o600); err != nil {
		t.Fatal(err)
	}
	j = &journal{path: invalidTail, entries: make(map[string]journalEntry), healthy: true}
	if err := j.append(journalEntry{Serial: "new"}); err == nil {
		t.Fatal("invalid journal tail did not fail")
	}
	validTail := filepath.Join(dir, "valid-tail.log")
	if err := os.WriteFile(validTail, []byte(`{"serial":"abc"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	j = &journal{path: validTail, entries: make(map[string]journalEntry), healthy: true}
	if err := j.append(journalEntry{Serial: "new", RevokedAt: time.Now(), NotAfter: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if !j.has("new") {
		t.Fatal("appended journal entry missing")
	}
	openDir := &journal{path: dir, entries: make(map[string]journalEntry), healthy: true}
	if err := openDir.append(journalEntry{Serial: "x"}); err == nil {
		t.Fatal("journal opened on directory did not fail")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := j.snapshot(canceled, filepath.Join(dir, "snapshot")); err == nil {
		t.Fatal("canceled snapshot did not fail")
	}
	if err := j.snapshot(context.Background(), dir); err != nil {
		// A directory destination exercises the final rename failure.
		t.Log(err)
	}
	if err := j.snapshot(context.Background(), filepath.Join(dir, "blocked", "snapshot")); err != nil {
		t.Log(err)
	}
	if err := j.compact(time.Now()); err != nil {
		t.Fatal(err)
	}
	blocked := &journal{path: dir, entries: make(map[string]journalEntry), healthy: true}
	if err := blocked.compact(time.Now()); err == nil {
		t.Fatal("compacting over a directory did not fail")
	}
}

func TestRevokeErrorBranches(t *testing.T) {
	if _, _, err := (*Service)(nil).Revoke(context.Background(), Caller{}, "x", "test"); err == nil {
		t.Fatal("nil service revoke did not fail")
	}
	service, _, _ := newTestService(t, true, 10)
	if _, _, err := service.Revoke(context.Background(), Caller{}, "", "test"); err == nil {
		t.Fatal("empty serial revoke did not fail")
	}
	if _, _, err := service.Revoke(context.Background(), Caller{}, "deadbeef", "test"); err == nil {
		t.Fatal("missing serial revoke did not fail")
	}
	if err := service.appendJournal("x", time.Now(), "test"); err != nil {
		t.Fatal(err)
	}
	service.journal = nil
	if err := service.appendJournal("x", time.Now(), "test"); err == nil {
		t.Fatal("nil journal append did not fail")
	}
	if warnings := service.deleteFiles(state.Certificate{}); len(warnings) != 0 {
		t.Fatalf("empty delete warnings = %#v", warnings)
	}
	if err := removeFile(dirPath(t)); err == nil {
		t.Fatal("remove non-empty directory did not fail")
	}
	service.recordFailed(context.Background(), Caller{}, "action", "id", "role", "name", errTest)
}

func dirPath(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "non-empty")
	if err := os.MkdirAll(filepath.Join(dir, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

var errTest = errorString("test error")

type errorString string

func (e errorString) Error() string { return string(e) }

func TestNewInvalidCAAndStoreBranches(t *testing.T) {
	store, err := state.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := testCA(t)
	baseConfig := func(caPath string) *config.Config {
		return &config.Config{
			Listen: config.ListenConfig{TLS: config.TLSConfig{ClientCAFile: caPath}},
			CertificateAuthority: config.CertificateAuthorityConfig{
				Enabled: false, KeyFile: keyPath, CertificateDir: t.TempDir(),
				RevocationLogFile: filepath.Join(t.TempDir(), "journal.log"),
				DefaultTTLDays:    30, MaxTTLDays: 365, MaxActiveCertificates: 10,
			},
			State: config.StateConfig{AuditRetentionDays: 90},
		}
	}
	garbage := filepath.Join(t.TempDir(), "garbage.crt")
	if err := os.WriteFile(garbage, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{Config: baseConfig(garbage), Store: store}); err == nil {
		t.Fatal("garbage CA did not fail")
	}
	badDER := filepath.Join(t.TempDir(), "bad.crt")
	if err := os.WriteFile(badDER, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("bad")}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{Config: baseConfig(badDER), Store: store}); err == nil {
		t.Fatal("bad CA DER did not fail")
	}
	leafPath := filepath.Join(t.TempDir(), "leaf.crt")
	writeTestCertificate(t, leafPath, false, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if _, err := New(Options{Config: baseConfig(leafPath), Store: store}); err == nil {
		t.Fatal("non-CA certificate did not fail")
	}
	expiredPath := filepath.Join(t.TempDir(), "expired.crt")
	writeTestCertificate(t, expiredPath, true, time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour))
	if _, err := New(Options{Config: baseConfig(expiredPath), Store: store}); err == nil {
		t.Fatal("expired CA did not fail")
	}
	journalDir := t.TempDir()
	cfg := baseConfig(certPath)
	cfg.CertificateAuthority.RevocationLogFile = journalDir
	if _, err := New(Options{Config: cfg, Store: store}); err == nil {
		t.Fatal("directory journal did not fail")
	}
	cfg = baseConfig(certPath)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{Config: cfg, Store: store}); err == nil {
		t.Fatal("closed store did not fail")
	}
}

func writeTestCertificate(t *testing.T, path string, isCA bool, notBefore, notAfter time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test"},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		IsCA:                  isCA,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestIssueRevokeExternalAndJournalBranches(t *testing.T) {
	if _, err := (*Service)(nil).Issue(context.Background(), Caller{}, IssueRequest{}); err == nil {
		t.Fatal("nil issue did not fail")
	}
	service, _, _ := newTestService(t, true, 10)
	service.caKey = nil
	if _, err := service.Issue(context.Background(), Caller{}, IssueRequest{Role: "admin", Name: "x"}); err == nil {
		t.Fatal("nil CA key issue did not fail")
	}
	service, _, _ = newTestService(t, true, 10)
	service.caCert.NotAfter = service.now().Add(-time.Hour)
	if _, err := service.Issue(context.Background(), Caller{}, IssueRequest{Role: "admin", Name: "expired"}); err == nil {
		t.Fatal("expired CA issue did not fail")
	}
	service, store, _ := newTestService(t, true, 10)
	if err := os.Chmod(service.certDir, 0o500); err != nil { //nolint:gosec // deliberate mode test
		t.Fatal(err)
	}
	if _, err := service.Issue(context.Background(), Caller{}, IssueRequest{Role: "admin", Name: "file-error"}); err == nil {
		t.Fatal("file error issue did not fail")
	}
	if err := os.Chmod(service.certDir, 0o700); err != nil { //nolint:gosec // restore test directory
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Issue(context.Background(), Caller{}, IssueRequest{Role: "admin", Name: "closed"}); err == nil {
		t.Fatal("closed store issue did not fail")
	}
	service, _, _ = newTestService(t, true, 10)
	if _, _, err := (*Service)(nil).RevokeExternal(context.Background(), Caller{}, "x", "test"); err == nil {
		t.Fatal("nil external revoke did not fail")
	}
	if _, _, err := service.RevokeExternal(context.Background(), Caller{}, "bad", "test"); err == nil {
		t.Fatal("invalid external certificate did not fail")
	}
	service.journal = nil
	if err := service.SnapshotJournal(context.Background(), filepath.Join(t.TempDir(), "snapshot")); err == nil {
		t.Fatal("nil journal snapshot did not fail")
	}
	service.recordFailed(context.Background(), Caller{}, "action", "id", "role", "name", errTest)
}
