package state

import (
	"errors"
	"testing"
	"time"
)

func testCertificate(serial string, now time.Time) Certificate {
	return Certificate{
		SerialNumber:   serial,
		Role:           "admin",
		Name:           "bootstrap",
		IssuedBy:       "caller",
		IssuerSerial:   "issuer",
		CertificatePEM: "certificate-pem",
		ClientCAPEM:    "client-ca-pem",
		CertFile:       "admin-bootstrap-" + serial + ".crt",
		KeyFile:        "admin-bootstrap-" + serial + ".key",
		NotBefore:      now.Add(-time.Minute),
		NotAfter:       now.Add(time.Hour),
		CreatedAt:      now,
	}
}

func TestCertificateRegistryLifecycle(t *testing.T) {
	store, err := Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Now().UTC()
	first := testCertificate("a1", now)
	second := testCertificate("b2", now.Add(time.Second))
	if err := store.Transact(t.Context(), func(tx Tx) error {
		if err := tx.PutCertificate(first); err != nil {
			return err
		}
		return tx.PutCertificate(second)
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(t.Context(), func(tx Tx) error {
		got, err := tx.GetCertificate("a1")
		if err != nil {
			return err
		}
		if got.SerialNumber != "a1" || got.Revoked {
			t.Fatalf("certificate = %#v", got)
		}
		count, err := tx.CountActiveCertificates(now)
		if err != nil {
			return err
		}
		if count != 2 {
			t.Fatalf("count = %d, want 2", count)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(t.Context(), func(tx Tx) error {
		certificates, token, err := tx.ListCertificates(1, "")
		if err != nil {
			return err
		}
		if len(certificates) != 1 || token == "" {
			t.Fatalf("first page = %#v token=%q", certificates, token)
		}
		next, token2, err := tx.ListCertificates(1, token)
		if err != nil {
			return err
		}
		if len(next) != 1 || token2 != "" {
			t.Fatalf("second page = %#v token=%q", next, token2)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(t.Context(), func(tx Tx) error {
		revoked, err := tx.RevokeCertificate("a1", "test", now)
		if err != nil {
			return err
		}
		if !revoked.Revoked || revoked.RevokedAt == nil {
			t.Fatalf("revoked = %#v", revoked)
		}
		again, err := tx.RevokeCertificate("a1", "second", now)
		if err != nil {
			return err
		}
		if !again.Revoked {
			t.Fatalf("idempotent revoke = %#v", again)
		}
		serials, err := tx.ListRevokedSerials()
		if err != nil {
			return err
		}
		if len(serials) != 1 || serials[0] != "a1" {
			t.Fatalf("serials = %#v", serials)
		}
		return tx.DeleteCertificate("b2")
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(t.Context(), func(tx Tx) error {
		if _, err := tx.GetCertificate("b2"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("deleted certificate error = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredCertificateAndAuditEvents(t *testing.T) {
	store, err := Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	now := time.Now().UTC()
	expired := testCertificate("expired", now.Add(-2*time.Hour))
	expired.NotAfter = now.Add(-time.Hour)
	if err := store.Transact(t.Context(), func(tx Tx) error {
		if err := tx.PutCertificate(expired); err != nil {
			return err
		}
		if _, err := tx.GetCertificate("expired"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("expired get error = %v", err)
		}
		expiredRows, err := tx.ListExpiredCertificates(now)
		if err != nil {
			return err
		}
		if len(expiredRows) != 1 {
			t.Fatalf("expired rows = %d", len(expiredRows))
		}
		for i := 0; i < 3; i++ {
			if err := tx.PutAuditEvent(AuditEvent{
				CallerOwner: "admin", Action: "certificate_issue", Result: "succeeded",
				ResourceType: "certificate", ResourceID: "serial",
			}); err != nil {
				return err
			}
		}
		deleted, err := tx.DeleteAuditBefore(now.Add(time.Hour))
		if err != nil {
			return err
		}
		if deleted != 3 {
			t.Fatalf("deleted audit rows = %d, want 3", deleted)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
