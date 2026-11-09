package pki

import (
	"context"
	"errors"
	"time"

	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/warnings"
)

var (
	ErrDisabled         = errors.New("pki: certificate issuance is disabled")
	ErrCAUnavailable    = errors.New("pki: certificate authority is unavailable")
	ErrInvalidRole      = errors.New("pki: invalid certificate role")
	ErrInvalidName      = errors.New("pki: invalid certificate name")
	ErrInvalidTTL       = errors.New("pki: invalid certificate ttl")
	ErrInvalidSerial    = errors.New("pki: invalid certificate serial")
	ErrExternalInvalid  = errors.New("pki: invalid external certificate")
	ErrNotFound         = state.ErrNotFound
	ErrQuotaExceeded    = errors.New("pki: active certificate quota exceeded")
	ErrJournalUnhealthy = errors.New("pki: revocation journal is unhealthy")
	ErrFile             = errors.New("pki: certificate file operation failed")
	ErrUnsafeDirectory  = errors.New("pki: unsafe certificate directory")
)

// Caller is the authenticated identity performing an operation.
type Caller struct {
	Owner        string
	SerialNumber string
	RequestID    string
	Source       string
	RemoteAddr   string
}

type IssueRequest struct {
	Role    string
	Name    string
	TTLDays int32
}

type IssuedCertificate struct {
	CertificatePEM string
	PrivateKeyPEM  string
	ClientCAPEM    string
	SerialNumber   string
	Role           string
	Name           string
	IssuedBy       string
	IssuerSerial   string
	RequestID      string
	NotBefore      time.Time
	NotAfter       time.Time
	Warnings       []warnings.Warning
}

type CertificateRecord struct {
	SerialNumber   string
	Role           string
	Name           string
	IssuedBy       string
	IssuerSerial   string
	RequestID      *string
	CertificatePEM string
	ClientCAPEM    string
	CertFile       string
	KeyFile        string
	NotBefore      time.Time
	NotAfter       time.Time
	CreatedAt      time.Time
	RevokedAt      *time.Time
	RevokedReason  string
	Revoked        bool
}

type CertificateSummary struct {
	SerialNumber string
	Role         string
	Name         string
	IssuedBy     string
	IssuerSerial string
	NotBefore    time.Time
	NotAfter     time.Time
	CreatedAt    time.Time
	RevokedAt    *time.Time
	Revoked      bool
}

type CertificatePage struct {
	Certificates  []CertificateSummary
	NextPageToken string
}

type ListOptions struct {
	PageSize  int32
	PageToken string
}

type FileInfo struct {
	Name         string
	Role         string
	Kind         string
	SerialNumber string
	SizeBytes    int64
	ModifiedAt   time.Time
	Managed      bool
	Revoked      bool
	NotAfter     time.Time
}

// Interfaces are the consumer boundaries used by the agent and admin UI.
type Issuer interface {
	Issue(ctx context.Context, caller Caller, request IssueRequest) (IssuedCertificate, error)
}

type Registry interface {
	Get(ctx context.Context, serial string) (CertificateRecord, error)
	List(ctx context.Context, options ListOptions) (CertificatePage, error)
	Revoke(ctx context.Context, caller Caller, serial, reason string) (CertificateRecord, []warnings.Warning, error)
}

type ExternalRevoker interface {
	RevokeExternal(ctx context.Context, caller Caller, certificatePEM, reason string) (CertificateRecord, []warnings.Warning, error)
}

type FileStore interface {
	ListFiles(ctx context.Context) ([]FileInfo, error)
	ResolveFile(name string) (string, error)
}

type RevocationChecker interface {
	IsRevoked(serial string) bool
}

type CAStatus interface {
	Ready() bool
	CAState() error
}
