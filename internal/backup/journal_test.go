package backup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
)

type testJournalSnapshotter struct{}

func (testJournalSnapshotter) SnapshotJournal(_ context.Context, destination string) error {
	return os.WriteFile(destination, []byte("{\"serial\":\"abc\"}\n"), 0o600)
}

func TestJournalSnapshotIsManifested(t *testing.T) {
	store := openBackupStore(t)
	dir := t.TempDir()
	hostIDPath := filepath.Join(dir, "host-id")
	if err := os.WriteFile(hostIDPath, []byte(uuid.NewString()), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	manager, err := New(store, Options{
		TargetDir: dir, HostIDFile: hostIDPath,
		JournalSnapshotter: testJournalSnapshotter{}, RevocationLogName: "revoked-serials.log",
		RetentionShort: 2, RetentionDaily: 2, RetentionWeekly: 2,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.JournalPath == "" {
		t.Fatal("journal path is empty")
	}
	if data, err := os.ReadFile(result.JournalPath); err != nil || string(data) != "{\"serial\":\"abc\"}\n" {
		t.Fatalf("journal = %q, %v", data, err)
	}
	data, err := os.ReadFile(result.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var document manifest
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if document.RevocationLog == "" {
		t.Fatalf("manifest = %#v", document)
	}
}
