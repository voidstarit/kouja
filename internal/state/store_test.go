package state

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

var testTime = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return store
}

func runTx(t *testing.T, store *Store, fn func(Tx) error) {
	t.Helper()
	if err := store.Transact(context.Background(), fn); err != nil {
		t.Fatalf("Transact: %v", err)
	}
}

func withTxErr(store *Store, fn func(Tx) error) error {
	return store.Transact(context.Background(), fn)
}

func addVG(t *testing.T, tx Tx, name string) {
	t.Helper()
	if err := tx.PutVG(VG{
		Name:         name,
		UUID:         "uuid-" + name,
		State:        "active",
		RegisteredAt: testTime,
		UpdatedAt:    testTime,
	}); err != nil {
		t.Fatalf("PutVG: %v", err)
	}
}

func addVolume(t *testing.T, tx Tx, owner, volumeID, vgName string) Volume {
	t.Helper()
	volume := Volume{
		Owner:              owner,
		VolumeID:           volumeID,
		ResourceUUID:       "resource-" + volumeID,
		LVName:             "lv-" + volumeID,
		VGName:             vgName,
		RequestedSizeBytes: 1 << 30,
		ActualSizeBytes:    1 << 30,
		State:              "available",
		DriftReasons:       []string{"test"},
		LastOperationID:    "op-" + volumeID,
		ErrorCode:          "E_TEST",
		LegacyIdentity:     map[string]string{"source": "test"},
		CreatedAt:          testTime,
		UpdatedAt:          testTime,
	}
	if err := tx.PutVolume(volume); err != nil {
		t.Fatalf("PutVolume: %v", err)
	}
	return volume
}

func TestOpenConfiguresDatabaseAndPersistsSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	var journalMode string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
	var foreignKeys int
	if err := store.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}
	var synchronous int
	if err := store.db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatalf("synchronous: %v", err)
	}
	if synchronous != 2 {
		t.Fatalf("synchronous = %d, want 2 (FULL)", synchronous)
	}

	runTx(t, store, func(tx Tx) error {
		return tx.PutSetting("host_uuid", "host-1")
	})
	runTx(t, store, func(tx Tx) error {
		value, err := tx.GetSetting("host_uuid")
		if err != nil {
			return err
		}
		if value != "host-1" {
			t.Fatalf("setting = %q", value)
		}
		return nil
	})
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	runTx(t, reopened, func(tx Tx) error {
		value, err := tx.GetSetting("host_uuid")
		if err != nil {
			return err
		}
		if value != "host-1" {
			t.Fatalf("persisted setting = %q", value)
		}
		return nil
	})

	var migrations int
	if err := reopened.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&migrations); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if migrations != schemaVersion {
		t.Fatalf("migrations = %d, want %d", migrations, schemaVersion)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat database: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("database mode = %o, want 600", mode)
	}
}

