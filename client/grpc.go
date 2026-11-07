package client

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/url"
	"strings"

	koujav1 "github.com/voidstarit/kouja/gen/kouja/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

// GRPC is a combined VolumeService and AdminService gRPC client.
type GRPC struct {
	koujav1.VolumeServiceClient
	koujav1.AdminServiceClient
	conn *grpc.ClientConn
}

// NewGRPC creates a gRPC client for target. The TLS configuration must contain
// a client certificate trusted by the agent and the CA used to verify the
// agent certificate. A target may include an https:// prefix; it is removed
// before the connection is created.
//
// NewGRPC installs a unary interceptor to propagate request IDs. Supply any
// additional interceptors with grpc.WithChainUnaryInterceptor rather than
// grpc.WithUnaryInterceptor.
func NewGRPC(target string, tlsConfig *tls.Config, opts ...grpc.DialOption) (*GRPC, error) {
	if tlsConfig == nil {
		return nil, fmt.Errorf("client: TLS configuration is required")
	}
	dialOptions := make([]grpc.DialOption, 0, len(opts)+2)
	dialOptions = append(dialOptions, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig.Clone())))
	dialOptions = append(dialOptions, opts...)
	dialOptions = append(dialOptions, grpc.WithChainUnaryInterceptor(requestIDUnaryInterceptor))
	return newGRPC(target, dialOptions...)
}

// NewGRPCFromFiles creates a gRPC client using certificate files on disk.
func NewGRPCFromFiles(target, certFile, keyFile, caFile string, opts ...grpc.DialOption) (*GRPC, error) {
	tlsConfig, err := LoadTLSConfig(certFile, keyFile, caFile)
	if err != nil {
		return nil, err
	}
	return NewGRPC(target, tlsConfig, opts...)
}

func newGRPC(target string, opts ...grpc.DialOption) (*GRPC, error) {
	target, err := normalizeGRPCTarget(target)
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(target, opts...)
	if err != nil {
		return nil, fmt.Errorf("client: create gRPC client for %s: %w", target, err)
	}
	return &GRPC{
		VolumeServiceClient: koujav1.NewVolumeServiceClient(conn),
		AdminServiceClient:  koujav1.NewAdminServiceClient(conn),
		conn:                conn,
	}, nil
}

// Close closes the underlying gRPC connection.
func (client *GRPC) Close() error {
	if client == nil || client.conn == nil {
		return nil
	}
	return client.conn.Close()
}

func normalizeGRPCTarget(target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", fmt.Errorf("client: gRPC target is required")
	}
	if strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "http://") {
		parsed, err := url.Parse(target)
		if err != nil {
			return "", fmt.Errorf("client: parse gRPC target: %w", err)
		}
		if parsed.Host == "" {
			return "", fmt.Errorf("client: gRPC target must include a host: %q", target)
		}
		if parsed.Path != "" && parsed.Path != "/" {
			return "", fmt.Errorf("client: gRPC target must not include a path: %q", target)
		}
		return parsed.Host, nil
	}
	return target, nil
}

func requestIDUnaryInterceptor(
	ctx context.Context,
	method string,
	request any,
	reply any,
	conn *grpc.ClientConn,
	invoker grpc.UnaryInvoker,
	opts ...grpc.CallOption,
) error {
	if requestID := RequestIDFromContext(ctx); requestID != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-request-id", requestID)
	}
	return invoker(ctx, method, request, reply, conn, opts...)
}
