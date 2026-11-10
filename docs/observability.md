# OpenTelemetry metrics

`kouja-agent` exposes per-logical-volume metrics using the OpenTelemetry
metrics SDK and a Prometheus exporter. Metrics are pull-based: a Prometheus
server or an OpenTelemetry Collector Prometheus receiver scrapes the agent; the
agent does not push metrics.

## Configuration

```yaml
observability:
  metrics:
    enabled: true
    address: 127.0.0.1:9100
  otel:
    enabled: true
    service_name: kouja-agent
```

- `observability.metrics.enabled` starts a dedicated plaintext HTTP listener
  for `/metrics`. It defaults to disabled and loopback-only.
- `observability.otel.enabled` registers the per-LV OpenTelemetry instruments.
  It defaults to disabled.
- The existing mTLS `/metrics` endpoint on port 9443 remains available for
  administrative and legacy scrapes.
- If the dedicated metrics listener is bound to a non-loopback address, protect
  it with a host firewall or network ACL. It has no authentication.

## Metrics per LV

Each metric carries these labels:

- `kouja.owner`
- `kouja.volume_id`
- `kouja.lv_name`
- `kouja.vg_name`
- `kouja.storage_class`
- `kouja.state`
- `kouja.export_protocol`, when exported

OpenTelemetry names (translated to Prometheus names by the exporter):

| OTel name | Prometheus name | Meaning |
| --- | --- | --- |
| `kouja.lv.requested_bytes` | `kouja_lv_requested_bytes` | Requested LV size |
| `kouja.lv.actual_bytes` | `kouja_lv_actual_bytes` | Actual LV size |
| `kouja.lv.observed_bytes` | `kouja_lv_observed_bytes` | Size observed on the host |
| `kouja.lv.present` | `kouja_lv_present` | `1` when the LV is present |
| `kouja.lv.exported` | `kouja_lv_exported` | `1` when an export exists |
| `kouja.lv.active_sessions` | `kouja_lv_active_sessions` | Active export sessions |
| `kouja.lv.info` | `kouja_lv_info` | Identity/state marker; always `1` |

Resource attributes include `service.name`, `service.version`,
`service.instance.id`, and `host.id` (the kouja host UUID).

The callback reads all non-deleted volume statuses, so scraping is not limited
to the admin UI's 256-row page. Cardinality scales with the number of LVs.

## Example collector scrape

A typical Prometheus receiver configuration for the dedicated listener:

```yaml
receivers:
  prometheus:
    config:
      scrape_configs:
        - job_name: kouja
          scheme: http
          metrics_path: /metrics
          static_configs:
            - targets: ["storage-host:9100"]
```

To scrape the mTLS listener instead, use `scheme: https`, port `9443`, and a
client certificate trusted by the agent's client CA.
