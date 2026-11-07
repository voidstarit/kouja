# Client Errors and Retries

The Go client exposes the agent's gRPC status codes and `google.rpc.ErrorInfo`
details through both transports.

- gRPC returns a standard `*status.Status`.
- HTTP returns `*client.Error`, which implements `GRPCStatus()`.
- `client.ErrorInfo(err)` extracts the `kouja.v1` `ErrorInfo` from either transport.
- `client.IsRetryable(err)` applies the server's `retryable` metadata, with a
  code-based fallback when metadata is absent.

Use `IsRetryable` instead of deciding from the gRPC code alone. The same code can
be retryable in one phase and not another. For example, `Unavailable` during
execution may be retryable, while `Unavailable` from a request guard may not.

## Error metadata

The agent sets `ErrorInfo.domain` to `kouja.v1`. The metadata contains:

| Field | Meaning |
|---|---|
| `retryable` | `"true"` or `"false"` according to the server |
| `operation_id` | Journaled operation ID, when the error came from an operation |

HTTP error bodies are `google.rpc.Status` JSON objects with the same details.

## Retry decision table

`Retryable` below is the value the server puts in `ErrorInfo.metadata`. When the
metadata is absent, `IsRetryable` falls back to the gRPC code.

| Reason | gRPC code | HTTP | Server `retryable` | Guidance |
|---|---|---|---|---|
| `INVALID_ARGUMENT` | `InvalidArgument` | 400 | no | Fix the request. |
| `NOT_FOUND` | `NotFound` | 404 | no | Check the volume, VG, or owner. |
| `FORBIDDEN` | `PermissionDenied` | 403 | no | Fix caller identity or ownership. |
| `UNAUTHENTICATED` | `Unauthenticated` | 401 | no | Fix or renew the client certificate. |
| `VOLUME_IN_DRIFT` | `FailedPrecondition` | 400 | no | Resolve drift through the admin API. |
| `VOLUME_ALREADY_EXPORTED` | `FailedPrecondition` | 400 | no | Use the existing initiator or unexport first. |
| `VOLUME_REQUEST_MISMATCH` | `FailedPrecondition` | 400 | no | Reconcile with the existing volume; resize separately. |
| `SHRINK_NOT_SUPPORTED` | `FailedPrecondition` | 400 | no | Request a grow operation instead. |
| `SESSION_ACTIVE` | `FailedPrecondition` | 400 | no | Log out or disconnect the initiator, then retry. |
| `SESSION_UNKNOWN` | `FailedPrecondition` | 400 | no | Inspect target sessions; admin intervention may be needed. |
| `OPERATION_NEEDS_ATTENTION` | `FailedPrecondition` | 400 | no | Resolve the operation through the admin API. |
| `OPERATION_IN_PROGRESS` | `FailedPrecondition` | 400 | yes | Retry with the same `request_id` after a backoff. |
| `CONFLICT` | `Aborted` | 409 | no | Resolve the conflicting state. |
| `RESOURCE_EXHAUSTED` | `ResourceExhausted` | 429 | no | Wait for capacity or headroom; do not hot-loop. |
| `ADMISSION_REJECTED` | `ResourceExhausted` | 429 | no | Back off and reduce concurrent operations. |
| `UNAVAILABLE` | `Unavailable` | 503 | sometimes | Retry with backoff only when `IsRetryable` is true. |
| `DEADLINE_EXCEEDED` | `DeadlineExceeded` | 504 | sometimes | Retry idempotent requests with backoff when `IsRetryable` is true. |
| `CANCELED` | `Canceled` | 499 | no | The caller canceled; retry only intentionally. |
| `INTERNAL` | `Internal` | 500 | no | Report the failure; retry only for transient infrastructure issues. |
| `UNIMPLEMENTED` | `Unimplemented` | 501 | no | Do not retry; check the agent version and API support. |
| `RETRY_UNAVAILABLE` | `Internal` | 500 | no | Do not retry blindly; inspect the operation. |
| `OPERATION_SUPERSEDED` | `Internal` | 500 | no | Do not retry; inspect the latest operation. |

HTTP status mapping follows the grpc-gateway defaults. In particular,
`FailedPrecondition` is HTTP 400, not 412 or 409.

## Retryable operations

Read-only calls are safe to retry:

- `GetInfo`
- `GetCapacity`
- `ListStorageClasses`
- `GetVolume`
- `ListVolumes`
- `GetCredential`
- `ListVGs`
- `ListOrphans`

For mutations, reuse the same `request_id` when retrying:

- `CreateVolume`
- `ExportVolume`
- `UnexportVolume`
- `ResizeVolume`
- `DeleteVolume`
- `RotateCredential`
- `RegisterVG`
- `DeregisterVG`
- `ImportVolume`
- `ResolveDrift`
- `WipeVolume`
- `TransferOwnership`
- `AdminDeleteVolume`
- `GrantCredential`
- `RevokeGrant`

Reusing the `request_id` lets the agent replay the journaled operation and
return the original result instead of starting a second operation. Do not retry
a mutation with a new ID unless you have checked the volume's current state.

## Special cases

### `SESSION_ACTIVE`

`SESSION_ACTIVE` is a precondition failure, not an immediately retryable
error. Retrying the same request without disconnecting the initiator will fail
again. Log the session out, then retry the unexport, delete, or rotate request.

### `RESOURCE_EXHAUSTED` and `ADMISSION_REJECTED`

These are not hot-loop retries. Wait for capacity to be freed or concurrency to
drop, then try again with backoff. The agent marks these guards
`retryable=false`.

### `UNAVAILABLE` and `DEADLINE_EXCEEDED`

These are sometimes retryable. The server marks execution failures retryable
when the operation can be replayed, but guard or observation failures may be
marked false. Always use `IsRetryable`.

### Transport errors

If no agent response was received, `IsRetryable` cannot classify the error. A
mutating request may still have been applied. Retry with the same `request_id`
and use `GetVolume` or `ListVolumes` to reconcile when possible.

## HTTP example

```go
volume, err := httpClient.CreateVolume(ctx, request)
if err != nil {
	var clientErr *client.Error
	if errors.As(err, &clientErr) {
		log.Printf(
			"code=%s reason=%s retryable=%t operation=%s message=%s",
			clientErr.Code,
			clientErr.Reason,
			client.IsRetryable(err),
			clientErr.OperationID,
			clientErr.Message,
		)
	}
	if client.IsRetryable(err) {
		// Back off, then retry with the same request.RequestId.
	}
}
```

`clientErr.Retryable` is the raw server metadata. `clientErr.IsRetryable()` and
`client.IsRetryable(err)` apply the code-based fallback when metadata is absent.

## gRPC example

```go
_, err := grpcClient.CreateVolume(ctx, request)
if err != nil {
	info, ok := client.ErrorInfo(err)
	if ok {
		log.Printf("reason=%s retryable=%t operation=%s", info.GetReason(), client.IsRetryable(err), info.GetMetadata()["operation_id"])
	}
	if client.IsRetryable(err) {
		// Back off, then retry with the same request.RequestId.
	}
}
```

`client.IsRetryable` works with the raw gRPC status returned by the generated
stubs and with `*client.Error` from the HTTP client.
