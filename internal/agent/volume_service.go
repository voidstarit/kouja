package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/voidstarit/kouja/gen/kouja/v1"
	"github.com/voidstarit/kouja/internal/auth"
	"github.com/voidstarit/kouja/internal/config"
	"github.com/voidstarit/kouja/internal/credential"
	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/operations"
	"github.com/voidstarit/kouja/internal/state"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type OperationRunner interface {
	Run(ctx context.Context, request operations.Request) (operations.Result, error)
}

type ServiceOptions struct {
	HostUUID     string
	AgentVersion string
	APIVersion   string
	ISCSIEnabled bool
	ISCSIPrefix  string
	ISCSIPortals []config.Portal
	NVMeEnabled  bool
	NVMePrefix   string
	NVMePortals  []config.Portal
	Ready        func() bool
	Admission    *Admission
}

type VolumeService struct {
	koujav1.UnimplementedVolumeServiceServer
	store   *state.Store
	lvm     lvm.Backend
	runner  OperationRunner
	creds   credential.Manager
	options ServiceOptions
}

func NewVolumeService(store *state.Store, lvmBackend lvm.Backend, runner OperationRunner, creds credential.Manager, options ServiceOptions) *VolumeService {
	return &VolumeService{store: store, lvm: lvmBackend, runner: runner, creds: creds, options: options}
}

func (service *VolumeService) CreateVolume(ctx context.Context, request *koujav1.CreateVolumeRequest) (*koujav1.CreateVolumeResponse, error) {
	identity, err := requireIdentity(ctx, auth.RoleCaller, auth.RoleController, auth.RoleAdmin)
	if err != nil {
		return nil, statusError(err)
	}
	release, err := service.admit(ctx, identity)
	if err != nil {
		return nil, err
	}
	defer release()
	if request.GetWipe() {
		return nil, status.Error(codes.Unimplemented, "wipe is a stage 3 operation")
	}
	if request.GetVolumeId() == "" || request.GetSizeBytes() <= 0 || request.GetVg() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume_id, positive size_bytes, and vg are required")
	}
	export := request.Export == nil || request.GetExport()
	protocol := request.GetProtocol()
	protocolName := ""
	if export {
		if protocol == koujav1.Protocol_PROTOCOL_UNSPECIFIED {
			protocol = koujav1.Protocol_PROTOCOL_ISCSI
		}
		var protocolErr error
		protocolName, protocolErr = protocolString(protocol)
		if protocolErr != nil {
			return nil, statusError(protocolErr)
		}
	}
	resourceUUID := ""
	targetName := ""
	portal := ""
	if existing, err := service.getVolume(ctx, identity.Owner, request.GetVolumeId()); err == nil {
		resourceUUID = existing.ResourceUUID
		if existingExport, exportErr := service.getExport(ctx, identity.Owner, request.GetVolumeId()); exportErr == nil {
			targetName = existingExport.TargetName
			portal = existingExport.Portal
		}
	} else if !errors.Is(err, state.ErrNotFound) {
		return nil, statusError(err)
	}
	if resourceUUID == "" {
		resourceUUID = uuid.NewString()
	}
	if export {
		if initiator := request.GetInitiator(); initiator == "" {
			return nil, status.Error(codes.InvalidArgument, "initiator is required when export is true")
		}
		if targetName == "" {
			targetName, err = service.targetName(protocolName, resourceUUID)
			if err != nil {
				return nil, statusError(err)
			}
		}
		if portal == "" {
			portal, err = service.portal(protocolName)
			if err != nil {
				return nil, statusError(err)
			}
		}
	}
	intent := operations.CreateIntent{
		ResourceUUID: resourceUUID,
		SizeBytes:    request.GetSizeBytes(),
		VG:           request.GetVg(),
		Export:       export,
		Protocol:     protocolName,
		Initiator:    request.GetInitiator(),
		TargetName:   targetName,
		Portal:       portal,
	}
	raw, err := marshalJSON(intent)
	if err != nil {
		return nil, statusError(err)
	}
	result, err := service.runner.Run(ctx, operations.Request{
		Owner:     identity.Owner,
		VolumeID:  request.GetVolumeId(),
		Action:    operations.ActionCreate,
		Intent:    raw,
		RequestID: requestID(ctx, request.GetRequestId()),
	})
	if err != nil {
		return nil, statusError(err)
	}
	response := &koujav1.CreateVolumeResponse{
		Volume:      toProtoVolume(result.Volume),
		OperationId: result.OperationID,
		RequestId:   requestID(ctx, request.GetRequestId()),
	}
	if result.Export.VolumeID != "" {
		response.Export = toProtoExport(result.Export, state.ExportObservation{})
		if request.IncludeCredential == nil || request.GetIncludeCredential() {
			if credentialValue, err := service.credentialValue(ctx, identity, result.Volume.Owner, result.Volume.VolumeID); err == nil {
				response.Export.Credential = credentialValue
			}
		}
	}
	return response, nil
}

