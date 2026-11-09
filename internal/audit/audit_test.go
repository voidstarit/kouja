package audit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/voidstarit/kouja/internal/observability"
	"github.com/voidstarit/kouja/internal/state"
)

func TestRecorderWritesAndClassifiesFailures(t *testing.T) {
	store, err := state.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	recorder := NewRecorder(store, slog.New(slog.NewTextHandler(io.Discard, nil)), observability.New())
	if err := recorder.Record(t.Context(), Event{
		Caller: "admin", Action: "certificate_issue", Result: "succeeded",
		ResourceType: "certificate", ResourceID: "serial",
	}); err != nil {
		t.Fatal(err)
	}
	var deleted int64
	if err := store.Transact(t.Context(), func(tx state.Tx) error {
		var err error
		deleted, err = tx.DeleteAuditBefore(time.Now().UTC().Add(time.Hour))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	if err := store.Transact(t.Context(), func(tx state.Tx) error {
		return recorder.RecordTx(tx, Event{Caller: "admin", Action: "", Result: "succeeded"})
	}); !errors.Is(err, ErrWrite) {
		t.Fatalf("RecordTx error = %v, want ErrWrite", err)
	}
	nilRecorder := NewRecorder(nil, nil, nil)
	if err := nilRecorder.Record(context.Background(), Event{}); !errors.Is(err, ErrWrite) {
		t.Fatalf("nil recorder error = %v, want ErrWrite", err)
	}
}

func TestRecorderLogsAndCountsWriteFailures(t *testing.T) {
	store, err := state.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	metrics := observability.New()
	recorder := NewRecorder(store, slog.New(slog.NewTextHandler(io.Discard, nil)), metrics)
	_ = store.Close()
	if err := recorder.Record(t.Context(), Event{Caller: "admin", Action: "test", Result: "succeeded"}); err == nil {
		t.Fatal("Record against a closed store succeeded")
	}
	if metrics == nil {
		t.Fatal("metrics missing")
	}
	if rendered := metrics.Render(); !containsMetric(rendered, "kouja_audit_failure_total") {
		t.Fatalf("metrics = %q", rendered)
	}
}

func containsMetric(rendered, name string) bool {
	for _, line := range strings.Split(rendered, "\n") {
		if strings.HasPrefix(line, name) {
			return true
		}
	}
	return false
}
