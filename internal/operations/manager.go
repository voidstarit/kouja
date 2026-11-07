package operations

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/voidstarit/kouja/internal/credential"
	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
)

const defaultTotalTimeout = 5 * time.Minute

type Manager struct {
	store       *state.Store
	lvm         lvm.Backend
	iscsi       target.Exporter
	nvmet       target.Exporter
	creds       credential.Manager
	opts        Options
	volumeLocks *lockTable
	vgLocks     *lockTable
}

func New(store *state.Store, lvmBackend lvm.Backend, iscsi, nvmet target.Exporter, creds credential.Manager, opts Options) *Manager {
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 3
	}
	if opts.TotalTimeout <= 0 {
		opts.TotalTimeout = defaultTotalTimeout
	}
	if opts.Clock == nil {
		opts.Clock = systemClock{}
	}
	return &Manager{
		store:       store,
		lvm:         lvmBackend,
		iscsi:       iscsi,
		nvmet:       nvmet,
		creds:       creds,
		opts:        opts,
		volumeLocks: newLockTable(),
		vgLocks:     newLockTable(),
	}
}

// DisableTarget force-unexports a volume's target without deleting the volume
// or export row. The unique request intent prevents a later disable after a
// re-export from being mistaken for the previous idempotent unexport.
func (manager *Manager) DisableTarget(ctx context.Context, owner, volumeID string) error {
	requestID := "admin-ui-disable-" + uuid.NewString()
	intent, err := json.Marshal(UnexportIntent{Force: true, RequestID: requestID})
	if err != nil {
		return err
	}
	_, err = manager.Run(ctx, Request{
		Owner: owner, VolumeID: volumeID, Action: ActionUnexport,
		Intent: intent, RequestID: requestID,
	})
	return err
}

func (manager *Manager) Run(ctx context.Context, request Request) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if request.Owner == "" || request.VolumeID == "" {
		return Result{}, guardError("INVALID_ARGUMENT", fmt.Errorf("%w: owner and volume_id are required", ErrInvalidRequest))
	}
	if request.Action == "" {
		return Result{}, guardError("INVALID_ARGUMENT", fmt.Errorf("%w: action is required", ErrInvalidRequest))
	}
	if manager.store == nil || manager.lvm == nil {
		return Result{}, fmt.Errorf("%w: store and lvm backend are required", ErrInvalidRequest)
	}
	requestHash, err := hashRequest(request)
	if err != nil {
		return Result{}, guardError("INVALID_ARGUMENT", err)
	}

	volumeKey := request.Owner + "\x00" + request.VolumeID
	if result, found, err := manager.lookupOperation(ctx, request, requestHash); found || err != nil {
		if err != nil {
			return Result{}, err
		}
		return result, nil
	}

	unlock := manager.volumeLocks.Lock(volumeKey)
	defer unlock()

	if result, found, err := manager.lookupOperation(ctx, request, requestHash); found || err != nil {
		if err != nil {
			return Result{}, err
		}
		return result, nil
	}

	switch request.Action {
	case ActionCreate, ActionExport:
		return manager.runEnsure(ctx, request, requestHash, nil)
	case ActionUnexport:
		return manager.runUnexport(ctx, request, requestHash, nil)
	case ActionResize:
		return manager.runResize(ctx, request, requestHash, nil)
	case ActionDelete:
		return manager.runDelete(ctx, request, requestHash, nil)
	default:
		return Result{}, guardError("UNIMPLEMENTED", fmt.Errorf("%w: %s", ErrUnsupportedAction, request.Action))
	}
}

