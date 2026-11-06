package csi

import (
	"context"
	"crypto/tls"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	csipb "github.com/container-storage-interface/spec/lib/go/csi"
	koujav1 "github.com/voidstarit/kouja/gen/kouja/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	DriverName = "kouja.csi.voidstarit.com"
	Version    = "dev"
)

// Controller implements the CSI controller service by translating calls into
// kouja volume operations. Placement is deliberately done here, outside the
// storage host; a production deployment can replace the static host list with
// an SRV resolver without changing the wire contract.
type Controller struct {
	csipb.UnimplementedControllerServer

	mu          sync.RWMutex
	hosts       []*hostClient
	attachments AttachmentLookup
	policies    PolicyLookup
	placement   Placement
}

func NewController(endpoints []string, tlsConfig *tls.Config) (*Controller, error) {
	if len(endpoints) == 0 {
		return nil, errors.New("csi: at least one agent endpoint is required")
	}
	controller := &Controller{}
	for _, endpoint := range endpoints {
		host, err := dialHost(strings.TrimSpace(endpoint), tlsConfig)
		if err != nil {
			controller.Close()
			return nil, err
		}
		controller.hosts = append(controller.hosts, host)
	}
	return controller, nil
}

func (controller *Controller) SetAttachmentLookup(lookup AttachmentLookup) {
	controller.attachments = lookup
}

func (controller *Controller) SetPolicyLookup(lookup PolicyLookup) {
	controller.policies = lookup
}

func (controller *Controller) SetPlacement(placement Placement) {
	controller.placement = placement
}

func (controller *Controller) Close() {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	for _, host := range controller.hosts {
		_ = host.close()
	}
	controller.hosts = nil
}

func (controller *Controller) Ready() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	controller.mu.RLock()
	hosts := append([]*hostClient(nil), controller.hosts...)
	controller.mu.RUnlock()
	for _, host := range hosts {
		if err := host.refresh(ctx); err == nil && host.ready {
			return true
		}
	}
	return false
}

func (controller *Controller) CreateVolume(ctx context.Context, request *csipb.CreateVolumeRequest) (*csipb.CreateVolumeResponse, error) {
	if request.GetName() == "" || request.GetCapacityRange().GetRequiredBytes() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "name and positive required_bytes are required")
	}
	vg := request.GetParameters()["vg"]
	if vg == "" {
		return nil, status.Error(codes.InvalidArgument, "storage class parameter vg is required")
	}
	protocol := request.GetParameters()["protocol"]
	if protocol == "" {
		protocol = "iscsi"
	}
	size := request.GetCapacityRange().GetRequiredBytes()
	placementKey := request.GetName()
	var (
		host     *hostClient
		record   PlacementRecord
		recorded bool
		err      error
	)
	if controller.placement != nil {
		value, placementErr := controller.placement.Get(ctx, placementKey)
		switch {
		case placementErr == nil:
			record = value
			recorded = true
			host, err = controller.hostForUUID(ctx, record.HostUUID)
			if err != nil {
				return nil, status.Errorf(codes.Unavailable, "placement host %s is unavailable: %v", record.HostUUID, err)
			}
		case errors.Is(placementErr, ErrPlacementNotFound):
		default:
			return nil, status.Errorf(codes.Unavailable, "read volume placement: %v", placementErr)
		}
	}
	if host == nil {
		host, err = controller.selectHost(ctx, vg, protocol, size)
		if err != nil {
			return nil, err
		}
	}
	volumeID := request.GetName()
	if recorded && record.VolumeID != "" {
		volumeID = record.VolumeID
	}
	if controller.placement != nil && !recorded {
		if err := controller.placement.Pending(ctx, placementKey, Host{UUID: host.uuid, Endpoint: host.endpoint}, volumeID); err != nil {
			return nil, status.Errorf(codes.Internal, "record volume placement: %v", err)
		}
		recorded = true
	}
	export := false
	response, err := host.volume.CreateVolume(ctx, &koujav1.CreateVolumeRequest{
		VolumeId: volumeID, SizeBytes: size, Vg: vg,
		Export: &export, Protocol: protocolValue(protocol), RequestId: volumeID,
	})
	if err != nil {
		if controller.placement != nil && recorded {
			_ = controller.placement.Fail(ctx, placementKey, err)
		}
		return nil, err
	}
	if response.GetVolume() == nil {
		return nil, status.Error(codes.Internal, "agent returned no volume")
	}
	volume := response.GetVolume()
	if controller.placement != nil && recorded {
		if err := controller.placement.Bind(ctx, placementKey, volume.GetResourceUuid()); err != nil {
			return nil, status.Errorf(codes.Internal, "bind volume placement: %v", err)
		}
	}
	return controller.createResponse(request, host, volume, protocol), nil
}

