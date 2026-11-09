package pki

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
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

func newTestService(t *testing.T, enabled bool, maxActive int) (*Service, *state.Store, *config.Config) {
	t.Helper()
	store, err := state.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	certPath, keyPath := testCA(t)
	dir := t.TempDir()
	//nolint:gosec // test directory needs execute permission
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Listen: config.ListenConfig{TLS: config.TLSConfig{ClientCAFile: certPath}},
		CertificateAuthority: config.CertificateAuthorityConfig{
			Enabled: enabled, KeyFile: keyPath, CertificateDir: dir,
			RevocationLogFile: filepath.Join(t.TempDir(), "revoked.log"),
			DefaultTTLDays:    30, MaxTTLDays: 365, MaxActiveCertificates: maxActive,
		},
		State: config.StateConfig{AuditRetentionDays: 90},
	}
	metrics := observability.New()
	service, err := New(Options{Config: cfg, Store: store, Audit: audit.NewRecorder(store, nil, metrics), Metrics: metrics, Naming: naming.NewValidators()})
	if err != nil {
		t.Fatal(err)
	}
	return service, store, cfg
}

func TestFileStoreJournalSnapshotAndCache(t *testing.T) {
	service, _, _ := newTestService(t, true, 10)
	issued, err := service.Issue(t.Context(), Caller{Owner: "admin"}, IssueRequest{Role: "admin", Name: "files", TTLDays: 1})
	if err != nil {
		t.Fatal(err)
	}
	files, err := service.ListFiles(t.Context())
	if err != nil || len(files) != 2 {
		t.Fatalf("files = %#v, %v", files, err)
	}
	path, err := service.ResolveFile(files[0].Name)
	if err != nil || path == "" {
		t.Fatalf("resolve = %q, %v", path, err)
	}
	if _, err := service.ResolveFile("../escape.crt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unsafe resolve = %v", err)
	}
	if service.CAState() != nil || !service.Ready() {
		t.Fatalf("CA state = %v ready=%v", service.CAState(), service.Ready())
	}
	record, _, err := service.Revoke(t.Context(), Caller{Owner: "admin"}, issued.SerialNumber, "test")
	if err != nil || !record.Revoked {
		t.Fatalf("revoke = %#v, %v", record, err)
	}
	if !service.IsRevoked(issued.SerialNumber) {
		t.Fatal("serial is not in cache")
	}
	destination := filepath.Join(t.TempDir(), "journal.snapshot")
	if err := service.SnapshotJournal(t.Context(), destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination) //nolint:gosec // test output
	if err != nil || len(data) == 0 {
		t.Fatalf("snapshot = %q, %v", data, err)
	}
	service.cache.remove(issued.SerialNumber)
	if service.IsRevoked(issued.SerialNumber) {
		t.Fatal("cache remove failed")
	}
}

func TestIssueValidationAndDegradedMode(t *testing.T) {
	disabled, _, _ := newTestService(t, false, 10)
	if _, err := disabled.Issue(t.Context(), Caller{}, IssueRequest{Role: "admin", Name: "x"}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled issue = %v", err)
	}
	service, _, cfg := newTestService(t, true, 1)
	if _, err := service.Issue(t.Context(), Caller{}, IssueRequest{Role: "bad", Name: "x"}); !errors.Is(err, ErrInvalidRole) {
		t.Fatalf("invalid role = %v", err)
	}
	if _, err := service.Issue(t.Context(), Caller{}, IssueRequest{Role: "admin", Name: "-bad"}); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("invalid name = %v", err)
	}
	if _, err := service.Issue(t.Context(), Caller{}, IssueRequest{Role: "admin", Name: "ok", TTLDays: 366}); !errors.Is(err, ErrInvalidTTL) {
		t.Fatalf("invalid ttl = %v", err)
	}
	if _, err := service.Issue(t.Context(), Caller{Owner: "admin"}, IssueRequest{Role: "admin", Name: "one", TTLDays: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Issue(t.Context(), Caller{Owner: "admin"}, IssueRequest{Role: "admin", Name: "two", TTLDays: 1}); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("quota = %v", err)
	}
	cfg.CertificateAuthority.KeyFile = filepath.Join(t.TempDir(), "missing.key")
	degraded, err := New(Options{Config: cfg, Store: service.store, Audit: service.audit, Naming: naming.NewValidators()})
	if err != nil {
		t.Fatal(err)
	}
	if degraded.Ready() || degraded.CAState() == nil {
		t.Fatalf("degraded state = ready=%v err=%v", degraded.Ready(), degraded.CAState())
	}
	if _, err := degraded.Issue(t.Context(), Caller{}, IssueRequest{Role: "admin", Name: "x"}); !errors.Is(err, ErrCAUnavailable) {
		t.Fatalf("degraded issue = %v", err)
	}
}

