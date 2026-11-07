package operations

import (
	"context"
	"testing"

	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
)

func TestEnableTargetRecreatesExportAndCredential(t *testing.T) {
	manager, _, exporter, _, store := newTestManager(t)
	create := createRequest(t, "vol1", "resource-1", 64<<20, true)
	if _, err := manager.Run(context.Background(), create); err != nil {
		t.Fatalf("create: %v", err)
	}
	exporter.observation.SessionState = target.SessionActive
	if err := manager.DisableTarget(context.Background(), create.Owner, "vol1"); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if err := manager.EnableTarget(context.Background(), create.Owner, "vol1"); err != nil {
		t.Fatalf("enable: %v", err)
	}
	volume, err := getVolume(t, store, create.Owner, "vol1")
	if err != nil {
		t.Fatal(err)
	}
	if volume.State != "exported" || exporter.exportCalls != 2 {
		t.Fatalf("volume = %#v export calls = %d", volume, exporter.exportCalls)
	}
	var export state.Export
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		value, err := tx.GetExport(create.Owner, "vol1")
		export = value
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if export.State != "exported" {
		t.Fatalf("export = %#v", export)
	}
	credential := activeCredential(t, store, create.Owner, "vol1")
	if credential.State != "active" || credential.ID == "" {
		t.Fatalf("credential = %#v", credential)
	}
}

func TestDisableTargetForceUnexportsAndKeepsDefinition(t *testing.T) {
	manager, _, exporter, _, store := newTestManager(t)
	create := createRequest(t, "vol1", "resource-1", 64<<20, true)
	if _, err := manager.Run(context.Background(), create); err != nil {
		t.Fatalf("create: %v", err)
	}
	exporter.observation.SessionState = target.SessionActive
	for attempt := 0; attempt < 2; attempt++ {
		if err := manager.DisableTarget(context.Background(), create.Owner, "vol1"); err != nil {
			t.Fatalf("disable attempt %d: %v", attempt+1, err)
		}
	}
	if exporter.unexportCalls != 2 {
		t.Fatalf("unexport calls = %d, want 2", exporter.unexportCalls)
	}
	volume, err := getVolume(t, store, create.Owner, "vol1")
	if err != nil {
		t.Fatal(err)
	}
	if volume.State != "available" {
		t.Fatalf("volume state = %q", volume.State)
	}
	var export state.Export
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		value, err := tx.GetExport(create.Owner, "vol1")
		export = value
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if export.State != "unexported" || export.TargetName == "" || export.Initiator == "" {
		t.Fatalf("definition = %#v", export)
	}
}
