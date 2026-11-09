package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/voidstarit/kouja/gen/kouja/v1"
	"github.com/voidstarit/kouja/internal/auth"
	"github.com/voidstarit/kouja/internal/credential"
	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/operations"
	"github.com/voidstarit/kouja/internal/pki"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var vgNamePattern = regexp.MustCompile(`^[A-Za-z0-9+_.-]{1,127}$`)

type DriftRetrier interface {
	Retry(ctx context.Context, owner, volumeID string) (operations.Result, error)
}

type AdminService struct {
	koujav1.UnimplementedAdminServiceServer
	store         *state.Store
	lvm           lvm.Backend
	exporters     map[string]target.Exporter
	creds         credential.Manager
	retrier       DriftRetrier
	classes       map[string]string
	classRegistry ClassRegistry
	vgs           VGService
	vgClassGate   *sync.Mutex
	issuer        pki.Issuer
	registry      pki.Registry
	external      pki.ExternalRevoker
	files         pki.FileStore
}

type AdminOption func(*AdminService)

func WithTargetExporters(exporters map[string]target.Exporter) AdminOption {
	return func(service *AdminService) {
		service.exporters = exporters
	}
}

func WithCredentialManager(creds credential.Manager) AdminOption {
	return func(service *AdminService) {
		service.creds = creds
	}
}

func WithDriftRetrier(retrier DriftRetrier) AdminOption {
	return func(service *AdminService) {
		service.retrier = retrier
	}
}

func WithStorageClasses(classes map[string]string) AdminOption {
	return func(service *AdminService) {
		service.classes = classes
	}
}

func WithClassRegistry(registry ClassRegistry) AdminOption {
	return func(service *AdminService) {
		service.classRegistry = registry
	}
}

func WithVGService(vgs VGService) AdminOption {
	return func(service *AdminService) {
		service.vgs = vgs
	}
}

func WithVGClassGate(gate *sync.Mutex) AdminOption {
	return func(service *AdminService) {
		if gate != nil {
			service.vgClassGate = gate
		}
	}
}

func WithCertificateService(issuer pki.Issuer, registry pki.Registry, external pki.ExternalRevoker, files pki.FileStore) AdminOption {
	return func(service *AdminService) {
		service.issuer = issuer
		service.registry = registry
		service.external = external
		service.files = files
	}
}

func NewAdminService(store *state.Store, lvmBackend lvm.Backend, options ...AdminOption) *AdminService {
	service := &AdminService{
		store: store, lvm: lvmBackend, exporters: make(map[string]target.Exporter),
		vgClassGate: &sync.Mutex{},
	}
	for _, option := range options {
		option(service)
	}
	if service.vgs == nil {
		service.vgs = NewVGManager(store, lvmBackend, service.classRegistry)
	}
	if service.vgClassGate == nil {
		service.vgClassGate = &sync.Mutex{}
	}
	return service
}

func (service *AdminService) RegisterVG(ctx context.Context, request *koujav1.RegisterVGRequest) (*koujav1.RegisterVGResponse, error) {
	if _, err := requireIdentity(ctx, auth.RoleAdmin); err != nil {
		return nil, statusError(err)
	}
	if !vgNamePattern.MatchString(request.GetVgName()) {
		return nil, status.Error(codes.InvalidArgument, "invalid vg_name")
	}
	if service.vgs == nil {
		return nil, status.Error(codes.FailedPrecondition, "volume group service is not configured")
	}
	service.vgClassGate.Lock()
	defer service.vgClassGate.Unlock()
	capacity, err := service.vgs.Register(ctx, request.GetVgName(), request.GetVgUuid(), false)
	if err != nil {
		return nil, vgStatusError(err)
	}
	return &koujav1.RegisterVGResponse{
		Capacity: service.capacityProto(capacity), RequestId: requestID(ctx, request.GetRequestId()),
	}, nil
}

