package agent

import (
	"context"
	"testing"

	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/state"
)

type managerTestVGS struct {
	vg       state.VG
	capacity lvm.Capacity
}

func (v *managerTestVGS) Register(context.Context, string, string, bool) (lvm.Capacity, error) {
	return v.capacity, nil
}
func (v *managerTestVGS) RegisterWithAudit(context.Context, string, string, bool, *state.AuditEvent) (lvm.Capacity, error) {
	return v.capacity, nil
}
func (v *managerTestVGS) Deregister(context.Context, string) error { return nil }
func (v *managerTestVGS) DeregisterWithAudit(context.Context, string, *state.AuditEvent) error {
	return nil
}
func (v *managerTestVGS) Get(context.Context, string) (state.VG, error) { return v.vg, nil }
func (v *managerTestVGS) List(context.Context) ([]state.VG, error) {
	return []state.VG{v.vg}, nil
}
func (v *managerTestVGS) Inspect(context.Context, string) (lvm.Capacity, error) {
	return v.capacity, nil
}

type managerTestClasses struct{ values map[string]string }

func (c *managerTestClasses) Snapshot() map[string]string {
	result := make(map[string]string, len(c.values))
	for key, value := range c.values {
		result[key] = value
	}
	return result
}
func (c *managerTestClasses) ClassName(vg string) string {
	for class, value := range c.values {
		if value == vg {
			return class
		}
	}
	return ""
}
func (c *managerTestClasses) Resolve(class, vg string) (string, string, error) { return class, vg, nil }
func (c *managerTestClasses) Set(_ context.Context, class, vg string) ([]string, error) {
	c.values[class] = vg
	return nil, nil
}
func (c *managerTestClasses) Remove(_ context.Context, class string) ([]string, error) {
	delete(c.values, class)
	return nil, nil
}

func TestVGClassManagerRegistrationAndClassFlow(t *testing.T) {
	vgs := &managerTestVGS{
		vg:       state.VG{Name: "vg1", UUID: "uuid-1", State: "active"},
		capacity: lvm.Capacity{VGName: "vg1", VGUUID: "uuid-1", VGState: "active"},
	}
	classes := &managerTestClasses{values: map[string]string{}}
	manager := NewVGClassManager(vgs, classes, nil, nil)
	registration, _, err := manager.RegisterVG(t.Context(), "vg1", "", false, state.AuditEvent{CallerOwner: "local-admin", Result: "succeeded"})
	if err != nil {
		t.Fatal(err)
	}
	if registration.RegisteredVGUUID != "uuid-1" || registration.Capacity.VGUUID != "uuid-1" {
		t.Fatalf("registration = %#v", registration)
	}
	if _, err := manager.SetClass(t.Context(), "gold", "vg1", state.AuditEvent{CallerOwner: "local-admin", Result: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	values, err := manager.ListClasses(t.Context())
	if err != nil || values["gold"] != "vg1" {
		t.Fatalf("classes = %#v, %v", values, err)
	}
	if _, err := manager.RemoveClass(t.Context(), "gold", state.AuditEvent{CallerOwner: "local-admin", Result: "succeeded"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.GetVG(t.Context(), "vg1"); err != nil {
		t.Fatal(err)
	}
}
