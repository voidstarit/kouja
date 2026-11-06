package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/voidstarit/kouja/internal/state"
)

func openBackupStore(t testing.TB) *state.Store {
	t.Helper()
	store, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("state.Close: %v", err)
		}
	})
	return store
}

func bddTestRunCreatesCompleteBundle(t testing.TB) {
	store := openBackupStore(t)
	dir := t.TempDir()
	hostIDPath := filepath.Join(dir, "host-id")
	keyPath := filepath.Join(dir, "secret.key")
	hostUUID := uuid.NewString()
	if err := os.WriteFile(hostIDPath, []byte(hostUUID+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("key-material"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 123, time.UTC)
	manager, err := New(store, Options{
		TargetDir:       dir,
		HostIDFile:      hostIDPath,
		SecretKeyFile:   keyPath,
		RetentionShort:  2,
		RetentionDaily:  2,
		RetentionWeekly: 2,
		Now:             func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result, err := manager.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, path := range []string{result.DatabasePath, result.KeyPath, result.HostIDPath, result.ManifestPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 600", path, info.Mode().Perm())
		}
	}
	if data, err := os.ReadFile(result.KeyPath); err != nil || string(data) != "key-material" {
		t.Fatalf("backup key = %q, %v", data, err)
	}
	if data, err := os.ReadFile(result.HostIDPath); err != nil || string(data) != hostUUID+"\n" {
		t.Fatalf("backup host id = %q, %v", data, err)
	}
	var document manifest
	data, err := os.ReadFile(result.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if !document.HasKey || !document.HasHostID || document.HostUUID != hostUUID ||
		document.SchemaVersion != state.SchemaVersion() || document.CreatedAt.IsZero() {
		t.Fatalf("manifest = %#v", document)
	}
	backupStore, err := state.Open(context.Background(), result.DatabasePath)
	if err != nil {
		t.Fatalf("open backup database: %v", err)
	}
	_ = backupStore.Close()

	if _, err := manager.Run(context.Background()); !errors.Is(err, ErrConflict) {
		t.Fatalf("second Run = %v, want ErrConflict", err)
	}
}

func bddTestRunWithoutKeyAndMissingHostID(t testing.TB) {
	store := openBackupStore(t)
	dir := t.TempDir()
	hostIDPath := filepath.Join(dir, "host-id")
	if err := os.WriteFile(hostIDPath, []byte(uuid.NewString()), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(store, Options{
		TargetDir:      dir,
		HostIDFile:     hostIDPath,
		RetentionShort: 2, RetentionDaily: 2, RetentionWeekly: 2,
		Now: func() time.Time { return time.Date(2026, 11, 6, 13, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Run(context.Background())
	if err != nil {
		t.Fatalf("Run without key: %v", err)
	}
	if result.KeyPath != "" {
		t.Fatalf("KeyPath = %q, want empty", result.KeyPath)
	}
	var document manifest
	data, err := os.ReadFile(result.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if document.HasKey || document.SecretKey != "" {
		t.Fatalf("manifest unexpectedly has key: %#v", document)
	}

	if err := os.Remove(hostIDPath); err != nil {
		t.Fatal(err)
	}
	manager, err = New(store, Options{
		TargetDir:      dir,
		HostIDFile:     hostIDPath,
		RetentionShort: 2, RetentionDaily: 2, RetentionWeekly: 2,
		Now: func() time.Time { return time.Date(2026, 11, 6, 14, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Run(context.Background()); err == nil {
		t.Fatal("Run with missing host id succeeded")
	}
}

func bddTestRunRejectsInvalidTarget(t testing.TB) {
	store := openBackupStore(t)
	hostIDPath := filepath.Join(t.TempDir(), "host-id")
	if err := os.WriteFile(hostIDPath, []byte(uuid.NewString()), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(store, Options{
		TargetDir:      filepath.Join(t.TempDir(), "missing"),
		HostIDFile:     hostIDPath,
		RetentionShort: 1, RetentionDaily: 1, RetentionWeekly: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Run(context.Background()); !errors.Is(err, ErrTarget) {
		t.Fatalf("Run missing target = %v, want ErrTarget", err)
	}
}

func bddTestRotateRetainsShortDailyAndWeekly(t testing.TB) {
	store := openBackupStore(t)
	dir := t.TempDir()
	hostIDPath := filepath.Join(dir, "host-id")
	if err := os.WriteFile(hostIDPath, []byte(uuid.NewString()), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(store, Options{
		TargetDir:       dir,
		HostIDFile:      hostIDPath,
		RetentionShort:  2,
		RetentionDaily:  2,
		RetentionWeekly: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	makeBackup := func(at time.Time) string {
		base := "kouja-" + at.Format(timestampLayout)
		for _, suffix := range []string{".db", ".secret.key", ".host-id", ".restore.json"} {
			if err := os.WriteFile(filepath.Join(dir, base+suffix), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return base
	}
	old := makeBackup(now.Add(-10 * 24 * time.Hour))
	middle := makeBackup(now.Add(-3 * 24 * time.Hour))
	recent1 := makeBackup(now.Add(-2 * time.Hour))
	recent2 := makeBackup(now.Add(-1 * time.Hour))
	if err := manager.Rotate(now); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, old+".db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old backup still exists: %v", err)
	}
	for _, base := range []string{middle, recent1, recent2} {
		if _, err := os.Stat(filepath.Join(dir, base+".db")); err != nil {
			t.Fatalf("retained backup %s missing: %v", base, err)
		}
	}
	for _, base := range []string{old} {
		if _, err := os.Stat(filepath.Join(dir, base+".secret.key")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("old companion file still exists: %v", err)
		}
	}
}

func bddTestBackupValidationAndCopyErrors(t testing.TB) {
	store := openBackupStore(t)
	if _, err := New(nil, Options{}); err == nil {
		t.Fatal("nil store did not fail")
	}
	if _, err := New(store, Options{TargetDir: "relative", HostIDFile: "/tmp/host"}); err == nil {
		t.Fatal("relative target did not fail")
	}
	if _, err := New(store, Options{TargetDir: t.TempDir(), HostIDFile: "relative"}); err == nil {
		t.Fatal("relative host ID did not fail")
	}
	if _, err := New(store, Options{TargetDir: t.TempDir(), HostIDFile: "/tmp/host", SecretKeyFile: "relative"}); err == nil {
		t.Fatal("relative key did not fail")
	}
	if _, err := copyAtomic("", "dest", true); !errors.Is(err, ErrInvalid) {
		t.Fatalf("required empty source = %v", err)
	}
	if copied, err := copyAtomic("", "dest", false); err != nil || copied {
		t.Fatalf("optional empty source = %v, %v", copied, err)
	}
	if _, err := copyAtomic(filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "dest"), true); err == nil {
		t.Fatal("missing required source did not fail")
	}
	if copied, err := copyAtomic(filepath.Join(t.TempDir(), "missing"), filepath.Join(t.TempDir(), "dest"), false); err != nil || copied {
		t.Fatalf("missing optional source = %v, %v", copied, err)
	}
	if err := writeJSONAtomic(filepath.Join(t.TempDir(), "missing", "manifest"), map[string]string{"x": "y"}); err == nil {
		t.Fatal("write to missing directory did not fail")
	}
	if _, err := readHostID(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing host ID did not fail")
	}
	if err := syncDir(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing sync directory did not fail")
	}
}

func bddTestBackupDefaultsAndRunFailures(t testing.TB) {
	store := openBackupStore(t)
	dir := t.TempDir()
	hostID := filepath.Join(dir, "host-id")
	if err := os.WriteFile(hostID, []byte(uuid.NewString()), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(store, Options{TargetDir: dir, HostIDFile: hostID})
	if err != nil {
		t.Fatal(err)
	}
	if manager.opts.RetentionShort != 96 || manager.opts.RetentionDaily != 7 || manager.opts.RetentionWeekly != 4 {
		t.Fatalf("defaults = %#v", manager.opts)
	}
	missingKey := filepath.Join(dir, "missing.key")
	manager, err = New(store, Options{TargetDir: dir, HostIDFile: hostID, SecretKeyFile: missingKey, Now: func() time.Time {
		return time.Date(2026, 11, 6, 15, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := manager.Run(context.Background()); err != nil || result.KeyPath != "" {
		t.Fatalf("optional missing key = %#v, %v", result, err)
	}
	manager, err = New(store, Options{TargetDir: dir, HostIDFile: hostID, SecretKeyFile: func() string { d := filepath.Join(t.TempDir(), "directory-key"); _ = os.Mkdir(d, 0o700); return d }(), Now: func() time.Time {
		return time.Date(2026, 11, 6, 16, 0, 0, 0, time.UTC)
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Run(context.Background()); err == nil {
		t.Fatal("unreadable optional key did not fail")
	}
}