func (service *VolumeService) ExportVolume(ctx context.Context, request *koujav1.ExportVolumeRequest) (*koujav1.ExportVolumeResponse, error) {
	identity, err := requireIdentity(ctx, auth.RoleCaller, auth.RoleController, auth.RoleAdmin)
	if err != nil {
		return nil, statusError(err)
	}
	release, err := service.admit(ctx, identity)
	if err != nil {
		return nil, err
	}
	defer release()
	volume, err := service.getVolume(ctx, identity.Owner, request.GetVolumeId())
	if err != nil {
		return nil, statusError(err)
	}
	protocolName, err := protocolString(request.GetProtocol())
	if err != nil {
		return nil, statusError(err)
	}
	targetName, portal := "", ""
	if existing, err := service.getExport(ctx, identity.Owner, request.GetVolumeId()); err == nil {
		targetName, portal = existing.TargetName, existing.Portal
	} else if !errors.Is(err, state.ErrNotFound) {
		return nil, statusError(err)
	}
	if targetName == "" {
		targetName, err = service.targetName(protocolName, volume.ResourceUUID)
		if err != nil {
			return nil, statusError(err)
		}
	}
	if portal == "" {
		portal, err = service.portal(protocolName)
		if err != nil {
			return nil, statusError(err)
		}
	}
	intent := operations.ExportIntent{
		Protocol: protocolName, Initiator: request.GetInitiator(), TargetName: targetName, Portal: portal,
		RotateCredential: request.GetRotateCredential(), Force: request.GetForce(), AttachmentID: request.GetAttachmentId(),
	}
	raw, err := marshalJSON(intent)
	if err != nil {
		return nil, statusError(err)
	}
	result, err := service.runner.Run(ctx, operations.Request{
		Owner: identity.Owner, VolumeID: request.GetVolumeId(), Action: operations.ActionExport,
		Intent: raw, RequestID: requestID(ctx, request.GetRequestId()),
	})
	if err != nil {
		return nil, statusError(err)
	}
	return &koujav1.ExportVolumeResponse{
		Volume: toProtoVolume(result.Volume), Export: toProtoExport(result.Export, state.ExportObservation{}),
		OperationId: result.OperationID, RequestId: requestID(ctx, request.GetRequestId()),
	}, nil
}

func (service *VolumeService) UnexportVolume(ctx context.Context, request *koujav1.UnexportVolumeRequest) (*koujav1.UnexportVolumeResponse, error) {
	identity, err := requireIdentity(ctx, auth.RoleCaller, auth.RoleController, auth.RoleAdmin)
	if err != nil {
		return nil, statusError(err)
	}
	release, err := service.admit(ctx, identity)
	if err != nil {
		return nil, err
	}
	defer release()
	raw, err := marshalJSON(operations.UnexportIntent{Force: request.GetForce()})
	if err != nil {
		return nil, statusError(err)
	}
	result, err := service.runner.Run(ctx, operations.Request{
		Owner: identity.Owner, VolumeID: request.GetVolumeId(), Action: operations.ActionUnexport,
		Intent: raw, RequestID: requestID(ctx, request.GetRequestId()),
	})
	if err != nil {
		return nil, statusError(err)
	}
	return &koujav1.UnexportVolumeResponse{
		Volume: toProtoVolume(result.Volume), Export: toProtoExport(result.Export, state.ExportObservation{}),
		OperationId: result.OperationID, RequestId: requestID(ctx, request.GetRequestId()),
	}, nil
}

