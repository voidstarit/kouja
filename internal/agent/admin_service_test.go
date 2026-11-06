package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/voidstarit/kouja/gen/kouja/v1"
	"github.com/voidstarit/kouja/internal/auth"
	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/state"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func adminContext() context.Context {
	return WithIdentity(context.Background(), auth.Identity{
		Owner: "spiffe://kouja/admin/one", Role: auth.RoleAdmin,
	})
}

func bddTestAdminRegisterAndDeregisterVG(t testing.TB) {
	store := newAgentStore(t)
	backend := &fakeLVM{capacity: lvm.Capacity{
		VGName: "vg1", VGUUID: "vg-uuid", VGState: "active", TotalBytes: 10,
		FreeBytes: 8, AllocatableBytes: 8, ExtentSizeBytes: 1, MaxVolumeBytes: 8,
		ObservedAt: time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
	}}
	service := NewAdminService(store, backend)
	response, err := service.RegisterVG(adminContext(), &koujav1.RegisterVGRequest{
		VgName: "vg1", VgUuid: "vg-uuid", RequestId: "req-1",
	})
	if err != nil {
		t.Fatalf("RegisterVG: %v", err)
	}
	if response.GetCapacity().GetVgName() != "vg1" || response.GetRequestId() != "req-1" {
		t.Fatalf("response = %#v", response)
	}
	list, err := service.ListVGs(adminContext(), &koujav1.ListVGsRequest{})
	if err != nil {
		t.Fatalf("ListVGs: %v", err)
	}
	if len(list.Vgs) != 1 || list.Vgs[0].GetAllocatableBytes() != 8 {
		t.Fatalf("list = %#v", list)
	}
	if _, err := service.DeregisterVG(adminContext(), &koujav1.DeregisterVGRequest{VgName: "vg1"}); err != nil {
		t.Fatalf("DeregisterVG: %v", err)
	}
}

func bddTestAdminGrantsAndRevokesNodeCredential(t testing.TB) {
	store := newAgentStore(t)
	owner := "spiffe://kouja/controller/one"
	addAgentVolume(t, store, owner, "vol1", "resource-1", "exported")
	service := NewAdminService(store, &fakeLVM{})
	controller := WithIdentity(context.Background(), auth.Identity{Owner: owner, Role: auth.RoleController})
	expires := time.Now().UTC().Add(time.Hour)
	if _, err := service.GrantCredential(controller, &koujav1.GrantCredentialRequest{
		VolumeId: "vol1", NodeId: "node-1", NodeCallerId: "spiffe://kouja/node/node-1",
		Owner: owner, ExpiresAt: timestamppb.New(expires),
	}); err != nil {
		t.Fatalf("GrantCredential: %v", err)
	}
	var grant state.Grant
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		value, err := tx.GetGrant(owner, "vol1", "node-1")
		grant = value
		return err
	}); err != nil {
		t.Fatalf("GetGrant: %v", err)
	}
	if grant.NodeCallerID != "spiffe://kouja/node/node-1" {
		t.Fatalf("grant = %#v", grant)
	}
	if _, err := service.RevokeGrant(controller, &koujav1.RevokeGrantRequest{
		VolumeId: "vol1", NodeId: "node-1", Owner: owner,
	}); err != nil {
		t.Fatalf("RevokeGrant: %v", err)
	}
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		_, err := tx.GetGrant(owner, "vol1", "node-1")
		return err
	}); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("grant after revoke = %v, want ErrNotFound", err)
	}
}

func bddTestAdminRejectsNonAdminAndDeregisterWithVolume(t testing.TB) {
	store := newAgentStore(t)
	owner := "spiffe://kouja/caller/one"
	addAgentVolume(t, store, owner, "vol1", "resource-1", "available")
	backend := &fakeLVM{capacity: lvm.Capacity{VGName: "vg1", VGUUID: "vg-uuid"}}
	service := NewAdminService(store, backend)
	if _, err := service.RegisterVG(callerContext(owner), &koujav1.RegisterVGRequest{VgName: "vg2"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("non-admin RegisterVG code = %v, want PermissionDenied", status.Code(err))
	}
	if _, err := service.DeregisterVG(adminContext(), &koujav1.DeregisterVGRequest{VgName: "vg1"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("DeregisterVG with volume code = %v, want FailedPrecondition", status.Code(err))
	}
}

func bddTestAdminValidationBranches(t testing.TB) {
	store := newAgentStore(t)
	owner := "spiffe://kouja/controller/one"
	addAgentVolume(t, store, owner, "vol1", "resource-1", "available")
	service := NewAdminService(store, &fakeLVM{})
	controller := WithIdentity(context.Background(), auth.Identity{Owner: owner, Role: auth.RoleController})
	if _, err := service.GrantCredential(controller, &koujav1.GrantCredentialRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty grant = %v", err)
	}
	if _, err := service.GrantCredential(controller, &koujav1.GrantCredentialRequest{
		VolumeId: "missing", NodeId: "node", NodeCallerId: "caller",
	}); status.Code(err) != codes.NotFound {
		t.Fatalf("missing volume grant = %v", err)
	}
	if _, err := service.GrantCredential(controller, &koujav1.GrantCredentialRequest{
		VolumeId: "vol1", NodeId: "node", NodeCallerId: "caller", Owner: owner,
		ExpiresAt: timestamppb.New(time.Now().Add(-time.Hour)),
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expired grant = %v", err)
	}
	if _, err := service.RevokeGrant(controller, &koujav1.RevokeGrantRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty revoke = %v", err)
	}
	if _, err := service.RevokeGrant(controller, &koujav1.RevokeGrantRequest{VolumeId: "vol1", NodeId: "missing"}); err != nil {
		t.Fatalf("missing revoke = %v", err)
	}
	if _, err := service.ResolveDrift(adminContext(), &koujav1.ResolveDriftRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty resolve = %v", err)
	}
	if _, err := service.ResolveDrift(adminContext(), &koujav1.ResolveDriftRequest{Owner: owner, VolumeId: "vol1", Action: 99}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad resolve action = %v", err)
	}
}