// Retry re-arms the latest failed or needs-attention operation for a volume and
// executes its recorded intent under the volume lock.
func (manager *Manager) Retry(ctx context.Context, owner, volumeID string) (Result, error) {
	if owner == "" || volumeID == "" {
		return Result{}, guardError("INVALID_ARGUMENT", fmt.Errorf("%w: owner and volume_id are required", ErrInvalidRequest))
	}
	unlock := manager.volumeLocks.Lock(owner + "\x00" + volumeID)
	defer unlock()
	var operation state.Operation
	found := false
	if err := manager.store.Transact(ctx, func(tx state.Tx) error {
		operations, err := tx.ListOperations(owner, volumeID)
		if err != nil {
			return err
		}
		for _, candidate := range operations {
			if candidate.State == "failed" || candidate.State == "needs_attention" {
				operation = candidate
				found = true
				break
			}
		}
		return nil
	}); err != nil {
		return Result{}, err
	}
	if !found {
		return Result{}, operationError("RETRY_UNAVAILABLE", false, "", ErrOperationFailed)
	}
	request := Request{
		Owner: owner, VolumeID: volumeID, Action: Action(operation.Action),
		Intent: []byte(operation.IntentJSON), RequestID: operation.RequestID,
	}
	switch request.Action {
	case ActionCreate, ActionExport:
		return manager.runEnsure(ctx, request, operation.RequestHash, &operation)
	case ActionUnexport:
		return manager.runUnexport(ctx, request, operation.RequestHash, &operation)
	case ActionResize:
		return manager.runResize(ctx, request, operation.RequestHash, &operation)
	case ActionDelete:
		return manager.runDelete(ctx, request, operation.RequestHash, &operation)
	default:
		return Result{}, operationError("UNIMPLEMENTED", false, operation.ID, ErrUnsupportedAction)
	}
}

// Resume is intentionally conservative: it does not replay side effects
// blindly. The agent startup path marks stale running rows for administrator
// review; the normal Run path is idempotent for new requests.
func (manager *Manager) Resume(ctx context.Context) error {
	if manager.store == nil {
		return fmt.Errorf("%w: store is required", ErrInvalidRequest)
	}
	return manager.store.Transact(ctx, func(tx state.Tx) error {
		_, err := tx.ListRunningOperations()
		return err
	})
}