func (service *AdminService) DeregisterVG(ctx context.Context, request *koujav1.DeregisterVGRequest) (*koujav1.DeregisterVGResponse, error) {
	if _, err := requireIdentity(ctx, auth.RoleAdmin); err != nil {
		return nil, statusError(err)
	}
	if request.GetVgName() == "" {
		return nil, status.Error(codes.InvalidArgument, "vg_name is required")
	}
	if service.vgs == nil {
		return nil, status.Error(codes.FailedPrecondition, "volume group service is not configured")
	}
	service.vgClassGate.Lock()
	defer service.vgClassGate.Unlock()
	if err := service.vgs.Deregister(ctx, request.GetVgName()); err != nil {
		return nil, vgStatusError(err)
	}
	return &koujav1.DeregisterVGResponse{RequestId: requestID(ctx, request.GetRequestId())}, nil
}

func (service *AdminService) ListVGs(ctx context.Context, _ *koujav1.ListVGsRequest) (*koujav1.ListVGsResponse, error) {
	if _, err := requireIdentity(ctx, auth.RoleAdmin); err != nil {
		return nil, statusError(err)
	}
	var vgs []state.VG
	if service.vgs != nil {
		values, err := service.vgs.List(ctx)
		if err != nil {
			return nil, vgStatusError(err)
		}
		vgs = values
	} else if err := service.store.Transact(ctx, func(tx state.Tx) error {
		values, err := tx.ListVGs()
		vgs = values
		return err
	}); err != nil {
		return nil, statusError(err)
	}
	response := &koujav1.ListVGsResponse{Vgs: make([]*koujav1.Capacity, 0, len(vgs))}
	for _, vg := range vgs {
		capacity, err := service.lvm.Capacity(ctx, vg.Name)
		if err != nil {
			response.Vgs = append(response.Vgs, &koujav1.Capacity{
				VgName: vg.Name, VgUuid: vg.UUID, VgState: "unavailable",
				StorageClass: service.storageClass(vg.Name),
			})
			continue
		}
		response.Vgs = append(response.Vgs, service.capacityProto(capacity))
	}
	return response, nil
}

func (service *AdminService) storageClass(vg string) string {
	if service.classRegistry != nil {
		return service.classRegistry.ClassName(vg)
	}
	return storageClassForVG(service.classes, vg)
}

func vgStatusError(err error) error {
	switch {
	case errors.Is(err, ErrVGUUIDMismatch), errors.Is(err, ErrVGUnavailable),
		errors.Is(err, ErrVGInUse), errors.Is(err, ErrVGClassMapped):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, ErrVGNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, ErrVGConflict):
		return status.Error(codes.AlreadyExists, err.Error())
	default:
		return statusError(err)
	}
}

func (service *AdminService) GrantCredential(ctx context.Context, request *koujav1.GrantCredentialRequest) (*koujav1.GrantCredentialResponse, error) {
	identity, err := requireIdentity(ctx, auth.RoleController, auth.RoleAdmin)
	if err != nil {
		return nil, statusError(err)
	}
	if request.GetVolumeId() == "" || request.GetNodeId() == "" || request.GetNodeCallerId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume_id, node_id, and node_caller_id are required")
	}
	owner := request.GetOwner()
	if owner == "" {
		owner = identity.Owner
	}
	if _, err := service.loadVolume(ctx, owner, request.GetVolumeId()); err != nil {
		return nil, statusError(err)
	}
	now := time.Now().UTC()
	expires := now.Add(24 * time.Hour)
	if request.GetExpiresAt() != nil {
		expires = request.GetExpiresAt().AsTime()
	}
	if !expires.After(now) {
		return nil, status.Error(codes.InvalidArgument, "expires_at must be in the future")
	}
	if err := service.store.Transact(ctx, func(tx state.Tx) error {
		if err := tx.PutGrant(state.Grant{
			Owner: owner, VolumeID: request.GetVolumeId(), NodeID: request.GetNodeId(),
			NodeCallerID: request.GetNodeCallerId(), ExpiresAt: expires, CreatedAt: now,
		}); err != nil {
			return err
		}
		return tx.PutAudit(state.Audit{
			Timestamp: now, Caller: identity.Owner, OperationID: uuid.NewString(),
			RequestID: requestID(ctx, request.GetRequestId()),
			VolumeID:  request.GetVolumeId(), Action: "grant_credential", Result: "succeeded",
		})
	}); err != nil {
		return nil, statusError(err)
	}
	return &koujav1.GrantCredentialResponse{RequestId: requestID(ctx, request.GetRequestId())}, nil
}