func TestRevokeReconciliationAndCleanup(t *testing.T) {
	service, store, _ := newTestService(t, true, 10)
	issued, err := service.Issue(t.Context(), Caller{Owner: "admin"}, IssueRequest{Role: "admin", Name: "reconcile", TTLDays: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.journal.append(journalEntry{Serial: issued.SerialNumber, RevokedAt: time.Now().UTC(), NotAfter: time.Now().Add(time.Hour), Reason: "ahead"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Revoke(t.Context(), Caller{Owner: "admin"}, issued.SerialNumber, "test"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Revoke(t.Context(), Caller{Owner: "admin"}, issued.SerialNumber, "again"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	expired := state.Certificate{
		SerialNumber: "expired-cleanup", Role: "admin", Name: "cleanup",
		IssuedBy: "admin", CertificatePEM: "cert", ClientCAPEM: "ca",
		NotBefore: now.Add(-2 * time.Hour), NotAfter: now.Add(-time.Hour), CreatedAt: now.Add(-2 * time.Hour),
	}
	if err := store.Transact(t.Context(), func(tx state.Tx) error { return tx.PutCertificate(expired) }); err != nil {
		t.Fatal(err)
	}
	service.cache.add("expired-cleanup")
	service.cleanup(t.Context())
	if _, err := service.Get(t.Context(), "expired-cleanup"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cleanup get = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := service.Run(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestJournalUnhealthyAndMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "revoked.log")
	if err := os.WriteFile(path, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	j, err := openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if j.healthy {
		t.Fatal("malformed journal should be unhealthy")
	}
	if err := j.append(journalEntry{Serial: "x"}); !errors.Is(err, ErrJournalUnhealthy) {
		t.Fatalf("append = %v", err)
	}
	if err := j.snapshot(t.Context(), filepath.Join(t.TempDir(), "snapshot")); !errors.Is(err, ErrJournalUnhealthy) {
		t.Fatalf("snapshot = %v", err)
	}
}

func TestExternalInvalidCertificate(t *testing.T) {
	service, _, _ := newTestService(t, true, 10)
	if _, _, err := service.RevokeExternal(t.Context(), Caller{}, "not-pem", "test"); !errors.Is(err, ErrExternalInvalid) {
		t.Fatalf("invalid external = %v", err)
	}
}

func TestNewValidationAndJournalBranches(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("empty options should fail")
	}
	store, err := state.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	cfg := &config.Config{Listen: config.ListenConfig{TLS: config.TLSConfig{ClientCAFile: filepath.Join(t.TempDir(), "missing.crt")}}}
	if _, err := New(Options{Config: cfg, Store: store}); err == nil {
		t.Fatal("missing client CA should fail")
	}
	certPath, keyPath := testCA(t)
	dir := t.TempDir()
	//nolint:gosec // test directory
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg = &config.Config{
		Listen: config.ListenConfig{TLS: config.TLSConfig{ClientCAFile: certPath}},
		CertificateAuthority: config.CertificateAuthorityConfig{
			Enabled: true, KeyFile: keyPath, CertificateDir: dir,
			RevocationLogFile: filepath.Join(t.TempDir(), "journal.log"),
			DefaultTTLDays:    30, MaxTTLDays: 365, MaxActiveCertificates: 10,
		},
		State: config.StateConfig{AuditRetentionDays: 90},
	}
	service, err := New(Options{Config: cfg, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if service.Ready() || service.CAState() == nil {
		t.Fatal("unsafe cert dir should degrade issuance")
	}
	//nolint:gosec // deliberate unsafe-mode test
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.CertificateAuthority.CertificateDir = t.TempDir()
	service, err = New(Options{Config: cfg, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if service.Ready() || service.CAState() == nil {
		t.Fatal("unsafe key mode should degrade issuance")
	}
	if (*Service)(nil).CAState() == nil || (*Service)(nil).Ready() {
		t.Fatal("nil service state")
	}
}

func TestJournalSnapshotBranches(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "journal.log")
	j, err := openJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.append(journalEntry{Serial: "abc", RevokedAt: time.Now(), NotAfter: time.Now().Add(time.Hour), Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := j.append(journalEntry{Serial: "def", RevokedAt: time.Now(), NotAfter: time.Now().Add(time.Hour), Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "snapshot")
	if err := j.snapshot(t.Context(), destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination) //nolint:gosec // test output
	if err != nil || len(data) == 0 {
		t.Fatalf("snapshot = %q, %v", data, err)
	}
	if err := j.compact(time.Now().Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	journalData, err := os.ReadFile(journalPath) //nolint:gosec // test output
	if err != nil || len(journalData) != 0 {
		t.Fatalf("compacted = %q, %v", journalData, err)
	}
	empty := filepath.Join(t.TempDir(), "missing.log")
	missing, err := openJournal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if err := missing.snapshot(t.Context(), filepath.Join(t.TempDir(), "empty.snapshot")); err != nil {
		t.Fatal(err)
	}
}

func TestExternalRevokeExistingAndOrphanCleanup(t *testing.T) {
	service, _, _ := newTestService(t, true, 10)
	issued, err := service.Issue(t.Context(), Caller{Owner: "admin"}, IssueRequest{Role: "admin", Name: "external", TTLDays: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.RevokeExternal(t.Context(), Caller{Owner: "admin"}, issued.CertificatePEM, "existing"); err != nil {
		t.Fatal(err)
	}
	record, err := service.Get(t.Context(), issued.SerialNumber)
	if err != nil || !record.Revoked {
		t.Fatalf("existing external = %#v, %v", record, err)
	}

	external := makeExternalCertificateForService(t, service, "old", "admin", 99)
	legacyName := filepath.Join(service.certDir, "admin-old.crt")
	if err := os.WriteFile(legacyName, external, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(service.certDir, "admin-old.key"), []byte("key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.RevokeExternal(t.Context(), Caller{Owner: "admin"}, string(external), "orphan"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacyName); !os.IsNotExist(err) {
		t.Fatalf("legacy cert still exists: %v", err)
	}
}

func makeExternalCertificateForService(t *testing.T, service *Service, name, role string, serial int64) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: "kouja-" + role + "-" + name},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs: []*url.URL{{Scheme: "spiffe", Host: "kouja", Path: "/" + role + "/" + name}},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, service.caCert, &key.PublicKey, service.caKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestNormalizeSerialAndFileErrors(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"0012AB", "12ab"}, {"0", ""}, {"xyz", ""}, {"", ""},
	} {
		if got := normalizeSerial(test.input); got != test.want {
			t.Fatalf("normalize %q = %q, want %q", test.input, got, test.want)
		}
	}
	service, _, _ := newTestService(t, true, 10)
	if _, err := service.ResolveFile("bad/name.crt"); err == nil {
		t.Fatal("bad file name should fail")
	}
	if _, err := service.ResolveFile("not-a-cert.txt"); err == nil {
		t.Fatal("non-certificate name should fail")
	}
}
