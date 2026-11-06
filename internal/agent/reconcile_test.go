package agent

import (
	"context"
	"testing"
	"time"

	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
)

type reconcileLVM struct {
	*fakeLVM
	observations map[string]lvm.LV
}

func (backend *reconcileLVM) Observe(_ context.Context, vg, lv string) (lvm.LV, error) {
	value, ok := backend.observations[vg+"/"+lv]
	if !ok {
		return lvm.LV{}, lvm.ErrNotFound
	}
	return value, nil
}

type reconcileTarget struct {
	observation target.ExportObservation
	err         error
}

func (exporter *reconcileTarget) Export(context.Context, target.ExportSpec, target.Credential) (target.ExportResult, error) {
	return target.ExportResult{}, nil
}
func (exporter *reconcileTarget) Unexport(context.Context, target.ExportSpec, bool) error {
	return nil
}
func (exporter *reconcileTarget) Observe(context.Context, target.ExportSpec) (target.ExportObservation, error) {
	return exporter.observation, exporter.err
}
func (exporter *reconcileTarget) ListSessions(context.Context) ([]target.Session, error) {
	return nil, nil
}
func (exporter *reconcileTarget) Restore(context.Context, target.ExportSpec, target.Credential) error {
	return nil
}
func (exporter *reconcileTarget) Delete(context.Context, target.ExportSpec, bool) error { return nil }

func bddTestReconcileWritesObservationsAndDrift(t testing.TB) {
	store := newAgentStore(t)
	owner := "spiffe://kouja/caller/one"
	addAgentVolume(t, store, owner, "vol1", "resource-1", "exported")
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		return tx.PutExport(state.Export{
			Owner: owner, VolumeID: "vol1", Protocol: "iscsi", Initiator: "iqn.initiator",
			TargetName: "iqn.target", Portal: "127.0.0.1:3260", State: "exported",
			CreatedAt: now, UpdatedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}
	backend := &reconcileLVM{
		fakeLVM: &fakeLVM{},
		observations: map[string]lvm.LV{
			"vg1/kouja-resource-1": {VG: "vg1", Name: "kouja-resource-1", SizeBytes: 1 << 30, DevicePath: "/dev/vg1/lv1"},
		},
	}
	exporter := &reconcileTarget{observation: target.ExportObservation{
		TargetPresent: true, ACLPresent: true, SessionState: target.SessionNone,
		ObservedInitiator: "iqn.initiator",
	}}
	reconciler := NewReconciler(store, backend, map[string]target.Exporter{"iscsi": exporter}, time.Minute)
	reconciler.now = func() time.Time { return now }
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	var observation state.Observation
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		value, err := tx.GetObservation(owner, "vol1")
		observation = value
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !observation.LVPresent || observation.ObservedSizeBytes != 1<<30 || observation.DevicePath != "/dev/vg1/lv1" {
		t.Fatalf("observation = %#v", observation)
	}
	var exportObservation state.ExportObservation
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		value, err := tx.GetExportObservation(owner, "vol1")
		exportObservation = value
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !exportObservation.TargetPresent || !exportObservation.ACLPresent {
		t.Fatalf("export observation = %#v", exportObservation)
	}

	delete(backend.observations, "vg1/kouja-resource-1")
	exporter.observation.TargetPresent = false
	exporter.observation.ACLPresent = false
	if err := reconciler.Reconcile(context.Background()); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	volume, err := fetchStoredVolume(t, store, owner, "vol1")
	if err != nil {
		t.Fatal(err)
	}
	if volume.State != "drift" || !containsString(volume.DriftReasons, "lv_absent") ||
		!containsString(volume.DriftReasons, "target_absent") || !containsString(volume.DriftReasons, "acl_absent") {
		t.Fatalf("volume = %#v", volume)
	}
}