func (service *AdminService) RevokeGrant(ctx context.Context, request *koujav1.RevokeGrantRequest) (*koujav1.RevokeGrantResponse, error) {
	identity, err := requireIdentity(ctx, auth.RoleController, auth.RoleAdmin)
	if err != nil {
		return nil, statusError(err)
	}
	if request.GetVolumeId() == "" || request.GetNodeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume_id and node_id are required")
	}
	owner := request.GetOwner()
	if owner == "" {
		owner = identity.Owner
	}
	now := time.Now().UTC()
	if err := service.store.Transact(ctx, func(tx state.Tx) error {
		if err := tx.DeleteGrant(owner, request.GetVolumeId(), request.GetNodeId()); err != nil && !errors.Is(err, state.ErrNotFound) {
			return err
		}
		return tx.PutAudit(state.Audit{
			Timestamp: now, Caller: identity.Owner, OperationID: uuid.NewString(),
			RequestID: requestID(ctx, request.GetRequestId()),
			VolumeID:  request.GetVolumeId(), Action: "revoke_grant", Result: "succeeded",
		})
	}); err != nil {
		return nil, statusError(err)
	}
	return &koujav1.RevokeGrantResponse{RequestId: requestID(ctx, request.GetRequestId())}, nil
}

func (service *AdminService) ResolveDrift(ctx context.Context, request *koujav1.ResolveDriftRequest) (*koujav1.ResolveDriftResponse, error) {
	if _, err := requireIdentity(ctx, auth.RoleAdmin); err != nil {
		return nil, statusError(err)
	}
	if request.GetOwner() == "" || request.GetVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "owner and volume_id are required")
	}
	volume, err := service.loadVolume(ctx, request.GetOwner(), request.GetVolumeId())
	if err != nil {
		return nil, statusError(err)
	}
	now := time.Now().UTC()
	operationID := uuid.NewString()
	switch request.GetAction() {
	case koujav1.ResolveAction_RESOLVE_ACTION_MARK_FAILED:
		previous := volume.State
		volume.State = "failed"
		volume.DriftReasons = nil
		volume.UpdatedAt = now
		if err := service.updateDrift(ctx, volume, nil, operationID, request.GetRequestId(), previous, "failed"); err != nil {
			return nil, err
		}
	case koujav1.ResolveAction_RESOLVE_ACTION_MARK_AVAILABLE:
		present, err := service.lvPresent(ctx, volume)
		if err != nil {
			return nil, statusError(err)
		}
		if !present {
			return nil, status.Error(codes.FailedPrecondition, "LV is not present")
		}
		export, targetPresent, err := service.targetPresent(ctx, volume)
		if err != nil {
			return nil, statusError(err)
		}
		if targetPresent {
			return nil, status.Error(codes.FailedPrecondition, "target is still present")
		}
		previous := volume.State
		volume.State = "available"
		volume.DriftReasons = nil
		volume.UpdatedAt = now
		if export != nil {
			export.State = "unexported"
			export.UpdatedAt = now
		}
		if err := service.updateDrift(ctx, volume, export, operationID, request.GetRequestId(), previous, "available"); err != nil {
			return nil, err
		}
	case koujav1.ResolveAction_RESOLVE_ACTION_MARK_EXPORTED:
		present, err := service.lvPresent(ctx, volume)
		if err != nil {
			return nil, statusError(err)
		}
		if !present {
			return nil, status.Error(codes.FailedPrecondition, "LV is not present")
		}
		export, targetPresent, err := service.observedExport(ctx, volume)
		if err != nil {
			return nil, statusError(err)
		}
		if export == nil || !targetPresent.TargetPresent || !targetPresent.ACLPresent {
			return nil, status.Error(codes.FailedPrecondition, "target and ACL are not present")
		}
		if export.Protocol == "nvme-tcp" && !targetPresent.NamespacePresent {
			return nil, status.Error(codes.FailedPrecondition, "namespace is not present")
		}
		export.State = "exported"
		export.UpdatedAt = now
		previous := volume.State
		volume.State = "exported"
		volume.DriftReasons = nil
		volume.UpdatedAt = now
		if err := service.updateDrift(ctx, volume, export, operationID, request.GetRequestId(), previous, "exported"); err != nil {
			return nil, err
		}
	case koujav1.ResolveAction_RESOLVE_ACTION_MARK_DELETED:
		present, err := service.lvPresent(ctx, volume)
		if err != nil {
			return nil, statusError(err)
		}
		if present {
			return nil, status.Error(codes.FailedPrecondition, "LV is still present")
		}
		export, targetPresent, err := service.targetPresent(ctx, volume)
		if err != nil {
			return nil, statusError(err)
		}
		if targetPresent {
			return nil, status.Error(codes.FailedPrecondition, "target is still present")
		}
		if err := service.markDeleted(ctx, volume, export, operationID, request.GetRequestId(), now); err != nil {
			return nil, err
		}
		return &koujav1.ResolveDriftResponse{OperationId: operationID, RequestId: requestID(ctx, request.GetRequestId())}, nil
	case koujav1.ResolveAction_RESOLVE_ACTION_RECREATE_EXPORT:
		if err := service.recreateExport(ctx, &volume, operationID, request.GetRequestId(), now); err != nil {
			return nil, err
		}
		return &koujav1.ResolveDriftResponse{
			Volume: service.volumeProto(volume), OperationId: operationID,
			RequestId: requestID(ctx, request.GetRequestId()),
		}, nil
	case koujav1.ResolveAction_RESOLVE_ACTION_RETRY:
		if service.retrier == nil {
			return nil, status.Error(codes.Unimplemented, "RETRY is not configured")
		}
		result, err := service.retrier.Retry(ctx, request.GetOwner(), request.GetVolumeId())
		if err != nil {
			return nil, statusError(err)
		}
		return &koujav1.ResolveDriftResponse{
			Volume: service.volumeProto(result.Volume), OperationId: result.OperationID,
			RequestId: requestID(ctx, request.GetRequestId()),
		}, nil
	default:
		return nil, status.Error(codes.InvalidArgument, "unsupported resolve action")
	}
	return &koujav1.ResolveDriftResponse{
		Volume: service.volumeProto(volume), OperationId: operationID,
		RequestId: requestID(ctx, request.GetRequestId()),
	}, nil
}

