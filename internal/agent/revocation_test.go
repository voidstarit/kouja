package agent

import (
	"context"
	"testing"

	"github.com/voidstarit/kouja/internal/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type testRevocationChecker struct{ revoked bool }

func (c testRevocationChecker) IsRevoked(string) bool { return c.revoked }

func TestInterceptorRejectsRevokedCertificate(t *testing.T) {
	ctx := WithIdentity(context.Background(), auth.Identity{
		Owner: "spiffe://kouja/admin/test", Role: auth.RoleAdmin, SerialNumber: "abc123",
	})
	handler := func(context.Context, any) (any, error) { return "ok", nil }
	response, err := interceptIdentity(ctx, nil, &grpc.UnaryServerInfo{}, handler, testRevocationChecker{revoked: true})
	if response != nil || status.Code(err) != codes.Unauthenticated {
		t.Fatalf("response = %#v, error = %v", response, err)
	}
	if _, err := interceptIdentity(ctx, nil, &grpc.UnaryServerInfo{}, handler, testRevocationChecker{}); err != nil {
		t.Fatalf("non-revoked error = %v", err)
	}
}
