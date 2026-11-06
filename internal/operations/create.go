package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/voidstarit/kouja/internal/credential"
	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
)

func (manager *Manager) runEnsure(ctx context.Context, request Request, requestHash string, existing *state.Operation) (Result, error) {
	switch request.Action {
	case ActionExport:
		intent, err := parseExportIntent(request.Intent)
		if err != nil {
			return Result{}, guardError("INVALID_ARGUMENT", err)
		}
		return manager.runExport(ctx, request, intent, requestHash, existing)
	default:
		intent, err := parseCreateIntent(request.Intent)
		if err != nil {
			return Result{}, guardError("INVALID_ARGUMENT", err)
		}
		return manager.runCreate(ctx, request, intent, requestHash, existing)
	}
}

func (manager *Manager) runCreate(ctx context.Context, request Request, intent CreateIntent, requestHash string, existing *state.Operation) (Result, error) {
	volume, found, err := manager.lookupVolume(ctx, request.Owner, request.VolumeID)
	if err != nil {
		return Result{}, err
	}
	if found {
		if err := manager.validateExistingVolume(volume, intent); err != nil {
			return Result{}, err
		}
		if volume.State == "drift" {
			return Result{}, guardError("VOLUME_IN_DRIFT", ErrVolumeInDrift)
		}
		if volume.State == "failed" && existing == nil {
			return Result{}, guardError("VOLUME_FAILED", ErrOperationFailed)
		}
		if _, err := manager.lvm.Observe(ctx, volume.VGName, volume.LVName); errors.Is(err, lvm.ErrNotFound) {
			if existing == nil {
				return Result{}, guardError("VOLUME_IN_DRIFT", ErrVolumeInDrift)
			}
		} else if err != nil {
			return Result{}, guardError("UNAVAILABLE", err)
		}
	} else if err := manager.guardCreateCapacity(ctx, intent); err != nil {
		return Result{}, err
	}

	now := manager.now()
	operation := manager.operationFor(request, intent.ResourceUUID, requestHash, string(request.Intent), now, existing)
	if !found {
		volume = state.Volume{
			Owner:              request.Owner,
			VolumeID:           request.VolumeID,
			ResourceUUID:       intent.ResourceUUID,
			LVName:             "kouja-" + intent.ResourceUUID,
			VGName:             intent.VG,
			RequestedSizeBytes: intent.SizeBytes,
			State:              "creating",
			CreatedAt:          now,
			UpdatedAt:          now,
			LastOperationID:    operation.ID,
		}
	} else {
		volume.State = "creating"
		volume.LastOperationID = operation.ID
		volume.ErrorCode = ""
		volume.UpdatedAt = now
	}
	if err := manager.store.Transact(ctx, func(tx state.Tx) error {
		if err := tx.PutOperation(operation); err != nil {
			return err
		}
		return tx.PutVolume(volume)
	}); err != nil {
		return Result{}, err
	}

	stepCtx, cancel := manager.stepContext(ctx, ActionCreate)
	defer cancel()
	lv, err := manager.ensureLV(stepCtx, volume)
	if err != nil {
		return manager.failOperation(ctx, operation, &volume, err, request.RequestID)
	}
	volume.ActualSizeBytes = lv.SizeBytes
	volume.State = "available"

	if found {
		if existingExport, exportErr := manager.lookupExport(ctx, request.Owner, request.VolumeID); exportErr == nil && !intent.Export {
			volume.State = "exported"
			return manager.succeedOperation(ctx, operation, volume, &existingExport, nil, request.RequestID)
		}
	}

	if !intent.Export {
		return manager.succeedOperation(ctx, operation, volume, nil, nil, request.RequestID)
	}

	exportCtx, cancelExport := manager.stepContext(ctx, ActionExport)
	defer cancelExport()
	exportRecord, storedCredential, err := manager.ensureExport(exportCtx, volume, intent.Protocol, intent.Initiator, intent.TargetName, intent.Portal, false)
	if err != nil {
		return manager.failOperation(ctx, operation, &volume, err, request.RequestID)
	}
	volume.State = "exported"
	return manager.succeedOperation(ctx, operation, volume, exportRecord, storedCredential, request.RequestID)
}

