package state

import "time"

type Volume struct {
	Owner              string
	VolumeID           string
	ResourceUUID       string
	LVName             string
	VGName             string
	RequestedSizeBytes int64
	ActualSizeBytes    int64
	State              string
	DriftReasons       []string
	LastOperationID    string
	ErrorCode          string
	LegacyIdentity     map[string]string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type Export struct {
	Owner      string
	VolumeID   string
	Protocol   string
	Initiator  string
	TargetName string
	LUN        int32
	NSID       int32
	Portal     string
	State      string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type Observation struct {
	Owner             string
	VolumeID          string
	LVPresent         bool
	ObservedSizeBytes int64
	DevicePath        string
	ObservedAt        time.Time
	Source            string
}

type ExportObservation struct {
	Owner             string
	VolumeID          string
	TargetPresent     bool
	NamespacePresent  bool
	ACLPresent        bool
	ActiveSessions    int32
	SessionState      string
	ObservedInitiator string
	ObservedAt        time.Time
	Source            string
}

type Tombstone struct {
	Owner           string
	VolumeID        string
	ResourceUUID    string
	LVName          string
	VGName          string
	SizeBytes       int64
	DeletedAt       time.Time
	LastOperationID string
	Reason          string
}

type VG struct {
	Name         string
	UUID         string
	State        string
	RegisteredAt time.Time
	UpdatedAt    time.Time
}

type Grant struct {
	Owner        string
	VolumeID     string
	NodeID       string
	NodeCallerID string
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

type Credential struct {
	ID               string
	Owner            string
	VolumeID         string
	Protocol         string
	SecretCiphertext []byte
	Nonce            []byte
	KeyID            string
	KeyFingerprint   string
	State            string
	CreatedAt        time.Time
	RotatedAt        *time.Time
}

type Operation struct {
	ID                 string
	Owner              string
	VolumeID           string
	ResourceUUID       string
	Action             string
	RequestID          string
	RequestHash        string
	IntentJSON         string
	State              string
	Attempt            int
	MaxAttempts        int
	DeadlineAt         time.Time
	LastErrorCode      string
	LastErrorRetryable bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type Audit struct {
	ID           int64
	Timestamp    time.Time
	Caller       string
	OperationID  string
	RequestID    string
	VolumeID     string
	Action       string
	Protocol     string
	Initiator    string
	PreState     string
	PostState    string
	Result       string
	ErrorCode    string
	SourceIP     string
	ResourceType string
	ResourceID   string
	Role         string
	Name         string
	Detail       string
}

// AuditEvent is the explicit audit contract used by the shared recorder.
type AuditEvent struct {
	CallerOwner  string
	CallerSerial string
	OperationID  string
	RequestID    string
	ResourceType string
	ResourceID   string
	Role         string
	Name         string
	Action       string
	Result       string
	ErrorCode    string
	Detail       string
	RemoteAddr   string
}

// Certificate is a registry row. RequestID is nullable because external and
// revocation-only rows have no issuing request.
type Certificate struct {
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

type Page struct {
	Limit  int
	Cursor string
}
