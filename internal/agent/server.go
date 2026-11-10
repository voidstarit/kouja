package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/voidstarit/kouja/gen/kouja/v1"
	"github.com/voidstarit/kouja/internal/auth"
	"github.com/voidstarit/kouja/internal/config"
	"github.com/voidstarit/kouja/internal/observability"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type RevocationChecker interface {
	IsRevoked(serial string) bool
}

type Server struct {
	grpc        *grpc.Server
	http        *http.Server
	listener    net.Listener
	tlsConfig   *tls.Config
	ready       func() bool
	metrics     *observability.Metrics
	otel        *observability.OTel
	revocations RevocationChecker
}

type ServerOption func(*Server)

// WithReadiness wires the readiness predicate into /readyz.
func WithReadiness(ready func() bool) ServerOption {
	return func(server *Server) {
		if ready != nil {
			server.ready = ready
		}
	}
}

func WithMetrics(metrics *observability.Metrics) ServerOption {
	return func(server *Server) {
		server.metrics = metrics
	}
}

// WithOTelMetrics serves the pull-based OpenTelemetry registry, including the
// legacy metrics registered in Metrics, from /metrics.
func WithOTelMetrics(otel *observability.OTel) ServerOption {
	return func(server *Server) {
		server.otel = otel
	}
}

func WithRevocationChecker(checker RevocationChecker) ServerOption {
	return func(server *Server) {
		server.revocations = checker
	}
}

func NewServer(cfg *config.Config, volumeService koujav1.VolumeServiceServer, adminService koujav1.AdminServiceServer, options ...ServerOption) (*Server, error) {
	tlsConfig, err := loadTLS(cfg)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", cfg.Listen.Address)
	if err != nil {
		return nil, fmt.Errorf("agent: listen %s: %w", cfg.Listen.Address, err)
	}
	server := &Server{listener: listener, tlsConfig: tlsConfig, ready: func() bool { return true }}
	for _, option := range options {
		option(server)
	}
	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(server.unaryInterceptor))
	server.grpc = grpcServer
	koujav1.RegisterVolumeServiceServer(grpcServer, volumeService)
	koujav1.RegisterAdminServiceServer(grpcServer, adminService)
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(grpcServer, healthServer)

	mux := runtime.NewServeMux()
	if err := koujav1.RegisterVolumeServiceHandlerServer(context.Background(), mux, volumeService); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("agent: register volume HTTP gateway: %w", err)
	}
	if err := koujav1.RegisterAdminServiceHandlerServer(context.Background(), mux, adminService); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("agent: register admin HTTP gateway: %w", err)
	}
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestContext := withTLSIdentity(request.Context(), request)
		request = request.WithContext(requestContext)
		if server.revocations != nil {
			if identity, ok := IdentityFromContext(requestContext); ok && identity.SerialNumber != "" && server.revocations.IsRevoked(identity.SerialNumber) {
				response.Header().Set("Content-Type", "application/json")
				response.WriteHeader(http.StatusUnauthorized)
				_, _ = response.Write([]byte(`{"code":16,"message":"certificate revoked","details":[]}`))
				return
			}
		}
		if strings.HasPrefix(request.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(response, request)
			return
		}
		if request.URL.Path == "/healthz" {
			response.WriteHeader(http.StatusOK)
			_, _ = response.Write([]byte("ok\n"))
			return
		}
		if request.URL.Path == "/readyz" {
			if !server.ready() {
				response.WriteHeader(http.StatusServiceUnavailable)
				_, _ = response.Write([]byte("not ready\n"))
				return
			}
			response.WriteHeader(http.StatusOK)
			_, _ = response.Write([]byte("ok\n"))
			return
		}
		if request.URL.Path == "/metrics" {
			if server.otel != nil {
				server.otel.Handler().ServeHTTP(response, request)
				return
			}
			if server.metrics == nil {
				response.WriteHeader(http.StatusNotFound)
				return
			}
			server.metrics.Handler().ServeHTTP(response, request)
			return
		}
		mux.ServeHTTP(response, request)
	})
	httpServer := &http.Server{Handler: handler, TLSConfig: tlsConfig, ReadHeaderTimeout: 10 * time.Second}
	server.http = httpServer
	return server, nil
}

func (server *Server) Serve() error {
	tlsListener := tls.NewListener(server.listener, server.tlsConfig)
	err := server.http.Serve(tlsListener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (server *Server) Stop() {
	_ = server.http.Close()
	server.grpc.Stop()
}

func (server *Server) GracefulStop() {
	_ = server.http.Close()
	server.grpc.GracefulStop()
}

func (server *Server) Address() net.Addr {
	return server.listener.Addr()
}

func loadTLS(cfg *config.Config) (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(cfg.Listen.TLS.CertFile, cfg.Listen.TLS.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("agent: load server certificate: %w", err)
	}
	caPEM, err := os.ReadFile(cfg.Listen.TLS.ClientCAFile) //nolint:gosec // operator-configured CA path
	if err != nil {
		return nil, fmt.Errorf("agent: read client CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("agent: client CA file contains no certificates")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{certificate},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"h2", "http/1.1"},
	}, nil
}

func withTLSIdentity(ctx context.Context, request *http.Request) context.Context {
	if request.TLS == nil || len(request.TLS.PeerCertificates) == 0 {
		return ctx
	}
	identity, err := auth.Parse(request.TLS.PeerCertificates[0])
	if err != nil {
		return ctx
	}
	ctx = WithIdentity(ctx, identity)
	if requestID := request.Header.Get("X-Request-Id"); requestID != "" {
		ctx = WithRequestID(ctx, requestID)
	}
	return ctx
}

func UnaryInterceptor(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	return interceptIdentity(ctx, request, info, handler, nil)
}

func (server *Server) unaryInterceptor(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	return interceptIdentity(ctx, request, info, handler, server.revocations)
}

func interceptIdentity(ctx context.Context, request any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler, checker RevocationChecker) (any, error) {
	if _, ok := IdentityFromContext(ctx); !ok {
		peerInfo, ok := peer.FromContext(ctx)
		if !ok || peerInfo.AuthInfo == nil {
			return nil, status.Error(codes.Unauthenticated, "client certificate is required")
		}
		tlsInfo, ok := peerInfo.AuthInfo.(credentials.TLSInfo)
		if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
			return nil, status.Error(codes.Unauthenticated, "client certificate is required")
		}
		identity, err := auth.Parse(tlsInfo.State.PeerCertificates[0])
		if err != nil {
			return nil, status.Error(codes.Unauthenticated, "invalid client identity")
		}
		ctx = WithIdentity(ctx, identity)
	}
	if checker != nil {
		if identity, ok := IdentityFromContext(ctx); ok && identity.SerialNumber != "" && checker.IsRevoked(identity.SerialNumber) {
			return nil, status.Error(codes.Unauthenticated, "certificate revoked")
		}
	}
	if incoming, ok := metadata.FromIncomingContext(ctx); ok {
		if values := incoming.Get("x-request-id"); len(values) > 0 {
			ctx = WithRequestID(ctx, values[0])
		}
	}
	return handler(ctx, request)
}
