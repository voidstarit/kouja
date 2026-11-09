package agent

import (
	"context"
	"sort"
	"strings"

	"github.com/voidstarit/kouja/gen/kouja/v1"
	"github.com/voidstarit/kouja/internal/auth"
	"github.com/voidstarit/kouja/internal/pki"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/warnings"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (service *AdminService) IssueCertificate(ctx context.Context, request *koujav1.IssueCertificateRequest) (*koujav1.IssueCertificateResponse, error) {
	identity, err := requireIdentity(ctx, auth.RoleAdmin)
	if err != nil {
		return nil, statusError(err)
	}
	if service.issuer == nil {
		return nil, status.Error(codes.FailedPrecondition, "certificate issuer is not configured")
	}
	issued, err := service.issuer.Issue(ctx, pki.Caller{
		Owner: identity.Owner, SerialNumber: identity.SerialNumber,
		RequestID: requestID(ctx, request.GetRequestId()), Source: "mtls",
	}, pki.IssueRequest{Role: request.GetRole(), Name: request.GetName(), TTLDays: request.GetTtlDays()})
	if err != nil {
		return nil, statusError(err)
	}
	return &koujav1.IssueCertificateResponse{
		CertificatePem: issued.CertificatePEM, PrivateKeyPem: issued.PrivateKeyPEM,
		ClientCaPem: issued.ClientCAPEM, SerialNumber: issued.SerialNumber,
		Role: issued.Role, Name: issued.Name, NotBefore: timestamppb.New(issued.NotBefore),
		NotAfter: timestamppb.New(issued.NotAfter), RequestId: issued.RequestID,
		IssuerSerial: issued.IssuerSerial, Warnings: warningProtos(issued.Warnings),
	}, nil
}

func (service *AdminService) ListCertificates(ctx context.Context, request *koujav1.ListCertificatesRequest) (*koujav1.ListCertificatesResponse, error) {
	if _, err := requireIdentity(ctx, auth.RoleAdmin); err != nil {
		return nil, statusError(err)
	}
	if service.registry == nil {
		return nil, status.Error(codes.FailedPrecondition, "certificate registry is not configured")
	}
	page, err := service.registry.List(ctx, pki.ListOptions{PageSize: request.GetPageSize(), PageToken: request.GetPageToken()})
	if err != nil {
		return nil, statusError(err)
	}
	response := &koujav1.ListCertificatesResponse{NextPageToken: page.NextPageToken}
	for _, summary := range page.Certificates {
		response.Certificates = append(response.Certificates, certificateSummaryProto(summary))
	}
	return response, nil
}

func (service *AdminService) GetCertificate(ctx context.Context, request *koujav1.GetCertificateRequest) (*koujav1.GetCertificateResponse, error) {
	if _, err := requireIdentity(ctx, auth.RoleAdmin); err != nil {
		return nil, statusError(err)
	}
	if service.registry == nil {
		return nil, status.Error(codes.FailedPrecondition, "certificate registry is not configured")
	}
	record, err := service.registry.Get(ctx, request.GetSerialNumber())
	if err != nil {
		return nil, statusError(err)
	}
	return &koujav1.GetCertificateResponse{Certificate: certificateProto(record)}, nil
}

func (service *AdminService) RevokeCertificate(ctx context.Context, request *koujav1.RevokeCertificateRequest) (*koujav1.RevokeCertificateResponse, error) {
	identity, err := requireIdentity(ctx, auth.RoleAdmin)
	if err != nil {
		return nil, statusError(err)
	}
	if service.registry == nil {
		return nil, status.Error(codes.FailedPrecondition, "certificate registry is not configured")
	}
	record, verdicts, err := service.registry.Revoke(ctx, pki.Caller{
		Owner: identity.Owner, SerialNumber: identity.SerialNumber,
		RequestID: requestID(ctx, request.GetRequestId()), Source: "mtls",
	}, request.GetSerialNumber(), request.GetReason())
	if err != nil {
		return nil, statusError(err)
	}
	return &koujav1.RevokeCertificateResponse{
		Certificate: certificateProto(record), Warnings: warningProtos(verdicts),
		RequestId: requestID(ctx, request.GetRequestId()),
	}, nil
}

