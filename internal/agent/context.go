package agent

import (
	"context"
	"errors"

	"github.com/voidstarit/kouja/internal/auth"
)

var ErrForbidden = errors.New("agent: forbidden")

type identityKey struct{}
type requestIDKey struct{}

func WithIdentity(ctx context.Context, identity auth.Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, identity)
}

func IdentityFromContext(ctx context.Context) (auth.Identity, bool) {
	identity, ok := ctx.Value(identityKey{}).(auth.Identity)
	return identity, ok
}

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, requestID)
}

func RequestIDFromContext(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey{}).(string)
	return value
}

func requireIdentity(ctx context.Context, roles ...auth.Role) (auth.Identity, error) {
	identity, ok := IdentityFromContext(ctx)
	if !ok || identity.Owner == "" {
		return auth.Identity{}, errors.New("agent: missing caller identity")
	}
	for _, role := range roles {
		if identity.Role == role {
			return identity, nil
		}
	}
	return auth.Identity{}, ErrForbidden
}