func (manager *Manager) runExport(ctx context.Context, request Request, intent ExportIntent, requestHash string, existing *state.Operation) (Result, error) {
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
	if volume.State == "failed" && existing == nil {
		return Result{}, guardError("VOLUME_FAILED", ErrOperationFailed)
	}
	existingExport, exportErr := manager.lookupExport(ctx, request.Owner, request.VolumeID)
	if exportErr != nil && !errors.Is(exportErr, state.ErrNotFound) {
		return Result{}, exportErr
	}
	if exportErr == nil {
		if !intent.RotateCredential && !sameExport(existingExport, intent) {
			return Result{}, guardError("VOLUME_ALREADY_EXPORTED", ErrVolumeAlreadyExport)
		}
		if intent.RotateCredential {
			if existingExport.Protocol != intent.Protocol {
				return Result{}, guardError("VOLUME_ALREADY_EXPORTED", ErrVolumeAlreadyExport)
			}
			if err := manager.checkExportRotation(ctx, volume, existingExport, intent.Force); err != nil {
				return Result{}, err
			}
		}
	}
	now := manager.now()
	operation := manager.operationFor(request, volume.ResourceUUID, requestHash, string(request.Intent), now, existing)
	volume.State = "exporting"
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
	stepCtx, cancel := manager.stepContext(ctx, ActionExport)
	defer cancel()
	if intent.RotateCredential && exportErr == nil {
		if err := manager.rotateExport(stepCtx, volume, existingExport, intent.Force); err != nil {
			return manager.failOperation(ctx, operation, &volume, err, request.RequestID)
		}
	}
	exportRecord, storedCredential, err := manager.ensureExport(stepCtx, volume, intent.Protocol, intent.Initiator, intent.TargetName, intent.Portal, intent.RotateCredential)
	if err != nil {
		return manager.failOperation(ctx, operation, &volume, err, request.RequestID)
	}
	volume.State = "exported"
	return manager.succeedOperation(ctx, operation, volume, exportRecord, storedCredential, request.RequestID)
}

func (manager *Manager) guardCreateCapacity(ctx context.Context, intent CreateIntent) error {
	unlock := manager.vgLocks.Lock(intent.VG)
	defer unlock()
	if err := manager.lvm.Eligibility(ctx, intent.VG); err != nil {
		if errors.Is(err, lvm.ErrNotFound) {
			return guardError("NOT_FOUND", err)
		}
		return guardError("INELIGIBLE_VG", err)
	}
	capacity, err := manager.lvm.Capacity(ctx, intent.VG)
	if err != nil {
		return guardError("UNAVAILABLE", err)
	}
	if capacity.AllocatableBytes < intent.SizeBytes {
		return guardError("RESOURCE_EXHAUSTED", ErrResourceExhausted)
	}
	return nil
}

func (manager *Manager) ensureLV(ctx context.Context, volume state.Volume) (lvm.LV, error) {
	if existing, err := manager.lvm.Observe(ctx, volume.VGName, volume.LVName); err == nil {
		return existing, nil
	} else if !errors.Is(err, lvm.ErrNotFound) {
		return lvm.LV{}, err
	}
	unlock := manager.vgLocks.Lock(volume.VGName)
	defer unlock()
	capacity, err := manager.lvm.Capacity(ctx, volume.VGName)
	if err != nil {
		return lvm.LV{}, err
	}
	if capacity.AllocatableBytes < volume.RequestedSizeBytes {
		return lvm.LV{}, ErrResourceExhausted
	}
	ownerHash := ownerHash(volume.Owner)
	lv, err := manager.lvm.Create(ctx, volume.VGName, volume.LVName, volume.RequestedSizeBytes, map[string]string{
		"kouja.managed": "true",
		"kouja.volume":  volume.ResourceUUID,
		"kouja.owner":   ownerHash,
	})
	if err == nil {
		return lv, nil
	}
	if observed, observeErr := manager.lvm.Observe(ctx, volume.VGName, volume.LVName); observeErr == nil {
		return observed, nil
	}
	return lvm.LV{}, err
}

