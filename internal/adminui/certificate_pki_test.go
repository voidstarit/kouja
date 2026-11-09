package adminui

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voidstarit/kouja/internal/pki"
	"github.com/voidstarit/kouja/internal/warnings"
)

type fakeIssuer struct{ issued pki.IssuedCertificate }

func (f fakeIssuer) Issue(context.Context, pki.Caller, pki.IssueRequest) (pki.IssuedCertificate, error) {
	return f.issued, nil
}

type fakeRegistry struct {
	page   pki.CertificatePage
	record pki.CertificateRecord
}

func (f fakeRegistry) Get(context.Context, string) (pki.CertificateRecord, error) {
	return f.record, nil
}
func (f fakeRegistry) List(context.Context, pki.ListOptions) (pki.CertificatePage, error) {
	return f.page, nil
}
func (f fakeRegistry) Revoke(context.Context, pki.Caller, string, string) (pki.CertificateRecord, []warnings.Warning, error) {
	return f.record, nil, nil
}

type fakeExternal struct{ record pki.CertificateRecord }

func (f fakeExternal) RevokeExternal(context.Context, pki.Caller, string, string) (pki.CertificateRecord, []warnings.Warning, error) {
	return f.record, nil, nil
}

type fakeFiles struct{ path string }

func (f fakeFiles) ListFiles(context.Context) ([]pki.FileInfo, error) {
	return []pki.FileInfo{{Name: "admin-bootstrap-serial.crt", Role: "admin", Kind: "certificate", SerialNumber: "serial", SizeBytes: 1, ModifiedAt: time.Now()}}, nil
}
func (f fakeFiles) ResolveFile(string) (string, error) { return f.path, nil }

type fakeCAStatus struct{}

func (fakeCAStatus) Ready() bool    { return true }
func (fakeCAStatus) CAState() error { return nil }

func TestAdminUICertificatePKIRoutes(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	now := time.Now().UTC()
	server.options.Config.CertificateAuthority.Enabled = true
	server.options.CAStatus = fakeCAStatus{}
	server.options.Issuer = fakeIssuer{issued: pki.IssuedCertificate{
		CertificatePEM: "cert", PrivateKeyPEM: "key", SerialNumber: "serial",
		Role: "admin", Name: "bootstrap", NotBefore: now, NotAfter: now.Add(time.Hour),
	}}
	server.options.Registry = fakeRegistry{page: pki.CertificatePage{Certificates: []pki.CertificateSummary{{SerialNumber: "serial", Role: "admin", Name: "bootstrap", NotAfter: now.Add(time.Hour)}}}, record: pki.CertificateRecord{SerialNumber: "serial", Role: "admin", Name: "bootstrap", CertFile: "/tmp/admin-bootstrap-serial.crt", NotAfter: now.Add(time.Hour), Revoked: true}}
	server.options.ExternalRevoker = fakeExternal{record: pki.CertificateRecord{SerialNumber: "external", Role: "admin", Name: "old", Revoked: true}}
	path := filepath.Join(t.TempDir(), "admin-bootstrap-serial.crt")
	if err := os.WriteFile(path, []byte("certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	server.options.Files = fakeFiles{path: path}

	response := doRequest(t, server, http.MethodPost, "/api/certificates", []byte(`{"role":"admin","name":"bootstrap","ttl_days":1}`), map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", response.Code, response.Body.String())
	}
	var created CreateCertificateResponse
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.SerialNumber != "serial" {
		t.Fatalf("created = %#v, %v", created, err)
	}
	response = doRequest(t, server, http.MethodGet, "/api/certificates", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list = %d %s", response.Code, response.Body.String())
	}
	var listed CertificatesResponse
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil || len(listed.Certificates) != 1 {
		t.Fatalf("listed = %#v, %v", listed, err)
	}
	response = doRequest(t, server, http.MethodDelete, "/api/certificates/serial", nil, map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusOK {
		t.Fatalf("revoke = %d %s", response.Code, response.Body.String())
	}
	response = doRequest(t, server, http.MethodPost, "/api/certificates:revoke-external", []byte(`{"certificate_pem":"x","reason":"orphan"}`), map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusOK {
		t.Fatalf("external = %d %s", response.Code, response.Body.String())
	}
	response = doRequest(t, server, http.MethodGet, "/api/certificates/admin-bootstrap-serial.crt", nil, nil)
	if response.Code != http.StatusOK || response.Body.String() != "certificate" {
		t.Fatalf("download = %d %q", response.Code, response.Body.String())
	}
}
