package client

import (
	"context"
	"net"
	"testing"

	koujav1 "github.com/voidstarit/kouja/gen/kouja/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

type testVolumeService struct {
	koujav1.UnimplementedVolumeServiceServer
}

func (service *testVolumeService) GetInfo(context.Context, *koujav1.GetInfoRequest) (*koujav1.GetInfoResponse, error) {
	return &koujav1.GetInfoResponse{HostUuid: "host-1", AgentVersion: "test", Ready: true}, nil
}

type testAdminService struct {
	koujav1.UnimplementedAdminServiceServer
}

func (service *testAdminService) ListVGs(context.Context, *koujav1.ListVGsRequest) (*koujav1.ListVGsResponse, error) {
	return &koujav1.ListVGsResponse{Vgs: []*koujav1.Capacity{{VgName: "vg-1"}}}, nil
}

func bddTestGRPCClient(t testing.TB) {
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	koujav1.RegisterVolumeServiceServer(server, &testVolumeService{})
	koujav1.RegisterAdminServiceServer(server, &testAdminService{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	client, err := newGRPC(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("newGRPC: %v", err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	info, err := client.GetInfo(context.Background(), &koujav1.GetInfoRequest{})
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if info.GetHostUuid() != "host-1" || !info.GetReady() {
		t.Fatalf("GetInfo = %#v", info)
	}
	vgs, err := client.ListVGs(context.Background(), &koujav1.ListVGsRequest{})
	if err != nil {
		t.Fatalf("ListVGs: %v", err)
	}
	if len(vgs.GetVgs()) != 1 || vgs.GetVgs()[0].GetVgName() != "vg-1" {
		t.Fatalf("ListVGs = %#v", vgs)
	}

	if _, err := NewGRPC("localhost:9443", nil); err == nil {
		t.Fatal("NewGRPC accepted a nil TLS configuration")
	}
	if _, err := NewGRPCFromFiles("localhost:9443", "missing.crt", "missing.key", "missing-ca.crt"); err == nil {
		t.Fatal("NewGRPCFromFiles accepted missing files")
	}
	var nilClient *GRPC
	if err := nilClient.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}

	targets := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "https://storage.example:9443", want: "storage.example:9443", ok: true},
		{input: "http://storage.example:9443", want: "storage.example:9443", ok: true},
		{input: "storage.example:9443", want: "storage.example:9443", ok: true},
		{input: "dns:///storage.example:9443", want: "dns:///storage.example:9443", ok: true},
		{input: "", ok: false},
		{input: "https://", ok: false},
		{input: "https://storage.example:9443/path", ok: false},
	}
	for _, target := range targets {
		got, err := normalizeGRPCTarget(target.input)
		if target.ok && (err != nil || got != target.want) {
			t.Fatalf("normalizeGRPCTarget(%q) = %q, %v; want %q", target.input, got, err, target.want)
		}
		if !target.ok && err == nil {
			t.Fatalf("normalizeGRPCTarget(%q) succeeded", target.input)
		}
	}
}

func bddTestGRPCRequestIDInterceptor(t testing.TB) {
	ctx := WithRequestID(context.Background(), "req-1")
	if got := RequestIDFromContext(ctx); got != "req-1" {
		t.Fatalf("RequestIDFromContext = %q", got)
	}
	if got := RequestIDFromContext(context.Background()); got != "" {
		t.Fatalf("empty RequestIDFromContext = %q", got)
	}

	var outgoing metadata.MD
	err := requestIDUnaryInterceptor(
		ctx,
		"/kouja.v1.VolumeService/GetInfo",
		nil,
		nil,
		nil,
		func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			outgoing, _ = metadata.FromOutgoingContext(ctx)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("requestIDUnaryInterceptor: %v", err)
	}
	if got := outgoing.Get("x-request-id"); len(got) != 1 || got[0] != "req-1" {
		t.Fatalf("x-request-id metadata = %v", got)
	}

	outgoing = nil
	if err := requestIDUnaryInterceptor(
		context.Background(),
		"/kouja.v1.VolumeService/GetInfo",
		nil,
		nil,
		nil,
		func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
			outgoing, _ = metadata.FromOutgoingContext(ctx)
			return nil
		},
	); err != nil {
		t.Fatalf("requestIDUnaryInterceptor without ID: %v", err)
	}
	if got := outgoing.Get("x-request-id"); len(got) != 0 {
		t.Fatalf("unexpected x-request-id metadata = %v", got)
	}
}