func (service *AdminService) recreateExport(ctx context.Context, volume *state.Volume, operationID, requestID string, now time.Time) error {
	present, err := service.lvPresent(ctx, *volume)
	if err != nil {
		return statusError(err)
	}
	if !present {
		return status.Error(codes.FailedPrecondition, "LV is not present")
	}
	export, observation, err := service.observedExport(ctx, *volume)
	if err != nil {
		return statusError(err)
	}
	if export == nil {
		return status.Error(codes.FailedPrecondition, "export row is missing")
	}
	if observation.TargetPresent {
		return status.Error(codes.FailedPrecondition, "target is still present")
	}
	if service.creds == nil {
		return status.Error(codes.FailedPrecondition, "credential manager is not configured")
	}
	var stored state.Credential
	if err := service.store.Transact(ctx, func(tx state.Tx) error {
		value, err := tx.GetActiveCredential(volume.Owner, volume.VolumeID)
		if err != nil {
			return err
		}
		stored = value
		return nil
	}); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return status.Error(codes.FailedPrecondition, "active credential is missing")
		}
		return statusError(err)
	}
	plain, err := service.creds.Reveal(ctx, credential.Credential{
		ID: stored.ID, Owner: stored.Owner, VolumeID: stored.VolumeID,
		Protocol: credential.Protocol(stored.Protocol), SecretCiphertext: stored.SecretCiphertext,
		Nonce: stored.Nonce, KeyID: stored.KeyID, KeyFingerprint: stored.KeyFingerprint,
		State: stored.State, CreatedAt: stored.CreatedAt, RotatedAt: stored.RotatedAt,
	})
	if err != nil {
		return statusError(err)
	}
	exporter := service.exporters[export.Protocol]
	if exporter == nil {
		return status.Error(codes.FailedPrecondition, "target exporter is not configured")
	}
	result, err := exporter.Export(ctx, specForExport(*volume, *export), plain)
	if err != nil {
		return statusError(err)
	}
	previous := volume.State
	volume.State = "exported"
	volume.DriftReasons = nil
	volume.UpdatedAt = now
	export.State = "exported"
	export.LUN = result.LUN
	export.NSID = result.NSID
	export.UpdatedAt = now
	if err := service.updateDrift(ctx, *volume, export, operationID, requestID, previous, "exported"); err != nil {
		return statusError(err)
	}
	return nil
}

