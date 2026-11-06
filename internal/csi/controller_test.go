package csi

import (
	"context"
	"errors"
	"testing"

	csipb "github.com/container-storage-interface/spec/lib/go/csi"
	koujav1 "github.com/voidstarit/kouja/gen/kouja/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeVolumeClient struct {
	koujav1.VolumeServiceClient

	info         *koujav1.GetInfoResponse
	capacity     *koujav1.GetCapacityResponse
	volumes      []*koujav1.Volume
	createFn     func(*koujav1.CreateVolumeRequest) (*koujav1.CreateVolumeResponse, error)
	exportFn     func(*koujav1.ExportVolumeRequest) (*koujav1.ExportVolumeResponse, error)
	resizeFn     func(*koujav1.ResizeVolumeRequest) (*koujav1.ResizeVolumeResponse, error)
	unexportFn   func(*koujav1.UnexportVolumeRequest) (*koujav1.UnexportVolumeResponse, error)
	deleteFn     func(*koujav1.DeleteVolumeRequest) (*koujav1.DeleteVolumeResponse, error)
	credential   string
	infoErr      error
	createErr    error
	exportErr    error
	deleteErr    error
	listErr      error
	capacityErr  error
	resizeErr    error
	unexportErr  error
	lastCreate   *koujav1.CreateVolumeRequest
	lastExport   *koujav1.ExportVolumeRequest
	lastUnexport *koujav1.UnexportVolumeRequest
	lastDelete   *koujav1.DeleteVolumeRequest
}

func (client *fakeVolumeClient) GetInfo(context.Context, *koujav1.GetInfoRequest, ...grpc.CallOption) (*koujav1.GetInfoResponse, error) {
	if client.infoErr != nil {
		return nil, client.infoErr
	}
	if client.info != nil {
		return client.info, nil
	}
	return &koujav1.GetInfoResponse{
		HostUuid: "host-1", Ready: true,
		Protocols: []koujav1.Protocol{koujav1.Protocol_PROTOCOL_ISCSI, koujav1.Protocol_PROTOCOL_NVME_TCP},
	}, nil
}

func (client *fakeVolumeClient) CreateVolume(_ context.Context, request *koujav1.CreateVolumeRequest, _ ...grpc.CallOption) (*koujav1.CreateVolumeResponse, error) {
	if client.createErr != nil {
		return nil, client.createErr
	}
	client.lastCreate = request
	if client.createFn != nil {
		return client.createFn(request)
	}
	return &koujav1.CreateVolumeResponse{Volume: &koujav1.Volume{
		Owner: "spiffe://kouja/controller/test", VolumeId: request.GetVolumeId(),
		ResourceUuid: "resource-1", VgName: request.GetVg(),
		RequestedSizeBytes: request.GetSizeBytes(), ActualSizeBytes: request.GetSizeBytes(),
		State: koujav1.VolumeState_VOLUME_STATE_AVAILABLE,
	}}, nil
}

func (client *fakeVolumeClient) ExportVolume(_ context.Context, request *koujav1.ExportVolumeRequest, _ ...grpc.CallOption) (*koujav1.ExportVolumeResponse, error) {
	if client.exportErr != nil {
		return nil, client.exportErr
	}
	client.lastExport = request
	if client.exportFn != nil {
		return client.exportFn(request)
	}
	return &koujav1.ExportVolumeResponse{Export: &koujav1.Export{
		Protocol: request.GetProtocol(), TargetName: "target-1", Portal: "10.0.0.1:4420",
		Lun: 0, Nsid: 1, State: koujav1.ExportState_EXPORT_STATE_EXPORTED,
	}}, nil
}

func (client *fakeVolumeClient) UnexportVolume(_ context.Context, request *koujav1.UnexportVolumeRequest, _ ...grpc.CallOption) (*koujav1.UnexportVolumeResponse, error) {
	if client.unexportErr != nil {
		return nil, client.unexportErr
	}
	client.lastUnexport = request
	if client.unexportFn != nil {
		return client.unexportFn(request)
	}
	return &koujav1.UnexportVolumeResponse{}, nil
}

