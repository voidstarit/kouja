# OpenTelemetry metrics

`kouja-agent` exposes per-logical-volume metrics on its existing mTLS
`/metrics` endpoint. The service does not push metrics. An OpenTelemetry
Collector using the Prometheus receiver, or a Prometheus server, pulls them.

## Configuration

```yaml
observability:
  otel:
    enabled: true
    service_name: kouja-agent
```

OpenTelemetry metric instruments are disabled by default. When enabled, the
agent registers them with the same Prometheus registry as the existing kouja
metrics, so a single scrape returns both.

The endpoint remains protected by the agent's normal mTLS listener. Configure
the collector's Prometheus receiver with a client certificate trusted by the
agent's client CA, or place an authenticated proxy in front of `/metrics`.

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

A typical Prometheus receiver configuration:

```yaml
receivers:
  prometheus:
    config:
      scrape_configs:
        - job_name: kouja
          scheme: https
          metrics_path: /metrics
          static_configs:
            - targets: ["storage-host:9443"]
          tls_config:
            ca_file: /etc/kouja/client-ca.crt
            cert_file: /etc/kouja/collector.crt
            key_file: /etc/kouja/collector.key
```

The existing legacy kouja metrics remain available on the same endpoint.
