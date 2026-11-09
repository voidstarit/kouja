package pki

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/voidstarit/kouja/internal/audit"
	"github.com/voidstarit/kouja/internal/config"
	"github.com/voidstarit/kouja/internal/naming"
	"github.com/voidstarit/kouja/internal/observability"
	"github.com/voidstarit/kouja/internal/state"
)

type Options struct {
	Config  *config.Config
	Store   *state.Store
	Audit   *audit.Recorder
	Logger  *slog.Logger
	Now     func() time.Time
	Metrics *observability.Metrics
	Naming  *naming.Validators
}

type Service struct {
	cfg     *config.Config
	store   *state.Store
	audit   *audit.Recorder
	logger  *slog.Logger
	now     func() time.Time
	metrics *observability.Metrics
	naming  *naming.Validators

	caCert      *x509.Certificate
	caKey       crypto.Signer
	caPool      *x509.CertPool
	clientCAPEM []byte
	certDir     string
	journal     *journal
	cache       *revocationCache
	issueMu     sync.Mutex
	revokeMu    sync.Mutex
	ready       bool
	caErr       error
}

var (
	_ Issuer            = (*Service)(nil)
	_ Registry          = (*Service)(nil)
	_ ExternalRevoker   = (*Service)(nil)
	_ FileStore         = (*Service)(nil)
	_ RevocationChecker = (*Service)(nil)
	_ CAStatus          = (*Service)(nil)
)

func New(options Options) (*Service, error) {
	if options.Config == nil || options.Store == nil {
		return nil, fmt.Errorf("pki: config and state store are required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Naming == nil {
		options.Naming = naming.NewValidators()
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	service := &Service{
		cfg: options.Config, store: options.Store, audit: options.Audit,
		logger: options.Logger, now: options.Now, metrics: options.Metrics,
		naming: options.Naming, cache: newRevocationCache(),
		certDir: options.Config.CertificateAuthority.CertificateDir,
	}
	caPEM, err := os.ReadFile(options.Config.Listen.TLS.ClientCAFile) //nolint:gosec // configured CA path
	if err != nil {
		return nil, fmt.Errorf("pki: read client CA: %w", err)
	}
	service.clientCAPEM = caPEM
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("pki: client CA contains no certificates")
	}
	block, _ := pem.Decode(caPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("pki: client CA is not PEM")
	}
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !caCert.IsCA || !caCert.BasicConstraintsValid {
		return nil, fmt.Errorf("pki: client CA is invalid")
	}
	now := service.now().UTC()
	if now.Before(caCert.NotBefore) || now.After(caCert.NotAfter) {
		return nil, fmt.Errorf("pki: client CA is not currently valid")
	}
	service.caCert = caCert
	service.caPool = pool

	journalPath := options.Config.CertificateAuthority.RevocationLogFile
	service.journal, err = openJournal(journalPath)
	if err != nil {
		return nil, err
	}
	if !service.journal.healthy {
		options.Logger.Error("revocation journal is unhealthy", "path", journalPath)
	}
	for serial := range service.journal.entries {
		service.cache.add(serial)
	}
	if err := service.store.Transact(context.Background(), func(tx state.Tx) error {
		serials, err := tx.ListRevokedSerials()
		if err != nil {
			return err
		}
		service.cache.load(serials)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("pki: load certificate registry: %w", err)
	}

	if !options.Config.CertificateAuthority.Enabled {
		return service, nil
	}
	key, err := loadSigningKey(options.Config.CertificateAuthority.KeyFile)
	if err != nil {
		service.caErr = err
		options.Logger.Error("certificate authority key unavailable; issuance degraded", "error", err)
		return service, nil
	}
	if !publicKeysEqual(key.Public(), caCert.PublicKey) {
		service.caErr = fmt.Errorf("pki: CA key does not match client CA certificate")
		options.Logger.Error("certificate authority key mismatch; issuance degraded", "error", service.caErr)
		return service, nil
	}
	if err := service.validateCertificateDir(); err != nil {
		service.caErr = err
		options.Logger.Error("certificate directory unavailable; issuance degraded", "error", err)
		return service, nil
	}
	service.caKey = key
	service.ready = true
	return service, nil
}

func loadSigningKey(path string) (crypto.Signer, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("pki: inspect CA key: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("pki: CA key is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("pki: CA key must be 0600 or stricter")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		return nil, fmt.Errorf("pki: CA key must be owned by the agent user")
	}
	data, err := os.ReadFile(path) //nolint:gosec // configured CA key path
	if err != nil {
		return nil, fmt.Errorf("pki: read CA key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("pki: CA key is not PEM")
	}
	if _, encrypted := block.Headers["Proc-Type"]; encrypted {
		return nil, fmt.Errorf("pki: encrypted CA keys are not supported")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if signer, ok := key.(crypto.Signer); ok {
			return signer, nil
		}
	}
	if key, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, fmt.Errorf("pki: unsupported CA private key")
}

func publicKeysEqual(left, right any) bool {
	leftDER, err := x509.MarshalPKIXPublicKey(left)
	if err != nil {
		return false
	}
	rightDER, err := x509.MarshalPKIXPublicKey(right)
	if err != nil {
		return false
	}
	return string(leftDER) == string(rightDER)
}

func (s *Service) validateCertificateDir() error {
	if s.certDir == "" || !filepath.IsAbs(s.certDir) {
		return fmt.Errorf("%w: certificate directory must be absolute", ErrUnsafeDirectory)
	}
	if err := os.MkdirAll(s.certDir, 0o700); err != nil {
		return fmt.Errorf("%w: create certificate directory: %v", ErrUnsafeDirectory, err)
	}
	info, err := os.Lstat(s.certDir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: certificate directory is not a regular directory", ErrUnsafeDirectory)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: certificate directory must be 0700", ErrUnsafeDirectory)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("%w: certificate directory must be owned by the agent user", ErrUnsafeDirectory)
	}
	return nil
}

func (s *Service) Ready() bool { return s != nil && s.ready }
func (s *Service) CAState() error {
	if s == nil {
		return ErrCAUnavailable
	}
	return s.caErr
}

func (s *Service) IsRevoked(serial string) bool {
	return s != nil && s.cache != nil && s.cache.has(serial)
}

func (s *Service) Run(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.cleanup(ctx)
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.cleanup(ctx)
		}
	}
}

