package operations

import (
	"context"
	"errors"
	"fmt"

	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
)

func (manager *Manager) runResize(ctx context.Context, request Request, requestHash string, existing *state.Operation) (Result, error) {
	intent, err := parseResizeIntent(request.Intent)
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
	if intent.SizeBytes < volume.RequestedSizeBytes {
		return Result{}, guardError("SHRINK_NOT_SUPPORTED", fmt.Errorf("%w: shrinking is not supported", ErrInvalidRequest))
	}
	if intent.SizeBytes > volume.RequestedSizeBytes {
		capacity, capacityErr := manager.lvm.Capacity(ctx, volume.VGName)
		if capacityErr != nil {
			return Result{}, guardError("UNAVAILABLE", capacityErr)
		}
		if capacity.AllocatableBytes < intent.SizeBytes-volume.RequestedSizeBytes {
			return Result{}, guardError("RESOURCE_EXHAUSTED", ErrResourceExhausted)
		}
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
	}

	now := manager.now()
	operation := manager.operationFor(request, volume.ResourceUUID, requestHash, string(request.Intent), now, existing)
	previousState := volume.State
	volume.State = "resizing"
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

	stepCtx, cancel := manager.stepContext(ctx, ActionResize)
	defer cancel()
	lv, err := manager.lvm.Extend(stepCtx, volume.VGName, volume.LVName, intent.SizeBytes)
	if err != nil {
		return manager.failOperation(ctx, operation, &volume, err, request.RequestID)
	}
	volume.RequestedSizeBytes = intent.SizeBytes
	volume.ActualSizeBytes = lv.SizeBytes
	if err := ctx.Err(); err != nil {
		return manager.failOperation(ctx, operation, &volume, err, request.RequestID)
	}
	if exporter != nil {
		if resizer, ok := exporter.(target.Resizer); ok {
			if err := resizer.Resize(stepCtx, exportSpec(volume, export)); err != nil {
				return manager.failOperation(ctx, operation, &volume, err, request.RequestID)
			}
		}
	}
	volume.State = previousState
	if volume.State == "" {
		volume.State = "available"
	}
	var exportPointer *state.Export
	if exportErr == nil {
		export.UpdatedAt = now
		exportPointer = &export
	}
	return manager.succeedOperation(ctx, operation, volume, exportPointer, nil, request.RequestID)
}