func (service *AdminService) loadVolume(ctx context.Context, owner, volumeID string) (state.Volume, error) {
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

func (service *AdminService) lvPresent(ctx context.Context, volume state.Volume) (bool, error) {
	_, err := service.lvm.Observe(ctx, volume.VGName, volume.LVName)
	if errors.Is(err, lvm.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (service *AdminService) observedExport(ctx context.Context, volume state.Volume) (*state.Export, target.ExportObservation, error) {
	var export state.Export
	err := service.store.Transact(ctx, func(tx state.Tx) error {
		value, err := tx.GetExport(volume.Owner, volume.VolumeID)
		if err != nil {
			return err
		}
		export = value
		return nil
	})
	if errors.Is(err, state.ErrNotFound) {
		return nil, target.ExportObservation{}, nil
	}
	if err != nil {
		return nil, target.ExportObservation{}, err
	}
	exporter := service.exporters[export.Protocol]
	if exporter == nil {
		return &export, target.ExportObservation{}, fmt.Errorf("agent: no target exporter for %s", export.Protocol)
	}
	observation, err := exporter.Observe(ctx, specForExport(volume, export))
	if errors.Is(err, target.ErrNotFound) {
		return &export, target.ExportObservation{}, nil
	}
	if err != nil {
		return &export, target.ExportObservation{}, err
	}
	return &export, observation, nil
}

func (service *AdminService) targetPresent(ctx context.Context, volume state.Volume) (*state.Export, bool, error) {
	export, observation, err := service.observedExport(ctx, volume)
	if err != nil {
		return nil, false, err
	}
	return export, observation.TargetPresent, nil
}

func (service *AdminService) updateDrift(ctx context.Context, volume state.Volume, export *state.Export, operationID, requestID, previous, post string) error {
	return service.store.Transact(ctx, func(tx state.Tx) error {
		if err := tx.PutVolume(volume); err != nil {
			return err
		}
		if export != nil {
			if err := tx.PutExport(*export); err != nil {
				return err
			}
		}
		return tx.PutAudit(state.Audit{
			Timestamp: time.Now().UTC(), Caller: volume.Owner, OperationID: operationID,
			RequestID: requestID, VolumeID: volume.VolumeID, Action: "resolve_drift",
			PreState: previous, PostState: post, Result: "succeeded",
		})
	})
}

func (service *AdminService) volumeProto(volume state.Volume) *koujav1.Volume {
	proto := toProtoVolume(volume)
	proto.StorageClass = service.storageClass(volume.VGName)
	return proto
}

func (service *AdminService) capacityProto(capacity lvm.Capacity) *koujav1.Capacity {
	proto := toProtoCapacity(capacity)
	proto.StorageClass = service.storageClass(capacity.VGName)
	return proto
}

func (service *AdminService) markDeleted(ctx context.Context, volume state.Volume, export *state.Export, operationID, requestID string, now time.Time) error {
	return service.store.Transact(ctx, func(tx state.Tx) error {
		if err := tx.PutTombstone(state.Tombstone{
			Owner: volume.Owner, VolumeID: volume.VolumeID, ResourceUUID: volume.ResourceUUID,
			LVName: volume.LVName, VGName: volume.VGName, SizeBytes: volume.ActualSizeBytes,
			DeletedAt: now, LastOperationID: operationID, Reason: "resolve_drift_mark_deleted",
		}); err != nil {
			return err
		}
		if err := tx.DeleteVolume(volume.Owner, volume.VolumeID); err != nil {
			return err
		}
		return tx.PutAudit(state.Audit{
			Timestamp: now, Caller: volume.Owner, OperationID: operationID,
			RequestID: requestID, VolumeID: volume.VolumeID, Action: "resolve_drift",
			PreState: volume.State, PostState: "deleted", Result: "succeeded",
		})
	})
}