func (manager *Manager) ensureExport(ctx context.Context, volume state.Volume, protocol, initiator, targetName, portal string, replace bool) (*state.Export, *state.Credential, error) {
	exporter, err := manager.targetFor(protocol)
	if err != nil {
		return nil, nil, err
	}
	existing, existingErr := manager.lookupExport(ctx, volume.Owner, volume.VolumeID)
	if existingErr != nil && !errors.Is(existingErr, state.ErrNotFound) {
		return nil, nil, existingErr
	}
	if existingErr == nil && !replace && !sameExport(existing, ExportIntent{Protocol: protocol, Initiator: initiator, TargetName: targetName, Portal: portal}) {
		return nil, nil, ErrVolumeAlreadyExport
	}
	now := manager.now()
	stored, storedPointer, err := manager.ensureStoredCredential(ctx, volume, protocol)
	if err != nil {
		return nil, nil, err
	}
	plain, err := manager.reveal(ctx, stored)
	if err != nil {
		return nil, nil, err
	}
	spec := target.ExportSpec{
		Owner:        volume.Owner,
		VolumeID:     volume.VolumeID,
		ResourceUUID: volume.ResourceUUID,
		VGName:       volume.VGName,
		LVName:       volume.LVName,
		Protocol:     protocol,
		Initiator:    initiator,
		TargetName:   targetName,
		Portal:       portal,
		LUN:          0,
	}
	result, err := exporter.Export(ctx, spec, plain)
	if err != nil {
		return nil, nil, err
	}
	if err := manager.creds.Install(ctx, spec, stored); err != nil {
		return nil, nil, err
	}
	record := state.Export{
		Owner:      volume.Owner,
		VolumeID:   volume.VolumeID,
		Protocol:   protocol,
		Initiator:  initiator,
		TargetName: targetName,
		Portal:     portal,
		LUN:        result.LUN,
		NSID:       result.NSID,
		State:      "exported",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if existingErr == nil {
		record.CreatedAt = existing.CreatedAt
	}
	return &record, storedPointer, nil
}

func (manager *Manager) ensureStoredCredential(ctx context.Context, volume state.Volume, protocol string) (credential.Credential, *state.Credential, error) {
	var stored state.Credential
	err := manager.store.Transact(ctx, func(tx state.Tx) error {
		value, err := tx.GetActiveCredential(volume.Owner, volume.VolumeID)
		if err != nil {
			return err
		}
		stored = value
		return nil
	})
	if err == nil {
		return stateToCredential(stored), nil, nil
	}
	if !errors.Is(err, state.ErrNotFound) {
		return credential.Credential{}, nil, err
	}
	if manager.creds == nil {
		return credential.Credential{}, nil, fmt.Errorf("%w: credential manager is not configured", ErrInvalidRequest)
	}
	generated, err := manager.creds.Generate(ctx, volume.Owner, volume.VolumeID, volume.ResourceUUID, credential.Protocol(strings.ToLower(protocol)))
	if err != nil {
		return credential.Credential{}, nil, err
	}
	record := state.Credential{
		ID:               generated.ID,
		Owner:            generated.Owner,
		VolumeID:         generated.VolumeID,
		Protocol:         string(generated.Protocol),
		SecretCiphertext: generated.SecretCiphertext,
		Nonce:            generated.Nonce,
		KeyID:            generated.KeyID,
		KeyFingerprint:   generated.KeyFingerprint,
		State:            "active",
		CreatedAt:        generated.CreatedAt,
		RotatedAt:        generated.RotatedAt,
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = manager.now()
	}
	return generated, &record, nil
}

func (manager *Manager) lookupVolume(ctx context.Context, owner, volumeID string) (state.Volume, bool, error) {
	var volume state.Volume
	found := false
	err := manager.store.Transact(ctx, func(tx state.Tx) error {
		value, err := tx.GetVolume(owner, volumeID)
		if errors.Is(err, state.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		volume = value
		found = true
		return nil
	})
	return volume, found, err
}

func (manager *Manager) lookupExport(ctx context.Context, owner, volumeID string) (state.Export, error) {
	var export state.Export
	err := manager.store.Transact(ctx, func(tx state.Tx) error {
		value, err := tx.GetExport(owner, volumeID)
		if err != nil {
			return err
		}
		export = value
		return nil
	})
	return export, err
}

func (manager *Manager) validateExistingVolume(volume state.Volume, intent CreateIntent) error {
	if volume.VGName != intent.VG || volume.ResourceUUID != intent.ResourceUUID ||
		volume.RequestedSizeBytes != intent.SizeBytes {
		return guardError("VOLUME_REQUEST_MISMATCH", ErrVolumeAlreadyExists)
	}
	return nil
}

func sameExport(export state.Export, intent ExportIntent) bool {
	return export.Protocol == intent.Protocol && export.Initiator == intent.Initiator &&
		export.TargetName == intent.TargetName && export.Portal == intent.Portal
}

func stateToCredential(stored state.Credential) credential.Credential {
	return credential.Credential{
		ID:               stored.ID,
		Owner:            stored.Owner,
		VolumeID:         stored.VolumeID,
		Protocol:         credential.Protocol(stored.Protocol),
		SecretCiphertext: stored.SecretCiphertext,
		Nonce:            stored.Nonce,
		KeyID:            stored.KeyID,
		KeyFingerprint:   stored.KeyFingerprint,
		State:            stored.State,
		CreatedAt:        stored.CreatedAt,
		RotatedAt:        stored.RotatedAt,
	}
}

func ownerHash(owner string) string {
	sum := sha256.Sum256([]byte(owner))
	return hex.EncodeToString(sum[:])[:16]
}

func (manager *Manager) stepContext(ctx context.Context, action Action) (context.Context, func()) {
	timeout := manager.opts.StepTimeouts[action]
	if timeout <= 0 {
		switch action {
		case ActionCreate, ActionDelete, ActionResize:
			timeout = 120 * time.Second
		case ActionExport:
			timeout = 60 * time.Second
		default:
			timeout = manager.opts.TotalTimeout
		}
	}
	return context.WithTimeout(ctx, timeout)
}
