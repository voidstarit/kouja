package pki

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/voidstarit/kouja/internal/audit"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/warnings"
)

func (s *Service) RevokeExternal(ctx context.Context, caller Caller, certificatePEM, reason string) (CertificateRecord, []warnings.Warning, error) {
	if s == nil {
		return CertificateRecord{}, nil, ErrCAUnavailable
	}
	block, _ := pem.Decode([]byte(certificatePEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return CertificateRecord{}, nil, ErrExternalInvalid
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return CertificateRecord{}, nil, ErrExternalInvalid
	}
	if _, err := certificate.Verify(x509.VerifyOptions{
		Roots:     s.caPool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return CertificateRecord{}, nil, ErrExternalInvalid
	}
	role, name, err := certificateIdentity(certificate)
	if err != nil {
		return CertificateRecord{}, nil, ErrExternalInvalid
	}
	serial := normalizeSerial(certificate.SerialNumber.Text(16))
	if serial == "" {
		return CertificateRecord{}, nil, ErrExternalInvalid
	}
	if _, getErr := s.Get(ctx, serial); getErr == nil {
		record, verdicts, revokeErr := s.Revoke(ctx, caller, serial, reason)
		if revokeErr != nil {
			return CertificateRecord{}, verdicts, revokeErr
		}
		return record, append(verdicts, s.cleanupOrphanFiles(role, name, serial, certificate)...), nil
	} else if !errors.Is(getErr, ErrNotFound) {
		return CertificateRecord{}, nil, getErr
	}

	s.revokeMu.Lock()
	defer s.revokeMu.Unlock()
	if !s.journal.has(serial) {
		if err := s.appendJournal(serial, certificate.NotAfter, reason); err != nil {
			s.recordFailed(ctx, caller, "certificate_revoke_external", serial, role, name, err)
			return CertificateRecord{}, nil, err
		}
	}
	s.cache.add(serial)
	now := s.now().UTC()
	record := state.Certificate{
		SerialNumber: serial, Role: role, Name: name, IssuedBy: "external",
		CertificatePEM: certificatePEM, ClientCAPEM: string(s.clientCAPEM),
		CertFile: "", KeyFile: "", NotBefore: certificate.NotBefore,
		NotAfter: certificate.NotAfter, CreatedAt: now, RevokedAt: &now,
		RevokedReason: reason, Revoked: true,
	}
	if err := s.store.Transact(ctx, func(tx state.Tx) error {
		if err := tx.PutCertificate(record); err != nil {
			return err
		}
		return s.recordTx(tx, caller, audit.Event{
			Action: "certificate_revoke_external", Result: "succeeded",
			ResourceType: "certificate", ResourceID: serial, Role: role, Name: name,
			Detail: "external " + reason,
		})
	}); err != nil {
		s.recordFailed(ctx, caller, "certificate_revoke_external", serial, role, name, err)
		return CertificateRecord{}, nil, err
	}
	return mapRecord(record), s.cleanupOrphanFiles(role, name, serial, certificate), nil
}

func certificateIdentity(certificate *x509.Certificate) (string, string, error) {
	for _, uri := range certificate.URIs {
		if uri.Scheme != "spiffe" || uri.Host != "kouja" {
			continue
		}
		parts := strings.Split(strings.Trim(uri.Path, "/"), "/")
		if len(parts) != 2 {
			continue
		}
		switch parts[0] {
		case "admin", "caller", "controller", "node":
			return parts[0], parts[1], nil
		}
	}
	return "", "", fmt.Errorf("pki: no kouja URI SAN")
}

func (s *Service) cleanupOrphanFiles(role, name, serial string, certificate *x509.Certificate) []warnings.Warning {
	result := make([]warnings.Warning, 0)
	for _, suffix := range []string{".crt", ".key"} {
		path := filepath.Join(s.certDir, role+"-"+name+"-"+serial+suffix)
		if err := removeFile(path); err != nil {
			result = append(result, warnings.Warning{Code: warnings.CodeFileDeleteFailed, Message: err.Error()})
		}
	}
	legacyCert := filepath.Join(s.certDir, role+"-"+name+".crt")
	if data, err := os.ReadFile(legacyCert); err == nil { //nolint:gosec // constrained path
		block, _ := pem.Decode(data)
		if block == nil {
			result = append(result, warnings.Warning{Code: warnings.CodeFileDeleteFailed, Message: "legacy file is not PEM: " + legacyCert})
		} else if legacy, err := x509.ParseCertificate(block.Bytes); err != nil || normalizeSerial(legacy.SerialNumber.Text(16)) != serial {
			result = append(result, warnings.Warning{Code: warnings.CodeFileDeleteFailed, Message: "legacy certificate serial mismatch: " + legacyCert})
		} else {
			for _, suffix := range []string{".crt", ".key"} {
				if err := removeFile(filepath.Join(s.certDir, role+"-"+name+suffix)); err != nil {
					result = append(result, warnings.Warning{Code: warnings.CodeFileDeleteFailed, Message: err.Error()})
				}
			}
		}
	}
	return result
}
