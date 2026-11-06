package operations

import (
	"context"
	"errors"

	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
)

// checkExportRotation rejects a credential rotation while the previous
// attachment still has a live session unless the caller explicitly allows a
// forced teardown.
func (manager *Manager) checkExportRotation(ctx context.Context, volume state.Volume, export state.Export, force bool) error {
	exporter, err := manager.targetFor(export.Protocol)
	if err != nil {
		return guardError("INVALID_ARGUMENT", err)
	}
	observation, err := exporter.Observe(ctx, exportSpec(volume, export))
	if errors.Is(err, target.ErrNotFound) {
		return nil
	}
	if err != nil {
		return guardError("UNAVAILABLE", err)
	}
	if !observation.TargetPresent {
		return nil
	}
	switch observation.SessionState {
	case target.SessionActive:
		if !force {
			return guardError("SESSION_ACTIVE", ErrSessionActive)
		}
	case target.SessionUnknown:
		if !force {
			return guardError("SESSION_UNKNOWN", ErrSessionUnknown)
		}
	}
	return nil
}

// rotateExport removes the previous target/session and supersedes its
// credential. The caller then creates a fresh export with a new secret.
func (manager *Manager) rotateExport(ctx context.Context, volume state.Volume, export state.Export, force bool) error {
	exporter, err := manager.targetFor(export.Protocol)
	if err != nil {
		return guardError("INVALID_ARGUMENT", err)
	}
	spec := exportSpec(volume, export)
	observation, err := exporter.Observe(ctx, spec)
	if err != nil && !errors.Is(err, target.ErrNotFound) {
		return guardError("UNAVAILABLE", err)
	}
	if err == nil && observation.TargetPresent {
		if err := exporter.Unexport(ctx, spec, force); err != nil {
			return err
		}
	}
	old, err := manager.supersedeCredential(ctx, volume)
	if err != nil {
		return err
	}
	if old == nil {
		return nil
	}
	if err := manager.store.Transact(ctx, func(tx state.Tx) error {
		return tx.PutCredential(*old)
	}); err != nil {
		return err
	}
	return nil
}