func (client *fakeVolumeClient) ResizeVolume(_ context.Context, request *koujav1.ResizeVolumeRequest, _ ...grpc.CallOption) (*koujav1.ResizeVolumeResponse, error) {
	if client.resizeErr != nil {
		return nil, client.resizeErr
	}
	if client.resizeFn != nil {
		return client.resizeFn(request)
	}
	return &koujav1.ResizeVolumeResponse{Volume: &koujav1.Volume{
		VolumeId: request.GetVolumeId(), ActualSizeBytes: request.GetSizeBytes(),
	}}, nil
}

func (client *fakeVolumeClient) DeleteVolume(_ context.Context, request *koujav1.DeleteVolumeRequest, _ ...grpc.CallOption) (*koujav1.DeleteVolumeResponse, error) {
	if client.deleteErr != nil {
		return nil, client.deleteErr
	}
	client.lastDelete = request
	if client.deleteFn != nil {
		return client.deleteFn(request)
	}
	return &koujav1.DeleteVolumeResponse{}, nil
}

func (client *fakeVolumeClient) ListVolumes(context.Context, *koujav1.ListVolumesRequest, ...grpc.CallOption) (*koujav1.ListVolumesResponse, error) {
	if client.listErr != nil {
		return nil, client.listErr
	}
	return &koujav1.ListVolumesResponse{Volumes: client.volumes}, nil
}

func (client *fakeVolumeClient) GetCredential(context.Context, *koujav1.GetCredentialRequest, ...grpc.CallOption) (*koujav1.GetCredentialResponse, error) {
	return &koujav1.GetCredentialResponse{Credential: client.credential}, nil
}

func (client *fakeVolumeClient) GetCapacity(context.Context, *koujav1.GetCapacityRequest, ...grpc.CallOption) (*koujav1.GetCapacityResponse, error) {
	if client.capacityErr != nil {
		return nil, client.capacityErr
	}
	if client.capacity != nil {
		return client.capacity, nil
	}
	return &koujav1.GetCapacityResponse{Vgs: []*koujav1.Capacity{{
		VgName: "vg1", VgState: "active", AllocatableBytes: 1 << 30, MaxVolumeBytes: 1 << 30,
	}}}, nil
}

type fakeAdminClient struct {
	koujav1.AdminServiceClient
	grants    int
	revokes   int
	grantErr  error
	lastGrant *koujav1.GrantCredentialRequest
}

func (client *fakeAdminClient) GrantCredential(_ context.Context, request *koujav1.GrantCredentialRequest, _ ...grpc.CallOption) (*koujav1.GrantCredentialResponse, error) {
	if client.grantErr != nil {
		return nil, client.grantErr
	}
	client.grants++
	client.lastGrant = request
	return &koujav1.GrantCredentialResponse{}, nil
}

func (client *fakeAdminClient) RevokeGrant(context.Context, *koujav1.RevokeGrantRequest, ...grpc.CallOption) (*koujav1.RevokeGrantResponse, error) {
	client.revokes++
	return &koujav1.RevokeGrantResponse{}, nil
}

type fakePolicyLookup struct {
	policy      string
	unavailable bool
	protocol    string
	caller      string
}

func (lookup fakePolicyLookup) NodeFailurePolicy(context.Context, string) (string, error) {
	return lookup.policy, nil
}

func (lookup fakePolicyLookup) NodeUnavailable(context.Context, string) (bool, error) {
	return lookup.unavailable, nil
}

func (lookup fakePolicyLookup) NodeCallerID(context.Context, string) (string, error) {
	return lookup.caller, nil
}

func (lookup fakePolicyLookup) VolumeProtocol(context.Context, string) (string, error) {
	return lookup.protocol, nil
}

