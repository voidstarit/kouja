package audit

import (
	"context"
	"errors"
	"io"
	"log/slog"
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
	defer store.Close()
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
