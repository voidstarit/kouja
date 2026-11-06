package credential

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/voidstarit/kouja/internal/auth"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
)

const keySize = 32

var (
	ErrInvalid      = errors.New("credential: invalid request")
	ErrUnauthorized = errors.New("credential: unauthorized")
	ErrKeyMismatch  = errors.New("credential: key fingerprint does not match")
)

type KeyedManager struct {
	store       *state.Store
	key         []byte
	keyID       string
	fingerprint string
	rand        io.Reader
	now         func() time.Time
}

var _ Manager = (*KeyedManager)(nil)

// Open loads or creates the AES-256-GCM key used to protect target
// credentials. The key file is separate from SQLite and must be backed up
// independently.
func Open(store *state.Store, keyPath string) (*KeyedManager, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: state store is required", ErrInvalid)
	}
	if keyPath == "" {
		return nil, fmt.Errorf("%w: key path is required", ErrInvalid)
	}
	key, err := loadOrCreateKey(keyPath)
	if err != nil {
		return nil, err
	}
	fingerprint := sha256.Sum256(key)
	manager := &KeyedManager{
		store:       store,
		key:         key,
		keyID:       "key-" + hex.EncodeToString(fingerprint[:8]),
		fingerprint: hex.EncodeToString(fingerprint[:]),
		rand:        rand.Reader,
		now:         func() time.Time { return time.Now().UTC() },
	}
	return manager, nil
}

func (manager *KeyedManager) Generate(ctx context.Context, owner, volumeID, resourceUUID string, protocol Protocol) (Credential, error) {
	if owner == "" || volumeID == "" || resourceUUID == "" {
		return Credential{}, fmt.Errorf("%w: owner, volume_id, and resource_uuid are required", ErrInvalid)
	}
	if protocol != ProtocolISCSI && protocol != ProtocolNVMeTCP {
		return Credential{}, fmt.Errorf("%w: unsupported protocol %q", ErrInvalid, protocol)
	}
	plain := target.Credential{}
	switch protocol {
	case ProtocolISCSI:
		plain.Username = "kouja-" + resourceUUID
		secret := make([]byte, 32)
		if _, err := io.ReadFull(manager.rand, secret); err != nil {
			return Credential{}, fmt.Errorf("credential: generate CHAP secret: %w", err)
		}
		plain.Secret = base64.RawURLEncoding.EncodeToString(secret)
	case ProtocolNVMeTCP:
		command := exec.CommandContext(ctx, "nvme", "gen-tls-key", //nolint:gosec // fixed command, explicit argv
			"--subsysnqn", "nqn.kouja.local:credential")
		output, err := command.Output()
		if err != nil {
			return Credential{}, fmt.Errorf("credential: generate NVMe TLS key: %w", err)
		}
		plain.Secret = strings.TrimSpace(string(output))
		plain.Keyring = ".nvme"
	}
	plaintext, err := json.Marshal(plain) //nolint:gosec // plaintext is immediately encrypted below
	if err != nil {
		return Credential{}, fmt.Errorf("credential: encode plaintext: %w", err)
	}
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(manager.rand, nonce); err != nil {
		return Credential{}, fmt.Errorf("credential: generate nonce: %w", err)
	}
	aead, err := manager.aead()
	if err != nil {
		return Credential{}, err
	}
	recordID := uuid.NewString()
	ciphertext := aead.Seal(nil, nonce, plaintext, manager.aad(owner, volumeID, string(protocol), recordID))
	return Credential{
		ID:               recordID,
		Owner:            owner,
		VolumeID:         volumeID,
		Protocol:         protocol,
		SecretCiphertext: ciphertext,
		Nonce:            nonce,
		KeyID:            manager.keyID,
		KeyFingerprint:   manager.fingerprint,
		State:            "active",
		CreatedAt:        manager.now(),
	}, nil
}

func (manager *KeyedManager) Reveal(_ context.Context, stored Credential) (target.Credential, error) {
	if stored.KeyID != manager.keyID || stored.KeyFingerprint != manager.fingerprint {
		return target.Credential{}, ErrKeyMismatch
	}
	aead, err := manager.aead()
	if err != nil {
		return target.Credential{}, err
	}
	plaintext, err := aead.Open(nil, stored.Nonce, stored.SecretCiphertext,
		manager.aad(stored.Owner, stored.VolumeID, string(stored.Protocol), stored.ID))
	if err != nil {
		return target.Credential{}, fmt.Errorf("credential: decrypt: %w", err)
	}
	var result target.Credential
	if err := json.Unmarshal(plaintext, &result); err != nil {
		return target.Credential{}, fmt.Errorf("credential: decode plaintext: %w", err)
	}
	return result, nil
}

func (manager *KeyedManager) Get(ctx context.Context, caller auth.Identity, owner, volumeID string) (Credential, error) {
	if err := manager.authorizeRead(ctx, caller, owner, volumeID); err != nil {
		return Credential{}, err
	}
	var stored state.Credential
	err := manager.store.Transact(ctx, func(tx state.Tx) error {
		value, err := tx.GetActiveCredential(owner, volumeID)
		if err != nil {
			return err
		}
		stored = value
		return nil
	})
	if err != nil {
		return Credential{}, err
	}
	return fromState(stored), nil
}

