# Client SDK

The repository ships a Go client for both agent transports:

| Artifact | Path | Generated |
|---|---|---|
| gRPC stubs and clients | `gen/kouja/v1/*.pb.go`, `*_grpc.pb.go` | `buf generate` |
| HTTP/JSON gateway | `gen/kouja/v1/*.pb.gw.go` | `buf generate` |
| OpenAPI 2.0 document | `gen/openapi/openapi.swagger.json` | `buf generate` |
| Typed gRPC and HTTP wrappers | `gen/client/client.gen.go` | `buf generate` |

The wrappers use the same protobuf request and response messages as the
generated stubs, so callers can switch transports without converting models.
The `protoc-gen-kouja-client` generator lives in
`hack/protoc-gen-kouja-client`; `make tools` installs it and `make proto`
regenerates `gen/client/client.gen.go` from the `google.api.http` annotations.

## gRPC

```go
package main

import (
	"context"
	"log"

	"github.com/voidstarit/kouja/gen/client"
	koujav1 "github.com/voidstarit/kouja/gen/kouja/v1"
)

func main() {
	conn, err := client.NewGRPCFromFiles(
		"storage.example:9443",
		"/etc/kouja/tls/client.crt",
		"/etc/kouja/tls/client.key",
		"/etc/kouja/tls/ca.crt",
	)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	ctx := context.Background()
	info, err := conn.GetInfo(ctx, &koujav1.GetInfoRequest{})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("host=%s version=%s ready=%t", info.GetHostUuid(), info.GetAgentVersion(), info.GetReady())
}
```

`NewGRPC` accepts a prepared `*tls.Config`; `NewGRPCFromFiles` loads the
certificate, key, and CA from disk. The target may be a plain
`host:port`, `https://host:port`, or a gRPC resolver target such as
`dns:///host:port`.

## HTTP/JSON

```go
package main

import (
	"context"
	"log"

	"github.com/voidstarit/kouja/gen/client"
	koujav1 "github.com/voidstarit/kouja/gen/kouja/v1"
	"google.golang.org/protobuf/proto"
)

func main() {
	httpClient, err := client.NewHTTPFromFiles(
		"https://storage.example:9443",
		"/etc/kouja/tls/client.crt",
		"/etc/kouja/tls/client.key",
		"/etc/kouja/tls/ca.crt",
	)
	if err != nil {
		log.Fatal(err)
	}
	defer httpClient.CloseIdleConnections()

	volume, err := httpClient.CreateVolume(context.Background(), &koujav1.CreateVolumeRequest{
		VolumeId:  "example-1",
		SizeBytes: 1 << 30,
		Vg:        "kouja",
		Export:    proto.Bool(true),
		Protocol:  koujav1.Protocol_PROTOCOL_ISCSI,
		Initiator: "iqn.2026-11.io.example:consumer",
	})
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("state=%s target=%s", volume.GetVolume().GetState(), volume.GetExport().GetTargetName())
}
```

The HTTP client implements every RPC in `volume.proto` and `admin.proto`,
including the admin operations. Requests and responses are encoded with
`protojson`, matching the agent's grpc-gateway surface.

Use `client.WithHTTPClient` to supply a custom `*http.Client` for timeouts,
proxies, tracing, or tests. Use `client.WithTLSConfig` to attach an mTLS
transport to the default client.

## Request IDs and errors

`client.WithRequestID` stores a correlation ID in the context. The gRPC client
sends it as `x-request-id` metadata and the HTTP client sends it as the
`X-Request-Id` header, matching the agent's request-ID propagation.

HTTP errors are returned as `*client.Error`. It exposes the HTTP status, the
gRPC code, the kouja reason, the operation ID, and any `google.rpc.Status`
details. It also implements `GRPCStatus()`, so `status.Code` and
`status.Convert` work across both transports:

```go
_, err := httpClient.GetVolume(ctx, &koujav1.GetVolumeRequest{VolumeId: "missing"})
if status.Code(err) == codes.NotFound {
	// handle a missing volume
}
```

Use `client.IsRetryable(err)` to decide whether a request is safe to retry.
The complete reason table, retry rules, and mutation idempotency guidance are
in [Client Errors and Retries](client-errors.md).

## Regenerating clients and the OpenAPI document

Install the pinned tools once, then regenerate:

```sh
make tools
make proto
```

`make proto` runs `buf lint` and `buf generate`. It writes the Go gRPC and
gateway code under `gen/kouja/v1` and merges the OpenAPI document into
`gen/openapi/openapi.swagger.json`. The OpenAPI title, description, and scheme
come from `proto/openapi.yaml`.
