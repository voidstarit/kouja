package client

import (
	"context"
	"net"
	"net/http/httptest"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	koujav1 "github.com/voidstarit/kouja/gen/kouja/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type gatewayVolumeService struct {
	koujav1.UnimplementedVolumeServiceServer
	created    *koujav1.CreateVolumeRequest
	list       *koujav1.ListVolumesRequest
	deleted    *koujav1.DeleteVolumeRequest
	credential *koujav1.GetCredentialRequest
}

func (service *gatewayVolumeService) CreateVolume(_ context.Context, request *koujav1.CreateVolumeRequest) (*koujav1.CreateVolumeResponse, error) {
	service.created = request
	return &koujav1.CreateVolumeResponse{Volume: &koujav1.Volume{VolumeId: request.GetVolumeId()}}, nil
}

func (service *gatewayVolumeService) ListVolumes(_ context.Context, request *koujav1.ListVolumesRequest) (*koujav1.ListVolumesResponse, error) {
	service.list = request
	return &koujav1.ListVolumesResponse{}, nil
}

func (service *gatewayVolumeService) DeleteVolume(_ context.Context, request *koujav1.DeleteVolumeRequest) (*koujav1.DeleteVolumeResponse, error) {
	service.deleted = request
	return &koujav1.DeleteVolumeResponse{}, nil
}

func (service *gatewayVolumeService) GetCredential(_ context.Context, request *koujav1.GetCredentialRequest) (*koujav1.GetCredentialResponse, error) {
	service.credential = request
	return &koujav1.GetCredentialResponse{}, nil
}

func bddTestHTTPClientGatewayCompatibility(t testing.TB) {
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	service := &gatewayVolumeService{}
	koujav1.RegisterVolumeServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	mux := runtime.NewServeMux()
	if err := koujav1.RegisterVolumeServiceHandlerClient(context.Background(), mux, koujav1.NewVolumeServiceClient(conn)); err != nil {
		t.Fatalf("RegisterVolumeServiceHandlerClient: %v", err)
	}
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	client, err := NewHTTP(httpServer.URL)
	if err != nil {
		t.Fatalf("NewHTTP: %v", err)
	}
	if _, err := client.CreateVolume(context.Background(), &koujav1.CreateVolumeRequest{VolumeId: "vol-1", SizeBytes: 1024, Vg: "vg-1"}); err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	if service.created.GetVolumeId() != "vol-1" || service.created.GetSizeBytes() != 1024 || service.created.GetVg() != "vg-1" {
		t.Fatalf("CreateVolume request = %#v", service.created)
	}
	if _, err := client.ListVolumes(context.Background(), &koujav1.ListVolumesRequest{PageSize: 10, PageToken: "next"}); err != nil {
		t.Fatalf("ListVolumes: %v", err)
	}
	if service.list.GetPageSize() != 10 || service.list.GetPageToken() != "next" {
		t.Fatalf("ListVolumes request = %#v", service.list)
	}
	if _, err := client.DeleteVolume(context.Background(), &koujav1.DeleteVolumeRequest{VolumeId: "vol-1", Force: true, RequestId: "req-1"}); err != nil {
		t.Fatalf("DeleteVolume: %v", err)
	}
	if service.deleted.GetVolumeId() != "vol-1" || !service.deleted.GetForce() || service.deleted.GetRequestId() != "req-1" {
		t.Fatalf("DeleteVolume request = %#v", service.deleted)
	}
	if _, err := client.GetCredential(context.Background(), &koujav1.GetCredentialRequest{VolumeId: "vol-1", Owner: "alice"}); err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if service.credential.GetVolumeId() != "vol-1" || service.credential.GetOwner() != "alice" {
		t.Fatalf("GetCredential request = %#v", service.credential)
	}
}
