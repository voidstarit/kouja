package hostid

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/voidstarit/kouja/internal/state"
)

const (
	HostUUIDSetting = "host_uuid"
	BootIDSetting   = "boot_id"
	defaultBootID   = "/proc/sys/kernel/random/boot_id"
)

var (
	ErrMismatch = errors.New("hostid: host identity does not match the database")
	ErrInvalid  = errors.New("hostid: invalid host identity")
)

type Manager struct {
	path  string
	store *state.Store
}

func New(path string, store *state.Store) *Manager {
	return &Manager{path: path, store: store}
}

// Ensure loads the host UUID and reconciles it with the database. A missing or
// mismatched identity is fatal while volumes exist; on a fresh database it is
// created or repaired.
func (manager *Manager) Ensure(ctx context.Context) (string, error) {
	if manager.path == "" {
		return "", fmt.Errorf("%w: empty host id path", ErrInvalid)
	}
	if manager.store == nil {
		return "", fmt.Errorf("%w: state store is required", ErrInvalid)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	fileID, fileErr := manager.read()
	if fileErr != nil && !errors.Is(fileErr, os.ErrNotExist) {
		return "", fileErr
	}

	if fileErr == nil {
		var result string
		err := manager.store.Transact(ctx, func(tx state.Tx) error {
			setting, err := tx.GetSetting(HostUUIDSetting)
			if errors.Is(err, state.ErrNotFound) {
				volumes, err := volumesExist(tx)
				if err != nil {
					return err
				}
				if volumes {
					return ErrMismatch
				}
				result = fileID
				return tx.PutSetting(HostUUIDSetting, fileID)
			}
			if err != nil {
				return err
			}
			if setting == fileID {
				result = fileID
				return nil
			}
			volumes, err := volumesExist(tx)
			if err != nil {
				return err
			}
			if volumes {
				return ErrMismatch
			}
			result = fileID
			return tx.PutSetting(HostUUIDSetting, fileID)
		})
		return result, err
	}

	var result string
	err := manager.store.Transact(ctx, func(tx state.Tx) error {
		volumes, err := volumesExist(tx)
		if err != nil {
			return err
		}
		if volumes {
			return ErrMismatch
		}
		setting, err := tx.GetSetting(HostUUIDSetting)
		if err == nil {
			if _, parseErr := uuid.Parse(setting); parseErr != nil {
				return fmt.Errorf("%w: database host UUID %q is invalid", ErrInvalid, setting)
			}
			result = setting
		} else if errors.Is(err, state.ErrNotFound) {
			result = uuid.NewString()
		} else {
			return err
		}
		return tx.PutSetting(HostUUIDSetting, result)
	})
	if err != nil {
		return "", err
	}
	if err := manager.write(result); err != nil {
		return "", err
	}
	return result, nil
}

func (manager *Manager) read() (string, error) {
	data, err := os.ReadFile(manager.path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if _, err := uuid.Parse(value); err != nil {
		return "", fmt.Errorf("%w: host id file %q is not a UUID: %v", ErrInvalid, manager.path, err)
	}
	return value, nil
}

func (manager *Manager) write(value string) error {
	if _, err := uuid.Parse(value); err != nil {
		return fmt.Errorf("%w: generated host UUID is invalid: %v", ErrInvalid, err)
	}
	dir := filepath.Dir(manager.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("hostid: create directory %q: %w", dir, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // the host-id directory is intentionally private
		return fmt.Errorf("hostid: set directory mode %q: %w", dir, err)
	}
	temp, err := os.CreateTemp(dir, ".host-id-*")
	if err != nil {
		return fmt.Errorf("hostid: create temporary host id: %w", err)
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if err := temp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("hostid: set temporary host id mode: %w", err)
	}
	if _, err := temp.WriteString(value + "\n"); err != nil {
		cleanup()
		return fmt.Errorf("hostid: write temporary host id: %w", err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("hostid: sync temporary host id: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("hostid: close temporary host id: %w", err)
	}
	if err := os.Rename(tempPath, manager.path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("hostid: install host id: %w", err)
	}
	if dirHandle, err := os.Open(dir); err == nil { //nolint:gosec // dir is derived from the configured host-id path
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}
	return nil
}

func volumesExist(tx state.Tx) (bool, error) {
	volumes, err := tx.ListVolumes("", state.Page{Limit: 1})
	if err != nil {
		return false, err
	}
	return len(volumes) > 0, nil
}

func ReadBootID(path string) (string, error) {
	if path == "" {
		path = defaultBootID
	}
	data, err := os.ReadFile(path) //nolint:gosec // path is the configured boot-id path
	if err != nil {
		return "", fmt.Errorf("hostid: read boot id %q: %w", path, err)
	}
	value := strings.TrimSpace(string(data))
	if _, err := uuid.Parse(value); err != nil {
		return "", fmt.Errorf("%w: boot id %q is not a UUID: %v", ErrInvalid, value, err)
	}
	return value, nil
}

func (manager *Manager) RecordBootID(ctx context.Context) (string, error) {
	if manager.store == nil {
		return "", fmt.Errorf("%w: state store is required", ErrInvalid)
	}
	bootID, err := ReadBootID("")
	if err != nil {
		return "", err
	}
	if err := manager.store.Transact(ctx, func(tx state.Tx) error {
		return tx.PutSetting(BootIDSetting, bootID)
	}); err != nil {
		return "", err
	}
	return bootID, nil
}
