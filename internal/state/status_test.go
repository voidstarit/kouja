package state

import (
	"context"
	"testing"
	"time"
)

func TestVolumeStatusQueries(t *testing.T) {
	store := openTestStore(t)
	runTx(t, store, func(tx Tx) error {
		addVG(t, tx, "vg1")
		addVolume(t, tx, "owner-a", "vol1", "vg1")
		if err := tx.PutExport(Export{
			Owner: "owner-a", VolumeID: "vol1", Protocol: "iscsi", Initiator: "iqn.i",
			TargetName: "iqn.t", Portal: "127.0.0.1:3260", State: "exported",
			CreatedAt: testTime, UpdatedAt: testTime,
		}); err != nil {
			return err
		}
		if err := tx.PutObservation(Observation{
			Owner: "owner-a", VolumeID: "vol1", LVPresent: true,
			ObservedSizeBytes: 1 << 30, DevicePath: "/dev/vg1/lv-vol1",
			ObservedAt: testTime, Source: "lvm",
		}); err != nil {
			return err
		}
		return tx.PutExportObservation(ExportObservation{
			Owner: "owner-a", VolumeID: "vol1", TargetPresent: true,
			NamespacePresent: true, ACLPresent: true, ActiveSessions: 1,
			SessionState: "active", ObservedInitiator: "iqn.i",
			ObservedAt: testTime, Source: "lio",
		})
	})
	var statuses []VolumeStatus
	if err := store.Transact(context.Background(), func(tx Tx) error {
		values, err := tx.ListVolumeStatuses(10)
		statuses = values
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 {
		t.Fatalf("statuses = %d", len(statuses))
	}
	var allStatuses []VolumeStatus
	if err := store.Transact(context.Background(), func(tx Tx) error {
		values, err := tx.ListVolumeStatusesAll()
		allStatuses = values
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(allStatuses) != 1 || allStatuses[0].VolumeID != "vol1" {
		t.Fatalf("all statuses = %#v", allStatuses)
	}
	status := statuses[0]
	if status.Export == nil || status.Observation == nil || status.ExportObservation == nil {
		t.Fatalf("missing joined rows: %#v", status)
	}
	if status.Observation.ObservedSizeBytes != 1<<30 || !status.ExportObservation.ACLPresent {
		t.Fatalf("joined values = %#v", status)
	}
	var counts map[string]int
	if err := store.Transact(context.Background(), func(tx Tx) error {
		values, err := tx.VolumeStateCounts()
		counts = values
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if counts["available"] != 1 || counts["drift"] != 0 {
		t.Fatalf("counts = %#v", counts)
	}
}

func TestOperationStatusQueries(t *testing.T) {
	store := openTestStore(t)
	now := time.Now().UTC()
	runTx(t, store, func(tx Tx) error {
		for _, operation := range []Operation{
			{ID: "running", Owner: "o", VolumeID: "v", ResourceUUID: "r", Action: "create", RequestHash: "h1", IntentJSON: "{}", State: "running", Attempt: 1, MaxAttempts: 3, DeadlineAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now},
			{ID: "failed", Owner: "o", VolumeID: "v", ResourceUUID: "r", Action: "create", RequestHash: "h2", IntentJSON: "{}", State: "failed", Attempt: 3, MaxAttempts: 3, DeadlineAt: now, CreatedAt: now, UpdatedAt: now},
			{ID: "succeeded", Owner: "o", VolumeID: "v", ResourceUUID: "r", Action: "create", RequestHash: "h3", IntentJSON: "{}", State: "succeeded", Attempt: 1, MaxAttempts: 3, DeadlineAt: now, CreatedAt: now, UpdatedAt: now},
		} {
			if err := tx.PutOperation(operation); err != nil {
				return err
			}
		}
		return nil
	})
	var operations []Operation
	var count int
	if err := store.Transact(context.Background(), func(tx Tx) error {
		values, err := tx.ListRecentOperations(10)
		operations = values
		if err != nil {
			return err
		}
		count, err = tx.OperationCount()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(operations) != 2 || count != 2 {
		t.Fatalf("operations = %#v count=%d", operations, count)
	}
	for _, operation := range operations {
		if operation.State == "succeeded" {
			t.Fatalf("unexpected succeeded operation: %#v", operation)
		}
	}
}

func TestEmptyStatusQueries(t *testing.T) {
	store := openTestStore(t)
	if err := store.Transact(context.Background(), func(tx Tx) error {
		statuses, err := tx.ListVolumeStatuses(0)
		if err != nil || len(statuses) != 0 {
			t.Fatalf("empty statuses = %#v %v", statuses, err)
		}
		counts, err := tx.VolumeStateCounts()
		if err != nil || counts["exported"] != 0 {
			t.Fatalf("empty counts = %#v %v", counts, err)
		}
		operations, err := tx.ListRecentOperations(0)
		if err != nil || len(operations) != 0 {
			t.Fatalf("empty operations = %#v %v", operations, err)
		}
		count, err := tx.OperationCount()
		if err != nil || count != 0 {
			t.Fatalf("empty operation count = %d %v", count, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestVolumeStatusWithoutRelatedRows(t *testing.T) {
	store := openTestStore(t)
	runTx(t, store, func(tx Tx) error {
		addVG(t, tx, "vg1")
		addVolume(t, tx, "owner", "vol1", "vg1")
		return nil
	})
	if err := store.Transact(context.Background(), func(tx Tx) error {
		statuses, err := tx.ListVolumeStatuses(1)
		if err != nil {
			return err
		}
		if len(statuses) != 1 || statuses[0].Export != nil || statuses[0].Observation != nil || statuses[0].ExportObservation != nil {
			t.Fatalf("unexpected related rows: %#v", statuses)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