func (controller *Controller) createResponse(request *csipb.CreateVolumeRequest, host *hostClient, volume *koujav1.Volume, protocol string) *csipb.CreateVolumeResponse {
	handle := FormatVolumeHandle(host.uuid, volume.GetResourceUuid())
	fsType := request.GetParameters()["fsType"]
	if fsType == "" {
		fsType = request.GetParameters()["csi.storage.k8s.io/fstype"]
	}
	volumeContext := map[string]string{
		"endpoint": host.endpoint, "host_uuid": host.uuid, "volume_id": volume.GetVolumeId(),
		"vg": volume.GetVgName(), "protocol": protocol, "fsType": fsType,
	}
	if initiator := request.GetParameters()["initiator"]; initiator != "" {
		volumeContext["initiator"] = initiator
	}
	forceReattach := strings.EqualFold(request.GetParameters()["forceReattach"], "true")
	volumeContext["forceReattach"] = strconv.FormatBool(forceReattach)
	nodeFailurePolicy := strings.ToLower(strings.TrimSpace(request.GetParameters()["nodeFailurePolicy"]))
	if nodeFailurePolicy == "" {
		nodeFailurePolicy = "wait"
	}
	volumeContext["nodeFailurePolicy"] = nodeFailurePolicy
	return &csipb.CreateVolumeResponse{Volume: &csipb.Volume{
		VolumeId:      handle,
		CapacityBytes: volume.GetActualSizeBytes(),
		VolumeContext: volumeContext,
	}}
}

