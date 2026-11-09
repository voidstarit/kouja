package agent

import (
	"context"
	"testing"
	"time"

	"github.com/voidstarit/kouja/gen/kouja/v1"
	"github.com/voidstarit/kouja/internal/auth"
	"github.com/voidstarit/kouja/internal/pki"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/warnings"
)

type certTestIssuer struct{ issued pki.IssuedCertificate }

func (f certTestIssuer) Issue(context.Context, pki.Caller, pki.IssueRequest) (pki.IssuedCertificate, error) {
	return f.issued, nil
}

type certTestRegistry struct {
	record pki.CertificateRecord
	page   pki.CertificatePage
}

func (f certTestRegistry) Get(context.Context, string) (pki.CertificateRecord, error) {
	return f.record, nil
}
func (f certTestRegistry) List(context.Context, pki.ListOptions) (pki.CertificatePage, error) {
	return f.page, nil
}
func (f certTestRegistry) Revoke(context.Context, pki.Caller, string, string) (pki.CertificateRecord, []warnings.Warning, error) {
	return f.record, nil, nil
}

type certTestExternal struct{ record pki.CertificateRecord }

func (f certTestExternal) RevokeExternal(context.Context, pki.Caller, string, string) (pki.CertificateRecord, []warnings.Warning, error) {
	return f.record, nil, nil
}

func TestAdminServiceCertificateRPCs(t *testing.T) {
	store, err := state.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	issued := pki.IssuedCertificate{
		CertificatePEM: "cert", PrivateKeyPEM: "key", ClientCAPEM: "ca", SerialNumber: "serial",
		Role: "admin", Name: "bootstrap", NotBefore: now, NotAfter: now.Add(time.Hour), RequestID: "req",
	}
	record := pki.CertificateRecord{
		SerialNumber: "serial", Role: "admin", Name: "bootstrap", CertificatePEM: "cert",
		NotBefore: now, NotAfter: now.Add(time.Hour), CreatedAt: now,
	}
	service := NewAdminService(store, nil, WithCertificateService(
		certTestIssuer{issued: issued},
		certTestRegistry{record: record, page: pki.CertificatePage{Certificates: []pki.CertificateSummary{{SerialNumber: "serial", Role: "admin", Name: "bootstrap", NotBefore: now, NotAfter: now.Add(time.Hour), CreatedAt: now}}}},
		certTestExternal{record: record}, nil,
	))
	ctx := WithIdentity(t.Context(), auth.Identity{Owner: "spiffe://kouja/admin/test", Role: auth.RoleAdmin, SerialNumber: "issuer"})
	response, err := service.IssueCertificate(ctx, &koujav1.IssueCertificateRequest{Role: "admin", Name: "bootstrap", TtlDays: 1, RequestId: "req"})
	if err != nil || response.GetSerialNumber() != "serial" || response.GetPrivateKeyPem() != "key" {
		t.Fatalf("issue = %#v, %v", response, err)
	}
	if _, err := service.ListCertificates(ctx, &koujav1.ListCertificatesRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetCertificate(ctx, &koujav1.GetCertificateRequest{SerialNumber: "serial"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RevokeCertificate(ctx, &koujav1.RevokeCertificateRequest{SerialNumber: "serial", Reason: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RevokeExternalCertificate(ctx, &koujav1.RevokeExternalCertificateRequest{CertificatePem: "pem", Reason: "test"}); err != nil {
		t.Fatal(err)
	}
}