func (service *VolumeService) ResizeVolume(ctx context.Context, request *koujav1.ResizeVolumeRequest) (*koujav1.ResizeVolumeResponse, error) {
	identity, err := requireIdentity(ctx, auth.RoleCaller, auth.RoleController, auth.RoleAdmin)
	if err != nil {
		return nil, statusError(err)
	}
	release, err := service.admit(ctx, identity)
	if err != nil {
		return nil, err
	}
	defer release()
	raw, err := marshalJSON(operations.ResizeIntent{
		SizeBytes: request.GetSizeBytes(), AllowShrink: request.GetAllowShrink(),
	})
	if err != nil {
		return nil, statusError(err)
	}
	result, err := service.runner.Run(ctx, operations.Request{
		Owner: identity.Owner, VolumeID: request.GetVolumeId(), Action: operations.ActionResize,
		Intent: raw, RequestID: requestID(ctx, request.GetRequestId()),
	})
	if err != nil {
		return nil, statusError(err)
	}
	response := &koujav1.ResizeVolumeResponse{
		Volume: toProtoVolume(result.Volume), RescanRequired: true,
		OperationId: result.OperationID, RequestId: requestID(ctx, request.GetRequestId()),
	}
	if result.Export.VolumeID != "" {
		response.Export = toProtoExport(result.Export, state.ExportObservation{})
	}
	return response, nil
}

func (service *VolumeService) DeleteVolume(ctx context.Context, request *koujav1.DeleteVolumeRequest) (*koujav1.DeleteVolumeResponse, error) {
	identity, err := requireIdentity(ctx, auth.RoleCaller, auth.RoleController, auth.RoleAdmin)
	if err != nil {
		return nil, statusError(err)
	}
	release, err := service.admit(ctx, identity)
	if err != nil {
		return nil, err
	}
	defer release()
	raw, err := marshalJSON(operations.DeleteIntent{Force: request.GetForce()})
	if err != nil {
		return nil, statusError(err)
	}
	result, err := service.runner.Run(ctx, operations.Request{
		Owner: identity.Owner, VolumeID: request.GetVolumeId(), Action: operations.ActionDelete,
		Intent: raw, RequestID: requestID(ctx, request.GetRequestId()),
	})
	if err != nil {
		return nil, statusError(err)
	}
	return &koujav1.DeleteVolumeResponse{OperationId: result.OperationID, RequestId: requestID(ctx, request.GetRequestId())}, nil
}

func (service *VolumeService) GetVolume(ctx context.Context, request *koujav1.GetVolumeRequest) (*koujav1.GetVolumeResponse, error) {
	identity, err := requireIdentity(ctx, auth.RoleCaller, auth.RoleController, auth.RoleAdmin)
	if err != nil {
		return nil, statusError(err)
	}
	volume, err := service.getVolume(ctx, identity.Owner, request.GetVolumeId())
	if err != nil {
		return nil, statusError(err)
	}
	response := &koujav1.GetVolumeResponse{
		Desired:     toProtoVolume(volume),
		OperationId: volume.LastOperationID,
	}
	export, exportErr := service.getExport(ctx, identity.Owner, request.GetVolumeId())
	var exportObservation state.ExportObservation
	if exportErr == nil {
		exportObservation, _ = service.getExportObservation(ctx, identity.Owner, request.GetVolumeId())
		response.Export = toProtoExport(export, exportObservation)
	} else if !errors.Is(exportErr, state.ErrNotFound) {
		return nil, statusError(exportErr)
	}
	observation, observationErr := service.getObservation(ctx, identity.Owner, request.GetVolumeId())
	if observationErr == nil {
		response.Observed = toProtoObservation(observation, exportObservation)
	} else if !errors.Is(observationErr, state.ErrNotFound) {
		return nil, statusError(observationErr)
	}
	response.DriftReasons = driftReasons(volume.DriftReasons)
	return response, nil
}

