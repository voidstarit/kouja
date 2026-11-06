package target

import (
	"context"
	"time"
)

type SessionState string

const (
	SessionNone    SessionState = "none"
	SessionActive  SessionState = "active"
	SessionUnknown SessionState = "unknown"
)

type ExportSpec struct {
	Owner        string
	VolumeID     string
	ResourceUUID string
	VGName       string
	LVName       string
	Protocol     string
	Initiator    string
	TargetName   string
	LUN          int32
	NSID         int32
	Portal       string
	Force        bool
}

type ExportResult struct {
	TargetName string
	Portal     string
	LUN        int32
	NSID       int32
	Credential Credential
}

type Credential struct {
	Username string
	Secret   string
	Keyring  string
}

type ExportObservation struct {
	TargetPresent     bool
	NamespacePresent  bool
	ACLPresent        bool
	ActiveSessions    int32
	SessionState      SessionState
	ObservedInitiator string
	ObservedAt        time.Time
}

type Session struct {
	Initiator string
	Target    string
	State     SessionState
}

type Exporter interface {
	Export(ctx context.Context, spec ExportSpec, credential Credential) (ExportResult, error)
	Unexport(ctx context.Context, spec ExportSpec, force bool) error
	Observe(ctx context.Context, spec ExportSpec) (ExportObservation, error)
	ListSessions(ctx context.Context) ([]Session, error)
	Restore(ctx context.Context, spec ExportSpec, credential Credential) error
	Delete(ctx context.Context, spec ExportSpec, force bool) error
}

// Resizer is implemented by exporters that need to refresh target metadata
// after the backing LV grows. The optional interface keeps existing exporter
// test doubles and future adapters simple.
type Resizer interface {
	Resize(ctx context.Context, spec ExportSpec) error
}