func TestVolumeExportObservationAndCascades(t *testing.T) {
	store := openTestStore(t)
	runTx(t, store, func(tx Tx) error {
		addVG(t, tx, "vg1")
		volume := addVolume(t, tx, "owner-a", "vol1", "vg1")
		got, err := tx.GetVolume("owner-a", "vol1")
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(got.DriftReasons, volume.DriftReasons) ||
			!reflect.DeepEqual(got.LegacyIdentity, volume.LegacyIdentity) ||
			!got.CreatedAt.Equal(volume.CreatedAt) || !got.UpdatedAt.Equal(volume.UpdatedAt) {
			t.Fatalf("volume round trip mismatch: %#v", got)
		}
		if _, err := tx.GetVolumeByResourceUUID("resource-vol1"); err != nil {
			return err
		}
		if err := tx.PutExport(Export{
			Owner:      "owner-a",
			VolumeID:   "vol1",
			Protocol:   "iscsi",
			Initiator:  "iqn.initiator",
			TargetName: "iqn.target",
			LUN:        3,
			Portal:     "10.0.0.1:3260",
			State:      "exported",
			CreatedAt:  testTime,
			UpdatedAt:  testTime,
		}); err != nil {
			return err
		}
		if err := tx.PutObservation(Observation{
			Owner:             "owner-a",
			VolumeID:          "vol1",
			LVPresent:         true,
			ObservedSizeBytes: 1 << 30,
			DevicePath:        "/dev/vg1/lv-vol1",
			ObservedAt:        testTime,
			Source:            "lvm",
		}); err != nil {
			return err
		}
		if err := tx.PutExportObservation(ExportObservation{
			Owner:             "owner-a",
			VolumeID:          "vol1",
			TargetPresent:     true,
			NamespacePresent:  true,
			ACLPresent:        true,
			ActiveSessions:    1,
			SessionState:      "active",
			ObservedInitiator: "iqn.initiator",
			ObservedAt:        testTime,
			Source:            "lio",
		}); err != nil {
			return err
		}
		if err := tx.PutGrant(Grant{
			Owner:        "owner-a",
			VolumeID:     "vol1",
			NodeID:       "node-1",
			NodeCallerID: "caller-1",
			ExpiresAt:    testTime.Add(time.Hour),
			CreatedAt:    testTime,
		}); err != nil {
			return err
		}
		if err := tx.PutCredential(Credential{
			ID:               "cred-1",
			Owner:            "owner-a",
			VolumeID:         "vol1",
			Protocol:         "iscsi",
			SecretCiphertext: []byte{1, 2, 3},
			Nonce:            []byte{4, 5, 6},
			KeyID:            "key-1",
			KeyFingerprint:   "fingerprint",
			State:            "active",
			CreatedAt:        testTime,
		}); err != nil {
			return err
		}

		gotExport, err := tx.GetExport("owner-a", "vol1")
		if err != nil {
			return err
		}
		if gotExport.LUN != 3 || gotExport.State != "exported" {
			t.Fatalf("export round trip: %#v", gotExport)
		}
		gotObservation, err := tx.GetObservation("owner-a", "vol1")
		if err != nil {
			return err
		}
		if !gotObservation.LVPresent || gotObservation.DevicePath != "/dev/vg1/lv-vol1" {
			t.Fatalf("observation round trip: %#v", gotObservation)
		}
		gotExportObservation, err := tx.GetExportObservation("owner-a", "vol1")
		if err != nil {
			return err
		}
		if !gotExportObservation.ACLPresent || gotExportObservation.ActiveSessions != 1 {
			t.Fatalf("export observation round trip: %#v", gotExportObservation)
		}
		gotGrant, err := tx.GetGrant("owner-a", "vol1", "node-1")
		if err != nil {
			return err
		}
		if gotGrant.NodeCallerID != "caller-1" {
			t.Fatalf("grant round trip: %#v", gotGrant)
		}
		gotCredential, err := tx.GetActiveCredential("owner-a", "vol1")
		if err != nil {
			return err
		}
		if !bytes.Equal(gotCredential.SecretCiphertext, []byte{1, 2, 3}) || gotCredential.RotatedAt != nil {
			t.Fatalf("credential round trip: %#v", gotCredential)
		}

		if err := tx.DeleteVG("vg1"); !errors.Is(err, ErrConflict) {
			t.Fatalf("DeleteVG with volume = %v, want ErrConflict", err)
		}
		if err := tx.DeleteVolume("owner-a", "vol1"); err != nil {
			return err
		}
		if _, err := tx.GetVolume("owner-a", "vol1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetVolume after delete = %v, want ErrNotFound", err)
		}
		if _, err := tx.GetExport("owner-a", "vol1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetExport after delete = %v, want ErrNotFound", err)
		}
		if _, err := tx.GetObservation("owner-a", "vol1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetObservation after delete = %v, want ErrNotFound", err)
		}
		if _, err := tx.GetGrant("owner-a", "vol1", "node-1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetGrant after delete = %v, want ErrNotFound", err)
		}
		if _, err := tx.GetActiveCredential("owner-a", "vol1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("GetActiveCredential after delete = %v, want ErrNotFound", err)
		}
		if err := tx.DeleteVG("vg1"); err != nil {
			return err
		}
		if err := tx.PutTombstone(Tombstone{
			Owner:           "owner-a",
			VolumeID:        "vol1",
			ResourceUUID:    "resource-vol1",
			LVName:          "lv-vol1",
			VGName:          "vg1",
			SizeBytes:       1 << 30,
			DeletedAt:       testTime,
			LastOperationID: "op-delete",
			Reason:          "requested",
		}); err != nil {
			return err
		}
		tombstone, err := tx.GetTombstone("owner-a", "vol1")
		if err != nil {
			return err
		}
		if tombstone.Reason != "requested" {
			t.Fatalf("tombstone round trip: %#v", tombstone)
		}
		return nil
	})
}

