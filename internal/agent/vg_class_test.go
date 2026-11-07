package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/voidstarit/kouja/internal/config"
	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/state"
	"gopkg.in/yaml.v3"
)

type testClassRegistry struct {
	mu      sync.RWMutex
	classes map[string]string
}

func (registry *testClassRegistry) Snapshot() map[string]string {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	result := make(map[string]string, len(registry.classes))
	for class, vg := range registry.classes {
		result[class] = vg
	}
	return result
}
func (registry *testClassRegistry) ClassName(vg string) string {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	for class, mapped := range registry.classes {
		if mapped == vg {
			return class
		}
	}
	return vg
}
func (registry *testClassRegistry) Resolve(class, vg string) (string, string, error) {
	return resolveStorageClass(registry.Snapshot(), class, vg)
}
func (registry *testClassRegistry) Set(context.Context, string, string) ([]string, error) {
	return nil, nil
}
func (registry *testClassRegistry) Remove(context.Context, string) ([]string, error) {
	return nil, nil
}

func TestVGManagerRegistrationSemantics(t *testing.T) {
	store := newAgentStore(t)
	backend := &fakeLVM{capacity: lvmCapacityFor("vg1", "uuid-1")}
	manager := NewVGManager(store, backend, nil)
	ctx := context.Background()
	if _, err := manager.Register(ctx, "vg1", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Register(ctx, "vg1", "uuid-1", false); err != nil {
		t.Fatalf("idempotent register: %v", err)
	}
	if _, err := manager.Register(ctx, "vg1", "wrong", false); !errors.Is(err, ErrVGUUIDMismatch) {
		t.Fatalf("mismatch = %v", err)
	}
	backend.capacity.VGUUID = "uuid-2"
	if _, err := manager.Register(ctx, "vg1", "", false); !errors.Is(err, ErrVGConflict) {
		t.Fatalf("conflict = %v", err)
	}
	if _, err := manager.Register(ctx, "vg1", "uuid-2", true); err != nil {
		t.Fatalf("replace = %v", err)
	}
	if _, err := manager.Inspect(ctx, "vg1"); err != nil {
		t.Fatalf("inspect = %v", err)
	}
	if err := manager.Deregister(ctx, "vg1"); err != nil {
		t.Fatalf("deregister = %v", err)
	}
}

func TestVGManagerClassGuard(t *testing.T) {
	store := newAgentStore(t)
	now := time.Now().UTC()
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		return tx.PutVG(state.VG{Name: "vg1", UUID: "uuid-1", State: "active", RegisteredAt: now, UpdatedAt: now})
	}); err != nil {
		t.Fatal(err)
	}
	manager := NewVGManager(store, &fakeLVM{capacity: lvmCapacityFor("vg1", "uuid-1")}, &testClassRegistry{classes: map[string]string{"ssd": "vg1"}})
	if err := manager.Deregister(context.Background(), "vg1"); !errors.Is(err, ErrVGClassMapped) {
		t.Fatalf("class guard = %v", err)
	}
}

func TestClassMapPersistsAndRejectsConflicts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := config.Default()
	cfg.AdminUI.Enabled = false
	cfg.Storage.VGs = []string{"vg1"}
	cfg.Storage.Classes = map[string]string{}
	cfg.Targets.ISCSI.Portals = []config.Portal{{Address: "127.0.0.1", Port: 3260}}
	raw, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	classes := NewClassMap(map[string]string{}, path)
	if _, err := classes.Set(context.Background(), "ssd", "vg1"); err != nil {
		t.Fatal(err)
	}
	if classes.ClassName("vg1") != "ssd" {
		t.Fatalf("class name = %q", classes.ClassName("vg1"))
	}
	if _, err := classes.Set(context.Background(), "other", "vg1"); !errors.Is(err, ErrClassConflict) {
		t.Fatalf("conflict = %v", err)
	}
	if _, err := classes.Remove(context.Background(), "missing"); !errors.Is(err, config.ErrClassNotFound) {
		t.Fatalf("missing = %v", err)
	}
	if _, err := classes.Remove(context.Background(), "ssd"); err != nil {
		t.Fatal(err)
	}
	if classes.ClassName("vg1") != "vg1" {
		t.Fatalf("class name after remove = %q", classes.ClassName("vg1"))
	}
}

func lvmCapacityFor(name, uuid string) lvm.Capacity {
	return lvm.Capacity{
		VGName: name, VGUUID: uuid, VGState: "active",
		TotalBytes: 1 << 30, FreeBytes: 1 << 30, AllocatableBytes: 1 << 30,
		ExtentSizeBytes: 4 << 20, MaxVolumeBytes: 1 << 30,
		ObservedAt: time.Now().UTC(),
	}
}

func TestVGManagerUnavailableAndNotFound(t *testing.T) {
	store := newAgentStore(t)
	manager := NewVGManager(store, &fakeLVM{eligibilityErr: errors.New("not eligible")}, nil)
	if _, err := manager.Register(context.Background(), "vg1", "", false); !errors.Is(err, ErrVGUnavailable) {
		t.Fatalf("ineligible = %v", err)
	}
	if _, err := manager.Inspect(context.Background(), "bad name"); !errors.Is(err, ErrVGUnavailable) {
		t.Fatalf("bad name = %v", err)
	}
	if err := manager.Deregister(context.Background(), "missing"); !errors.Is(err, ErrVGNotFound) {
		t.Fatalf("missing deregister = %v", err)
	}
	if _, err := manager.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	classes := NewClassMap(map[string]string{"ssd": "vg1"}, "")
	if _, err := classes.Set(context.Background(), "hdd", "vg2"); err == nil {
		t.Fatal("class set without config path succeeded")
	}
	if _, err := classes.Remove(context.Background(), "missing"); err == nil {
		t.Fatal("remove without config path succeeded")
	}
}
