package agent

import (
	"testing"

	"github.com/voidstarit/kouja/gen/kouja/v1"
	"github.com/voidstarit/kouja/internal/auth"
	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/state"
)

func TestAdminServiceVGClassManagerRPCs(t *testing.T) {
	store, err := state.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	vg := state.VG{Name: "vg1", UUID: "uuid-1", State: "active"}
	if err := store.Transact(t.Context(), func(tx state.Tx) error { return tx.PutVG(vg) }); err != nil {
		t.Fatal(err)
	}
	vgs := &managerTestVGS{vg: vg, capacity: lvm.Capacity{VGName: "vg1", VGUUID: "uuid-1", VGState: "active"}}
	classes := &managerTestClasses{values: map[string]string{}}
	manager := NewVGClassManager(vgs, classes, nil, nil)
	service := NewAdminService(store, nil,
		WithVGService(vgs), WithClassRegistry(classes), WithVGClassManager(manager),
	)
	ctx := WithIdentity(t.Context(), auth.Identity{Owner: "spiffe://kouja/admin/test", Role: auth.RoleAdmin, SerialNumber: "issuer"})
	if _, err := service.RegisterVG(ctx, &koujav1.RegisterVGRequest{VgName: "vg1"}); err != nil {
		t.Fatal(err)
	}
	got, err := service.GetVG(ctx, &koujav1.GetVGRequest{VgName: "vg1"})
	if err != nil || got.GetRegisteredVgUuid() != "uuid-1" {
		t.Fatalf("GetVG = %#v, %v", got, err)
	}
	if _, err := service.ListClasses(ctx, &koujav1.ListClassesRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetClass(ctx, &koujav1.SetClassRequest{ClassName: "gold", VgName: "vg1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetClass(ctx, &koujav1.GetClassRequest{ClassName: "gold"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RemoveClass(ctx, &koujav1.RemoveClassRequest{ClassName: "gold"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.DeregisterVG(ctx, &koujav1.DeregisterVGRequest{VgName: "vg1"}); err != nil {
		t.Fatal(err)
	}
}