func TestPaginationAndListing(t *testing.T) {
	store := openTestStore(t)
	runTx(t, store, func(tx Tx) error {
		addVG(t, tx, "vg1")
		for _, id := range []string{"v1", "v2", "v3", "v4", "v5"} {
			addVolume(t, tx, "owner-a", id, "vg1")
		}
		return nil
	})
	runTx(t, store, func(tx Tx) error {
		page, err := tx.ListVolumes("owner-a", Page{Limit: 2})
		if err != nil {
			return err
		}
		if len(page) != 2 || page[0].VolumeID != "v1" || page[1].VolumeID != "v2" {
			t.Fatalf("first page: %#v", page)
		}
		page, err = tx.ListVolumes("owner-a", Page{Limit: 2, Cursor: "v2"})
		if err != nil {
			return err
		}
		if len(page) != 2 || page[0].VolumeID != "v3" || page[1].VolumeID != "v4" {
			t.Fatalf("second page: %#v", page)
		}
		page, err = tx.ListVolumes("owner-a", Page{Limit: 500, Cursor: "v4"})
		if err != nil {
			return err
		}
		if len(page) != 1 || page[0].VolumeID != "v5" {
			t.Fatalf("final page: %#v", page)
		}
		all, err := tx.ListVolumes("", Page{Limit: 100})
		if err != nil {
			return err
		}
		if len(all) != 5 {
			t.Fatalf("global list: %#v", all)
		}
		return nil
	})
}

func TestOperationsAndGrants(t *testing.T) {
	store := openTestStore(t)
	operation := Operation{
		ID:                 "op-1",
		Owner:              "owner-a",
		VolumeID:           "vol1",
		ResourceUUID:       "resource-vol1",
		Action:             "create",
		RequestID:          "request-1",
		RequestHash:        "hash-1",
		IntentJSON:         `{"volume_id":"vol1"}`,
		State:              "running",
		Attempt:            1,
		MaxAttempts:        3,
		DeadlineAt:         testTime.Add(time.Minute),
		LastErrorCode:      "E_TEST",
		LastErrorRetryable: true,
		CreatedAt:          testTime,
		UpdatedAt:          testTime,
	}
	runTx(t, store, func(tx Tx) error {
		addVG(t, tx, "vg1")
		addVolume(t, tx, "owner-a", "vol1", "vg1")
		if err := tx.PutOperation(operation); err != nil {
			return err
		}
		got, err := tx.GetOperation("owner-a", "vol1", "create", "hash-1")
		if err != nil {
			return err
		}
		if got.ID != "op-1" || !got.LastErrorRetryable || !got.DeadlineAt.Equal(operation.DeadlineAt) {
			t.Fatalf("operation round trip: %#v", got)
		}
		if _, err := tx.GetOperationByID("op-1"); err != nil {
			return err
		}
		running, err := tx.ListRunningOperations()
		if err != nil {
			return err
		}
		if len(running) != 1 {
			t.Fatalf("running operations: %#v", running)
		}
		operation.State = "succeeded"
		operation.Attempt = 2
		operation.UpdatedAt = testTime.Add(time.Second)
		if err := tx.PutOperation(operation); err != nil {
			return err
		}
		running, err = tx.ListRunningOperations()
		if err != nil {
			return err
		}
		if len(running) != 0 {
			t.Fatalf("running operations after success: %#v", running)
		}
		conflict := operation
		conflict.ID = "op-2"
		if err := tx.PutOperation(conflict); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate operation = %v, want ErrConflict", err)
		}
		operations, err := tx.ListOperations("owner-a", "vol1")
		if err != nil {
			return err
		}
		if len(operations) != 1 || operations[0].State != "succeeded" {
			t.Fatalf("list operations: %#v", operations)
		}
		return nil
	})

	runTx(t, store, func(tx Tx) error {
		grant := Grant{
			Owner:        "owner-a",
			VolumeID:     "vol1",
			NodeID:       "node-1",
			NodeCallerID: "caller-1",
			ExpiresAt:    testTime.Add(time.Hour),
			CreatedAt:    testTime,
		}
		if err := tx.PutGrant(grant); err != nil {
			return err
		}
		grants, err := tx.ListGrants("owner-a", "vol1")
		if err != nil {
			return err
		}
		if len(grants) != 1 {
			t.Fatalf("grants: %#v", grants)
		}
		if err := tx.DeleteGrant("owner-a", "vol1", "node-1"); err != nil {
			return err
		}
		if err := tx.DeleteGrant("owner-a", "vol1", "node-1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("delete missing grant = %v, want ErrNotFound", err)
		}
		return nil
	})
}

