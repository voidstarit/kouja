package operations

import (
	"context"
	"errors"

	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
)

func (manager *Manager) runDelete(ctx context.Context, request Request, requestHash string, existing *state.Operation) (Result, error) {
	intent, err := parseDeleteIntent(request.Intent)
	if err != nil {
		return Result{}, guardError("INVALID_ARGUMENT", err)
	}
	volume, found, err := manager.lookupVolume(ctx, request.Owner, request.VolumeID)
	if err != nil {
		return Result{}, err
	}
	if !found {
		return Result{}, nil
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
		observation, observeErr := exporter.Observe(ctx, exportSpec(volume, export))
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
	volume.State = "deleting"
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
		stepCtx, cancel := manager.stepContext(ctx, ActionDelete)
		defer cancel()
		if err := exporter.Delete(stepCtx, exportSpec(volume, export), intent.Force); err != nil {
			return manager.failOperation(ctx, operation, &volume, err, request.RequestID)
		}
	}
	stepCtx, cancel := manager.stepContext(ctx, ActionDelete)
	defer cancel()
	if err := manager.lvm.Remove(stepCtx, volume.VGName, volume.LVName); err != nil && !errors.Is(err, lvm.ErrNotFound) {
		return manager.failOperation(ctx, operation, &volume, err, request.RequestID)
	}

	operation.State = "succeeded"
	operation.UpdatedAt = now
	sizeBytes := volume.ActualSizeBytes
	if sizeBytes == 0 {
		sizeBytes = volume.RequestedSizeBytes
	}
	err = manager.store.Transact(ctx, func(tx state.Tx) error {
		if err := tx.PutTombstone(state.Tombstone{
			Owner:           volume.Owner,
			VolumeID:        volume.VolumeID,
			ResourceUUID:    volume.ResourceUUID,
			LVName:          volume.LVName,
			VGName:          volume.VGName,
			SizeBytes:       sizeBytes,
			DeletedAt:       now,
			LastOperationID: operation.ID,
			Reason:          "requested",
		}); err != nil {
			return err
		}
		if err := tx.DeleteVolume(volume.Owner, volume.VolumeID); err != nil {
			return err
		}
		if err := tx.PutOperation(operation); err != nil {
			return err
		}
		return tx.PutAudit(state.Audit{
			Timestamp:   now,
			Caller:      operation.Owner,
			OperationID: operation.ID,
			RequestID:   request.RequestID,
			VolumeID:    operation.VolumeID,
			Action:      operation.Action,
			Result:      "succeeded",
			PostState:   "deleted",
		})
	})
	if err != nil {
		return Result{}, err
	}
	return Result{OperationID: operation.ID}, nil
}
