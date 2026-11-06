package credential

import (
	"context"
	"time"

	"github.com/voidstarit/kouja/internal/auth"
	"github.com/voidstarit/kouja/internal/target"
)

type Protocol string

const (
	ProtocolISCSI   Protocol = "iscsi"
	ProtocolNVMeTCP Protocol = "nvme-tcp"
)

type Credential struct {
	ID               string
	Owner            string
	VolumeID         string
	Protocol         Protocol
	SecretCiphertext []byte
	Nonce            []byte
	KeyID            string
	KeyFingerprint   string
	State            string
	CreatedAt        time.Time
	RotatedAt        *time.Time
}

type Manager interface {
	Generate(ctx context.Context, owner, volumeID, resourceUUID string, protocol Protocol) (Credential, error)
	// Reveal decrypts a stored credential for installation in a target. The
	// plaintext must not be logged or persisted by callers.
	Reveal(ctx context.Context, credential Credential) (target.Credential, error)
	Get(ctx context.Context, caller auth.Identity, owner, volumeID string) (Credential, error)
	Rotate(ctx context.Context, caller auth.Identity, owner, volumeID string) (Credential, error)
	Install(ctx context.Context, spec target.ExportSpec, credential Credential) error
	Revoke(ctx context.Context, spec target.ExportSpec) error
}
