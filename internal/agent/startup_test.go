package agent

import (
	"context"
	"testing"
	"time"

	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/state"
)

type startupBackend struct {
	*fakeLVM
	capacities map[string]lvm.Capacity
	errs       map[string]error
}

func (backend *startupBackend) Capacity(_ context.Context, vg string) (lvm.Capacity, error) {
	if err := backend.errs[vg]; err != nil {
		return lvm.Capacity{}, err
	}
	capacity, ok := backend.capacities[vg]
	if !ok {
		return lvm.Capacity{}, lvm.ErrNotFound
	}
	return capacity, nil
}

func (backend *startupBackend) Eligibility(_ context.Context, vg string) error {
	if err := backend.errs[vg]; err != nil {
		return err
	}
	_, ok := backend.capacities[vg]
	if !ok {
		return lvm.ErrNotFound
	}
	return nil
}

func TestInitializeVGsSeedsOnlyOnce(t *testing.T) {
	store := newAgentStore(t)
	backend := &startupBackend{
		fakeLVM: &fakeLVM{},
		capacities: map[string]lvm.Capacity{
			"vg1": {VGName: "vg1", VGUUID: "vg-uuid", VGState: "active", ObservedAt: time.Now()},
		},
		errs: map[string]error{},
	}
	if err := InitializeVGs(context.Background(), store, backend, []string{"vg1"}); err != nil {
		t.Fatalf("InitializeVGs: %v", err)
	}
	var vgs []state.VG
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		values, err := tx.ListVGs()
		vgs = values
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(vgs) != 1 || vgs[0].Name != "vg1" {
		t.Fatalf("VGs = %#v", vgs)
	}
	if err := InitializeVGs(context.Background(), store, backend, []string{"vg2"}); err != nil {
		t.Fatalf("second InitializeVGs: %v", err)
	}
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		values, err := tx.ListVGs()
		vgs = values
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(vgs) != 1 {
		t.Fatalf("configured VGs were seeded twice: %#v", vgs)
	}
}

func TestVerifyVGsMarksUnavailableAndVolumesDrift(t *testing.T) {
	store := newAgentStore(t)
	owner := "spiffe://kouja/caller/one"
	addAgentVolume(t, store, owner, "vol1", "resource-1", "available")
	backend := &startupBackend{
		fakeLVM:    &fakeLVM{},
		capacities: map[string]lvm.Capacity{},
		errs:       map[string]error{"vg1": lvm.ErrNotFound},
	}
	if err := VerifyVGs(context.Background(), store, backend); err != nil {
		t.Fatalf("VerifyVGs: %v", err)
	}
	var vg state.VG
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		value, err := tx.GetVG("vg1")
		vg = value
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if vg.State != "unavailable" {
		t.Fatalf("VG state = %q, want unavailable", vg.State)
	}
	volume, err := fetchStoredVolume(t, store, owner, "vol1")
	if err != nil {
		t.Fatal(err)
	}
	if volume.State != "drift" || !containsString(volume.DriftReasons, "vg_unavailable") {
		t.Fatalf("volume = %#v", volume)
	}
	backend.errs = map[string]error{}
	backend.capacities["vg1"] = lvm.Capacity{VGName: "vg1", VGUUID: "vg-uuid", VGState: "active"}
	if err := VerifyVGs(context.Background(), store, backend); err != nil {
		t.Fatalf("recovery VerifyVGs: %v", err)
	}
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		value, err := tx.GetVG("vg1")
		vg = value
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if vg.State != "active" {
		t.Fatalf("recovered VG state = %q, want active", vg.State)
	}
	volume, err = fetchStoredVolume(t, store, owner, "vol1")
	if err != nil {
		t.Fatal(err)
	}
	if volume.State != "drift" {
		t.Fatalf("volume auto-repaired: %#v", volume)
	}
}

func TestVerifyVGsRejectsUUIDMismatch(t *testing.T) {
	store := newAgentStore(t)
	owner := "spiffe://kouja/caller/one"
	addAgentVolume(t, store, owner, "vol1", "resource-1", "available")
	backend := &startupBackend{
		fakeLVM: &fakeLVM{},
		capacities: map[string]lvm.Capacity{
			"vg1": {VGName: "vg1", VGUUID: "different-uuid", VGState: "active"},
		},
		errs: map[string]error{},
	}
	if err := VerifyVGs(context.Background(), store, backend); err != nil {
		t.Fatalf("VerifyVGs: %v", err)
	}
	var vg state.VG
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		value, err := tx.GetVG("vg1")
		vg = value
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if vg.State != "unavailable" {
		t.Fatalf("VG state = %q, want unavailable", vg.State)
	}
}

func fetchStoredVolume(t *testing.T, store *state.Store, owner, volumeID string) (state.Volume, error) {
	t.Helper()
	var volume state.Volume
	err := store.Transact(context.Background(), func(tx state.Tx) error {
		value, err := tx.GetVolume(owner, volumeID)
		volume = value
		return err
	})
	return volume, err
}
