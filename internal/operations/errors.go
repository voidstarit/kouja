package operations

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidRequest      = errors.New("operations: invalid request")
	ErrUnsupportedAction   = errors.New("operations: unsupported action")
	ErrOperationInProgress = errors.New("operations: operation is already running")
	ErrOperationFailed     = errors.New("operations: operation failed")
	ErrOperationNeedsUser  = errors.New("operations: operation needs administrator attention")
	ErrVolumeInDrift       = errors.New("operations: volume is in drift")
	ErrVolumeAlreadyExists = errors.New("operations: volume already exists with different parameters")
	ErrVolumeAlreadyExport = errors.New("operations: volume is already exported to another initiator")
	ErrSessionActive       = errors.New("operations: session is active")
	ErrSessionUnknown      = errors.New("operations: session state is unknown")
	ErrResourceExhausted   = errors.New("operations: resource exhausted")
)

// OperationError carries the stable code and operation ID returned through the
// API. Guard errors have an empty OperationID because they create no journal row.
type OperationError struct {
	Code        string
	Retryable   bool
	OperationID string
	Err         error
}

func (err *OperationError) Error() string {
	if err.OperationID == "" {
		return fmt.Sprintf("%s: %v", err.Code, err.Err)
	}
	return fmt.Sprintf("%s (operation %s): %v", err.Code, err.OperationID, err.Err)
}

func (err *OperationError) Unwrap() error {
	return err.Err
}

func guardError(code string, err error) error {
	return &OperationError{Code: code, Retryable: false, Err: err}
}

func operationError(code string, retryable bool, operationID string, err error) error {
	return &OperationError{Code: code, Retryable: retryable, OperationID: operationID, Err: err}
}
