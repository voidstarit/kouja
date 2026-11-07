package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/voidstarit/kouja/gen/kouja/v1"
	"github.com/voidstarit/kouja/internal/auth"
	"github.com/voidstarit/kouja/internal/config"
	"github.com/voidstarit/kouja/internal/credential"
	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/operations"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeRunner struct {
	request operations.Request
	result  operations.Result
	err     error
	calls   int
}

func (runner *fakeRunner) Run(_ context.Context, request operations.Request) (operations.Result, error) {
	runner.calls++
	runner.request = request
	return runner.result, runner.err
}

type fakeLVM struct {
	capacity       lvm.Capacity
	capacityErr    error
	eligibilityErr error
}

func (backend *fakeLVM) Create(context.Context, string, string, int64, map[string]string) (lvm.LV, error) {
	return lvm.LV{}, nil
}
func (backend *fakeLVM) Extend(context.Context, string, string, int64) (lvm.LV, error) {
	return lvm.LV{}, nil
}
func (backend *fakeLVM) Shrink(context.Context, string, string, int64) (lvm.LV, error) {
	return lvm.LV{}, nil
}
func (backend *fakeLVM) Remove(context.Context, string, string) error { return nil }
func (backend *fakeLVM) Activate(context.Context, string, string) error {
	return nil
}
func (backend *fakeLVM) Observe(context.Context, string, string) (lvm.LV, error) {
	return lvm.LV{}, lvm.ErrNotFound
}
func (backend *fakeLVM) ListManaged(context.Context) ([]lvm.LV, error) { return nil, nil }
func (backend *fakeLVM) ListCandidates(context.Context) ([]lvm.LV, error) {
	return nil, nil
}
func (backend *fakeLVM) SetOwnerTag(context.Context, string, string, string) error { return nil }
func (backend *fakeLVM) Capacity(context.Context, string) (lvm.Capacity, error) {
	return backend.capacity, backend.capacityErr
}
func (backend *fakeLVM) Eligibility(context.Context, string) error { return backend.eligibilityErr }

type fakeCredentialManager struct {
	record credential.Credential
	err    error
}

func (manager *fakeCredentialManager) Generate(context.Context, string, string, string, credential.Protocol) (credential.Credential, error) {
	return manager.record, manager.err
}
func (manager *fakeCredentialManager) Reveal(context.Context, credential.Credential) (target.Credential, error) {
	return target.Credential{Username: "user", Secret: "secret"}, manager.err
}
func (manager *fakeCredentialManager) Get(context.Context, auth.Identity, string, string) (credential.Credential, error) {
	return manager.record, manager.err
}
func (manager *fakeCredentialManager) Rotate(context.Context, auth.Identity, string, string) (credential.Credential, error) {
	return manager.record, manager.err
}
func (manager *fakeCredentialManager) Install(context.Context, target.ExportSpec, credential.Credential) error {
	return nil
}
func (manager *fakeCredentialManager) Revoke(context.Context, target.ExportSpec) error { return nil }