func (manager *KeyedManager) Rotate(ctx context.Context, caller auth.Identity, owner, volumeID string) (Credential, error) {
	if caller.Owner != owner && caller.Role != auth.RoleAdmin {
		return Credential{}, ErrUnauthorized
	}
	var volume state.Volume
	var old state.Credential
	if err := manager.store.Transact(ctx, func(tx state.Tx) error {
		value, err := tx.GetVolume(owner, volumeID)
		if err != nil {
			return err
		}
		volume = value
		active, err := tx.GetActiveCredential(owner, volumeID)
		if err != nil {
			return err
		}
		old = active
		return nil
	}); err != nil {
		return Credential{}, err
	}
	replacement, err := manager.Generate(ctx, owner, volumeID, volume.ResourceUUID, Protocol(old.Protocol))
	if err != nil {
		return Credential{}, err
	}
	now := manager.now()
	old.State = "superseded"
	old.RotatedAt = &now
	if err := manager.store.Transact(ctx, func(tx state.Tx) error {
		if err := tx.PutCredential(old); err != nil {
			return err
		}
		return tx.PutCredential(toState(replacement))
	}); err != nil {
		return Credential{}, err
	}
	return replacement, nil
}

// Install is a no-op for iSCSI because the LIO adapter writes ACLs during
// Export. NVMe/TCP credentials are inserted into the .nvme keyring before the
// initiator connects.
func (manager *KeyedManager) Install(ctx context.Context, spec target.ExportSpec, stored Credential) error {
	if spec.Protocol != string(ProtocolNVMeTCP) {
		return nil
	}
	plain, err := manager.Reveal(ctx, stored)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, "nvme", "check-tls-key", "--insert", //nolint:gosec // explicit argv; no shell
		"--hostnqn", spec.Initiator, "--subsysnqn", spec.TargetName,
		"--keydata", plain.Secret, "--identity", "1")
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("credential: install NVMe TLS key: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func (manager *KeyedManager) Revoke(context.Context, target.ExportSpec) error { return nil }

func (manager *KeyedManager) authorizeRead(ctx context.Context, caller auth.Identity, owner, volumeID string) error {
	if caller.Owner == owner || caller.Role == auth.RoleAdmin {
		return nil
	}
	if caller.Role != auth.RoleNode {
		return ErrUnauthorized
	}
	err := manager.store.Transact(ctx, func(tx state.Tx) error {
		grants, err := tx.ListGrants(owner, volumeID)
		if err != nil {
			return err
		}
		now := manager.now()
		for _, grant := range grants {
			if grant.NodeCallerID == caller.Owner && grant.ExpiresAt.After(now) {
				return nil
			}
		}
		return ErrUnauthorized
	})
	return err
}

func (manager *KeyedManager) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(manager.key)
	if err != nil {
		return nil, fmt.Errorf("credential: create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("credential: create GCM: %w", err)
	}
	return aead, nil
}

func (manager *KeyedManager) aad(owner, volumeID, protocol, recordID string) []byte {
	return []byte(strings.Join([]string{owner, volumeID, protocol, recordID, manager.keyID}, "\x00"))
}

func loadOrCreateKey(path string) ([]byte, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is the configured credential key file
	if err == nil {
		if len(data) != keySize {
			return nil, fmt.Errorf("%w: key file %q must contain %d bytes", ErrInvalid, path, keySize)
		}
		_ = os.Chmod(path, 0o600)
		return data, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("credential: read key: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("credential: create key directory: %w", err)
	}
	key := make([]byte, keySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("credential: generate key: %w", err)
	}
	temporary, err := os.CreateTemp(dir, ".secret-*")
	if err != nil {
		return nil, fmt.Errorf("credential: create temporary key: %w", err)
	}
	tempPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(tempPath)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return nil, fmt.Errorf("credential: set key mode: %w", err)
	}
	if _, err := temporary.Write(key); err != nil {
		cleanup()
		return nil, fmt.Errorf("credential: write key: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return nil, fmt.Errorf("credential: sync key: %w", err)
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("credential: close key: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return nil, fmt.Errorf("credential: install key: %w", err)
	}
	return key, nil
}

func toState(record Credential) state.Credential {
	return state.Credential{
		ID:               record.ID,
		Owner:            record.Owner,
		VolumeID:         record.VolumeID,
		Protocol:         string(record.Protocol),
		SecretCiphertext: record.SecretCiphertext,
		Nonce:            record.Nonce,
		KeyID:            record.KeyID,
		KeyFingerprint:   record.KeyFingerprint,
		State:            record.State,
		CreatedAt:        record.CreatedAt,
		RotatedAt:        record.RotatedAt,
	}
}

func fromState(record state.Credential) Credential {
	return Credential{
		ID:               record.ID,
		Owner:            record.Owner,
		VolumeID:         record.VolumeID,
		Protocol:         Protocol(record.Protocol),
		SecretCiphertext: record.SecretCiphertext,
		Nonce:            record.Nonce,
		KeyID:            record.KeyID,
		KeyFingerprint:   record.KeyFingerprint,
		State:            record.State,
		CreatedAt:        record.CreatedAt,
		RotatedAt:        record.RotatedAt,
	}
}
