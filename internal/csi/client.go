package csi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"

	koujav1 "github.com/voidstarit/kouja/gen/kouja/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// LoadTLSConfig builds the client configuration used to reach kouja agents.
func LoadTLSConfig(certFile, keyFile, caFile string) (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("csi: load client certificate: %w", err)
	}
	caPEM, err := os.ReadFile(caFile) //nolint:gosec // operator-selected CA path
	if err != nil {
		return nil, fmt.Errorf("csi: load CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("csi: no certificates found in %s", caFile)
	}
	return &tls.Config{ //nolint:gosec // TLS 1.3 is negotiated by gRPC; hostname verification remains enabled
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
		RootCAs:      roots,
	}, nil
}

type hostClient struct {
	endpoint string
	conn     *grpc.ClientConn
	volume   koujav1.VolumeServiceClient
	admin    koujav1.AdminServiceClient
	uuid     string
	ready    bool
}

func dialHost(endpoint string, tlsConfig *tls.Config) (*hostClient, error) {
	target := strings.TrimPrefix(endpoint, "https://")
	target = strings.TrimPrefix(target, "http://")
	if target == endpoint && strings.Contains(target, "://") {
		return nil, fmt.Errorf("csi: unsupported agent endpoint %q", endpoint)
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig.Clone())))
	if err != nil {
		return nil, fmt.Errorf("csi: dial %s: %w", endpoint, err)
	}
	return &hostClient{
		endpoint: target,
		conn:     conn,
		volume:   koujav1.NewVolumeServiceClient(conn),
		admin:    koujav1.NewAdminServiceClient(conn),
	}, nil
}

func (host *hostClient) close() error {
	if host.conn == nil {
		return nil
	}
	return host.conn.Close()
}

func (host *hostClient) refresh(ctx context.Context) error {
	info, err := host.volume.GetInfo(ctx, &koujav1.GetInfoRequest{})
	if err != nil {
		return err
	}
	host.uuid = info.GetHostUuid()
	host.ready = info.GetReady()
	return nil
}