func (service *VolumeService) ListVolumes(ctx context.Context, request *koujav1.ListVolumesRequest) (*koujav1.ListVolumesResponse, error) {
	identity, err := requireIdentity(ctx, auth.RoleCaller, auth.RoleController, auth.RoleAdmin)
	if err != nil {
		return nil, statusError(err)
	}
	owner := identity.Owner
	if identity.Role == auth.RoleAdmin {
		owner = ""
	}
	limit := int(request.GetPageSize())
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	var volumes []state.Volume
	if err := service.store.Transact(ctx, func(tx state.Tx) error {
		values, err := tx.ListVolumes(owner, state.Page{Limit: limit, Cursor: request.GetPageToken()})
		volumes = values
		return err
	}); err != nil {
		return nil, statusError(err)
	}
	response := &koujav1.ListVolumesResponse{Volumes: make([]*koujav1.Volume, 0, len(volumes))}
	for _, volume := range volumes {
		response.Volumes = append(response.Volumes, toProtoVolume(volume))
	}
	if len(volumes) == limit {
		response.NextPageToken = volumes[len(volumes)-1].VolumeID
	}
	return response, nil
}

func (service *VolumeService) GetCredential(ctx context.Context, request *koujav1.GetCredentialRequest) (*koujav1.GetCredentialResponse, error) {
	identity, err := requireIdentity(ctx, auth.RoleCaller, auth.RoleController, auth.RoleNode, auth.RoleAdmin)
	if err != nil {
		return nil, statusError(err)
	}
	owner := request.GetOwner()
	if owner == "" {
		owner = identity.Owner
	}
	if owner != identity.Owner && identity.Role != auth.RoleAdmin && identity.Role != auth.RoleNode {
		return nil, status.Error(codes.PermissionDenied, "owner mismatch")
	}
	stored, err := service.creds.Get(ctx, identity, owner, request.GetVolumeId())
	if err != nil {
		return nil, statusError(err)
	}
	plain, err := service.creds.Reveal(ctx, stored)
	if err != nil {
		return nil, statusError(err)
	}
	return &koujav1.GetCredentialResponse{
		Protocol: protocolProto(stored.Protocol), Credential: plain.Secret,
		OperationId: "",
	}, nil
}

func (service *VolumeService) RotateCredential(ctx context.Context, request *koujav1.RotateCredentialRequest) (*koujav1.RotateCredentialResponse, error) {
	identity, err := requireIdentity(ctx, auth.RoleCaller, auth.RoleAdmin)
	if err != nil {
		return nil, statusError(err)
	}
	export, err := service.getExport(ctx, identity.Owner, request.GetVolumeId())
	if errors.Is(err, state.ErrNotFound) {
		return nil, status.Error(codes.FailedPrecondition, "volume is not exported")
	}
	if err != nil {
		return nil, statusError(err)
	}
	intent := operations.ExportIntent{
		Protocol: export.Protocol, Initiator: export.Initiator, TargetName: export.TargetName, Portal: export.Portal,
		RotateCredential: true, Force: true, AttachmentID: "rotate-" + uuid.NewString(),
	}
	raw, err := marshalJSON(intent)
	if err != nil {
		return nil, statusError(err)
	}
	if _, err := service.runner.Run(ctx, operations.Request{
		Owner: identity.Owner, VolumeID: request.GetVolumeId(), Action: operations.ActionExport,
		Intent: raw, RequestID: requestID(ctx, request.GetRequestId()),
	}); err != nil {
		return nil, statusError(err)
	}
	stored, err := service.creds.Get(ctx, identity, identity.Owner, request.GetVolumeId())
	if err != nil {
		return nil, statusError(err)
	}
	plain, err := service.creds.Reveal(ctx, stored)
	if err != nil {
		return nil, statusError(err)
	}
	return &koujav1.RotateCredentialResponse{
		Credential: plain.Secret, RequestId: requestID(ctx, request.GetRequestId()),
	}, nil
}

