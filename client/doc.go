// Package client provides typed gRPC and HTTP/JSON clients for the kouja
// agent API.
//
// The gRPC client wraps the generated stubs in gen/kouja/v1. The HTTP client
// speaks the grpc-gateway REST/JSON surface defined by the google.api.http
// annotations in proto/kouja/v1. Both transports use mTLS with a client
// certificate trusted by the agent.
//
// Use [NewGRPC] or [NewGRPCFromFiles] for gRPC, and [NewHTTP] or
// [NewHTTPFromFiles] for HTTP/JSON. The OpenAPI description is generated at
// gen/openapi/openapi.swagger.json.
package client
