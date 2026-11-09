package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/voidstarit/kouja/internal/audit"
	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/operations"
	"github.com/voidstarit/kouja/internal/pki"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func statusError(err error) error {
	if err == nil {
		return nil
	}
	code := codes.Internal
	reason := "INTERNAL"
	retryable := false
	operationID := ""
	var operationError *operations.OperationError
	if errors.As(err, &operationError) {
		reason = operationError.Code
		retryable = operationError.Retryable
		operationID = operationError.OperationID
		if reason == "" {
			reason = "OPERATION_FAILED"
		}
		switch reason {
		case "INVALID_ARGUMENT":
			code = codes.InvalidArgument
		case "NOT_FOUND":
			code = codes.NotFound
		case "VOLUME_IN_DRIFT", "VOLUME_ALREADY_EXPORTED", "SESSION_ACTIVE", "SESSION_UNKNOWN",
			"SHRINK_NOT_SUPPORTED", "OPERATION_NEEDS_ATTENTION", "VOLUME_REQUEST_MISMATCH", "OPERATION_IN_PROGRESS":
			code = codes.FailedPrecondition
		case "RESOURCE_EXHAUSTED":
			code = codes.ResourceExhausted
		case "UNAVAILABLE":
			code = codes.Unavailable
		case "DEADLINE_EXCEEDED":
			code = codes.DeadlineExceeded
		case "UNIMPLEMENTED":
			code = codes.Unimplemented
		case "CONFLICT":
			code = codes.Aborted
		}
	} else {
		switch {
		case errors.Is(err, ErrForbidden):
			code, reason = codes.PermissionDenied, "FORBIDDEN"
		case errors.Is(err, state.ErrNotFound), errors.Is(err, lvm.ErrNotFound), errors.Is(err, target.ErrNotFound):
			code, reason = codes.NotFound, "NOT_FOUND"
		case errors.Is(err, state.ErrConflict), errors.Is(err, lvm.ErrInvalid):
			code, reason = codes.FailedPrecondition, "CONFLICT"
		case errors.Is(err, lvm.ErrResourceExhausted):
			code, reason = codes.ResourceExhausted, "RESOURCE_EXHAUSTED"
		case errors.Is(err, ErrAdmission):
			code, reason = codes.ResourceExhausted, "ADMISSION_REJECTED"
		case errors.Is(err, context.Canceled):
			code, reason = codes.Canceled, "CANCELED"
		case errors.Is(err, context.DeadlineExceeded):
			code, reason = codes.DeadlineExceeded, "DEADLINE_EXCEEDED"
		case errors.Is(err, operations.ErrInvalidRequest), errors.Is(err, operations.ErrUnsupportedAction):
			code, reason = codes.InvalidArgument, "INVALID_ARGUMENT"
		case errors.Is(err, pki.ErrDisabled):
			code, reason = codes.FailedPrecondition, "CERTIFICATE_ISSUANCE_DISABLED"
		case errors.Is(err, pki.ErrCAUnavailable):
			code, reason = codes.FailedPrecondition, "CERTIFICATE_CA_UNAVAILABLE"
		case errors.Is(err, pki.ErrInvalidRole):
			code, reason = codes.InvalidArgument, "CERTIFICATE_ROLE_INVALID"
		case errors.Is(err, pki.ErrInvalidName):
			code, reason = codes.InvalidArgument, "CERTIFICATE_NAME_INVALID"
		case errors.Is(err, pki.ErrInvalidTTL):
			code, reason = codes.InvalidArgument, "CERTIFICATE_TTL_INVALID"
		case errors.Is(err, pki.ErrInvalidSerial):
			code, reason = codes.InvalidArgument, "CERTIFICATE_SERIAL_INVALID"
		case errors.Is(err, pki.ErrExternalInvalid):
			code, reason = codes.InvalidArgument, "CERTIFICATE_EXTERNAL_INVALID"
		case errors.Is(err, pki.ErrQuotaExceeded):
			code, reason = codes.ResourceExhausted, "CERTIFICATE_QUOTA_EXCEEDED"
		case errors.Is(err, pki.ErrJournalUnhealthy):
			code, reason = codes.FailedPrecondition, "JOURNAL_UNAVAILABLE"
		case errors.Is(err, pki.ErrFile), errors.Is(err, pki.ErrUnsafeDirectory):
			code, reason = codes.Internal, "CERTIFICATE_FILE_ERROR"
		case errors.Is(err, audit.ErrWrite):
			code, reason = codes.Internal, "AUDIT_WRITE_FAILED"
		case errors.Is(err, target.ErrSessionActive):
			code, reason = codes.FailedPrecondition, "SESSION_ACTIVE"
		case errors.Is(err, target.ErrSessionUnknown):
			code, reason = codes.FailedPrecondition, "SESSION_UNKNOWN"
		}
	}
	message := err.Error()
	if code == codes.Internal {
		message = "internal error"
	}
	result, detailErr := status.New(code, message).WithDetails(&errdetails.ErrorInfo{
		Domain: "kouja.v1",
		Reason: reason,
		Metadata: map[string]string{
			"retryable":    fmt.Sprintf("%t", retryable),
			"operation_id": operationID,
		},
	})
	if detailErr != nil {
		return status.Error(code, message)
	}
	return result.Err()
}