func (service *VolumeService) GetCapacity(ctx context.Context, _ *koujav1.GetCapacityRequest) (*koujav1.GetCapacityResponse, error) {
	if service.options.Admission != nil {
		if err := service.options.Admission.AcquireCapacity(); err != nil {
			return nil, statusError(err)
		}
	}
	vgs, err := service.listVGs(ctx)
	if err != nil {
		return nil, statusError(err)
	}
	response := &koujav1.GetCapacityResponse{Vgs: make([]*koujav1.Capacity, 0, len(vgs))}
	for _, vg := range vgs {
		capacity, err := service.lvm.Capacity(ctx, vg.Name)
		if err != nil {
			response.Vgs = append(response.Vgs, &koujav1.Capacity{VgName: vg.Name, VgUuid: vg.UUID, VgState: "unavailable"})
			continue
		}
		response.Vgs = append(response.Vgs, toProtoCapacity(capacity))
	}
	return response, nil
}

func (service *VolumeService) GetInfo(context.Context, *koujav1.GetInfoRequest) (*koujav1.GetInfoResponse, error) {
	protocols := make([]koujav1.Protocol, 0, 2)
	if service.options.ISCSIEnabled {
		protocols = append(protocols, koujav1.Protocol_PROTOCOL_ISCSI)
	}
	if service.options.NVMeEnabled {
		protocols = append(protocols, koujav1.Protocol_PROTOCOL_NVME_TCP)
	}
	ready := true
	if service.options.Ready != nil {
		ready = service.options.Ready()
	}
	return &koujav1.GetInfoResponse{
		HostUuid: service.options.HostUUID, AgentVersion: service.options.AgentVersion,
		ApiVersion: service.options.APIVersion, Protocols: protocols,
		AuthModes: []string{"mtls"}, Ready: ready,
	}, nil
}

func (service *VolumeService) admit(ctx context.Context, identity auth.Identity) (func(), error) {
	if service.options.Admission == nil {
		return func() {}, nil
	}
	release, err := service.options.Admission.AcquireOperation(ctx, identity.Owner)
	if err != nil {
		return nil, statusError(err)
	}
	return release, nil
}

func (service *VolumeService) getVolume(ctx context.Context, owner, volumeID string) (state.Volume, error) {
	var volume state.Volume
	err := service.store.Transact(ctx, func(tx state.Tx) error {
		value, err := tx.GetVolume(owner, volumeID)
		if err != nil {
			return err
		}
		volume = value
		return nil
	})
	return volume, err
}

func (service *VolumeService) getExport(ctx context.Context, owner, volumeID string) (state.Export, error) {
	var export state.Export
	err := service.store.Transact(ctx, func(tx state.Tx) error {
		value, err := tx.GetExport(owner, volumeID)
		if err != nil {
			return err
		}
		export = value
		return nil
	})
	return export, err
}

func (service *VolumeService) getObservation(ctx context.Context, owner, volumeID string) (state.Observation, error) {
	var observation state.Observation
	err := service.store.Transact(ctx, func(tx state.Tx) error {
		value, err := tx.GetObservation(owner, volumeID)
		if err != nil {
			return err
		}
		observation = value
		return nil
	})
	return observation, err
}

func (service *VolumeService) getExportObservation(ctx context.Context, owner, volumeID string) (state.ExportObservation, error) {
	var observation state.ExportObservation
	err := service.store.Transact(ctx, func(tx state.Tx) error {
		value, err := tx.GetExportObservation(owner, volumeID)
		if err != nil {
			return err
		}
		observation = value
		return nil
	})
	return observation, err
}

func (service *VolumeService) listVGs(ctx context.Context) ([]state.VG, error) {
	var vgs []state.VG
	err := service.store.Transact(ctx, func(tx state.Tx) error {
		values, err := tx.ListVGs()
		vgs = values
		return err
	})
	return vgs, err
}

