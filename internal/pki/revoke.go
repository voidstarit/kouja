package pki

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/voidstarit/kouja/internal/audit"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/warnings"
)

func (s *Service) Revoke(ctx context.Context, caller Caller, serial, reason string) (CertificateRecord, []warnings.Warning, error) {
	if s == nil {
		return CertificateRecord{}, nil, ErrCAUnavailable
	}
	serial = normalizeSerial(serial)
	if serial == "" {
		return CertificateRecord{}, nil, ErrInvalidSerial
	}
	s.revokeMu.Lock()
	defer s.revokeMu.Unlock()
	var current state.Certificate
	if err := s.store.Transact(ctx, func(tx state.Tx) error {
		value, err := tx.GetCertificate(serial)
		current = value
		return err
	}); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return CertificateRecord{}, nil, ErrNotFound
		}
		return CertificateRecord{}, nil, err
	}
	journalPresent := s.journal != nil && s.journal.has(serial)
	switch {
	case current.Revoked && journalPresent:
		return mapRecord(current), s.deleteFiles(current), nil
	case !current.Revoked && journalPresent:
		s.cache.add(serial)
		updated, err := s.revokeRow(ctx, caller, serial, reason, "certificate_revoke")
		if err != nil {
			s.recordFailed(ctx, caller, "certificate_revoke", serial, current.Role, current.Name, err)
			return CertificateRecord{}, nil, err
		}
		return updated, s.deleteFiles(current), nil
	case current.Revoked && !journalPresent:
		if err := s.appendJournal(serial, current.NotAfter, reason); err != nil {
			s.recordFailed(ctx, caller, "certificate_revoke", serial, current.Role, current.Name, err)
			return CertificateRecord{}, nil, err
		}
		s.cache.add(serial)
		return mapRecord(current), s.deleteFiles(current), nil
	default:
		if err := s.appendJournal(serial, current.NotAfter, reason); err != nil {
			s.recordFailed(ctx, caller, "certificate_revoke", serial, current.Role, current.Name, err)
			return CertificateRecord{}, nil, err
		}
		s.cache.add(serial)
		updated, err := s.revokeRow(ctx, caller, serial, reason, "certificate_revoke")
		if err != nil {
			s.recordFailed(ctx, caller, "certificate_revoke", serial, current.Role, current.Name, err)
			return CertificateRecord{}, nil, err
		}
		return updated, s.deleteFiles(current), nil
	}
}

func (s *Service) revokeRow(ctx context.Context, caller Caller, serial, reason, action string) (CertificateRecord, error) {
	var updated state.Certificate
	if err := s.store.Transact(ctx, func(tx state.Tx) error {
		value, err := tx.RevokeCertificate(serial, reason, s.now().UTC())
		if err != nil {
			return err
		}
		updated = value
		return s.recordTx(tx, caller, audit.Event{
			Action: action, Result: "succeeded", ResourceType: "certificate",
			ResourceID: serial, Role: value.Role, Name: value.Name, Detail: reason,
		})
	}); err != nil {
		return CertificateRecord{}, err
	}
	if s.metrics != nil {
		s.metrics.Inc("kouja_certificates_revoked_total")
	}
	return mapRecord(updated), nil
}

func (s *Service) appendJournal(serial string, notAfter time.Time, reason string) error {
	if s.journal == nil {
		return ErrCAUnavailable
	}
	return s.journal.append(journalEntry{
		Serial: serial, RevokedAt: s.now().UTC(), NotAfter: notAfter, Reason: reason,
	})
}

func (s *Service) deleteFiles(record state.Certificate) []warnings.Warning {
	result := make([]warnings.Warning, 0)
	for _, path := range []string{record.CertFile, record.KeyFile} {
		if path == "" {
			continue
		}
		if err := removeFile(path); err != nil {
			result = append(result, warnings.Warning{Code: warnings.CodeFileDeleteFailed, Message: err.Error()})
		}
	}
	return result
}

func normalizeSerial(serial string) string {
	serial = strings.ToLower(strings.TrimSpace(serial))
	serial = strings.TrimLeft(serial, "0")
	if serial == "" || len(serial) > 40 {
		return ""
	}
	for _, char := range serial {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return ""
		}
	}
	return serial
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("pki: remove %s: %w", path, err)
	}
	return nil
}