func (manager *Manager) lookupOperation(ctx context.Context, request Request, requestHash string) (Result, bool, error) {
	var operation state.Operation
	found := false
	err := manager.store.Transact(ctx, func(tx state.Tx) error {
		value, err := tx.GetOperation(request.Owner, request.VolumeID, string(request.Action), requestHash)
		if errors.Is(err, state.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if value.State == "succeeded" {
			tombstone, tombstoneErr := tx.GetTombstone(request.Owner, request.VolumeID)
			if tombstoneErr == nil && tombstone.VolumeID != "" {
				value.State = "superseded"
				value.RequestHash = value.RequestHash + "#superseded-" + value.ID
				value.UpdatedAt = manager.now()
				if err := tx.PutOperation(value); err != nil {
					return err
				}
				return nil
			}
			if tombstoneErr != nil && !errors.Is(tombstoneErr, state.ErrNotFound) {
				return tombstoneErr
			}
			// An export operation stops being replayable once the export has
			// been unexported. This is the boundary for a new attachment
			// generation and its new credential.
			if request.Action == ActionExport {
				export, exportErr := tx.GetExport(request.Owner, request.VolumeID)
				if errors.Is(exportErr, state.ErrNotFound) || (exportErr == nil && export.State == "unexported") {
					value.State = "superseded"
					value.RequestHash = value.RequestHash + "#unexported-" + value.ID
					value.UpdatedAt = manager.now()
					if err := tx.PutOperation(value); err != nil {
						return err
					}
					return nil
				}
				if exportErr != nil && !errors.Is(exportErr, state.ErrNotFound) {
					return exportErr
				}
			}
		}
		operation = value
		found = true
		return nil
	})
	if err != nil || !found {
		return Result{}, found, err
	}
	switch operation.State {
	case "succeeded":
		result, err := manager.resultForOperation(ctx, request, operation)
		return result, true, err
	case "failed":
		return Result{}, true, operationError(operation.LastErrorCode, operation.LastErrorRetryable, operation.ID, ErrOperationFailed)
	case "needs_attention":
		return Result{}, true, operationError(operation.LastErrorCode, false, operation.ID, ErrOperationNeedsUser)
	case "running":
		return Result{}, true, operationError("OPERATION_IN_PROGRESS", true, operation.ID, ErrOperationInProgress)
	default:
		return Result{}, true, operationError("OPERATION_SUPERSEDED", false, operation.ID, ErrOperationFailed)
	}
}

func (manager *Manager) resultForOperation(ctx context.Context, request Request, operation state.Operation) (Result, error) {
	result := Result{OperationID: operation.ID}
	err := manager.store.Transact(ctx, func(tx state.Tx) error {
		volume, err := tx.GetVolume(request.Owner, request.VolumeID)
		if err == nil {
			result.Volume = volume
		} else if !errors.Is(err, state.ErrNotFound) {
			return err
		}
		export, err := tx.GetExport(request.Owner, request.VolumeID)
		if err == nil {
			result.Export = export
		} else if !errors.Is(err, state.ErrNotFound) {
			return err
		}
		return nil
	})
	return result, err
}

func hashRequest(request Request) (string, error) {
	if len(request.Intent) == 0 {
		return "", fmt.Errorf("%w: intent is required", ErrInvalidRequest)
	}
	if !json.Valid(request.Intent) {
		return "", fmt.Errorf("%w: intent is not valid JSON", ErrInvalidRequest)
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(request.Action))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(request.Intent)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func (manager *Manager) now() time.Time {
	return manager.opts.Clock.Now().UTC()
}

func (manager *Manager) operationFor(request Request, resourceUUID, requestHash, intent string, now time.Time, existing *state.Operation) state.Operation {
	if existing != nil {
		operation := *existing
		operation.State = "running"
		operation.Attempt++
		if operation.Attempt < 1 {
			operation.Attempt = 1
		}
		operation.DeadlineAt = now.Add(manager.opts.TotalTimeout)
		operation.UpdatedAt = now
		operation.RequestHash = requestHash
		operation.IntentJSON = intent
		return operation
	}
	return manager.newOperation(request, resourceUUID, intent, now)
}

func (manager *Manager) newOperation(request Request, resourceUUID, intent string, now time.Time) state.Operation {
	return state.Operation{
		ID:           uuid.NewString(),
		Owner:        request.Owner,
		VolumeID:     request.VolumeID,
		ResourceUUID: resourceUUID,
		Action:       string(request.Action),
		RequestID:    request.RequestID,
		RequestHash:  mustHash(request),
		IntentJSON:   intent,
		State:        "running",
		Attempt:      1,
		MaxAttempts:  manager.opts.MaxAttempts,
		DeadlineAt:   now.Add(manager.opts.TotalTimeout),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

func mustHash(request Request) string {
	hash, err := hashRequest(request)
	if err != nil {
		return ""
	}
	return hash
}

func (manager *Manager) targetFor(protocol string) (target.Exporter, error) {
	switch strings.ToLower(protocol) {
	case "iscsi":
		if manager.iscsi == nil {
			return nil, fmt.Errorf("%w: iSCSI exporter is not configured", ErrInvalidRequest)
		}
		return manager.iscsi, nil
	case "nvme-tcp", "nvmetcp", "nvme":
		if manager.nvmet == nil {
			return nil, fmt.Errorf("%w: NVMe-oF exporter is not configured", ErrInvalidRequest)
		}
		return manager.nvmet, nil
	default:
		return nil, fmt.Errorf("%w: unsupported protocol %q", ErrInvalidRequest, protocol)
	}
}

func (manager *Manager) reveal(ctx context.Context, stored credential.Credential) (target.Credential, error) {
	if manager.creds == nil {
		return target.Credential{}, fmt.Errorf("%w: credential manager is not configured", ErrInvalidRequest)
	}
	return manager.creds.Reveal(ctx, stored)
}

func classifyFailure(err error) (code string, retryable bool, needsAttention bool) {
	switch {
	case errors.Is(err, target.ErrSessionActive), errors.Is(err, ErrSessionActive):
		return "SESSION_ACTIVE", false, false
	case errors.Is(err, target.ErrSessionUnknown), errors.Is(err, ErrSessionUnknown):
		return "SESSION_UNKNOWN", false, true
	case errors.Is(err, lvm.ErrResourceExhausted), errors.Is(err, ErrResourceExhausted):
		return "RESOURCE_EXHAUSTED", false, false
	case errors.Is(err, target.ErrNotFound), errors.Is(err, state.ErrNotFound), errors.Is(err, lvm.ErrNotFound):
		return "NOT_FOUND", false, false
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "DEADLINE_EXCEEDED", true, false
	case errors.Is(err, target.ErrConflict), errors.Is(err, state.ErrConflict), errors.Is(err, lvm.ErrInvalid):
		return "CONFLICT", false, false
	default:
		return "UNAVAILABLE", true, false
	}
}

func (manager *Manager) failOperation(ctx context.Context, operation state.Operation, volume *state.Volume, cause error, requestID string) (Result, error) {
	log.Printf("kouja operation %s (%s/%s) failed: %v", operation.ID, operation.VolumeID, operation.Action, cause)
	manager.opts.Metrics.Inc("kouja_operation_failed_total")
	code, retryable, needsAttention := classifyFailure(cause)
	now := manager.now()
	operation.State = "failed"
	if needsAttention {
		operation.State = "needs_attention"
	}
	operation.LastErrorCode = code
	operation.LastErrorRetryable = retryable
	operation.UpdatedAt = now
	err := manager.store.Transact(ctx, func(tx state.Tx) error {
		if volume != nil {
			volume.State = "failed"
			volume.ErrorCode = code
			volume.LastOperationID = operation.ID
			volume.UpdatedAt = now
			if err := tx.PutVolume(*volume); err != nil {
				return err
			}
		}
		if err := tx.PutOperation(operation); err != nil {
			return err
		}
		return tx.PutAudit(state.Audit{
			Timestamp:   now,
			Caller:      operation.Owner,
			OperationID: operation.ID,
			RequestID:   requestID,
			VolumeID:    operation.VolumeID,
			Action:      operation.Action,
			Result:      "failed",
			ErrorCode:   code,
		})
	})
	if err != nil {
		return Result{}, err
	}
	return Result{OperationID: operation.ID}, operationError(code, retryable, operation.ID, cause)
}

func (manager *Manager) succeedOperation(ctx context.Context, operation state.Operation, volume state.Volume, export *state.Export, storedCredential *state.Credential, requestID string) (Result, error) {
	manager.opts.Metrics.Inc("kouja_operation_succeeded_total")
	now := manager.now()
	operation.State = "succeeded"
	operation.UpdatedAt = now
	volume.LastOperationID = operation.ID
	volume.ErrorCode = ""
	volume.UpdatedAt = now
	err := manager.store.Transact(ctx, func(tx state.Tx) error {
		if err := tx.PutVolume(volume); err != nil {
			return err
		}
		if export != nil {
			if err := tx.PutExport(*export); err != nil {
				return err
			}
		}
		if storedCredential != nil {
			if err := tx.PutCredential(*storedCredential); err != nil {
				return err
			}
		}
		if err := tx.PutOperation(operation); err != nil {
			return err
		}
		return tx.PutAudit(state.Audit{
			Timestamp:   now,
			Caller:      operation.Owner,
			OperationID: operation.ID,
			RequestID:   requestID,
			VolumeID:    operation.VolumeID,
			Action:      operation.Action,
			Protocol:    exportProtocol(export),
			Result:      "succeeded",
		})
	})
	if err != nil {
		return Result{}, err
	}
	result := Result{Volume: volume, OperationID: operation.ID}
	if export != nil {
		result.Export = *export
	}
	return result, nil
}

func exportProtocol(export *state.Export) string {
	if export == nil {
		return ""
	}
	return export.Protocol
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now().UTC() }