func newAgentStore(t testing.TB) *state.Store {
	t.Helper()
	store, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func addAgentVolume(t testing.TB, store *state.Store, owner, volumeID, resourceUUID, stateValue string) {
	t.Helper()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	err := store.Transact(context.Background(), func(tx state.Tx) error {
		if err := tx.PutVG(state.VG{Name: "vg1", UUID: "vg-uuid", State: "active", RegisteredAt: now, UpdatedAt: now}); err != nil {
			return err
		}
		return tx.PutVolume(state.Volume{
			Owner: owner, VolumeID: volumeID, ResourceUUID: resourceUUID,
			LVName: "kouja-" + resourceUUID, VGName: "vg1",
			RequestedSizeBytes: 1 << 30, ActualSizeBytes: 1 << 30,
			State: stateValue, CreatedAt: now, UpdatedAt: now,
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func callerContext(owner string) context.Context {
	return WithIdentity(context.Background(), auth.Identity{Owner: owner, Role: auth.RoleCaller})
}

func bddTestCreateVolumeBuildsCanonicalIntent(t testing.TB) {
	store := newAgentStore(t)
	runner := &fakeRunner{result: operations.Result{OperationID: "op-1"}}
	service := NewVolumeService(store, &fakeLVM{}, runner, &fakeCredentialManager{}, ServiceOptions{
		HostUUID: "host-uuid", AgentVersion: "test", APIVersion: "v1",
		ISCSIEnabled: true, ISCSIPrefix: "iqn.2026-11.io.kouja",
		ISCSIPortals: []config.Portal{{Address: "127.0.0.1", Port: 3260}},
	})
	response, err := service.CreateVolume(callerContext("spiffe://kouja/caller/one"), &koujav1.CreateVolumeRequest{
		VolumeId: "vol1", SizeBytes: 1 << 30, Vg: "vg1", Initiator: "iqn.initiator",
		Protocol: koujav1.Protocol_PROTOCOL_ISCSI,
	})
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	if runner.calls != 1 || runner.request.Action != operations.ActionCreate || response.OperationId != "op-1" {
		t.Fatalf("runner call = %#v response=%#v", runner.request, response)
	}
	var intent operations.CreateIntent
	if err := json.Unmarshal(runner.request.Intent, &intent); err != nil {
		t.Fatal(err)
	}
	if intent.ResourceUUID == "" || intent.Export != true || intent.Protocol != "iscsi" ||
		intent.TargetName != "iqn.2026-11.io.kouja:host-uuid:"+intent.ResourceUUID ||
		intent.Portal != "127.0.0.1:3260" {
		t.Fatalf("intent = %#v", intent)
	}
}

func bddTestCreateVolumeUnexportedDoesNotRequireProtocol(t testing.TB) {
	store := newAgentStore(t)
	runner := &fakeRunner{}
	service := NewVolumeService(store, &fakeLVM{}, runner, &fakeCredentialManager{}, ServiceOptions{
		HostUUID: "host-uuid", ISCSIEnabled: true, ISCSIPrefix: "iqn.test",
		ISCSIPortals: []config.Portal{{Address: "127.0.0.1", Port: 3260}},
	})
	export := false
	if _, err := service.CreateVolume(callerContext("spiffe://kouja/caller/one"), &koujav1.CreateVolumeRequest{
		VolumeId: "vol1", SizeBytes: 1 << 20, Vg: "vg1", Export: &export,
	}); err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	var intent operations.CreateIntent
	if err := json.Unmarshal(runner.request.Intent, &intent); err != nil {
		t.Fatal(err)
	}
	if intent.Export || intent.Protocol != "" || intent.TargetName != "" {
		t.Fatalf("intent = %#v", intent)
	}
}

func bddTestGetVolumeOwnerScopingAndMapping(t testing.TB) {
	store := newAgentStore(t)
	owner := "spiffe://kouja/caller/one"
	addAgentVolume(t, store, owner, "vol1", "resource-1", "available")
	service := NewVolumeService(store, &fakeLVM{}, &fakeRunner{}, &fakeCredentialManager{}, ServiceOptions{})
	response, err := service.GetVolume(callerContext(owner), &koujav1.GetVolumeRequest{VolumeId: "vol1"})
	if err != nil {
		t.Fatalf("GetVolume: %v", err)
	}
	if response.Desired.GetVolumeId() != "vol1" || response.Desired.GetState() != koujav1.VolumeState_VOLUME_STATE_AVAILABLE {
		t.Fatalf("response = %#v", response)
	}
	_, err = service.GetVolume(callerContext("spiffe://kouja/caller/two"), &koujav1.GetVolumeRequest{VolumeId: "vol1"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("non-owner GetVolume code = %v, want NotFound", status.Code(err))
	}
}

func bddTestListVolumesPaginationAndCapacity(t testing.TB) {
	store := newAgentStore(t)
	owner := "spiffe://kouja/caller/one"
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		if err := tx.PutVG(state.VG{Name: "vg1", UUID: "vg-uuid", State: "active", RegisteredAt: now, UpdatedAt: now}); err != nil {
			return err
		}
		for _, id := range []string{"vol1", "vol2", "vol3"} {
			if err := tx.PutVolume(state.Volume{
				Owner: owner, VolumeID: id, ResourceUUID: "resource-" + id,
				LVName: "lv-" + id, VGName: "vg1", RequestedSizeBytes: 1,
				ActualSizeBytes: 1, State: "available", CreatedAt: now, UpdatedAt: now,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	service := NewVolumeService(store, &fakeLVM{capacity: lvm.Capacity{
		VGName: "vg1", VGUUID: "vg-uuid", VGState: "active", TotalBytes: 10,
		FreeBytes: 8, AllocatableBytes: 8, ExtentSizeBytes: 1, MaxVolumeBytes: 8, ObservedAt: now,
	}}, &fakeRunner{}, &fakeCredentialManager{}, ServiceOptions{})
	list, err := service.ListVolumes(callerContext(owner), &koujav1.ListVolumesRequest{PageSize: 2})
	if err != nil {
		t.Fatalf("ListVolumes: %v", err)
	}
	if len(list.Volumes) != 2 || list.NextPageToken != "vol2" {
		t.Fatalf("list = %#v", list)
	}
	capacity, err := service.GetCapacity(context.Background(), &koujav1.GetCapacityRequest{})
	if err != nil {
		t.Fatalf("GetCapacity: %v", err)
	}
	if len(capacity.Vgs) != 1 || capacity.Vgs[0].GetAllocatableBytes() != 8 {
		t.Fatalf("capacity = %#v", capacity)
	}
}

func bddTestOperationErrorMapsToStatus(t testing.TB) {
	store := newAgentStore(t)
	runner := &fakeRunner{err: &operations.OperationError{
		Code: "SESSION_ACTIVE", Retryable: false, OperationID: "op-1", Err: operations.ErrSessionActive,
	}}
	service := NewVolumeService(store, &fakeLVM{}, runner, &fakeCredentialManager{}, ServiceOptions{})
	export := false
	_, err := service.CreateVolume(callerContext("spiffe://kouja/caller/one"), &koujav1.CreateVolumeRequest{
		VolumeId: "vol1", SizeBytes: 1, Vg: "vg1", Export: &export,
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("error code = %v, want FailedPrecondition", status.Code(err))
	}
}

func bddTestGetCredentialRevealsSecret(t testing.TB) {
	store := newAgentStore(t)
	owner := "spiffe://kouja/caller/one"
	addAgentVolume(t, store, owner, "vol1", "resource-1", "available")
	manager := &fakeCredentialManager{record: credential.Credential{ID: "cred-1", Protocol: credential.ProtocolISCSI}}
	service := NewVolumeService(store, &fakeLVM{}, &fakeRunner{}, manager, ServiceOptions{})
	response, err := service.GetCredential(callerContext(owner), &koujav1.GetCredentialRequest{VolumeId: "vol1"})
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if response.GetProtocol() != koujav1.Protocol_PROTOCOL_ISCSI || response.GetCredential() != "secret" {
		t.Fatalf("credential = %#v", response)
	}
}

func bddTestVolumeServiceMutationRPCsAndCredentialValue(t testing.TB) {
	store := newAgentStore(t)
	owner := "spiffe://kouja/caller/one"
	addAgentVolume(t, store, owner, "vol1", "resource-1", "exported")
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	export := state.Export{
		Owner: owner, VolumeID: "vol1", Protocol: "iscsi", Initiator: "iqn.test",
		TargetName: "iqn.target", Portal: "127.0.0.1:3260", State: "exported",
		CreatedAt: now, UpdatedAt: now,
	}
	storedCredential := state.Credential{
		ID: "cred-1", Owner: owner, VolumeID: "vol1", Protocol: "iscsi",
		KeyID: "key-1", KeyFingerprint: "fingerprint", SecretCiphertext: []byte("cipher"), Nonce: []byte("nonce"), State: "active", CreatedAt: now,
	}
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		if err := tx.PutExport(export); err != nil {
			return err
		}
		return tx.PutCredential(storedCredential)
	}); err != nil {
		t.Fatal(err)
	}
	volume := state.Volume{
		Owner: owner, VolumeID: "vol1", ResourceUUID: "resource-1", LVName: "lv1",
		VGName: "vg1", RequestedSizeBytes: 1, ActualSizeBytes: 1, State: "exported",
		CreatedAt: now, UpdatedAt: now,
	}
	runner := &fakeRunner{result: operations.Result{Volume: volume, Export: export, OperationID: "op-1"}}
	manager := &fakeCredentialManager{record: credential.Credential{ID: "cred-1", Owner: owner, VolumeID: "vol1", Protocol: credential.ProtocolISCSI}}
	service := NewVolumeService(store, &fakeLVM{}, runner, manager, ServiceOptions{})
	ctx := callerContext(owner)

	exportResponse, err := service.ExportVolume(ctx, &koujav1.ExportVolumeRequest{
		VolumeId: "vol1", Protocol: koujav1.Protocol_PROTOCOL_ISCSI, Initiator: "iqn.test",
	})
	if err != nil || exportResponse.GetOperationId() != "op-1" || runner.request.Action != operations.ActionExport {
		t.Fatalf("ExportVolume = %#v, %v", exportResponse, err)
	}
	unexportResponse, err := service.UnexportVolume(ctx, &koujav1.UnexportVolumeRequest{VolumeId: "vol1", Force: true})
	if err != nil || unexportResponse.GetOperationId() != "op-1" || runner.request.Action != operations.ActionUnexport {
		t.Fatalf("UnexportVolume = %#v, %v", unexportResponse, err)
	}
	resizeResponse, err := service.ResizeVolume(ctx, &koujav1.ResizeVolumeRequest{VolumeId: "vol1", SizeBytes: 2})
	if err != nil || !resizeResponse.GetRescanRequired() || runner.request.Action != operations.ActionResize {
		t.Fatalf("ResizeVolume = %#v, %v", resizeResponse, err)
	}
	deleteResponse, err := service.DeleteVolume(ctx, &koujav1.DeleteVolumeRequest{VolumeId: "vol1", Force: true})
	if err != nil || deleteResponse.GetOperationId() != "op-1" || runner.request.Action != operations.ActionDelete {
		t.Fatalf("DeleteVolume = %#v, %v", deleteResponse, err)
	}
	rotateResponse, err := service.RotateCredential(ctx, &koujav1.RotateCredentialRequest{VolumeId: "vol1"})
	if err != nil || rotateResponse.GetCredential() != "secret" {
		t.Fatalf("RotateCredential = %#v, %v", rotateResponse, err)
	}
	include := true
	createResponse, err := service.CreateVolume(ctx, &koujav1.CreateVolumeRequest{
		VolumeId: "vol1", SizeBytes: 1, Vg: "vg1", Protocol: koujav1.Protocol_PROTOCOL_ISCSI,
		Initiator: "iqn.test", IncludeCredential: &include,
	})
	if err != nil || createResponse.GetExport().GetCredential() != "secret" {
		t.Fatalf("CreateVolume with credential = %#v, %v", createResponse, err)
	}
}

func bddTestVolumeServiceHelpersAndStatusErrors(t testing.TB) {
	store := newAgentStore(t)
	service := NewVolumeService(store, &fakeLVM{}, &fakeRunner{}, &fakeCredentialManager{}, ServiceOptions{})
	release, err := service.admit(context.Background(), auth.Identity{Owner: "owner"})
	if err != nil {
		t.Fatalf("nil admission: %v", err)
	}
	release()
	if _, err := protocolString(koujav1.Protocol_PROTOCOL_UNSPECIFIED); err == nil {
		t.Fatal("unspecified protocol did not fail")
	}
	if protocol, err := protocolString(koujav1.Protocol_PROTOCOL_NVME_TCP); err != nil || protocol != "nvme-tcp" {
		t.Fatalf("NVMe protocol = %q, %v", protocol, err)
	}
	if protocolProto("iscsi") != koujav1.Protocol_PROTOCOL_ISCSI || protocolProto("nvme-tcp") != koujav1.Protocol_PROTOCOL_NVME_TCP {
		t.Fatal("protocol mapping failed")
	}
	if protocolProto("other") != koujav1.Protocol_PROTOCOL_UNSPECIFIED {
		t.Fatal("unknown protocol mapping failed")
	}
	if volumeState("exported") != koujav1.VolumeState_VOLUME_STATE_EXPORTED ||
		volumeState("unknown") != koujav1.VolumeState_VOLUME_STATE_UNSPECIFIED {
		t.Fatal("volume state mapping failed")
	}
	if exportState("exported") != koujav1.ExportState_EXPORT_STATE_EXPORTED ||
		exportState("unknown") != koujav1.ExportState_EXPORT_STATE_UNSPECIFIED {
		t.Fatal("export state mapping failed")
	}
	if sessionState("active") != koujav1.SessionState_SESSION_STATE_ACTIVE ||
		sessionState("unknown") != koujav1.SessionState_SESSION_STATE_UNKNOWN ||
		sessionState("other") != koujav1.SessionState_SESSION_STATE_UNSPECIFIED {
		t.Fatal("session state mapping failed")
	}
	if len(driftReasons([]string{"lv_absent", "unknown"})) != 2 {
		t.Fatal("drift reason mapping failed")
	}
	if _, err := marshalJSON(make(chan int)); err == nil {
		t.Fatal("marshal channel did not fail")
	}
	if _, err := service.targetName("bad", "resource"); err == nil {
		t.Fatal("bad target protocol did not fail")
	}
	if _, err := service.portal("bad"); err == nil {
		t.Fatal("bad portal protocol did not fail")
	}
	if _, err := service.portal("iscsi"); err == nil {
		t.Fatal("unconfigured portal did not fail")
	}
	if _, err := service.getExportObservation(context.Background(), "owner", "missing"); err == nil {
		t.Fatal("missing export observation did not fail")
	}
	for _, test := range []struct {
		err  error
		code codes.Code
	}{
		{&operations.OperationError{Code: "INVALID_ARGUMENT", Err: operations.ErrInvalidRequest}, codes.InvalidArgument},
		{&operations.OperationError{Code: "NOT_FOUND", Err: state.ErrNotFound}, codes.NotFound},
		{&operations.OperationError{Code: "SESSION_ACTIVE", Err: operations.ErrSessionActive}, codes.FailedPrecondition},
		{&operations.OperationError{Code: "RESOURCE_EXHAUSTED", Err: operations.ErrResourceExhausted}, codes.ResourceExhausted},
		{&operations.OperationError{Code: "UNAVAILABLE", Err: errors.New("unavailable")}, codes.Unavailable},
		{&operations.OperationError{Code: "DEADLINE_EXCEEDED", Err: context.DeadlineExceeded}, codes.DeadlineExceeded},
		{&operations.OperationError{Code: "UNIMPLEMENTED", Err: errors.New("unimplemented")}, codes.Unimplemented},
		{&operations.OperationError{Code: "CONFLICT", Err: errors.New("conflict")}, codes.Aborted},
		{ErrForbidden, codes.PermissionDenied},
		{ErrAdmission, codes.ResourceExhausted},
		{context.Canceled, codes.Canceled},
		{context.DeadlineExceeded, codes.DeadlineExceeded},
		{operations.ErrInvalidRequest, codes.InvalidArgument},
		{target.ErrSessionActive, codes.FailedPrecondition},
		{target.ErrSessionUnknown, codes.FailedPrecondition},
		{errors.New("other"), codes.Internal},
	} {
		if code := status.Code(statusError(test.err)); code != test.code {
			t.Fatalf("statusError(%v) = %v, want %v", test.err, code, test.code)
		}
	}
}

func bddTestVolumeServiceRPCErrorBranches(t testing.TB) {
	store := newAgentStore(t)
	owner := "spiffe://kouja/caller/one"
	addAgentVolume(t, store, owner, "vol1", "resource-1", "available")
	runner := &fakeRunner{err: &operations.OperationError{Code: "UNAVAILABLE", Retryable: true, Err: errors.New("backend")}}
	service := NewVolumeService(store, &fakeLVM{}, runner, &fakeCredentialManager{}, ServiceOptions{})
	ctx := callerContext(owner)
	if _, err := service.ExportVolume(ctx, &koujav1.ExportVolumeRequest{
		VolumeId: "vol1", Protocol: koujav1.Protocol_PROTOCOL_ISCSI, Initiator: "iqn",
	}); err == nil {
		t.Fatal("export runner error did not fail")
	}
	if _, err := service.UnexportVolume(ctx, &koujav1.UnexportVolumeRequest{VolumeId: "vol1"}); err == nil {
		t.Fatal("unexport runner error did not fail")
	}
	if _, err := service.ResizeVolume(ctx, &koujav1.ResizeVolumeRequest{VolumeId: "vol1", SizeBytes: 1}); err == nil {
		t.Fatal("resize runner error did not fail")
	}
	if _, err := service.DeleteVolume(ctx, &koujav1.DeleteVolumeRequest{VolumeId: "vol1"}); err == nil {
		t.Fatal("delete runner error did not fail")
	}
	if _, err := service.RotateCredential(ctx, &koujav1.RotateCredentialRequest{VolumeId: "missing"}); err == nil {
		t.Fatal("missing rotate did not fail")
	}
	if _, err := service.ExportVolume(ctx, &koujav1.ExportVolumeRequest{VolumeId: "missing", Protocol: koujav1.Protocol_PROTOCOL_ISCSI, Initiator: "iqn"}); err == nil {
		t.Fatal("missing export did not fail")
	}
	if _, err := service.ExportVolume(ctx, &koujav1.ExportVolumeRequest{VolumeId: "vol1", Protocol: koujav1.Protocol_PROTOCOL_UNSPECIFIED, Initiator: "iqn"}); err == nil {
		t.Fatal("invalid protocol did not fail")
	}
	admission := NewAdmission(1, 1, 1)
	admitted := NewVolumeService(store, &fakeLVM{}, runner, &fakeCredentialManager{}, ServiceOptions{Admission: admission})
	release, err := admitted.admit(ctx, auth.Identity{Owner: owner})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	release()
}