func (service *AdminService) RevokeExternalCertificate(ctx context.Context, request *koujav1.RevokeExternalCertificateRequest) (*koujav1.RevokeExternalCertificateResponse, error) {
	identity, err := requireIdentity(ctx, auth.RoleAdmin)
	if err != nil {
		return nil, statusError(err)
	}
	if service.external == nil {
		return nil, status.Error(codes.FailedPrecondition, "external certificate revoker is not configured")
	}
	record, verdicts, err := service.external.RevokeExternal(ctx, pki.Caller{
		Owner: identity.Owner, SerialNumber: identity.SerialNumber,
		RequestID: requestID(ctx, request.GetRequestId()), Source: "mtls",
	}, request.GetCertificatePem(), request.GetReason())
	if err != nil {
		return nil, statusError(err)
	}
	return &koujav1.RevokeExternalCertificateResponse{
		Certificate: certificateProto(record), Warnings: warningProtos(verdicts),
		RequestId: requestID(ctx, request.GetRequestId()),
	}, nil
}

func (service *AdminService) GetVG(ctx context.Context, request *koujav1.GetVGRequest) (*koujav1.GetVGResponse, error) {
	if _, err := requireIdentity(ctx, auth.RoleAdmin); err != nil {
		return nil, statusError(err)
	}
	if request.GetVgName() == "" {
		return nil, status.Error(codes.InvalidArgument, "vg_name is required")
	}
	var stored state.VG
	if err := service.store.Transact(ctx, func(tx state.Tx) error {
		value, err := tx.GetVG(request.GetVgName())
		stored = value
		return err
	}); err != nil {
		if err == state.ErrNotFound {
			return nil, status.Error(codes.NotFound, "volume group not found")
		}
		return nil, statusError(err)
	}
	response := &koujav1.GetVGResponse{RegisteredVgUuid: stored.UUID}
	if service.vgs != nil {
		capacity, err := service.vgs.Inspect(ctx, stored.Name)
		if err != nil {
			response.Capacity = &koujav1.Capacity{VgName: stored.Name, VgState: "unavailable", StorageClass: service.storageClass(stored.Name)}
		} else {
			response.Capacity = service.capacityProto(capacity)
			response.UuidDrift = capacity.VGUUID != "" && capacity.VGUUID != stored.UUID
		}
	} else {
		response.Capacity = &koujav1.Capacity{VgName: stored.Name, VgState: "unavailable", StorageClass: service.storageClass(stored.Name)}
	}
	return response, nil
}

func (service *AdminService) ListClasses(ctx context.Context, _ *koujav1.ListClassesRequest) (*koujav1.ListClassesResponse, error) {
	if _, err := requireIdentity(ctx, auth.RoleAdmin); err != nil {
		return nil, statusError(err)
	}
	classes := service.classSnapshot()
	names := make([]string, 0, len(classes))
	for name := range classes {
		names = append(names, name)
	}
	sort.Strings(names)
	response := &koujav1.ListClassesResponse{}
	for _, name := range names {
		response.Classes = append(response.Classes, &koujav1.ClassMapping{ClassName: name, VgName: classes[name]})
	}
	return response, nil
}

func (service *AdminService) GetClass(ctx context.Context, request *koujav1.GetClassRequest) (*koujav1.GetClassResponse, error) {
	if _, err := requireIdentity(ctx, auth.RoleAdmin); err != nil {
		return nil, statusError(err)
	}
	if request.GetClassName() == "" {
		return nil, status.Error(codes.InvalidArgument, "class_name is required")
	}
	classes := service.classSnapshot()
	vg, ok := classes[request.GetClassName()]
	if !ok {
		return nil, status.Error(codes.NotFound, "CLASS_NOT_FOUND")
	}
	return &koujav1.GetClassResponse{Class: &koujav1.ClassMapping{ClassName: request.GetClassName(), VgName: vg}}, nil
}

