package operations

import (
	"context"
	"errors"

	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
)

func (manager *Manager) runUnexport(ctx context.Context, request Request, requestHash string, existing *state.Operation) (Result, error) {
	intent, err := parseUnexportIntent(request.Intent)
	if err != nil {
		return Result{}, guardError("INVALID_ARGUMENT", err)
	}
	volume, found, err := manager.lookupVolume(ctx, request.Owner, request.VolumeID)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{}, guardError("NOT_FOUND", state.ErrNotFound)
	}
	if volume.State == "drift" {
		return Result{}, guardError("VOLUME_IN_DRIFT", ErrVolumeInDrift)
	}
	export, exportErr := manager.lookupExport(ctx, request.Owner, request.VolumeID)
	if exportErr != nil && !errors.Is(exportErr, state.ErrNotFound) {
		return Result{}, exportErr
	}
	var exporter target.Exporter
	if exportErr == nil {
		exporter, err = manager.targetFor(export.Protocol)
		if err != nil {
			return Result{}, guardError("INVALID_ARGUMENT", err)
		}
		spec := exportSpec(volume, export)
		observation, observeErr := exporter.Observe(ctx, spec)
		if observeErr != nil && !errors.Is(observeErr, target.ErrNotFound) {
			return Result{}, guardError("UNAVAILABLE", observeErr)
		}
		if observeErr == nil {
			switch observation.SessionState {
			case target.SessionActive:
				if !intent.Force {
					return Result{}, guardError("SESSION_ACTIVE", ErrSessionActive)
				}
			case target.SessionUnknown:
				if !intent.Force {
					return Result{}, guardError("SESSION_UNKNOWN", ErrSessionUnknown)
				}
			}
		}
	}

	now := manager.now()
	operation := manager.operationFor(request, volume.ResourceUUID, requestHash, string(request.Intent), now, existing)
	volume.State = "unexporting"
	volume.LastOperationID = operation.ID
	volume.UpdatedAt = now
	if err := manager.store.Transact(ctx, func(tx state.Tx) error {
		if err := tx.PutOperation(operation); err != nil {
			return err
		}
		return tx.PutVolume(volume)
	}); err != nil {
		return Result{}, err
	}

	if exporter != nil {
		stepCtx, cancel := manager.stepContext(ctx, ActionUnexport)
		defer cancel()
		if err := exporter.Unexport(stepCtx, exportSpec(volume, export), intent.Force); err != nil {
			return manager.failOperation(ctx, operation, &volume, err, request.RequestID)
		}
	}
	volume.State = "available"
	var exportPointer *state.Export
	if exportErr == nil {
		export.State = "unexported"
		export.UpdatedAt = now
		exportPointer = &export
	}
	var credentialRecord *state.Credential
	if !intent.KeepCredential {
		credentialRecord, err = manager.supersedeCredential(ctx, volume)
		if err != nil {
			return manager.failOperation(ctx, operation, &volume, err, request.RequestID)
		}
	}
	return manager.succeedOperation(ctx, operation, volume, exportPointer, credentialRecord, request.RequestID)
}

func (manager *Manager) supersedeCredential(ctx context.Context, volume state.Volume) (*state.Credential, error) {
	var credentialRecord state.Credential
	err := manager.store.Transact(ctx, func(tx state.Tx) error {
		value, err := tx.GetActiveCredential(volume.Owner, volume.VolumeID)
		if errors.Is(err, state.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		credentialRecord = value
		return nil
	})
	if err != nil {
		return nil, err
	}
	if credentialRecord.ID == "" {
		return nil, nil
	}
	now := manager.now()
	credentialRecord.State = "superseded"
	credentialRecord.RotatedAt = &now
	return &credentialRecord, nil
}

func exportSpec(volume state.Volume, export state.Export) target.ExportSpec {
	return target.ExportSpec{
		Owner:        volume.Owner,
		VolumeID:     volume.VolumeID,
		ResourceUUID: volume.ResourceUUID,
		VGName:       volume.VGName,
		LVName:       volume.LVName,
		Protocol:     export.Protocol,
		Initiator:    export.Initiator,
		TargetName:   export.TargetName,
		LUN:          export.LUN,
		NSID:         export.NSID,
		Portal:       export.Portal,
	}
}
