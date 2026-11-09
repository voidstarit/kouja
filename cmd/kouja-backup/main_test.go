package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireLockExcludesConcurrentWorkers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backup.lock")
	first, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireLock(path); err == nil {
		t.Fatal("second lock acquisition succeeded")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := acquireLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestJournalFileSnapshotter(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "revoked.log")
	if err := os.WriteFile(source, []byte(`{"serial":"abc"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "snapshot.log")
	if err := (journalFileSnapshotter{path: source}).SnapshotJournal(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination) //nolint:gosec // test output
	if err != nil || len(data) == 0 {
		t.Fatalf("snapshot = %q, %v", data, err)
	}
}
