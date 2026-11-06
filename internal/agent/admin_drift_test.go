package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/voidstarit/kouja/gen/kouja/v1"
	"github.com/voidstarit/kouja/internal/credential"
	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func addDriftExport(t *testing.T, store *state.Store, owner string) {
	t.Helper()
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
}

func TestResolveDriftMarkActions(t *testing.T) {
	owner := "spiffe://kouja/caller/one"
	newService := func(t *testing.T, present bool, targetPresent bool) (*AdminService, *state.Store) {
		t.Helper()
		store := newAgentStore(t)
		addAgentVolume(t, store, owner, "vol1", "resource-1", "drift")
		addDriftExport(t, store, owner)
		backend := &reconcileLVM{
			fakeLVM:      &fakeLVM{},
			observations: map[string]lvm.LV{},
		}
		if present {
			backend.observations["vg1/kouja-resource-1"] = lvm.LV{
				VG: "vg1", Name: "kouja-resource-1", SizeBytes: 1 << 30, DevicePath: "/dev/vg1/lv1",
			}
		}
		exporter := &reconcileTarget{observation: target.ExportObservation{
			TargetPresent: targetPresent, ACLPresent: targetPresent, SessionState: target.SessionNone,
		}}
		return NewAdminService(store, backend, WithTargetExporters(map[string]target.Exporter{"iscsi": exporter})), store
	}

	t.Run("mark available", func(t *testing.T) {
		service, store := newService(t, true, false)
		response, err := service.ResolveDrift(adminContext(), &koujav1.ResolveDriftRequest{
			Owner: owner, VolumeId: "vol1", Action: koujav1.ResolveAction_RESOLVE_ACTION_MARK_AVAILABLE,
		})
		if err != nil {
			t.Fatalf("ResolveDrift: %v", err)
		}
		if response.GetVolume().GetState() != koujav1.VolumeState_VOLUME_STATE_AVAILABLE {
			t.Fatalf("volume = %#v", response.GetVolume())
		}
		volume, err := fetchStoredVolume(t, store, owner, "vol1")
		if err != nil || volume.State != "available" || len(volume.DriftReasons) != 0 {
			t.Fatalf("stored volume = %#v, %v", volume, err)
		}
	})

	t.Run("mark exported", func(t *testing.T) {
		service, store := newService(t, true, true)
		response, err := service.ResolveDrift(adminContext(), &koujav1.ResolveDriftRequest{
			Owner: owner, VolumeId: "vol1", Action: koujav1.ResolveAction_RESOLVE_ACTION_MARK_EXPORTED,
		})
		if err != nil {
			t.Fatalf("ResolveDrift: %v", err)
		}
		if response.GetVolume().GetState() != koujav1.VolumeState_VOLUME_STATE_EXPORTED {
			t.Fatalf("volume = %#v", response.GetVolume())
		}
		if err := store.Transact(context.Background(), func(tx state.Tx) error {
			export, err := tx.GetExport(owner, "vol1")
			if err != nil {
				return err
			}
			if export.State != "exported" {
				t.Fatalf("export = %#v", export)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("mark failed", func(t *testing.T) {
		service, store := newService(t, true, false)
		if _, err := service.ResolveDrift(adminContext(), &koujav1.ResolveDriftRequest{
			Owner: owner, VolumeId: "vol1", Action: koujav1.ResolveAction_RESOLVE_ACTION_MARK_FAILED,
		}); err != nil {
			t.Fatalf("ResolveDrift: %v", err)
		}
		volume, err := fetchStoredVolume(t, store, owner, "vol1")
		if err != nil || volume.State != "failed" {
			t.Fatalf("stored volume = %#v, %v", volume, err)
		}
	})

	t.Run("mark deleted", func(t *testing.T) {
		service, store := newService(t, false, false)
		if _, err := service.ResolveDrift(adminContext(), &koujav1.ResolveDriftRequest{
			Owner: owner, VolumeId: "vol1", Action: koujav1.ResolveAction_RESOLVE_ACTION_MARK_DELETED,
		}); err != nil {
			t.Fatalf("ResolveDrift: %v", err)
		}
		if _, err := fetchStoredVolume(t, store, owner, "vol1"); !errors.Is(err, state.ErrNotFound) {
			t.Fatalf("volume = %v, want ErrNotFound", err)
		}
		if err := store.Transact(context.Background(), func(tx state.Tx) error {
			_, err := tx.GetTombstone(owner, "vol1")
			return err
		}); err != nil {
			t.Fatalf("tombstone: %v", err)
		}
	})

	t.Run("mark available rejects present target", func(t *testing.T) {
		service, _ := newService(t, true, true)
		if _, err := service.ResolveDrift(adminContext(), &koujav1.ResolveDriftRequest{
			Owner: owner, VolumeId: "vol1", Action: koujav1.ResolveAction_RESOLVE_ACTION_MARK_AVAILABLE,
		}); status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("code = %v, want FailedPrecondition", status.Code(err))
		}
	})

	t.Run("non admin rejected", func(t *testing.T) {
		service, _ := newService(t, true, false)
		if _, err := service.ResolveDrift(callerContext(owner), &koujav1.ResolveDriftRequest{
			Owner: owner, VolumeId: "vol1", Action: koujav1.ResolveAction_RESOLVE_ACTION_MARK_FAILED,
		}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("code = %v, want PermissionDenied", status.Code(err))
		}
	})
}

func TestResolveDriftRecreateExport(t *testing.T) {
	owner := "spiffe://kouja/caller/one"
	store := newAgentStore(t)
	addAgentVolume(t, store, owner, "vol1", "resource-1", "drift")
	addDriftExport(t, store, owner)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		return tx.PutCredential(state.Credential{
			ID: "cred-1", Owner: owner, VolumeID: "vol1", Protocol: "iscsi",
			SecretCiphertext: []byte("cipher"), Nonce: []byte("nonce"),
			KeyID: "key-1", KeyFingerprint: "fingerprint", State: "active", CreatedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}
	backend := &reconcileLVM{fakeLVM: &fakeLVM{}, observations: map[string]lvm.LV{
		"vg1/kouja-resource-1": {VG: "vg1", Name: "kouja-resource-1", SizeBytes: 1 << 30, DevicePath: "/dev/vg1/lv1"},
	}}
	exporter := &reconcileTarget{observation: target.ExportObservation{TargetPresent: false, ACLPresent: false}}
	credentials := &fakeCredentialManager{record: credential.Credential{ID: "cred-1", Protocol: credential.ProtocolISCSI}}
	service := NewAdminService(store, backend,
		WithTargetExporters(map[string]target.Exporter{"iscsi": exporter}),
		WithCredentialManager(credentials),
	)
	response, err := service.ResolveDrift(adminContext(), &koujav1.ResolveDriftRequest{
		Owner: owner, VolumeId: "vol1", Action: koujav1.ResolveAction_RESOLVE_ACTION_RECREATE_EXPORT,
	})
	if err != nil {
		t.Fatalf("ResolveDrift: %v", err)
	}
	if response.GetVolume().GetState() != koujav1.VolumeState_VOLUME_STATE_EXPORTED {
		t.Fatalf("volume = %#v", response.GetVolume())
	}
	export, err := func() (state.Export, error) {
		var value state.Export
		err := store.Transact(context.Background(), func(tx state.Tx) error {
			export, err := tx.GetExport(owner, "vol1")
			value = export
			return err
		})
		return value, err
	}()
	if err != nil || export.State != "exported" {
		t.Fatalf("export = %#v, %v", export, err)
	}
}