func (controller *Controller) DeleteVolume(ctx context.Context, request *csipb.DeleteVolumeRequest) (*csipb.DeleteVolumeResponse, error) {
	handle, err := ParseVolumeHandle(request.GetVolumeId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	host, err := controller.hostForUUID(ctx, handle.HostUUID)
	if err != nil {
		return nil, err
	}
	volume, err := controller.volumeByResource(ctx, host, handle.ResourceUUID)
	if errors.Is(err, errVolumeNotFound) {
		controller.deletePlacement(ctx, handle.ResourceUUID)
		return &csipb.DeleteVolumeResponse{}, nil
	}
	if err != nil {
		return nil, err
	}
	force := false
	if controller.policies != nil {
		if protocol, protocolErr := controller.policies.VolumeProtocol(ctx, request.GetVolumeId()); protocolErr == nil {
			// nvmet has no session observation on the supported kernels, so
			// DeleteVolume must be able to finish after a successful
			// ControllerUnpublish. CSI invokes DeleteVolume only after the
			// volume is detached; iSCSI keeps the conservative default.
			force = isNVMeProtocol(protocol)
		}
	}
	_, err = host.volume.DeleteVolume(ctx, &koujav1.DeleteVolumeRequest{VolumeId: volume.GetVolumeId(), Force: force})
	if status.Code(err) == codes.NotFound {
		controller.deletePlacement(ctx, handle.ResourceUUID)
		return &csipb.DeleteVolumeResponse{}, nil
	}
	if err != nil {
		return nil, err
	}
	controller.deletePlacement(ctx, handle.ResourceUUID)
	return &csipb.DeleteVolumeResponse{}, nil
}

func (controller *Controller) deletePlacement(ctx context.Context, resourceUUID string) {
	if controller.placement == nil {
		return
	}
	records, err := controller.placement.List(ctx)
	if err != nil {
		return
	}
	for _, record := range records {
		if record.ResourceUUID == resourceUUID {
			_ = controller.placement.Delete(ctx, record.Key)
			return
		}
	}
}

func (controller *Controller) ControllerPublishVolume(ctx context.Context, request *csipb.ControllerPublishVolumeRequest) (*csipb.ControllerPublishVolumeResponse, error) {
	handle, err := ParseVolumeHandle(request.GetVolumeId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	initiator := request.GetVolumeContext()["initiator"]
	if initiator == "" {
		return nil, status.Error(codes.InvalidArgument, "volume context parameter initiator is required")
	}
	host, err := controller.hostForUUID(ctx, handle.HostUUID)
	if err != nil {
		return nil, err
	}
	volume, err := controller.volumeByResource(ctx, host, handle.ResourceUUID)
	if err != nil {
		if errors.Is(err, errVolumeNotFound) {
			return nil, status.Error(codes.NotFound, "volume not found")
		}
		return nil, err
	}
	protocol := request.GetVolumeContext()["protocol"]
	if protocol == "" {
		protocol = "iscsi"
	}
	forceReattach := strings.EqualFold(request.GetVolumeContext()["forceReattach"], "true") ||
		strings.EqualFold(request.GetVolumeContext()["nodeFailurePolicy"], "force")
	attachmentID := request.GetNodeId() + "|" + initiator
	if controller.attachments != nil && request.GetNodeId() != "" {
		resolved, resolveErr := controller.attachments.AttachmentID(ctx, request.GetVolumeId(), request.GetNodeId())
		if resolveErr != nil {
			return nil, status.Errorf(codes.Unavailable, "resolve volume attachment: %v", resolveErr)
		}
		attachmentID = resolved + "|" + initiator
	}
	export, err := host.volume.ExportVolume(ctx, &koujav1.ExportVolumeRequest{
		VolumeId: volume.GetVolumeId(), Protocol: protocolValue(protocol), Initiator: initiator,
		RotateCredential: true, Force: forceReattach, AttachmentId: attachmentID,
	})
	if err != nil {
		return nil, err
	}
	publishContext := map[string]string{
		"target":    export.GetExport().GetTargetName(),
		"portal":    export.GetExport().GetPortal(),
		"lun":       strconv.FormatInt(int64(export.GetExport().GetLun()), 10),
		"nsid":      strconv.FormatInt(int64(export.GetExport().GetNsid()), 10),
		"protocol":  protocol,
		"initiator": initiator,
		"endpoint":  host.endpoint,
		"volume_id": volume.GetVolumeId(),
		"owner":     volume.GetOwner(),
	}
	granted := false
	if controller.policies != nil && request.GetNodeId() != "" && host.admin != nil {
		nodeCallerID, callerErr := controller.policies.NodeCallerID(ctx, request.GetNodeId())
		if callerErr != nil {
			return nil, status.Errorf(codes.Unavailable, "resolve node identity: %v", callerErr)
		}
		if _, grantErr := host.admin.GrantCredential(ctx, &koujav1.GrantCredentialRequest{
			VolumeId: volume.GetVolumeId(), NodeId: request.GetNodeId(), NodeCallerId: nodeCallerID,
			Owner: volume.GetOwner(), RequestId: attachmentID,
			ExpiresAt: timestamppb.New(time.Now().UTC().Add(24 * time.Hour)),
		}); grantErr != nil {
			return nil, grantErr
		}
		granted = true
	}
	if !granted {
		// Standalone deployments without the Kubernetes policy lookup keep
		// the legacy publish-context credential.
		credential, credentialErr := host.volume.GetCredential(ctx, &koujav1.GetCredentialRequest{VolumeId: volume.GetVolumeId()})
		if credentialErr != nil {
			return nil, credentialErr
		}
		publishContext["credential"] = credential.GetCredential()
	}
	if strings.EqualFold(protocol, "iscsi") {
		publishContext["username"] = "kouja-" + volume.GetResourceUuid()
	}
	return &csipb.ControllerPublishVolumeResponse{PublishContext: publishContext}, nil
}

func (controller *Controller) ControllerUnpublishVolume(ctx context.Context, request *csipb.ControllerUnpublishVolumeRequest) (*csipb.ControllerUnpublishVolumeResponse, error) {
	handle, err := ParseVolumeHandle(request.GetVolumeId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	host, err := controller.hostForUUID(ctx, handle.HostUUID)
	if err != nil {
		return nil, err
	}
	volume, err := controller.volumeByResource(ctx, host, handle.ResourceUUID)
	if errors.Is(err, errVolumeNotFound) {
		return &csipb.ControllerUnpublishVolumeResponse{}, nil
	}
	if err != nil {
		return nil, err
	}
	force := false
	if controller.policies != nil && request.GetNodeId() != "" {
		policy, policyErr := controller.policies.NodeFailurePolicy(ctx, request.GetVolumeId())
		unavailable, unavailableErr := controller.policies.NodeUnavailable(ctx, request.GetNodeId())
		protocol, protocolErr := controller.policies.VolumeProtocol(ctx, request.GetVolumeId())
		if unavailableErr == nil && unavailable && policyErr == nil && strings.EqualFold(policy, "force") {
			// A failed node is only fenced when the StorageClass explicitly
			// opts into forced failover.
			force = true
		} else if unavailableErr == nil && !unavailable && protocolErr == nil && isNVMeProtocol(protocol) {
			// nvmet cannot report session state on the supported kernels.
			// Once the node is reachable, the node plugin has completed its
			// unmount/disconnect, so the controller can tear down the target
			// without waiting for an unobservable session count.
			force = true
		}
	}
	_, err = host.volume.UnexportVolume(ctx, &koujav1.UnexportVolumeRequest{VolumeId: volume.GetVolumeId(), Force: force})
	if status.Code(err) == codes.NotFound {
		controller.revokeGrant(ctx, host, volume, request.GetNodeId())
		return &csipb.ControllerUnpublishVolumeResponse{}, nil
	}
	if err != nil {
		return nil, err
	}
	controller.revokeGrant(ctx, host, volume, request.GetNodeId())
	return &csipb.ControllerUnpublishVolumeResponse{}, nil
}

func (controller *Controller) revokeGrant(ctx context.Context, host *hostClient, volume *koujav1.Volume, nodeID string) {
	if controller.policies == nil || host.admin == nil || nodeID == "" {
		return
	}
	_, _ = host.admin.RevokeGrant(ctx, &koujav1.RevokeGrantRequest{
		VolumeId: volume.GetVolumeId(), NodeId: nodeID, Owner: volume.GetOwner(),
	})
}

func isNVMeProtocol(protocol string) bool {
	switch strings.ToLower(protocol) {
	case "nvme", "nvme-tcp", "nvme_tcp":
		return true
	default:
		return false
	}
}

func (controller *Controller) ValidateVolumeCapabilities(_ context.Context, request *csipb.ValidateVolumeCapabilitiesRequest) (*csipb.ValidateVolumeCapabilitiesResponse, error) {
	if request.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume_id is required")
	}
	for _, capability := range request.GetVolumeCapabilities() {
		if capability.GetMount() == nil && capability.GetBlock() == nil {
			continue
		}
		mode := capability.GetAccessMode().GetMode()
		if mode == csipb.VolumeCapability_AccessMode_SINGLE_NODE_WRITER || mode == csipb.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER {
			return &csipb.ValidateVolumeCapabilitiesResponse{Confirmed: &csipb.ValidateVolumeCapabilitiesResponse_Confirmed{
				VolumeCapabilities: request.GetVolumeCapabilities(),
			}}, nil
		}
	}
	return &csipb.ValidateVolumeCapabilitiesResponse{Message: "only single-node write access is supported"}, nil
}

func (controller *Controller) ListVolumes(ctx context.Context, request *csipb.ListVolumesRequest) (*csipb.ListVolumesResponse, error) {
	limit := int(request.GetMaxEntries())
	if limit <= 0 {
		limit = 1000
	}
	start := int64(0)
	if request.GetStartingToken() != "" {
		parsed, err := strconv.ParseInt(request.GetStartingToken(), 10, 64)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid starting_token")
		}
		start = parsed
	}
	var entries []*csipb.ListVolumesResponse_Entry
	var index int64
	controller.mu.RLock()
	hosts := append([]*hostClient(nil), controller.hosts...)
	controller.mu.RUnlock()
	for _, host := range hosts {
		values, err := listHostVolumes(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, volume := range values {
			if index < start {
				index++
				continue
			}
			if len(entries) >= limit {
				return &csipb.ListVolumesResponse{Entries: entries, NextToken: strconv.FormatInt(index, 10)}, nil
			}
			entries = append(entries, &csipb.ListVolumesResponse_Entry{Volume: &csipb.Volume{
				VolumeId:      FormatVolumeHandle(host.uuid, volume.GetResourceUuid()),
				CapacityBytes: volume.GetActualSizeBytes(),
			}})
			index++
		}
	}
	return &csipb.ListVolumesResponse{Entries: entries}, nil
}

func (controller *Controller) GetCapacity(ctx context.Context, request *csipb.GetCapacityRequest) (*csipb.GetCapacityResponse, error) {
	var available, maximum int64
	requestedVG := request.GetParameters()["vg"]
	controller.mu.RLock()
	hosts := append([]*hostClient(nil), controller.hosts...)
	controller.mu.RUnlock()
	for _, host := range hosts {
		response, err := host.volume.GetCapacity(ctx, &koujav1.GetCapacityRequest{})
		if err != nil {
			continue
		}
		for _, capacity := range response.GetVgs() {
			if requestedVG != "" && capacity.GetVgName() != requestedVG {
				continue
			}
			available += capacity.GetAllocatableBytes()
			if capacity.GetMaxVolumeBytes() > maximum {
				maximum = capacity.GetMaxVolumeBytes()
			}
		}
	}
	return &csipb.GetCapacityResponse{AvailableCapacity: available, MaximumVolumeSize: wrapperspb.Int64(maximum)}, nil
}

func (controller *Controller) ControllerGetCapabilities(context.Context, *csipb.ControllerGetCapabilitiesRequest) (*csipb.ControllerGetCapabilitiesResponse, error) {
	capabilities := []csipb.ControllerServiceCapability_RPC_Type{
		csipb.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		csipb.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
		csipb.ControllerServiceCapability_RPC_EXPAND_VOLUME,
		csipb.ControllerServiceCapability_RPC_GET_CAPACITY,
	}
	response := &csipb.ControllerGetCapabilitiesResponse{}
	for _, capability := range capabilities {
		response.Capabilities = append(response.Capabilities, &csipb.ControllerServiceCapability{
			Type: &csipb.ControllerServiceCapability_Rpc{Rpc: &csipb.ControllerServiceCapability_RPC{Type: capability}},
		})
	}
	return response, nil
}

func (controller *Controller) ControllerExpandVolume(ctx context.Context, request *csipb.ControllerExpandVolumeRequest) (*csipb.ControllerExpandVolumeResponse, error) {
	handle, err := ParseVolumeHandle(request.GetVolumeId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if request.GetCapacityRange().GetRequiredBytes() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "positive required_bytes is required")
	}
	host, err := controller.hostForUUID(ctx, handle.HostUUID)
	if err != nil {
		return nil, err
	}
	volume, err := controller.volumeByResource(ctx, host, handle.ResourceUUID)
	if err != nil {
		return nil, err
	}
	response, err := host.volume.ResizeVolume(ctx, &koujav1.ResizeVolumeRequest{
		VolumeId: volume.GetVolumeId(), SizeBytes: request.GetCapacityRange().GetRequiredBytes(),
	})
	if err != nil {
		return nil, err
	}
	return &csipb.ControllerExpandVolumeResponse{
		CapacityBytes: response.GetVolume().GetActualSizeBytes(), NodeExpansionRequired: true,
	}, nil
}

func (controller *Controller) ControllerGetVolume(ctx context.Context, request *csipb.ControllerGetVolumeRequest) (*csipb.ControllerGetVolumeResponse, error) {
	handle, err := ParseVolumeHandle(request.GetVolumeId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	host, err := controller.hostForUUID(ctx, handle.HostUUID)
	if err != nil {
		return nil, err
	}
	volume, err := controller.volumeByResource(ctx, host, handle.ResourceUUID)
	if err != nil {
		return nil, err
	}
	condition := &csipb.ControllerGetVolumeResponse_VolumeStatus{}
	if volume.GetState() == koujav1.VolumeState_VOLUME_STATE_DRIFT || len(volume.GetDriftReasons()) > 0 {
		condition.VolumeCondition = &csipb.VolumeCondition{Abnormal: true, Message: "storage host reports drift"}
	}
	return &csipb.ControllerGetVolumeResponse{Volume: &csipb.Volume{
		VolumeId: request.GetVolumeId(), CapacityBytes: volume.GetActualSizeBytes(),
	}, Status: condition}, nil
}

func (controller *Controller) selectHost(ctx context.Context, vg, protocol string, size int64) (*hostClient, error) {
	controller.mu.RLock()
	hosts := append([]*hostClient(nil), controller.hosts...)
	controller.mu.RUnlock()
	var lastErr error
	for _, host := range hosts {
		if err := host.refresh(ctx); err != nil {
			lastErr = err
			continue
		}
		if !host.ready || !supportsProtocol(ctx, host, protocol) {
			continue
		}
		capacity, err := host.volume.GetCapacity(ctx, &koujav1.GetCapacityRequest{})
		if err != nil {
			lastErr = err
			continue
		}
		for _, value := range capacity.GetVgs() {
			if value.GetVgName() == vg && value.GetAllocatableBytes() >= size {
				return host, nil
			}
		}
	}
	if lastErr != nil {
		return nil, status.Errorf(codes.Unavailable, "no kouja host available: %v", lastErr)
	}
	return nil, status.Error(codes.ResourceExhausted, "no kouja host has sufficient capacity")
}

func supportsProtocol(ctx context.Context, host *hostClient, protocol string) bool {
	info, err := host.volume.GetInfo(ctx, &koujav1.GetInfoRequest{})
	if err != nil {
		return false
	}
	value := protocolValue(protocol)
	for _, supported := range info.GetProtocols() {
		if supported == value {
			return true
		}
	}
	return false
}

func (controller *Controller) hostForUUID(ctx context.Context, uuid string) (*hostClient, error) {
	controller.mu.RLock()
	hosts := append([]*hostClient(nil), controller.hosts...)
	controller.mu.RUnlock()
	for _, host := range hosts {
		if host.uuid == "" {
			if err := host.refresh(ctx); err != nil {
				continue
			}
		}
		if host.uuid == uuid {
			return host, nil
		}
	}
	return nil, status.Errorf(codes.NotFound, "kouja host %s is not configured", uuid)
}

var errVolumeNotFound = errors.New("csi: volume not found")

func (controller *Controller) volumeByResource(ctx context.Context, host *hostClient, resourceUUID string) (*koujav1.Volume, error) {
	values, err := listHostVolumes(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, volume := range values {
		if volume.GetResourceUuid() == resourceUUID {
			return volume, nil
		}
	}
	return nil, errVolumeNotFound
}

func listHostVolumes(ctx context.Context, host *hostClient) ([]*koujav1.Volume, error) {
	var result []*koujav1.Volume
	pageToken := ""
	for {
		response, err := host.volume.ListVolumes(ctx, &koujav1.ListVolumesRequest{PageSize: 1000, PageToken: pageToken})
		if err != nil {
			return nil, err
		}
		result = append(result, response.GetVolumes()...)
		if response.GetNextPageToken() == "" {
			return result, nil
		}
		pageToken = response.GetNextPageToken()
	}
}

func protocolValue(protocol string) koujav1.Protocol {
	switch strings.ToLower(protocol) {
	case "nvme", "nvme-tcp", "nvme_tcp":
		return koujav1.Protocol_PROTOCOL_NVME_TCP
	default:
		return koujav1.Protocol_PROTOCOL_ISCSI
	}
}