func TestValidationForeignKeysAndAudit(t *testing.T) {
	store := openTestStore(t)
	runTx(t, store, func(tx Tx) error {
		addVG(t, tx, "vg1")
		volume := addVolume(t, tx, "owner-a", "vol1", "vg1")
		if err := tx.PutVolume(Volume{
			Owner:              volume.Owner,
			VolumeID:           "vol2",
			ResourceUUID:       volume.ResourceUUID,
			LVName:             "lv-vol2",
			VGName:             "vg1",
			RequestedSizeBytes: 1,
			State:              "available",
			CreatedAt:          testTime,
			UpdatedAt:          testTime,
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate resource UUID = %v, want ErrConflict", err)
		}
		if err := tx.PutVolume(Volume{
			Owner:              "owner-a",
			VolumeID:           "missing-vg",
			ResourceUUID:       "resource-missing-vg",
			LVName:             "lv-missing-vg",
			VGName:             "missing",
			RequestedSizeBytes: 1,
			State:              "available",
			CreatedAt:          testTime,
			UpdatedAt:          testTime,
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("missing VG = %v, want ErrConflict", err)
		}
		if err := tx.PutExport(Export{
			Owner:      "owner-a",
			VolumeID:   "missing",
			Protocol:   "iscsi",
			Initiator:  "iqn",
			TargetName: "target",
			Portal:     "127.0.0.1:3260",
			State:      "exported",
			CreatedAt:  testTime,
			UpdatedAt:  testTime,
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("export with missing volume = %v, want ErrConflict", err)
		}
		if err := tx.PutCredential(Credential{
			ID:               "cred-1",
			Owner:            "owner-a",
			VolumeID:         "vol1",
			Protocol:         "iscsi",
			SecretCiphertext: []byte{1},
			Nonce:            []byte{2},
			KeyID:            "key-1",
			KeyFingerprint:   "fingerprint",
			State:            "active",
			CreatedAt:        testTime,
		}); err != nil {
			return err
		}
		if err := tx.PutCredential(Credential{
			ID:               "cred-2",
			Owner:            "owner-a",
			VolumeID:         "vol1",
			Protocol:         "iscsi",
			SecretCiphertext: []byte{3},
			Nonce:            []byte{4},
			KeyID:            "key-1",
			KeyFingerprint:   "fingerprint",
			State:            "active",
			CreatedAt:        testTime,
		}); !errors.Is(err, ErrConflict) {
			t.Fatalf("second active credential = %v, want ErrConflict", err)
		}
		if _, err := tx.GetVolume("owner-a", "missing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing volume = %v, want ErrNotFound", err)
		}
		return nil
	})

	runTx(t, store, func(tx Tx) error {
		return tx.PutAudit(Audit{
			Caller:      "owner-a",
			OperationID: "op-1",
			VolumeID:    "vol1",
			Action:      "create",
			Result:      "succeeded",
		})
	})
	var count int
	if err := store.db.QueryRow("SELECT COUNT(*) FROM audit").Scan(&count); err != nil {
		t.Fatalf("audit count: %v", err)
	}
	if count != 1 {
		t.Fatalf("audit count = %d, want 1", count)
	}
}

func TestSchemaNewerRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := store.db.Exec("INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)",
		schemaVersion+1, timeText(testTime)); err != nil {
		t.Fatalf("insert future schema: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := Open(context.Background(), path); !errors.Is(err, ErrSchemaNewer) {
		t.Fatalf("Open newer schema = %v, want ErrSchemaNewer", err)
	}
}

func TestBackupCreatesRestorableCopy(t *testing.T) {
	store := openTestStore(t)
	runTx(t, store, func(tx Tx) error {
		return tx.PutSetting("host_uuid", "host-1")
	})
	destination := filepath.Join(t.TempDir(), "backup.db")
	if err := store.Backup(context.Background(), destination); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %o, want 600", info.Mode().Perm())
	}
	copyStore, err := Open(context.Background(), destination)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer func() { _ = copyStore.Close() }()
	runTx(t, copyStore, func(tx Tx) error {
		value, err := tx.GetSetting("host_uuid")
		if err != nil {
			return err
		}
		if value != "host-1" {
			t.Fatalf("backup setting = %q", value)
		}
		return nil
	})
	if err := store.Backup(context.Background(), destination); !errors.Is(err, ErrConflict) {
		t.Fatalf("second backup = %v, want ErrConflict", err)
	}
}

func TestTransactionRollback(t *testing.T) {
	store := openTestStore(t)
	marker := errors.New("rollback")
	if err := withTxErr(store, func(tx Tx) error {
		addVG(t, tx, "rollback-vg")
		return marker
	}); !errors.Is(err, marker) {
		t.Fatalf("Transact = %v, want marker", err)
	}
	runTx(t, store, func(tx Tx) error {
		if _, err := tx.GetVG("rollback-vg"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("rolled-back VG = %v, want ErrNotFound", err)
		}
		return nil
	})
}

func TestStateMiscellaneousCoverage(t *testing.T) {
	store := openTestStore(t)
	if SchemaVersion() == 0 {
		t.Fatal("schema version is zero")
	}
	runTx(t, store, func(tx Tx) error {
		addVG(t, tx, "vg1")
		vgs, err := tx.ListVGs()
		if err != nil || len(vgs) != 1 {
			t.Fatalf("ListVGs = %#v, %v", vgs, err)
		}
		if _, err := tx.GetVG("missing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing VG = %v", err)
		}
		if _, err := tx.GetVG(""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("empty VG = %v", err)
		}
		if err := tx.PutSetting("k", "v"); err != nil {
			return err
		}
		if value, err := tx.GetSetting("k"); err != nil || value != "v" {
			t.Fatalf("setting = %q, %v", value, err)
		}
		volume := addVolume(t, tx, "owner", "vol1", "vg1")
		if err := tx.PutTombstone(Tombstone{
			Owner: "owner", VolumeID: "vol1", ResourceUUID: volume.ResourceUUID,
			LVName: volume.LVName, VGName: volume.VGName, SizeBytes: 1,
			DeletedAt: testTime, LastOperationID: "op", Reason: "test",
		}); err != nil {
			return err
		}
		if tombstones, err := tx.ListTombstones("owner"); err != nil || len(tombstones) != 1 {
			t.Fatalf("tombstones = %#v, %v", tombstones, err)
		}
		if tombstones, err := tx.ListTombstones(""); err != nil || len(tombstones) != 1 {
			t.Fatalf("all tombstones = %#v, %v", tombstones, err)
		}
		if err := tx.PutExport(Export{
			Owner: "owner", VolumeID: "vol1", Protocol: "iscsi", Initiator: "iqn",
			TargetName: "target", Portal: "portal", State: "exported", CreatedAt: testTime, UpdatedAt: testTime,
		}); err != nil {
			return err
		}
		if err := tx.DeleteExport("owner", "vol1"); err != nil {
			return err
		}
		if err := tx.DeleteExport("owner", "vol1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second DeleteExport = %v", err)
		}
		if err := tx.PutCredential(Credential{
			ID: "cred", Owner: "owner", VolumeID: "vol1", Protocol: "iscsi",
			SecretCiphertext: []byte("c"), Nonce: []byte("n"), KeyID: "k",
			KeyFingerprint: "f", State: "active", CreatedAt: testTime,
		}); err != nil {
			return err
		}
		if err := tx.DeleteCredential("owner", "vol1"); err != nil {
			return err
		}
		if err := tx.DeleteCredential("owner", "vol1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second DeleteCredential = %v", err)
		}
		if err := tx.PutOperation(Operation{
			ID: "op1", Owner: "owner", VolumeID: "vol1", ResourceUUID: "resource-vol1",
			Action: "create", RequestHash: "hash", IntentJSON: "{}", State: "running",
			Attempt: 1, MaxAttempts: 2, DeadlineAt: testTime, CreatedAt: testTime, UpdatedAt: testTime,
		}); err != nil {
			return err
		}
		if _, err := tx.GetOperationByID("op1"); err != nil {
			return err
		}
		if _, err := tx.GetOperation("", "vol1", "create", "hash"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid operation = %v", err)
		}
		if operations, err := tx.ListOperations("", ""); err != nil || len(operations) != 1 {
			t.Fatalf("all operations = %#v, %v", operations, err)
		}
		if operations, err := tx.ListOperations("owner", ""); err != nil || len(operations) != 1 {
			t.Fatalf("owner operations = %#v, %v", operations, err)
		}
		if operations, err := tx.ListOperations("", "vol1"); err != nil || len(operations) != 1 {
			t.Fatalf("volume operations = %#v, %v", operations, err)
		}
		if operations, err := tx.ListRunningOperations(); err != nil || len(operations) != 1 {
			t.Fatalf("running operations = %#v, %v", operations, err)
		}
		if err := tx.PutGrant(Grant{
			Owner: "owner", VolumeID: "vol1", NodeID: "node", NodeCallerID: "caller",
			ExpiresAt: testTime.Add(time.Hour), CreatedAt: testTime,
		}); err != nil {
			return err
		}
		if grants, err := tx.ListGrants("", ""); err != nil || len(grants) != 1 {
			t.Fatalf("all grants = %#v, %v", grants, err)
		}
		if grants, err := tx.ListGrants("owner", ""); err != nil || len(grants) != 1 {
			t.Fatalf("owner grants = %#v, %v", grants, err)
		}
		if grants, err := tx.ListGrants("", "vol1"); err != nil || len(grants) != 1 {
			t.Fatalf("volume grants = %#v, %v", grants, err)
		}
		if err := tx.DeleteGrant("owner", "vol1", "node"); err != nil {
			return err
		}
		if err := tx.DeleteGrant("owner", "vol1", "node"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second DeleteGrant = %v", err)
		}
		if err := tx.PutAudit(Audit{ID: 7, Timestamp: testTime, Caller: "owner", OperationID: "op1", Action: "test", Result: "ok"}); err != nil {
			return err
		}
		return nil
	})
	backupPath := filepath.Join(t.TempDir(), "backup.db")
	if err := store.Backup(context.Background(), backupPath); err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if err := store.Backup(context.Background(), backupPath); !errors.Is(err, ErrConflict) {
		t.Fatalf("second Backup = %v", err)
	}
}

