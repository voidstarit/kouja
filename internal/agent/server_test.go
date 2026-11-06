package agent

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net/url"
	"testing"
	"time"

	"github.com/voidstarit/kouja/internal/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func testCertificate(t testing.TB, san string) *x509.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := url.Parse(san)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{uri},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}

func bddTestUnaryInterceptorPropagatesIdentityAndRequestID(t testing.TB) {
	certificate := testCertificate(t, "spiffe://kouja/caller/test")
	ctx := peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}}},
	})
	ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("x-request-id", "req-1"))
	response, err := UnaryInterceptor(ctx, "request", nil, func(ctx context.Context, _ any) (any, error) {
		identity, ok := IdentityFromContext(ctx)
		if !ok || identity.Owner != "spiffe://kouja/caller/test" || identity.Role != auth.RoleCaller {
			t.Fatalf("identity = %#v", identity)
		}
		if RequestIDFromContext(ctx) != "req-1" {
			t.Fatalf("request id = %q", RequestIDFromContext(ctx))
		}
		return "response", nil
	})
	if err != nil {
		t.Fatalf("interceptor: %v", err)
	}
	if response != "response" {
		t.Fatalf("response = %#v", response)
	}
}

func bddTestUnaryInterceptorRejectsMissingOrInvalidCertificate(t testing.TB) {
	if _, err := UnaryInterceptor(context.Background(), nil, nil, func(context.Context, any) (any, error) {
		return nil, nil
	}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing peer code = %v, want Unauthenticated", status.Code(err))
	}
	certificate := testCertificate(t, "spiffe://example.com/not-kouja")
	ctx := peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate}}},
	})
	if _, err := UnaryInterceptor(ctx, nil, nil, func(context.Context, any) (any, error) {
		return nil, nil
	}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("invalid SAN code = %v, want Unauthenticated", status.Code(err))
	}
}
