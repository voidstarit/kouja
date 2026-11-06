package hostid

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/voidstarit/kouja/internal/state"
)

func openState(t testing.TB) *state.Store {
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

func addVolume(t testing.TB, store *state.Store) {
	t.Helper()
	now := time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)
	err := store.Transact(context.Background(), func(tx state.Tx) error {
		if err := tx.PutVG(state.VG{Name: "vg1", UUID: "vg-uuid", State: "active", RegisteredAt: now, UpdatedAt: now}); err != nil {
			return err
		}
		return tx.PutVolume(state.Volume{
			Owner:              "owner",
			VolumeID:           "volume",
			ResourceUUID:       "resource",
			LVName:             "lv",
			VGName:             "vg1",
			RequestedSizeBytes: 1,
			ActualSizeBytes:    1,
			State:              "available",
			CreatedAt:          now,
			UpdatedAt:          now,
		})
	})
	if err != nil {
		t.Fatalf("add volume: %v", err)
	}
}

func bddTestEnsureCreatesPersistsAndRepairsFile(t testing.TB) {
	store := openState(t)
	path := filepath.Join(t.TempDir(), "host-id")
	manager := New(path, store)
	id, err := manager.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if _, err := uuid.Parse(id); err != nil {
		t.Fatalf("host id is not a UUID: %q", id)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat host id: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("host id mode = %o, want 600", info.Mode().Perm())
	}
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		setting, err := tx.GetSetting(HostUUIDSetting)
		if err != nil {
			return err
		}
		if setting != id {
			t.Fatalf("setting = %q, want %q", setting, id)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	recovered, err := manager.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure after file removal: %v", err)
	}
	if recovered != id {
		t.Fatalf("recovered id = %q, want %q", recovered, id)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("host id file was not recreated: %v", err)
	}
}

func bddTestMismatchAndMissingFileWithVolumesFailClosed(t testing.TB) {
	store := openState(t)
	addVolume(t, store)
	path := filepath.Join(t.TempDir(), "host-id")
	if err := os.WriteFile(path, []byte(uuid.NewString()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path, store).Ensure(context.Background()); !errors.Is(err, ErrMismatch) {
		t.Fatalf("mismatched identity = %v, want ErrMismatch", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path, store).Ensure(context.Background()); !errors.Is(err, ErrMismatch) {
		t.Fatalf("missing identity with volumes = %v, want ErrMismatch", err)
	}
}

func bddTestMismatchWithoutVolumesRepairsSetting(t testing.TB) {
	store := openState(t)
	path := filepath.Join(t.TempDir(), "host-id")
	first := uuid.NewString()
	if err := os.WriteFile(path, []byte(first+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := New(path, store).Ensure(context.Background())
	if err != nil || id != first {
		t.Fatalf("first Ensure = %q, %v", id, err)
	}
	second := uuid.NewString()
	if err := os.WriteFile(path, []byte(second+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err = New(path, store).Ensure(context.Background())
	if err != nil || id != second {
		t.Fatalf("repair Ensure = %q, %v", id, err)
	}
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		setting, err := tx.GetSetting(HostUUIDSetting)
		if err != nil {
			return err
		}
		if setting != second {
			t.Fatalf("setting = %q, want %q", setting, second)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func bddTestInvalidFileAndBootID(t testing.TB) {
	store := openState(t)
	path := filepath.Join(t.TempDir(), "host-id")
	if err := os.WriteFile(path, []byte("not-a-uuid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path, store).Ensure(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid file = %v, want ErrInvalid", err)
	}
	manager := New(filepath.Join(t.TempDir(), "host-id"), store)
	bootID, err := manager.RecordBootID(context.Background())
	if err != nil {
		t.Fatalf("RecordBootID: %v", err)
	}
	if _, err := uuid.Parse(bootID); err != nil {
		t.Fatalf("boot ID is not a UUID: %q", bootID)
	}
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		setting, err := tx.GetSetting(BootIDSetting)
		if err != nil {
			return err
		}
		if setting != bootID {
			t.Fatalf("boot setting = %q, want %q", setting, bootID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func bddTestHostIDErrorPaths(t testing.TB) {
	store := openState(t)
	if _, err := New("", store).Ensure(context.Background()); err == nil {
		t.Fatal("empty path did not fail")
	}
	if _, err := New(filepath.Join(t.TempDir(), "id"), nil).Ensure(context.Background()); err == nil {
		t.Fatal("nil store did not fail")
	}
	manager := New(filepath.Join(t.TempDir(), "nested", "host-id"), store)
	if err := manager.write(uuid.NewString()); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := manager.write("not-a-uuid"); err == nil {
		t.Fatal("invalid write did not fail")
	}
	if _, err := ReadBootID(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing boot id did not fail")
	}
	badBoot := filepath.Join(t.TempDir(), "boot")
	if err := os.WriteFile(badBoot, []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBootID(badBoot); err == nil {
		t.Fatal("invalid boot id did not fail")
	}
}

func bddTestHostIDReadBootAndStoreErrors(t testing.TB) {
	if value, err := ReadBootID(""); err != nil || value == "" {
		t.Fatalf("default boot ID = %q, %v", value, err)
	}
	store := openState(t)
	manager := New(filepath.Join(t.TempDir(), "host-id"), store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Ensure(context.Background()); err == nil {
		t.Fatal("Ensure on closed store did not fail")
	}
	if _, err := manager.RecordBootID(context.Background()); err == nil {
		t.Fatal("RecordBootID on closed store did not fail")
	}
}

func bddTestHostIDEnsureReadErrors(t testing.TB) {
	store := openState(t)
	directory := t.TempDir()
	if _, err := New(directory, store).Ensure(context.Background()); err == nil {
		t.Fatal("directory host-id path did not fail")
	}
	manager := New(filepath.Join(directory, "host-id"), store)
	if err := manager.write("not-a-uuid"); err == nil {
		t.Fatal("invalid write did not fail")
	}
}

func bddTestHostIDWriteAndReadFailureBranches(t testing.TB) {
	store := openState(t)
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(filepath.Join(blocker, "host-id"), store).Ensure(context.Background()); err == nil {
		t.Fatal("host id below a file did not fail")
	}
	directoryTarget := filepath.Join(t.TempDir(), "host-id")
	if err := os.Mkdir(directoryTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := New(directoryTarget, store).Ensure(context.Background()); err == nil {
		t.Fatal("directory host-id target did not fail")
	}
	if err := New(directoryTarget, store).write(uuid.NewString()); err == nil {
		t.Fatal("write over directory did not fail")
	}
	unreadable := filepath.Join(t.TempDir(), "host-id")
	if err := os.WriteFile(unreadable, []byte(uuid.NewString()), 0o000); err != nil {
		t.Fatal(err)
	}
	if _, err := New(unreadable, store).Ensure(context.Background()); err == nil {
		t.Fatal("unreadable host-id file did not fail")
	}
}

func bddTestHostIDInvalidStoredSetting(t testing.TB) {
	store := openState(t)
	manager := New(filepath.Join(t.TempDir(), "host-id"), store)
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		return tx.PutSetting(HostUUIDSetting, "not-a-uuid")
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Ensure(context.Background()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid stored UUID = %v", err)
	}
	nilStore := New(filepath.Join(t.TempDir(), "host-id"), nil)
	if _, err := nilStore.RecordBootID(context.Background()); err == nil {
		t.Fatal("nil store RecordBootID did not fail")
	}
}
