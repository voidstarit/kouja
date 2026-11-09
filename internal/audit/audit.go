// Package audit provides the single audit writer used by the certificate,
// VG, and class mutation paths.
package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/voidstarit/kouja/internal/observability"
	"github.com/voidstarit/kouja/internal/state"
)

// ErrWrite marks an audit-insert failure so callers can map it to
// AUDIT_WRITE_FAILED without inspecting database errors.
var ErrWrite = errors.New("audit: write failed")

// Event is the explicit audit payload for a mutation.
type Event struct {
	Caller       string
	OperationID  string
	RequestID    string
	ResourceType string
	ResourceID   string
	Role         string
	Name         string
	Action       string
	Result       string
	ErrorCode    string
	Detail       string
	RemoteAddr   string
}

// Recorder writes audit events through the state store.
type Recorder struct {
	store   *state.Store
	logger  *slog.Logger
	metrics *observability.Metrics
}

// NewRecorder constructs the shared audit recorder.
func NewRecorder(store *state.Store, logger *slog.Logger, metrics *observability.Metrics) *Recorder {
	return &Recorder{store: store, logger: logger, metrics: metrics}
}

// Record writes one event in its own transaction.
func (r *Recorder) Record(ctx context.Context, event Event) error {
	if r == nil || r.store == nil {
		return fmt.Errorf("%w: audit store is not configured", ErrWrite)
	}
	err := r.store.Transact(ctx, func(tx state.Tx) error {
		return r.RecordTx(tx, event)
	})
	if err != nil {
		r.failure(err)
	}
	return err
}

// RecordTx writes one event inside an existing transaction.
func (r *Recorder) RecordTx(tx state.Tx, event Event) error {
	if tx == nil {
		return fmt.Errorf("%w: nil transaction", ErrWrite)
	}
	err := tx.PutAuditEvent(state.AuditEvent{
		CallerOwner:  event.Caller,
		OperationID:  event.OperationID,
		RequestID:    event.RequestID,
		ResourceType: event.ResourceType,
		ResourceID:   event.ResourceID,
		Role:         event.Role,
		Name:         event.Name,
		Action:       event.Action,
		Result:       event.Result,
		ErrorCode:    event.ErrorCode,
		Detail:       event.Detail,
		RemoteAddr:   event.RemoteAddr,
	})
	if err != nil {
		return fmt.Errorf("%w: %v", ErrWrite, err)
	}
	return nil
}

func (r *Recorder) failure(err error) {
	if r.metrics != nil {
		r.metrics.Inc("kouja_audit_failure_total")
	}
	if r.logger != nil {
		r.logger.Error("audit write failed", "error", err)
	}
}