func (service *VolumeService) credentialValue(ctx context.Context, identity auth.Identity, owner, volumeID string) (string, error) {
	stored, err := service.creds.Get(ctx, identity, owner, volumeID)
	if err != nil {
		return "", err
	}
	plain, err := service.creds.Reveal(ctx, stored)
	if err != nil {
		return "", err
	}
	return plain.Secret, nil
}

func (service *VolumeService) targetName(protocol, resourceUUID string) (string, error) {
	switch protocol {
	case "iscsi":
		if service.options.ISCSIPrefix == "" {
			return "", fmt.Errorf("agent: iSCSI prefix is not configured")
		}
		return service.options.ISCSIPrefix + ":" + service.options.HostUUID + ":" + resourceUUID, nil
	case "nvme-tcp":
		if service.options.NVMePrefix == "" {
			return "", fmt.Errorf("agent: NVMe prefix is not configured")
		}
		return service.options.NVMePrefix + ":" + service.options.HostUUID + ":" + resourceUUID, nil
	default:
		return "", fmt.Errorf("agent: unsupported protocol %q", protocol)
	}
}

func (service *VolumeService) portal(protocol string) (string, error) {
	var portals []config.Portal
	switch protocol {
	case "iscsi":
		portals = service.options.ISCSIPortals
	case "nvme-tcp":
		portals = service.options.NVMePortals
	}
	if len(portals) == 0 {
		return "", fmt.Errorf("agent: no portal configured for %s", protocol)
	}
	return net.JoinHostPort(portals[0].Address, strconv.Itoa(portals[0].Port)), nil
}

func protocolString(protocol koujav1.Protocol) (string, error) {
	switch protocol {
	case koujav1.Protocol_PROTOCOL_ISCSI:
		return "iscsi", nil
	case koujav1.Protocol_PROTOCOL_NVME_TCP:
		return "nvme-tcp", nil
	default:
		return "", fmt.Errorf("agent: protocol is required")
	}
}

func protocolProto(protocol credential.Protocol) koujav1.Protocol {
	switch protocol {
	case credential.ProtocolISCSI:
		return koujav1.Protocol_PROTOCOL_ISCSI
	case credential.ProtocolNVMeTCP:
		return koujav1.Protocol_PROTOCOL_NVME_TCP
	default:
		return koujav1.Protocol_PROTOCOL_UNSPECIFIED
	}
}

func toProtoVolume(volume state.Volume) *koujav1.Volume {
	return &koujav1.Volume{
		Owner: volume.Owner, VolumeId: volume.VolumeID, ResourceUuid: volume.ResourceUUID,
		LvName: volume.LVName, VgName: volume.VGName,
		RequestedSizeBytes: volume.RequestedSizeBytes, ActualSizeBytes: volume.ActualSizeBytes,
		State: volumeState(volume.State), DriftReasons: driftReasons(volume.DriftReasons),
		CreatedAt: timestamppb.New(volume.CreatedAt), UpdatedAt: timestamppb.New(volume.UpdatedAt),
		LegacyIdentity: volume.LegacyIdentity,
	}
}

func toProtoExport(export state.Export, observation state.ExportObservation) *koujav1.Export {
	return &koujav1.Export{
		Protocol: protocolProto(credential.Protocol(export.Protocol)), Initiator: export.Initiator,
		TargetName: export.TargetName, Lun: export.LUN, Nsid: export.NSID, Portal: export.Portal,
		State: exportState(export.State), SessionState: sessionState(observation.SessionState),
	}
}

func toProtoObservation(observation state.Observation, exportObservation state.ExportObservation) *koujav1.Observation {
	return &koujav1.Observation{
		LvPresent: observation.LVPresent, ObservedSizeBytes: observation.ObservedSizeBytes,
		DevicePath: observation.DevicePath, TargetPresent: exportObservation.TargetPresent,
		NamespacePresent: exportObservation.NamespacePresent, AclPresent: exportObservation.ACLPresent,
		ActiveSessions:    exportObservation.ActiveSessions,
		SessionState:      sessionState(exportObservation.SessionState),
		ObservedInitiator: exportObservation.ObservedInitiator,
		ObservedAt:        timestamppb.New(observation.ObservedAt), Source: observation.Source,
	}
}

