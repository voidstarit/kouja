package state

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotSourceReadsLiveDatabase(t *testing.T) {
	ctx := context.Background()
	sourcePath := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if err := store.Transact(ctx, func(tx Tx) error {
		return tx.PutSetting("snapshot-test", "value")
	}); err != nil {
		t.Fatal(err)
	}
	source, err := OpenSnapshotSource(ctx, sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	destination := filepath.Join(t.TempDir(), "snapshot.db")
	if err := source.Snapshot(ctx, destination); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode = %v, %v", info, err)
	}
	copyStore, err := Open(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = copyStore.Close() }()
	if err := copyStore.Transact(ctx, func(tx Tx) error {
		value, err := tx.GetSetting("snapshot-test")
		if err != nil {
			return err
		}
		if value != "value" {
			t.Fatalf("snapshot setting = %q", value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := source.Snapshot(ctx, destination); err == nil {
		t.Fatal("existing snapshot destination did not fail")
	}
}

func TestOpenSnapshotSourceErrors(t *testing.T) {
	if _, err := OpenSnapshotSource(context.Background(), ""); err == nil {
		t.Fatal("empty snapshot path did not fail")
	}
	if _, err := OpenSnapshotSource(context.Background(), filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Fatal("missing snapshot source did not fail")
	}
	if err := (&SnapshotSource{}).Snapshot(context.Background(), "dest"); err == nil {
		t.Fatal("closed snapshot source did not fail")
	}
	if err := (*SnapshotSource)(nil).Close(); err != nil {
		t.Fatal(err)
	}
}