type fakeAttachmentLookup struct {
	id  string
	err error
}

func (lookup fakeAttachmentLookup) AttachmentID(context.Context, string, string) (string, error) {
	return lookup.id, lookup.err
}

type fakePlacement struct {
	records map[string]PlacementRecord
	pending int
	binds   int
	fails   int
	deletes int
	getErr  error
}

func (placement *fakePlacement) Get(_ context.Context, key string) (PlacementRecord, error) {
	if placement.getErr != nil {
		return PlacementRecord{}, placement.getErr
	}
	record, ok := placement.records[key]
	if !ok {
		return PlacementRecord{}, ErrPlacementNotFound
	}
	return record, nil
}

func (placement *fakePlacement) List(context.Context) ([]PlacementRecord, error) {
	result := make([]PlacementRecord, 0, len(placement.records))
	for _, record := range placement.records {
		result = append(result, record)
	}
	return result, nil
}

func (placement *fakePlacement) Pending(_ context.Context, key string, host Host, volumeID string) error {
	placement.pending++
	placement.records[key] = PlacementRecord{Key: key, HostUUID: host.UUID, VolumeID: volumeID, Phase: "Pending"}
	return nil
}

func (placement *fakePlacement) Bind(_ context.Context, key, resourceUUID string) error {
	placement.binds++
	record := placement.records[key]
	record.ResourceUUID = resourceUUID
	record.Phase = "Bound"
	placement.records[key] = record
	return nil
}

func (placement *fakePlacement) Fail(_ context.Context, key string, _ error) error {
	placement.fails++
	record := placement.records[key]
	record.Phase = "Failed"
	placement.records[key] = record
	return nil
}

func (placement *fakePlacement) Delete(_ context.Context, key string) error {
	placement.deletes++
	delete(placement.records, key)
	return nil
}

func newTestController() (*Controller, *fakeVolumeClient, *fakeAdminClient, *fakePolicyLookup, *fakePlacement) {
	volume := &fakeVolumeClient{}
	admin := &fakeAdminClient{}
	policy := &fakePolicyLookup{policy: "wait", protocol: "iscsi", caller: "spiffe://kouja/node/node-1"}
	placement := &fakePlacement{records: map[string]PlacementRecord{}}
	host := &hostClient{
		endpoint: "agent-1:9443", uuid: "host-1", ready: true,
		volume: volume, admin: admin,
	}
	controller := &Controller{
		hosts: []*hostClient{host}, attachments: fakeAttachmentLookup{id: "attachment-1"},
		policies: policy, placement: placement,
	}
	return controller, volume, admin, policy, placement
}

func bddTestControllerCreateDeleteAndPlacement(t testing.TB) {
	controller, volume, _, _, placement := newTestController()
	request := &csipb.CreateVolumeRequest{
		Name:          "vol-1",
		CapacityRange: &csipb.CapacityRange{RequiredBytes: 1 << 20},
		Parameters: map[string]string{
			"vg": "vg1", "protocol": "iscsi", "initiator": "iqn.test",
			"forceReattach": "true", "nodeFailurePolicy": "force",
		},
	}
	response, err := controller.CreateVolume(context.Background(), request)
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	if response.GetVolume().GetVolumeId() != "v1:host-1:resource-1" {
		t.Fatalf("volume handle = %q", response.GetVolume().GetVolumeId())
	}
	if placement.pending != 1 || placement.binds != 1 || placement.records["vol-1"].Phase != "Bound" {
		t.Fatalf("placement = %#v", placement)
	}
	if volume.lastCreate.GetVolumeId() != "vol-1" || volume.lastCreate.GetProtocol() != koujav1.Protocol_PROTOCOL_ISCSI {
		t.Fatalf("create request = %#v", volume.lastCreate)
	}
	volume.volumes = []*koujav1.Volume{{VolumeId: "vol-1", ResourceUuid: "resource-1", Owner: "owner", ActualSizeBytes: 1 << 20}}
	if _, err := controller.DeleteVolume(context.Background(), &csipb.DeleteVolumeRequest{VolumeId: "v1:host-1:resource-1"}); err != nil {
		t.Fatalf("DeleteVolume: %v", err)
	}
	if volume.lastDelete.GetVolumeId() != "vol-1" || placement.deletes != 1 {
		t.Fatalf("delete = %#v placement=%#v", volume.lastDelete, placement)
	}
}