func TestStateValidationBranches(t *testing.T) {
	store := openTestStore(t)
	runTx(t, store, func(tx Tx) error {
		if _, err := tx.GetVolume("", "v"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("GetVolume invalid = %v", err)
		}
		if _, err := tx.GetVolumeByResourceUUID(""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("GetVolumeByResourceUUID invalid = %v", err)
		}
		if err := tx.PutVolume(Volume{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("PutVolume invalid = %v", err)
		}
		if _, err := tx.GetExport("", "v"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("GetExport invalid = %v", err)
		}
		if err := tx.PutExport(Export{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("PutExport invalid = %v", err)
		}
		if err := tx.DeleteExport("", "v"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("DeleteExport invalid = %v", err)
		}
		if _, err := tx.GetTombstone("", "v"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("GetTombstone invalid = %v", err)
		}
		if err := tx.PutTombstone(Tombstone{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("PutTombstone invalid = %v", err)
		}
		if err := tx.PutOperation(Operation{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("PutOperation invalid = %v", err)
		}
		if err := tx.PutCredential(Credential{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("PutCredential invalid = %v", err)
		}
		if err := tx.PutGrant(Grant{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("PutGrant invalid = %v", err)
		}
		if _, err := tx.GetGrant("owner", "vol", ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("GetGrant invalid = %v", err)
		}
		if err := tx.DeleteGrant("owner", "vol", ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("DeleteGrant invalid = %v", err)
		}
		if _, err := tx.GetOperation("owner", "vol", "", "hash"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("GetOperation invalid = %v", err)
		}
		if err := tx.PutAudit(Audit{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("PutAudit invalid = %v", err)
		}
		if err := tx.PutVG(VG{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("PutVG invalid = %v", err)
		}
		if _, err := tx.GetSetting(""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("GetSetting invalid = %v", err)
		}
		if err := tx.PutSetting("", "value"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("PutSetting invalid = %v", err)
		}
		if _, err := tx.GetObservation("owner", "missing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing observation = %v", err)
		}
		if _, err := tx.GetExportObservation("owner", "missing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing export observation = %v", err)
		}
		if _, err := tx.GetTombstone("owner", "missing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing tombstone = %v", err)
		}
		if _, err := tx.GetGrant("owner", "missing", "node"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing grant = %v", err)
		}
		if _, err := tx.GetOperationByID("missing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing operation = %v", err)
		}
		return nil
	})
	if err := store.Transact(context.Background(), nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil transaction = %v", err)
	}
}

func TestNormalizePageAndObservationValidation(t *testing.T) {
	if limit, _ := normalizePage(Page{}); limit != 50 {
		t.Fatalf("default page = %d", limit)
	}
	if limit, _ := normalizePage(Page{Limit: 1000}); limit != 100 {
		t.Fatalf("large page = %d", limit)
	}
	store := openTestStore(t)
	runTx(t, store, func(tx Tx) error {
		if err := tx.PutExportObservation(ExportObservation{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid export observation = %v", err)
		}
		if err := tx.PutObservation(Observation{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid observation = %v", err)
		}
		return nil
	})
}
