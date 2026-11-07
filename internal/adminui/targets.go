package adminui

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/voidstarit/kouja/internal/operations"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
)

func (server *Server) handleDisableTarget(response http.ResponseWriter, request *http.Request) {
	owner, volumeID, ok := server.readTargetAction(response, request)
	if !ok {
		return
	}
	export, ok := server.targetActionDefinition(response, request, owner, volumeID)
	if !ok {
		return
	}
	if export.Protocol != "iscsi" {
		writeAPIError(response, http.StatusUnprocessableEntity, "invalid_target", "only iSCSI targets can be disabled here")
		return
	}
	if server.options.DisableTarget == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "unavailable", "target disable is not configured")
		return
	}
	if err := server.options.DisableTarget.DisableTarget(request.Context(), owner, volumeID); err != nil {
		writeTargetActionError(response, err)
		return
	}
	if auditErr := server.writeAudit(request.Context(), "target_disable", "", "", "unexported", "succeeded", "", request.RemoteAddr); auditErr != nil {
		server.options.Logger.Warn("admin UI audit write failed", "action", "target_disable", "error", auditErr)
	}
	response.WriteHeader(http.StatusNoContent)
}

func (server *Server) handleEnableTarget(response http.ResponseWriter, request *http.Request) {
	owner, volumeID, ok := server.readTargetAction(response, request)
	if !ok {
		return
	}
	export, ok := server.targetActionDefinition(response, request, owner, volumeID)
	if !ok {
		return
	}
	if export.Protocol != "iscsi" {
		writeAPIError(response, http.StatusUnprocessableEntity, "invalid_target", "only iSCSI targets can be enabled here")
		return
	}
	if server.options.EnableTarget == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "unavailable", "target enable is not configured")
		return
	}
	if err := server.options.EnableTarget.EnableTarget(request.Context(), owner, volumeID); err != nil {
		writeTargetActionError(response, err)
		return
	}
	if auditErr := server.writeAudit(request.Context(), "target_enable", "", "", "exported", "succeeded", "", request.RemoteAddr); auditErr != nil {
		server.options.Logger.Warn("admin UI audit write failed", "action", "target_enable", "error", auditErr)
	}
	response.WriteHeader(http.StatusNoContent)
}

func (server *Server) readTargetAction(response http.ResponseWriter, request *http.Request) (string, string, bool) {
	body, err := readLimitedBody(response, request, 1<<20)
	if err != nil {
		writeMappedError(response, err)
		return "", "", false
	}
	var payload struct {
		Owner    string `json:"owner"`
		VolumeID string `json:"volume_id"`
	}
	if err := decodeJSON(body, &payload); err != nil {
		writeMappedError(response, fmt.Errorf("%w: %v", errBadRequest, err))
		return "", "", false
	}
	if payload.Owner == "" || payload.VolumeID == "" {
		writeMappedError(response, fmt.Errorf("%w: owner and volume_id are required", errBadRequest))
		return "", "", false
	}
	return payload.Owner, payload.VolumeID, true
}

func (server *Server) targetActionDefinition(response http.ResponseWriter, request *http.Request, owner, volumeID string) (state.Export, bool) {
	var export state.Export
	err := server.options.Store.Transact(request.Context(), func(tx state.Tx) error {
		value, err := tx.GetExport(owner, volumeID)
		if err != nil {
			return err
		}
		export = value
		return nil
	})
	if errors.Is(err, state.ErrNotFound) {
		writeAPIError(response, http.StatusNotFound, "not_found", "target is not defined")
		return state.Export{}, false
	}
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "internal_error", "could not read target definition")
		return state.Export{}, false
	}
	return export, true
}

func writeTargetActionError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, state.ErrNotFound), errors.Is(err, target.ErrNotFound):
		writeAPIError(response, http.StatusNotFound, "not_found", "target is not defined")
	case errors.Is(err, operations.ErrVolumeInDrift):
		writeAPIError(response, http.StatusConflict, "volume_in_drift", "volume is in drift; resolve it before changing the target")
	case errors.Is(err, operations.ErrSessionActive), errors.Is(err, target.ErrSessionActive):
		writeAPIError(response, http.StatusConflict, "session_active", "active session could not be dropped")
	case errors.Is(err, operations.ErrSessionUnknown), errors.Is(err, target.ErrSessionUnknown):
		writeAPIError(response, http.StatusConflict, "session_unknown", "session state is unknown")
	case errors.Is(err, operations.ErrOperationInProgress):
		writeAPIError(response, http.StatusConflict, "operation_in_progress", "another operation is in progress")
	case errors.Is(err, operations.ErrVolumeAlreadyExport):
		writeAPIError(response, http.StatusConflict, "already_exported", "target is already exported")
	case errors.Is(err, operations.ErrInvalidRequest), errors.Is(err, operations.ErrUnsupportedAction),
		errors.Is(err, target.ErrInvalid), errors.Is(err, state.ErrInvalid):
		writeAPIError(response, http.StatusUnprocessableEntity, "invalid_target", err.Error())
	case errors.Is(err, target.ErrConflict), errors.Is(err, state.ErrConflict):
		writeAPIError(response, http.StatusConflict, "conflict", "target changed while applying the action")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeAPIError(response, http.StatusGatewayTimeout, "timeout", "target action timed out")
	default:
		writeAPIError(response, http.StatusInternalServerError, "internal_error", "could not change target state")
	}
}
