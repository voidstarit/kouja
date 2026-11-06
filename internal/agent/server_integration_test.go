package agent

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voidstarit/kouja/gen/kouja/v1"
	"github.com/voidstarit/kouja/internal/config"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

func writeTestCertificates(t testing.TB) (caFile, serverCert, serverKey, clientCert, clientKey string) {
	t.Helper()
	dir := t.TempDir()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	caFile = filepath.Join(dir, "ca.crt")
	writePEM(t, caFile, "CERTIFICATE", caDER)

	serverKeyFile, serverCertFile := issueCertificate(t, dir, caCert, caKey, "server", true)
	clientKeyFile, clientCertFile := issueCertificate(t, dir, caCert, caKey, "spiffe://kouja/caller/test", false)
	return caFile, serverCertFile, serverKeyFile, clientCertFile, clientKeyFile
}

func issueCertificate(t testing.TB, dir string, ca *x509.Certificate, caKey *rsa.PrivateKey, identity string, server bool) (keyFile, certFile string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
	}
	if server {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		template.DNSNames = []string{"localhost"}
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	} else {
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		uri, err := url.Parse(identity)
		if err != nil {
			t.Fatal(err)
		}
		template.URIs = []*url.URL{uri}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	label := "client"
	if server {
		label = "server"
	}
	certFile = filepath.Join(dir, fmt.Sprintf("%s-%d.crt", label, time.Now().UnixNano()))
	keyFile = filepath.Join(dir, fmt.Sprintf("%s-%d.key", label, time.Now().UnixNano()))
	writePEM(t, certFile, "CERTIFICATE", der)
	keyDER := x509.MarshalPKCS1PrivateKey(key)
	writePEM(t, keyFile, "RSA PRIVATE KEY", keyDER)
	return keyFile, certFile
}

func writePEM(t testing.TB, path, blockType string, data []byte) {
	t.Helper()
	file, err := os.Create(path) //nolint:gosec // temporary test certificate path
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err := pem.Encode(file, &pem.Block{Type: blockType, Bytes: data}); err != nil {
		t.Fatal(err)
	}
}

func bddTestServerServesGRPCAndHTTPOnOneTLSListener(t testing.TB) {
	caFile, serverCert, serverKey, clientCert, clientKey := writeTestCertificates(t)
	cfg := &config.Config{}
	cfg.Listen.Address = "127.0.0.1:0"
	cfg.Listen.TLS.CertFile = serverCert
	cfg.Listen.TLS.KeyFile = serverKey
	cfg.Listen.TLS.ClientCAFile = caFile
	volumeService := NewVolumeService(nil, nil, nil, nil, ServiceOptions{HostUUID: "host-1", AgentVersion: "test", APIVersion: "v1"})
	adminService := NewAdminService(nil, nil)
	server, err := NewServer(cfg, volumeService, adminService)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(server.Stop)
	go func() { _ = server.Serve() }()
	address := server.Address().String()

	clientCertificate, err := tls.LoadX509KeyPair(clientCert, clientKey)
	if err != nil {
		t.Fatal(err)
	}
	caPEM, err := os.ReadFile(caFile) //nolint:gosec // temporary test CA path
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("failed to add CA")
	}
	tlsConfig := &tls.Config{
		RootCAs:      pool,
		Certificates: []tls.Certificate{clientCertificate},
		ServerName:   "127.0.0.1",
		MinVersion:   tls.VersionTLS13,
	}

	httpClient := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsConfig}}
	response, err := httpClient.Get("https://" + address + "/v1/info")
	if err != nil {
		t.Fatalf("HTTP gateway: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "host-1") {
		t.Fatalf("HTTP response %d: %s", response.StatusCode, body)
	}

	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	if err != nil {
		t.Fatalf("gRPC client: %v", err)
	}
	defer func() { _ = connection.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := koujav1.NewVolumeServiceClient(connection).GetInfo(ctx, &koujav1.GetInfoRequest{})
	if err != nil {
		t.Fatalf("gRPC GetInfo: %v", err)
	}
	if info.GetHostUuid() != "host-1" {
		t.Fatalf("gRPC host = %q", info.GetHostUuid())
	}
}
