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
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/voidstarit/kouja/internal/audit"
	"github.com/voidstarit/kouja/internal/fsutil"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/warnings"
)

func (s *Service) Issue(ctx context.Context, caller Caller, request IssueRequest) (IssuedCertificate, error) {
	if s == nil || !s.cfg.CertificateAuthority.Enabled {
		return IssuedCertificate{}, ErrDisabled
	}
	if !s.ready || s.caKey == nil || s.caCert == nil {
		return IssuedCertificate{}, ErrCAUnavailable
	}
	role := strings.ToLower(strings.TrimSpace(request.Role))
	switch role {
	case "admin", "caller", "controller", "node":
	default:
		return IssuedCertificate{}, ErrInvalidRole
	}
	if err := s.naming.CertificateName(strings.TrimSpace(request.Name)); err != nil {
		return IssuedCertificate{}, fmt.Errorf("%w: %v", ErrInvalidName, err)
	}
	name := strings.TrimSpace(request.Name)
	now := s.now().UTC()
	ttlDays := int(request.TTLDays)
	if ttlDays == 0 {
		ttlDays = s.cfg.CertificateAuthority.DefaultTTLDays
	}
	if ttlDays < 1 || ttlDays > s.cfg.CertificateAuthority.MaxTTLDays {
		return IssuedCertificate{}, ErrInvalidTTL
	}
	if !s.caCert.NotAfter.After(now.Add(5 * time.Minute)) {
		s.ready = false
		s.caErr = ErrCAUnavailable
		if s.metrics != nil {
			s.metrics.Set("kouja_certificate_authority_ready", 0)
		}
		return IssuedCertificate{}, ErrCAUnavailable
	}

	s.issueMu.Lock()
	defer s.issueMu.Unlock()
	var active int
	if err := s.store.Transact(ctx, func(tx state.Tx) error {
		var err error
		active, err = tx.CountActiveCertificates(now)
		return err
	}); err != nil {
		return IssuedCertificate{}, err
	}
	if active >= s.cfg.CertificateAuthority.MaxActiveCertificates {
		return IssuedCertificate{}, ErrQuotaExceeded
	}

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return IssuedCertificate{}, fmt.Errorf("pki: generate private key: %w", err)
	}
	serial, err := randomSerialNumber()
	if err != nil {
		return IssuedCertificate{}, err
	}
	notBefore := now.Add(-5 * time.Minute)
	notAfter := now.Add(time.Duration(ttlDays) * 24 * time.Hour)
	if notAfter.After(s.caCert.NotAfter) {
		notAfter = s.caCert.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "kouja-" + role + "-" + name},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		URIs:                  []*url.URL{{Scheme: "spiffe", Host: "kouja", Path: "/" + role + "/" + name}},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, s.caCert, &privateKey.PublicKey, s.caKey)
	if err != nil {
		return IssuedCertificate{}, fmt.Errorf("pki: sign certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return IssuedCertificate{}, fmt.Errorf("pki: encode private key: %w", err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if certificatePEM == nil || keyPEM == nil {
		return IssuedCertificate{}, fmt.Errorf("pki: encode credential")
	}
	serialText := serial.Text(16)
	certPath := filepath.Join(s.certDir, role+"-"+name+"-"+serialText+".crt")
	keyPath := filepath.Join(s.certDir, role+"-"+name+"-"+serialText+".key")
	resultWarnings := make([]warnings.Warning, 0)
	cleanupFiles := func() {
		_ = os.Remove(certPath)
		_ = os.Remove(keyPath)
	}
	if err := fsutil.WriteFileExclusiveAtomic(certPath, certificatePEM, 0o644); err != nil {
		if !errors.Is(err, fsutil.ErrDurabilityUncertain) {
			cleanupFiles()
			return IssuedCertificate{}, fmt.Errorf("%w: %v", ErrFile, err)
		}
		resultWarnings = append(resultWarnings, warnings.Warning{Code: warnings.CodeDurabilityUncertain, Message: err.Error()})
	}
	if err := fsutil.WriteFileExclusiveAtomic(keyPath, keyPEM, 0o600); err != nil {
		if !errors.Is(err, fsutil.ErrDurabilityUncertain) {
			cleanupFiles()
			return IssuedCertificate{}, fmt.Errorf("%w: %v", ErrFile, err)
		}
		resultWarnings = append(resultWarnings, warnings.Warning{Code: warnings.CodeDurabilityUncertain, Message: err.Error()})
	}
	record := state.Certificate{
		SerialNumber: serialText, Role: role, Name: name, IssuedBy: caller.Owner,
		IssuerSerial: caller.SerialNumber, CertificatePEM: string(certificatePEM),
		ClientCAPEM: string(s.clientCAPEM), CertFile: certPath, KeyFile: keyPath,
		NotBefore: notBefore, NotAfter: notAfter, CreatedAt: now,
	}
	if err := s.store.Transact(ctx, func(tx state.Tx) error {
		if err := tx.PutCertificate(record); err != nil {
			return err
		}
		return s.recordTx(tx, caller, audit.Event{
			Action: "certificate_issue", Result: "succeeded", ResourceType: "certificate",
			ResourceID: serialText, Role: role, Name: name,
			Detail: fmt.Sprintf("request_id=%s issuer_serial=%s ttl_days=%d", caller.RequestID, caller.SerialNumber, ttlDays),
		})
	}); err != nil {
		cleanupFiles()
		s.recordFailed(ctx, caller, "certificate_issue", serialText, role, name, err)
		return IssuedCertificate{}, err
	}
	if s.metrics != nil {
		s.metrics.Inc("kouja_certificates_issued_total")
		s.metrics.Set("kouja_certificate_registry_size", float64(active+1))
	}
	return IssuedCertificate{
		CertificatePEM: string(certificatePEM), PrivateKeyPEM: string(keyPEM),
		ClientCAPEM: string(s.clientCAPEM), SerialNumber: serialText, Role: role,
		Name: name, IssuedBy: caller.Owner, IssuerSerial: caller.SerialNumber,
		RequestID: caller.RequestID, NotBefore: notBefore, NotAfter: notAfter,
		Warnings: resultWarnings,
	}, nil
}

func randomSerialNumber() (*big.Int, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return nil, fmt.Errorf("pki: random serial: %w", err)
	}
	serial := new(big.Int).SetBytes(buffer)
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	return serial, nil
}

func (s *Service) recordTx(tx state.Tx, caller Caller, event audit.Event) error {
	if s.audit == nil {
		return nil
	}
	event.Caller = caller.Owner
	event.OperationID = ""
	event.RequestID = caller.RequestID
	event.RemoteAddr = caller.RemoteAddr
	return s.audit.RecordTx(tx, event)
}

func (s *Service) recordFailed(ctx context.Context, caller Caller, action, resourceID, role, name string, cause error) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Record(ctx, audit.Event{
		Caller: caller.Owner, RequestID: caller.RequestID, RemoteAddr: caller.RemoteAddr,
		Action: action, Result: "failed", ResourceType: "certificate", ResourceID: resourceID,
		Role: role, Name: name, ErrorCode: cause.Error(),
	})
}