func (service *AdminService) SetClass(ctx context.Context, request *koujav1.SetClassRequest) (*koujav1.SetClassResponse, error) {
	if _, err := requireIdentity(ctx, auth.RoleAdmin); err != nil {
		return nil, statusError(err)
	}
	if service.classRegistry == nil {
		return nil, status.Error(codes.FailedPrecondition, "class registry is not configured")
	}
	if request.GetClassName() == "" || request.GetVgName() == "" {
		return nil, status.Error(codes.InvalidArgument, "class_name and vg_name are required")
	}
	service.vgClassGate.Lock()
	defer service.vgClassGate.Unlock()
	if service.vgs != nil {
		if _, err := service.vgs.Inspect(ctx, request.GetVgName()); err != nil {
			return nil, status.Error(codes.FailedPrecondition, "CLASS_VG_NOT_REGISTERED")
		}
	}
	verdicts, err := service.classRegistry.Set(ctx, request.GetClassName(), request.GetVgName())
	if err != nil {
		return nil, statusError(err)
	}
	return &koujav1.SetClassResponse{
		Class:    &koujav1.ClassMapping{ClassName: request.GetClassName(), VgName: request.GetVgName()},
		Warnings: warningStrings(verdicts), RequestId: requestID(ctx, request.GetRequestId()),
	}, nil
}

func (service *AdminService) RemoveClass(ctx context.Context, request *koujav1.RemoveClassRequest) (*koujav1.RemoveClassResponse, error) {
	if _, err := requireIdentity(ctx, auth.RoleAdmin); err != nil {
		return nil, statusError(err)
	}
	if service.classRegistry == nil {
		return nil, status.Error(codes.FailedPrecondition, "class registry is not configured")
	}
	if request.GetClassName() == "" {
		return nil, status.Error(codes.InvalidArgument, "class_name is required")
	}
	service.vgClassGate.Lock()
	defer service.vgClassGate.Unlock()
	verdicts, err := service.classRegistry.Remove(ctx, request.GetClassName())
	if err != nil {
		if IsClassNotFound(err) {
			return nil, status.Error(codes.NotFound, "CLASS_NOT_FOUND")
		}
		return nil, statusError(err)
	}
	return &koujav1.RemoveClassResponse{Warnings: warningStrings(verdicts), RequestId: requestID(ctx, request.GetRequestId())}, nil
}

func (service *AdminService) classSnapshot() map[string]string {
	if service.classRegistry != nil {
		return service.classRegistry.Snapshot()
	}
	result := make(map[string]string, len(service.classes))
	for class, vg := range service.classes {
		result[class] = vg
	}
	return result
}

func certificateProto(record pki.CertificateRecord) *koujav1.Certificate {
	proto := &koujav1.Certificate{
		SerialNumber: record.SerialNumber, Role: record.Role, Name: record.Name,
		IssuedBy: record.IssuedBy, IssuerSerial: record.IssuerSerial,
		NotBefore: timestamppb.New(record.NotBefore), NotAfter: timestamppb.New(record.NotAfter),
		CreatedAt: timestamppb.New(record.CreatedAt), RevokedReason: record.RevokedReason,
		Revoked: record.Revoked, CertificatePem: record.CertificatePEM, ClientCaPem: record.ClientCAPEM,
	}
	if record.RevokedAt != nil {
		proto.RevokedAt = timestamppb.New(*record.RevokedAt)
	}
	return proto
}

func certificateSummaryProto(summary pki.CertificateSummary) *koujav1.CertificateSummary {
	proto := &koujav1.CertificateSummary{
		SerialNumber: summary.SerialNumber, Role: summary.Role, Name: summary.Name,
		IssuedBy: summary.IssuedBy, IssuerSerial: summary.IssuerSerial,
		NotBefore: timestamppb.New(summary.NotBefore), NotAfter: timestamppb.New(summary.NotAfter),
		CreatedAt: timestamppb.New(summary.CreatedAt), Revoked: summary.Revoked,
	}
	if summary.RevokedAt != nil {
		proto.RevokedAt = timestamppb.New(*summary.RevokedAt)
	}
	return proto
}

func warningProtos(values []warnings.Warning) []*koujav1.Warning {
	result := make([]*koujav1.Warning, 0, len(values))
	for _, value := range values {
		result = append(result, &koujav1.Warning{Code: value.Code, Message: value.Message})
	}
	return result
}

func warningStrings(values []string) []*koujav1.Warning {
	result := make([]*koujav1.Warning, 0, len(values))
	for _, value := range values {
		result = append(result, &koujav1.Warning{Code: value, Message: strings.ReplaceAll(value, "_", " ")})
	}
	return result
}

var _ = context.Background