func (s *Service) cleanup(ctx context.Context) {
	now := s.now().UTC()
	var expired []state.Certificate
	if err := s.store.Transact(ctx, func(tx state.Tx) error {
		rows, err := tx.ListExpiredCertificates(now)
		if err != nil {
			return err
		}
		expired = rows
		for _, row := range rows {
			if err := tx.DeleteCertificate(row.SerialNumber); err != nil {
				return err
			}
		}
		cutoff := now.Add(-time.Duration(maxInt(s.cfg.State.AuditRetentionDays, s.cfg.CertificateAuthority.MaxTTLDays)) * 24 * time.Hour)
		for {
			deleted, err := tx.DeleteAuditBefore(cutoff)
			if err != nil || deleted == 0 {
				return err
			}
		}
	}); err != nil {
		s.logger.Error("certificate cleanup failed", "error", err)
		return
	}
	for _, row := range expired {
		s.cache.remove(row.SerialNumber)
		for _, path := range []string{row.CertFile, row.KeyFile} {
			_ = os.Remove(path)
		}
	}
	if s.journal != nil && s.journal.healthy {
		if err := s.journal.compact(now); err != nil {
			s.logger.Error("revocation journal compaction failed", "error", err)
		}
	}
	if s.metrics != nil {
		s.metrics.Set("kouja_revocation_journal_healthy", boolFloat(s.journal == nil || s.journal.healthy))
	}
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func (s *Service) Get(_ context.Context, serial string) (CertificateRecord, error) {
	if serial == "" {
		return CertificateRecord{}, ErrInvalidSerial
	}
	var record state.Certificate
	if err := s.store.Transact(context.Background(), func(tx state.Tx) error {
		value, err := tx.GetCertificate(serial)
		record = value
		return err
	}); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return CertificateRecord{}, ErrNotFound
		}
		return CertificateRecord{}, err
	}
	return mapRecord(record), nil
}

func (s *Service) List(_ context.Context, options ListOptions) (CertificatePage, error) {
	var rows []state.Certificate
	var next string
	if err := s.store.Transact(context.Background(), func(tx state.Tx) error {
		values, token, err := tx.ListCertificates(options.PageSize, options.PageToken)
		rows, next = values, token
		return err
	}); err != nil {
		return CertificatePage{}, err
	}
	page := CertificatePage{Certificates: make([]CertificateSummary, 0, len(rows)), NextPageToken: next}
	for _, row := range rows {
		page.Certificates = append(page.Certificates, mapSummary(row))
	}
	return page, nil
}

func mapRecord(row state.Certificate) CertificateRecord {
	return CertificateRecord{
		SerialNumber: row.SerialNumber, Role: row.Role, Name: row.Name,
		IssuedBy: row.IssuedBy, IssuerSerial: row.IssuerSerial, RequestID: row.RequestID,
		CertificatePEM: row.CertificatePEM, ClientCAPEM: row.ClientCAPEM,
		CertFile: row.CertFile, KeyFile: row.KeyFile, NotBefore: row.NotBefore,
		NotAfter: row.NotAfter, CreatedAt: row.CreatedAt, RevokedAt: row.RevokedAt,
		RevokedReason: row.RevokedReason, Revoked: row.Revoked,
	}
}

func mapSummary(row state.Certificate) CertificateSummary {
	return CertificateSummary{
		SerialNumber: row.SerialNumber, Role: row.Role, Name: row.Name,
		IssuedBy: row.IssuedBy, IssuerSerial: row.IssuerSerial,
		NotBefore: row.NotBefore, NotAfter: row.NotAfter, CreatedAt: row.CreatedAt,
		RevokedAt: row.RevokedAt, Revoked: row.Revoked,
	}
}