func toProtoCapacity(capacity lvm.Capacity) *koujav1.Capacity {
	return &koujav1.Capacity{
		VgName: capacity.VGName, VgUuid: capacity.VGUUID, VgState: capacity.VGState,
		TotalBytes: capacity.TotalBytes, FreeBytes: capacity.FreeBytes,
		AllocatableBytes: capacity.AllocatableBytes, ExtentSizeBytes: capacity.ExtentSizeBytes,
		MaxVolumeBytes: capacity.MaxVolumeBytes, ObservedAt: timestamppb.New(capacity.ObservedAt),
	}
}

func volumeState(value string) koujav1.VolumeState {
	switch value {
	case "creating":
		return koujav1.VolumeState_VOLUME_STATE_CREATING
	case "available":
		return koujav1.VolumeState_VOLUME_STATE_AVAILABLE
	case "exporting":
		return koujav1.VolumeState_VOLUME_STATE_EXPORTING
	case "exported":
		return koujav1.VolumeState_VOLUME_STATE_EXPORTED
	case "resizing":
		return koujav1.VolumeState_VOLUME_STATE_RESIZING
	case "deleting":
		return koujav1.VolumeState_VOLUME_STATE_DELETING
	case "deleted":
		return koujav1.VolumeState_VOLUME_STATE_DELETED
	case "failed":
		return koujav1.VolumeState_VOLUME_STATE_FAILED
	case "drift":
		return koujav1.VolumeState_VOLUME_STATE_DRIFT
	default:
		return koujav1.VolumeState_VOLUME_STATE_UNSPECIFIED
	}
}

func exportState(value string) koujav1.ExportState {
	switch value {
	case "exporting":
		return koujav1.ExportState_EXPORT_STATE_EXPORTING
	case "exported":
		return koujav1.ExportState_EXPORT_STATE_EXPORTED
	case "unexporting":
		return koujav1.ExportState_EXPORT_STATE_UNEXPORTING
	case "unexported":
		return koujav1.ExportState_EXPORT_STATE_UNEXPORTED
	case "failed":
		return koujav1.ExportState_EXPORT_STATE_FAILED
	case "drift":
		return koujav1.ExportState_EXPORT_STATE_DRIFT
	default:
		return koujav1.ExportState_EXPORT_STATE_UNSPECIFIED
	}
}

func sessionState(value string) koujav1.SessionState {
	switch value {
	case "none":
		return koujav1.SessionState_SESSION_STATE_NONE
	case "active":
		return koujav1.SessionState_SESSION_STATE_ACTIVE
	case "unknown":
		return koujav1.SessionState_SESSION_STATE_UNKNOWN
	default:
		return koujav1.SessionState_SESSION_STATE_UNSPECIFIED
	}
}

func driftReasons(values []string) []koujav1.DriftReason {
	mapping := map[string]koujav1.DriftReason{
		"lv_absent":            koujav1.DriftReason_DRIFT_REASON_LV_ABSENT,
		"size_mismatch":        koujav1.DriftReason_DRIFT_REASON_SIZE_MISMATCH,
		"target_absent":        koujav1.DriftReason_DRIFT_REASON_TARGET_ABSENT,
		"namespace_absent":     koujav1.DriftReason_DRIFT_REASON_NAMESPACE_ABSENT,
		"acl_absent":           koujav1.DriftReason_DRIFT_REASON_ACL_ABSENT,
		"initiator_mismatch":   koujav1.DriftReason_DRIFT_REASON_INITIATOR_MISMATCH,
		"unattributed_session": koujav1.DriftReason_DRIFT_REASON_UNATTRIBUTED_SESSION,
	}
	result := make([]koujav1.DriftReason, 0, len(values))
	for _, value := range values {
		result = append(result, mapping[strings.ToLower(value)])
	}
	return result
}

func requestID(ctx context.Context, explicit string) string {
	if explicit != "" {
		return explicit
	}
	return RequestIDFromContext(ctx)
}

func marshalJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: encode intent: %v", operations.ErrInvalidRequest, err)
	}
	return encoded, nil
}