func bddTestControllerPublishGrantsAndUnpublishRevokes(t testing.TB) {
	controller, volume, admin, policy, _ := newTestController()
	volume.volumes = []*koujav1.Volume{{
		Owner: "spiffe://kouja/controller/test", VolumeId: "vol-1", ResourceUuid: "resource-1",
	}}
	response, err := controller.ControllerPublishVolume(context.Background(), &csipb.ControllerPublishVolumeRequest{
		VolumeId: "v1:host-1:resource-1", NodeId: "node-1",
		VolumeContext: map[string]string{"protocol": "nvme-tcp", "initiator": "nqn.test"},
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if response.GetPublishContext()["credential"] != "" {
		t.Fatal("publish context leaks a credential")
	}
	if admin.grants != 1 || admin.lastGrant.GetNodeCallerId() != "spiffe://kouja/node/node-1" {
		t.Fatalf("grant = %#v", admin)
	}
	if response.GetPublishContext()["endpoint"] != "agent-1:9443" {
		t.Fatalf("publish context = %#v", response.GetPublishContext())
	}
	policy.protocol = "nvme-tcp"
	policy.policy = "wait"
	policy.unavailable = false
	if _, err := controller.ControllerUnpublishVolume(context.Background(), &csipb.ControllerUnpublishVolumeRequest{
		VolumeId: "v1:host-1:resource-1", NodeId: "node-1",
	}); err != nil {
		t.Fatalf("unpublish: %v", err)
	}
	if volume.lastUnexport.GetForce() != true {
		t.Fatal("ready NVMe unpublish should force")
	}
	if admin.revokes != 1 {
		t.Fatalf("revokes = %d", admin.revokes)
	}
}

func bddTestControllerExpandStatusCapabilitiesAndIdentity(t testing.TB) {
	controller, volume, _, _, _ := newTestController()
	volume.volumes = []*koujav1.Volume{{VolumeId: "vol-1", ResourceUuid: "resource-1", ActualSizeBytes: 2 << 20}}
	expanded, err := controller.ControllerExpandVolume(context.Background(), &csipb.ControllerExpandVolumeRequest{
		VolumeId: "v1:host-1:resource-1", CapacityRange: &csipb.CapacityRange{RequiredBytes: 2 << 20},
	})
	if err != nil || !expanded.GetNodeExpansionRequired() || expanded.GetCapacityBytes() != 2<<20 {
		t.Fatalf("expand = %#v, %v", expanded, err)
	}
	capacity, err := controller.GetCapacity(context.Background(), &csipb.GetCapacityRequest{Parameters: map[string]string{"vg": "vg1"}})
	if err != nil || capacity.GetAvailableCapacity() != 1<<30 {
		t.Fatalf("capacity = %#v, %v", capacity, err)
	}
	list, err := controller.ListVolumes(context.Background(), &csipb.ListVolumesRequest{})
	if err != nil || len(list.GetEntries()) != 1 {
		t.Fatalf("list = %#v, %v", list, err)
	}
	statusResponse, err := controller.ControllerGetVolume(context.Background(), &csipb.ControllerGetVolumeRequest{VolumeId: "v1:host-1:resource-1"})
	if err != nil || statusResponse.GetVolume().GetCapacityBytes() != 2<<20 {
		t.Fatalf("status = %#v, %v", statusResponse, err)
	}
	capabilities, _ := controller.ControllerGetCapabilities(context.Background(), &csipb.ControllerGetCapabilitiesRequest{})
	if len(capabilities.GetCapabilities()) != 4 {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	validated, err := controller.ValidateVolumeCapabilities(context.Background(), &csipb.ValidateVolumeCapabilitiesRequest{
		VolumeId: "v1:host-1:resource-1",
		VolumeCapabilities: []*csipb.VolumeCapability{{
			AccessMode: &csipb.VolumeCapability_AccessMode{Mode: csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
			AccessType: &csipb.VolumeCapability_Mount{Mount: &csipb.VolumeCapability_MountVolume{}},
		}},
	})
	if err != nil || validated.GetConfirmed() == nil {
		t.Fatalf("validate = %#v, %v", validated, err)
	}
	identity := &Identity{Name: DriverName, Version: "test", Controller: true, Ready: func() bool { return true }}
	info, _ := identity.GetPluginInfo(context.Background(), &csipb.GetPluginInfoRequest{})
	if info.GetName() != DriverName {
		t.Fatalf("identity = %#v", info)
	}
	pluginCaps, _ := identity.GetPluginCapabilities(context.Background(), &csipb.GetPluginCapabilitiesRequest{})
	if len(pluginCaps.GetCapabilities()) != 1 {
		t.Fatalf("plugin caps = %#v", pluginCaps)
	}
	probe, _ := identity.Probe(context.Background(), &csipb.ProbeRequest{})
	if probe.GetReady().GetValue() != true {
		t.Fatalf("probe = %#v", probe)
	}
}

func bddTestControllerErrorsAndDuplicatePlacement(t testing.TB) {
	controller, volume, _, _, placement := newTestController()
	if _, err := controller.CreateVolume(context.Background(), &csipb.CreateVolumeRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty create = %v", err)
	}
	placement.records["vol-1"] = PlacementRecord{Key: "vol-1", HostUUID: "host-1", VolumeID: "vol-1", Phase: "Bound", ResourceUUID: "resource-1"}
	volume.volumes = []*koujav1.Volume{{VolumeId: "vol-1", ResourceUuid: "resource-1"}}
	if _, err := controller.CreateVolume(context.Background(), &csipb.CreateVolumeRequest{
		Name: "vol-1", CapacityRange: &csipb.CapacityRange{RequiredBytes: 1 << 20}, Parameters: map[string]string{"vg": "vg1"},
	}); err != nil {
		t.Fatalf("idempotent create: %v", err)
	}
	if placement.pending != 0 || volume.lastCreate.GetVolumeId() != "vol-1" {
		t.Fatalf("recorded create = %#v", volume.lastCreate)
	}
	volume.deleteFn = func(*koujav1.DeleteVolumeRequest) (*koujav1.DeleteVolumeResponse, error) {
		return nil, status.Error(codes.NotFound, "gone")
	}
	if _, err := controller.DeleteVolume(context.Background(), &csipb.DeleteVolumeRequest{VolumeId: "v1:host-1:missing"}); err != nil {
		t.Fatalf("delete missing: %v", err)
	}
	if !isNVMeProtocol("NVME_TCP") || isNVMeProtocol("iscsi") {
		t.Fatal("protocol classifier failed")
	}
	if _, err := controller.CreateVolume(context.Background(), &csipb.CreateVolumeRequest{
		Name: "vol-2", CapacityRange: &csipb.CapacityRange{RequiredBytes: 1}, Parameters: map[string]string{},
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing vg = %v", err)
	}
}

func bddTestControllerErrorPaths(t testing.TB) {
	controller, volume, admin, policy, placement := newTestController()
	ctx := context.Background()
	validCreate := &csipb.CreateVolumeRequest{
		Name: "vol-err", CapacityRange: &csipb.CapacityRange{RequiredBytes: 1 << 20},
		Parameters: map[string]string{"vg": "vg1", "protocol": "iscsi"},
	}
	volume.infoErr = errors.New("agent down")
	if _, err := controller.CreateVolume(ctx, validCreate); status.Code(err) != codes.Unavailable {
		t.Fatalf("host failure = %v", err)
	}
	volume.infoErr = nil
	placement.getErr = errors.New("API down")
	if _, err := controller.CreateVolume(ctx, validCreate); status.Code(err) != codes.Unavailable {
		t.Fatalf("placement failure = %v", err)
	}
	placement.getErr = nil
	volume.createErr = errors.New("create failed")
	if _, err := controller.CreateVolume(ctx, validCreate); err == nil || placement.fails != 1 {
		t.Fatalf("create failure = %v, placement=%#v", err, placement)
	}
	volume.createErr = nil
	volume.createFn = func(*koujav1.CreateVolumeRequest) (*koujav1.CreateVolumeResponse, error) {
		return &koujav1.CreateVolumeResponse{}, nil
	}
	if _, err := controller.CreateVolume(ctx, validCreate); status.Code(err) != codes.Internal {
		t.Fatalf("empty create response = %v", err)
	}
	volume.createFn = nil
	if _, err := controller.DeleteVolume(ctx, &csipb.DeleteVolumeRequest{VolumeId: "bad"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad delete handle = %v", err)
	}
	if _, err := controller.DeleteVolume(ctx, &csipb.DeleteVolumeRequest{VolumeId: "v1:missing:resource"}); status.Code(err) != codes.NotFound {
		t.Fatalf("missing delete host = %v", err)
	}
	volume.volumes = []*koujav1.Volume{{VolumeId: "vol-1", ResourceUuid: "resource-1"}}
	volume.deleteErr = errors.New("delete failed")
	if _, err := controller.DeleteVolume(ctx, &csipb.DeleteVolumeRequest{VolumeId: "v1:host-1:resource-1"}); err == nil {
		t.Fatal("delete failure did not fail")
	}
	volume.deleteErr = nil
	volume.listErr = errors.New("list failed")
	if _, err := controller.DeleteVolume(ctx, &csipb.DeleteVolumeRequest{VolumeId: "v1:host-1:resource-1"}); err == nil {
		t.Fatal("list failure did not fail")
	}
	volume.listErr = nil

	if _, err := controller.ControllerPublishVolume(ctx, &csipb.ControllerPublishVolumeRequest{VolumeId: "bad"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad publish handle = %v", err)
	}
	if _, err := controller.ControllerPublishVolume(ctx, &csipb.ControllerPublishVolumeRequest{VolumeId: "v1:host-1:resource-1"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing initiator = %v", err)
	}
	volume.exportErr = errors.New("export failed")
	_, err := controller.ControllerPublishVolume(ctx, &csipb.ControllerPublishVolumeRequest{
		VolumeId: "v1:host-1:resource-1", NodeId: "node-1",
		VolumeContext: map[string]string{"protocol": "iscsi", "initiator": "iqn.test"},
	})
	if err == nil {
		t.Fatal("export failure did not fail")
	}
	volume.exportErr = nil
	admin.grantErr = errors.New("grant failed")
	_, err = controller.ControllerPublishVolume(ctx, &csipb.ControllerPublishVolumeRequest{
		VolumeId: "v1:host-1:resource-1", NodeId: "node-1",
		VolumeContext: map[string]string{"protocol": "iscsi", "initiator": "iqn.test"},
	})
	if err == nil {
		t.Fatal("grant failure did not fail")
	}
	admin.grantErr = nil
	policy.caller = "spiffe://kouja/node/node-1"
	controller.attachments = fakeAttachmentLookup{err: errors.New("attachment lookup failed")}
	if _, err := controller.ControllerPublishVolume(ctx, &csipb.ControllerPublishVolumeRequest{
		VolumeId: "v1:host-1:resource-1", NodeId: "node-1",
		VolumeContext: map[string]string{"protocol": "iscsi", "initiator": "iqn.test"},
	}); status.Code(err) != codes.Unavailable {
		t.Fatalf("attachment lookup = %v", err)
	}
	controller.attachments = fakeAttachmentLookup{id: "attachment-1"}
	controller.policies = nil
	volume.credential = "fallback"
	response, err := controller.ControllerPublishVolume(ctx, &csipb.ControllerPublishVolumeRequest{
		VolumeId: "v1:host-1:resource-1", NodeId: "node-1",
		VolumeContext: map[string]string{"protocol": "iscsi", "initiator": "iqn.test"},
	})
	if err != nil || response.GetPublishContext()["credential"] != "fallback" {
		t.Fatalf("fallback publish = %#v, %v", response, err)
	}
	controller.policies = policy

	if _, err := controller.ControllerUnpublishVolume(ctx, &csipb.ControllerUnpublishVolumeRequest{VolumeId: "bad"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad unpublish handle = %v", err)
	}
	volume.unexportErr = errors.New("unexport failed")
	if _, err := controller.ControllerUnpublishVolume(ctx, &csipb.ControllerUnpublishVolumeRequest{VolumeId: "v1:host-1:resource-1"}); err == nil {
		t.Fatal("unexport failure did not fail")
	}
	volume.unexportErr = nil
	policy.unavailable = true
	policy.policy = "force"
	admin.revokes = 0
	if _, err := controller.ControllerUnpublishVolume(ctx, &csipb.ControllerUnpublishVolumeRequest{VolumeId: "v1:host-1:resource-1", NodeId: "node-1"}); err != nil {
		t.Fatalf("force unpublish: %v", err)
	}
	if !volume.lastUnexport.GetForce() {
		t.Fatal("force policy did not force")
	}

	if _, err := controller.ControllerExpandVolume(ctx, &csipb.ControllerExpandVolumeRequest{VolumeId: "bad"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("bad expand handle = %v", err)
	}
	if _, err := controller.ControllerExpandVolume(ctx, &csipb.ControllerExpandVolumeRequest{
		VolumeId: "v1:host-1:resource-1", CapacityRange: &csipb.CapacityRange{},
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty expand size = %v", err)
	}
	volume.resizeErr = errors.New("resize failed")
	if _, err := controller.ControllerExpandVolume(ctx, &csipb.ControllerExpandVolumeRequest{
		VolumeId: "v1:host-1:resource-1", CapacityRange: &csipb.CapacityRange{RequiredBytes: 1},
	}); err == nil {
		t.Fatal("resize failure did not fail")
	}
	volume.resizeErr = nil
	volume.listErr = errors.New("list failed")
	if _, err := controller.ListVolumes(ctx, &csipb.ListVolumesRequest{}); err == nil {
		t.Fatal("list failure did not fail")
	}
	volume.listErr = nil
	volume.capacityErr = errors.New("capacity failed")
	if capacity, err := controller.GetCapacity(ctx, &csipb.GetCapacityRequest{}); err != nil || capacity.GetAvailableCapacity() != 0 {
		t.Fatalf("capacity failure = %#v, %v", capacity, err)
	}
	volume.capacityErr = nil
	volume.volumes = []*koujav1.Volume{{VolumeId: "vol-1", ResourceUuid: "resource-1", State: koujav1.VolumeState_VOLUME_STATE_DRIFT}}
	if _, err := controller.ControllerGetVolume(ctx, &csipb.ControllerGetVolumeRequest{VolumeId: "v1:host-1:resource-1"}); err != nil {
		t.Fatalf("drift volume: %v", err)
	}
	if _, err := controller.ControllerGetVolume(ctx, &csipb.ControllerGetVolumeRequest{VolumeId: "v1:host-1:missing"}); err == nil {
		t.Fatal("missing volume did not fail")
	}
}
