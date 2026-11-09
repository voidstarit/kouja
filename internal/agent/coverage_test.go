package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/voidstarit/kouja/internal/config"
)

func TestAdminOptionsAndClassHelpers(t *testing.T) {
	classes := map[string]string{"gold": "vg1"}
	gate := &sync.Mutex{}
	classMap := NewClassMap(classes, "")
	service := NewAdminService(nil, nil,
		WithStorageClasses(classes),
		WithClassRegistry(classMap),
		WithVGClassGate(gate),
		WithVGClassGate(nil),
		WithVGService(NewVGManager(nil, nil, nil)),
		WithVGClassManager(nil),
		WithCertificateService(nil, nil, nil, nil),
		WithTargetExporters(nil),
		WithCredentialManager(nil),
		WithDriftRetrier(nil),
	)
	if service.classes["gold"] != "vg1" || service.classRegistry != classMap || service.vgClassGate != gate {
		t.Fatalf("options were not applied: %#v", service)
	}
	snapshot := classMap.Snapshot()
	snapshot["changed"] = "vg2"
	if len(classMap.Snapshot()) != 1 {
		t.Fatal("class snapshot is not a copy")
	}
	if classMap.ClassName("vg1") != "gold" {
		t.Fatalf("class name = %q", classMap.ClassName("vg1"))
	}
	if _, _, err := classMap.Resolve("gold", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := classMap.Resolve("gold", "vg2"); err == nil {
		t.Fatal("class/VG mismatch did not fail")
	}
	if _, _, err := classMap.Resolve("", ""); err == nil {
		t.Fatal("empty class and VG did not fail")
	}
	if _, err := classMap.Set(context.Background(), "gold", "vg1"); err == nil {
		t.Fatal("set without config path did not fail")
	}
	if _, err := classMap.Remove(context.Background(), "gold"); err == nil {
		t.Fatal("remove without config path did not fail")
	}
	if _, err := classMap.Remove(context.Background(), "missing"); err == nil {
		t.Fatal("remove missing without config path did not fail")
	}
	if !IsClassNotFound(config.ErrClassNotFound) || IsClassNotFound(nil) {
		t.Fatal("class-not-found predicate is wrong")
	}
}

func TestAdminClassSnapshotAndWarnings(t *testing.T) {
	registry := NewClassMap(map[string]string{"gold": "vg1"}, "")
	service := &AdminService{classRegistry: registry}
	if snapshot := service.classSnapshot(); snapshot["gold"] != "vg1" {
		t.Fatalf("registry snapshot = %#v", snapshot)
	}
	fallback := &AdminService{classes: map[string]string{"silver": "vg2"}}
	if snapshot := fallback.classSnapshot(); snapshot["silver"] != "vg2" {
		t.Fatalf("fallback snapshot = %#v", snapshot)
	}
	warnings := warningStrings([]string{"DURABILITY_UNCERTAIN"})
	if len(warnings) != 1 || warnings[0].Code != "DURABILITY_UNCERTAIN" || warnings[0].Message != "DURABILITY UNCERTAIN" {
		t.Fatalf("warnings = %#v", warnings)
	}
}

func TestServerOptionsAndVGWrapperBranches(t *testing.T) {
	server := &Server{}
	WithReadiness(nil)(server)
	if server.ready != nil {
		t.Fatal("nil readiness replaced default")
	}
	WithReadiness(func() bool { return true })(server)
	if server.ready == nil || !server.ready() {
		t.Fatal("readiness was not applied")
	}
	WithMetrics(nil)(server)
	WithRevocationChecker(nil)(server)
	if server.revocations != nil {
		t.Fatal("nil revocation checker was applied")
	}
	manager := NewVGManager(nil, nil, nil)
	if _, err := manager.RegisterWithAudit(context.Background(), "vg1", "", false, nil); err == nil {
		t.Fatal("register without LVM backend did not fail")
	}
	if err := manager.DeregisterWithAudit(context.Background(), "", nil); err == nil {
		t.Fatal("deregister empty name did not fail")
	}
	if _, err := manager.Get(context.Background(), ""); err == nil {
		t.Fatal("get empty name did not fail")
	}
}
