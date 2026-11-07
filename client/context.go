package client

import "context"

type requestIDKey struct{}

// WithRequestID returns a context that propagates requestID to the agent. The
// gRPC client sends it as x-request-id metadata and the HTTP client sends it
// as the X-Request-Id header.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, requestID)
}

// RequestIDFromContext returns the request ID stored by [WithRequestID].
func RequestIDFromContext(ctx context.Context) string {
	requestID, _ := ctx.Value(requestIDKey{}).(string)
	return requestID
}
