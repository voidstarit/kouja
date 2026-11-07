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
	body, err := readLimitedBody(response, request, 1<<20)
	if err != nil {
		writeMappedError(response, err)
		return
	}
	var payload struct {
		Owner    string `json:"owner"`
		VolumeID string `json:"volume_id"`
	}
	if err := decodeJSON(body, &payload); err != nil {
		writeMappedError(response, fmt.Errorf("%w: %v", errBadRequest, err))
		return
	}
	if payload.Owner == "" || payload.VolumeID == "" {
		writeMappedError(response, fmt.Errorf("%w: owner and volume_id are required", errBadRequest))
		return
	}
	var export state.Export
	err = server.options.Store.Transact(request.Context(), func(tx state.Tx) error {
		value, err := tx.GetExport(payload.Owner, payload.VolumeID)
		if err != nil {
			return err
		}
		export = value
		return nil
	})
	if errors.Is(err, state.ErrNotFound) {
		writeAPIError(response, http.StatusNotFound, "not_found", "target is not defined")
		return
	}
	if err != nil {
		writeAPIError(response, http.StatusInternalServerError, "internal_error", "could not read target definition")
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
	if err := server.options.DisableTarget.DisableTarget(request.Context(), payload.Owner, payload.VolumeID); err != nil {
		writeTargetDisableError(response, err)
		return
	}
	if auditErr := server.writeAudit(request.Context(), "target_disable", "", "", "unexported", "succeeded", "", request.RemoteAddr); auditErr != nil {
		server.options.Logger.Warn("admin UI audit write failed", "action", "target_disable", "error", auditErr)
	}
	response.WriteHeader(http.StatusNoContent)
}

func writeTargetDisableError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, state.ErrNotFound), errors.Is(err, target.ErrNotFound):
		writeAPIError(response, http.StatusNotFound, "not_found", "target is not defined")
	case errors.Is(err, operations.ErrVolumeInDrift):
		writeAPIError(response, http.StatusConflict, "volume_in_drift", "volume is in drift; resolve it before disabling the target")
	case errors.Is(err, operations.ErrSessionActive), errors.Is(err, target.ErrSessionActive):
		writeAPIError(response, http.StatusConflict, "session_active", "active session could not be dropped")
	case errors.Is(err, operations.ErrSessionUnknown), errors.Is(err, target.ErrSessionUnknown):
		writeAPIError(response, http.StatusConflict, "session_unknown", "session state is unknown")
	case errors.Is(err, operations.ErrOperationInProgress):
		writeAPIError(response, http.StatusConflict, "operation_in_progress", "another operation is in progress")
	case errors.Is(err, operations.ErrInvalidRequest), errors.Is(err, operations.ErrUnsupportedAction),
		errors.Is(err, target.ErrInvalid):
		writeAPIError(response, http.StatusUnprocessableEntity, "invalid_target", err.Error())
	case errors.Is(err, target.ErrConflict), errors.Is(err, state.ErrConflict):
		writeAPIError(response, http.StatusConflict, "conflict", "target changed while disabling")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeAPIError(response, http.StatusGatewayTimeout, "timeout", "target disable timed out")
	default:
		writeAPIError(response, http.StatusInternalServerError, "internal_error", "could not disable target")
	}
}
