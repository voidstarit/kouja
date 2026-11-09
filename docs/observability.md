# OpenTelemetry metrics

`kouja-agent` can export per-logical-volume metrics over OTLP/gRPC in addition
to the existing Prometheus endpoint on `/metrics`.

## Configuration

```yaml
observability:
  otel:
    enabled: true
    endpoint: otel-collector:4317
    insecure: false
    interval_seconds: 30
    service_name: kouja-agent
    headers:
      x-scope-orgid: kouja
```

- `endpoint` is an OTLP/gRPC host and port. If empty, the exporter uses the
  standard OpenTelemetry endpoint/environment defaults.
- Set `insecure: true` for a plaintext collector, typically on localhost or a
  trusted monitoring network.
- `headers` is optional metadata sent with every export.
- OpenTelemetry is disabled by default and never blocks agent startup. If the
  exporter cannot be created, the agent logs the error and continues serving.

## Metrics per LV

Each metric has these attributes:

- `kouja.owner`
- `kouja.volume_id`
- `kouja.lv_name`
- `kouja.vg_name`
- `kouja.storage_class`
- `kouja.state`
- `kouja.export_protocol`, when the LV is exported

Metrics:

| Metric | Type | Meaning |
| --- | --- | --- |
| `kouja.lv.requested_bytes` | int64 gauge | Requested LV size |
| `kouja.lv.actual_bytes` | int64 gauge | Actual LV size |
| `kouja.lv.observed_bytes` | int64 gauge | Size observed on the host |
| `kouja.lv.present` | int64 gauge | `1` when the LV is present, otherwise `0` |
| `kouja.lv.exported` | int64 gauge | `1` when an export exists, otherwise `0` |
| `kouja.lv.active_sessions` | int64 gauge | Observed active export sessions |
| `kouja.lv.info` | int64 gauge | Identity/state marker; always `1` |

Resource attributes include `service.name`, `service.version`,
`service.instance.id`, and `host.id` (the kouja host UUID).

The callback reads all non-deleted volume statuses, so metrics are not limited
to the admin UI's 256-row page. Cardinality scales with the number of LVs; keep
that in mind when retaining metrics.
