# kouja Local Admin UI

*   **Author:** kouja maintainers
*   **Status:** Proposed
*   **Date:** 2026-11-07

--------------------------------------------------------------------------------

## Overview

`kouja-agent` gets a second HTTP listener that serves a local administration UI.
The listener is separate from the existing mTLS gRPC/HTTP API, binds to a
loopback address, and carries no application-level authentication. It is
disabled by default and enabled explicitly by the administrator. The UI is
written with Preact and TypeScript, built with Vite, and compiled into the
agent binary with `go:embed`. The production binary needs no separate static
server, CDN, or runtime asset directory; Node.js is required only to build the
full UI.

The UI covers two jobs:

1.  **Configuration.** Show the effective running configuration and the raw
    YAML file, let the administrator edit and save the file, and show whether
    the disk configuration differs from the effective running configuration.
    The UI never restarts the service. It reports that a restart is required
    only when a binding socket changes: the mTLS listener address, the admin
    listener enabled flag or (when enabled in either the running or candidate
    configuration) its address, or a target portal address/port or protocol
    enable flag on an enabled target.
2.  **Status and live storage administration.** Show agent readiness, host
    identity, version and uptime, volume groups, per-volume state, serving
    status, active sessions, session state, drift reasons, observations, and
    problem operations. Show live iSCSI read/write traffic, command counts,
    per-initiator session traffic, client IP/port connections, and
    target/LUN error and reset counters.
    Provide live volume-group registration and deregistration through the
    shared VG logic used by the existing admin service, and live storage-class
    mapping changes through a new local class registry. These actions do not
    restart the service.

The existing mTLS API remains the surface for volume lifecycle operations,
credential handling, wipe, import, drift resolution, and ownership transfer.
The local UI does not create, export, resize, or delete volumes.

The listener is a network boundary, not a user boundary. Any local process that
can reach the loopback port can read the configuration, see volume ownership and
initiators, rewrite the root-owned configuration, register or deregister volume
groups, and change class mappings. The accepted deployment precondition is a
single-administrator host with no untrusted local users, no host-network
containers, and no SSRF-capable local service. The installer and README must
state this precondition.

## The Problem

An administrator currently needs several unrelated tools to answer basic
questions about one storage host:

*   `sudoedit /etc/kouja/config.yaml` and `systemctl restart kouja-agent` to
    change configuration.
*   `systemctl status kouja-agent` and `journalctl -u kouja-agent` to see
    whether the process is alive and why it failed.
*   `curl` with a client certificate to call `/v1/info`, `/v1/capacity`,
    `/v1/volumes`, or the admin VG endpoints.
*   Direct SQLite queries, or an understanding of the state machine, to find
    volumes in `drift`, failed operations, or active sessions.

The mTLS API is designed for callers, not for a person at the console. It
requires certificate material, produces JSON, and separates configuration from
observation. There is no single local view that answers "is this host healthy,
what capacity does it have, which volumes are not in a normal state, what is
running, and what configuration is actually loaded?"

A local UI reduces that work. It also makes configuration errors visible before
a restart: the administrator can validate the file, see the running
configuration alongside the file on disk, and keep the old text if validation
fails. Volume groups and class mappings are common administrative changes. VG
registration/deregistration reuses the existing admin service; class mappings
use a new live registry. Neither restarts the whole storage service.

## Goals

*   Serve one embedded UI from `kouja-agent` on a separate loopback-only HTTP
    listener. The listener is disabled by default, enabled explicitly, and
    must never bind to a non-loopback address. An admin listener bind or serve
    failure is logged and does not stop the mTLS API or the storage data path.
*   Show the effective running configuration and the raw YAML file on disk.
    Report `config_changed` when the parsed disk file differs from the
    effective running configuration. Report `restart_required` only when a
    binding socket
    field changes. Never restart the service from the UI.
*   Validate a proposed configuration with the existing `config.Parse` path
    before writing it. Reject invalid YAML, unknown fields, relative paths, and
    invalid values without changing the file. Missing runtime files such as TLS
    material are preflight warnings, not parse errors.
*   Write an accepted configuration atomically with mode `0600`, keep the
    previous disk file as `.bak`, and keep the startup configuration that
    reached readiness as `.last-good` when the admin UI is enabled. Saves and
    live class writes do not refresh `.last-good`; it is the recovery point for
    the configuration that last reached readiness.
*   Register and deregister volume groups live through the existing admin
    service logic. Add and remove storage-class mappings live. These actions
    persist across restarts and do not restart the service.
*   Show agent readiness, version, host UUID, supported protocols, uptime, the
    configuration path, and the live configuration state.
*   Show every registered volume group and every configured-but-unregistered
    volume group, with storage class, UUID, state, total, free, allocatable,
    extent, and maximum-volume capacity.
*   Show each volume with owner, volume ID, resource UUID, LV name, VG and
    class, requested and actual size, state, drift reasons, serving status,
    export protocol and initiator, target/namespace/ACL presence, active
    sessions (or unknown), session state, ACL/observed initiator, observation
    age, and last update time.
*   Show iSCSI LUN commands and coarse read/written totals, per-initiator
    session bytes and PDU counts, target login/logout/session errors, LUN
    aborts/resets, derived read/write/command rates, and established client
    IP/port connections. Metrics are read from LIO configfs plus
    `/proc/net/tcp(6)` and are best-effort; metrics unavailability never fails
    the status response.
*   Allow an operator to disable a live iSCSI target from the target table.
    Disabling force-unexports the target, drops active sessions, and leaves the
    volume and export definition in SQLite in the existing `available`/
    `unexported` state. It does not delete the volume or target definition.
*   Show up to 256 volumes and up to 64 problem operations. When the result is
    truncated, show the total counts and a visible truncation notice.
*   Show running, failed, and `needs_attention` operations with action, owner,
    volume, resource UUID, attempt count, deadline, and last error code.
*   Keep the existing mTLS API, state model, operation semantics, and volume
    data path unchanged. No schema migration is added by this feature.
*   Keep the agent a single runtime binary. `make build` builds the full Preact
    UI and embeds it. `make build-go` builds the Go binary with the fallback
    page and does not require Node.js. The runtime needs neither Node.js nor a
    static asset directory.
*   Reject non-loopback listener addresses at configuration validation. Set
    Host, Origin, `Sec-Fetch-Site`, and content-type checks on the admin HTTP
    server, emit no CORS headers, and never return secret values.

## Non-Goals

*   Authentication, authorization, user accounts, roles, login pages, or
    sessions on the admin listener. The listener is loopback-only.
*   TLS on the admin listener. Traffic stays inside the host.
*   Remote access from another machine. The listener must not bind to a public
    or private network address or be exposed through a proxy or port forward.
*   Restarting, stopping, or supervising the service from the UI. The UI only
    reports that a binding socket changed; the operator restarts the service
    with `systemctl` or the environment's supervisor.
*   Automatic restart, process re-exec, manual-exit status 75, or restart
    tracking/audit. The UI does not perform or observe restarts.
*   Volume lifecycle operations, credential display or rotation, wipe, import,
    drift resolution, or ownership transfer through the UI. Those remain on the
    mTLS API.
*   Live in-place reload of arbitrary configuration fields. The UI applies
    volume-group and class changes through the admin service; other fields are
    written to the file and take effect according to their owning subsystem or
    the operator's restart decision.
*   A structured form editor for every configuration field. The configuration
    tab edits raw YAML.
*   Fleet-wide or multi-host views. One UI serves one host.
*   A committed frontend bundle. Vite output is generated at build time and
    ignored by Git.
*   A browser-side router, a state-management library, or an external component
    library. The UI uses Preact and Preact hooks only.
*   Log streaming, metrics graphs, backup management, or a full audit browser.
    The current version shows cumulative counters and simple sampled rates,
    not historical graphs.
*   NVMe/TCP traffic counters. nvmet reports session state unknown on the
    supported kernel; iSCSI traffic counters are included.
*   New tables or columns for UI state. Status reads use the existing tables.
*   Automatic removal of a VG when its configuration entry is removed. Removal
    is an explicit live admin action.

## Open Questions

None. The restart policy is fixed: the UI never restarts the service; only
binding socket changes set `restart_required`. NVMe/TCP traffic counters remain
deferred because nvmet does not expose the equivalent per-session data.

## Terminology

*   **Startup configuration:** The parsed `config.Config` loaded at process
    start from `--config`.
*   **Effective running configuration:** The startup configuration with
    `Storage.Classes` overlaid by the live `ClassRegistry` snapshot. Services
    and `running_yaml` use this view.
*   **Disk configuration:** The YAML file at `--config`, parsed on demand.
*   **Config changed:** `disk_valid` is false, or the parsed disk configuration
    differs from the effective running configuration. List order that is
    semantically meaningless (for example `storage.vgs`) is normalized; target
    portal order is meaningful and is not normalized.
*   **Binding socket change:** A change to `listen.address`,
    `admin_ui.enabled`, an `admin_ui.address` change where the admin listener
    is enabled in the running or candidate configuration, a protocol enable
    flag, or a portal address/port change where the target is enabled in the
    running or candidate configuration. A listener disabled in both is
    config-only. These are the only fields that set `restart_required`;
    `restart_required` is not a general “any startup-bound change” flag.
    Non-socket startup-bound changes produce warnings only.
*   **Drift:** A mismatch between stored intent and the observed LV or target
    state. The reconciler records drift; the agent does not repair it
    automatically.
*   **Observation:** The reconciler's last snapshot of LV presence, size, and
    device path. It can be up to one reconcile interval old.
*   **Export observation:** The reconciler's last snapshot of target,
    namespace, ACL, session state, and session count.
*   **Serving:** The volume and export are stored as `exported`, the LV is
    present, the target and ACL are present, the namespace is present for
    `nvme-tcp`, and the observations are fresh.
*   **Storage class label:** `storage_class` is the current class-to-VG mapping
    label for a volume's VG. A live class remap relabels existing volumes in
    status and mTLS responses; it does not change the volume's VG or data
    placement.
*   **Capacity admission:** The mTLS `GetCapacity` path calls
    `Admission.AcquireCapacity`, a one-token burst rate limiter. The local
    status path does not call it because it performs no allocation.
*   **mTLS admin API:** The existing authenticated `AdminService` on the mTLS
    listener. It is distinct from the local admin UI.

## Critical User Journeys (CUJs)

### CUJ 1: Administrator checks host status

*   **Given**: `kouja-agent` is running, the admin listener is enabled on
    `127.0.0.1:9444`, and the administrator is logged in to the host.
*   **When**: The administrator opens `http://127.0.0.1:9444` and selects the
    status tab.
*   **Then**: The page shows readiness, version, host UUID, uptime, each VG's
    capacity, each volume's state, serving status, drift reasons, active
    sessions or unknown, session state, observations, and problem operations.
    No client certificate is requested.

### CUJ 2: Administrator edits and saves configuration

*   **Given**: The agent is running from `/etc/kouja/config.yaml` with a known
    good configuration.
*   **When**: The administrator edits the YAML and saves.
*   **Then**: The server parses the full file. On success it writes `.bak`
    atomically, writes the new file atomically, and returns the new ETag plus
    `config_changed` and `restart_required`. If a binding socket changed,
    `restart_required` is true and the page tells the administrator to restart
    the service manually. If no binding socket changed, the page says the file
    was saved and the UI does not restart anything. Validation failure returns
    the parser error and leaves the file and running process unchanged.

### CUJ 3: Invalid configuration is rejected

*   **Given**: The administrator submits malformed YAML, an unknown field, a
    relative path, or an invalid timeout.
*   **When**: The save request reaches the server.
*   **Then**: The server returns `422`, does not write the file, does not change
    the running process, and shows the error in the editor.

### CUJ 4: A bad but valid configuration fails on manual restart

*   **Given**: The new configuration parses but prevents the agent from starting,
    and the admin UI was previously enabled and reached readiness so
    `.last-good` exists.
*   **When**: The operator restarts the service manually and it fails.
*   **Then**: The UI is unavailable. The operator copies the failed file to
    `config.yaml.failed`, restores `config.yaml.last-good`, and starts the
    service. `.last-good` is the startup configuration, not the latest disk
    file; live class changes made after startup may need to be reapplied from
    the failed file. `.bak` holds the revision before the last config write and
    may be older than the failed file. If `.last-good` does not exist (first
    enablement), the operator uses the installer starter, `.bak`, or a shell
    backup.

### CUJ 5: Local network exposure is refused

*   **Given**: The host has a network interface with address `192.0.2.10`.
*   **When**: A client on another host connects to `192.0.2.10:9444`.
*   **Then**: The connection is refused because the listener is bound to a
    loopback address. The mTLS API on `0.0.0.0:9443` is unaffected.

### CUJ 6: Admin listener is disabled

*   **Given**: The configuration leaves `admin_ui.enabled` at `false`.
*   **When**: The agent starts.
*   **Then**: No admin port is opened and no `.last-good` file is written. The
    mTLS API, storage data path, and existing VG seed behavior are unchanged.

### CUJ 7: Operator applies a binding socket change

*   **Given**: The administrator changes `listen.address`, a target portal on
    an enabled target, or the admin listener enabled/address state while the
    admin listener is enabled in the running or candidate configuration.
*   **When**: The save succeeds.
*   **Then**: The response sets `restart_required: true` with a reason. The UI
    shows a banner and does not restart. The operator runs
    `systemctl restart kouja-agent` (or the environment's supervisor command).
    After the process returns, the new listener is used and the UI is reached at
    the new address if the admin address changed.

### CUJ 8: Administrator manages VGs and classes live

*   **Given**: The agent is running and the admin UI is open.
*   **When**: The administrator registers a new VG, deregisters an empty VG, or
    adds/removes a class mapping.
*   **Then**: The admin UI calls the shared VG service used by the mTLS admin
    API. Class mappings use the new local class registry; the mTLS API has no
    class mutation RPC. The VG or class change applies immediately, is
    persisted, and does not restart the service. A VG with volumes or a class
    mapping cannot be deregistered. A class change updates the config file and
    the live registry.

### CUJ 9: Administrator sees a config change without a restart

*   **Given**: The administrator changes a non-socket field, such as a timeout
    or headroom.
*   **When**: The save succeeds.
*   **Then**: `config_changed` is true and `restart_required` is false. The page
    says the file was saved. The UI does not set `restart_required` or perform a
    restart for non-socket fields; startup-bound warnings may note that a
    manual restart could be needed to apply them.

--------------------------------------------------------------------------------

## Technical Plan (Architecture)

### Architecture at a glance

```text
  browser on the host
          |
          | http://127.0.0.1:9444  (plain HTTP, no auth)
          v
  +---------------------------------------------------------------+
  | kouja-agent                                                   |
  |                                                               |
  |  admin HTTP server  ---> embedded assets (index.html, js, css)|
  |        |                                                      |
  |        +---> config reader/writer ---> /etc/kouja/config.yaml |
  |        |                                  + .bak, .last-good   |
  |        |                                                      |
  |        +---> status reader ---> SQLite + LVM capacity          |
  |        |                                                      |
  |        +---> VG/class service ---> state + LVM + config       |
  |                                                               |
  |  mTLS gRPC/HTTP API on 0.0.0.0:9443  (unchanged)               |
  +---------------------------------------------------------------+

  callers, CSI controller, CSI node ---> mTLS API ---> existing path
```

The admin listener is a second `http.Server` inside the same process. It does
not share the mTLS listener, its TLS configuration, or the gRPC gateway. It
shares the process, the SQLite store, the LVM backend, the VG/class service,
and the lifecycle context.

### Relationship to the existing design

`docs/designs/kouja.md` Alternative N rejected a separate admin listener and
said the admin API shares the mTLS listener with an admin certificate role.
This design supersedes that decision only for the local configuration and
status UI. The mTLS `AdminService` remains the authenticated API for volume and
administrative operations. The new loopback listener is disabled by default,
carries no application authentication, and serves only the two local views and
the live VG/class actions described here. The mTLS data path, caller API, and
systemd deployment are unchanged.

The base design's VG model remains: `InitializeVGs` seeds
`cfg.EffectiveVGs()` on the first start (`storage.vgs` plus every VG referenced
by `storage.classes`) and the admin API is authoritative afterward. This design
does not change `InitializeVGs`. It exposes the existing register/deregister
operations through the local UI and adds a live class registry. Removing a VG
from the YAML does not deregister it; use the UI or the mTLS admin API. After
the first start, adding a class for a new VG does not register that VG; use the
VG panel first.

### Component responsibilities

*   **`internal/config`**: owns `admin_ui`, `LoadWithData`, the shared
    `StorageClassForVG` helper, and a structured `storage.classes` updater that
    preserves YAML ordering and comments for live class changes.
*   **`internal/agent/vg_service.go`**: a new shared VG service used by the
    mTLS `AdminService` and the local UI. It owns register, deregister, and list
    logic, including eligibility, capacity, state-store writes, and the
    "VG still has volumes" guard.
*   **`internal/agent/class_registry.go`**: a new live class registry with a
    read/write mutex. It resolves classes for volume creation and reports class
    labels for status. It persists changes through the config updater.
*   **`internal/state`**: new read-only queries for status (volume status rows,
    state counts, problem operations, and operation count). No schema migration.
*   **`internal/adminui`**: the admin HTTP server, embedded assets, config
    writer, audit writer, status assembly, and VG/class handlers.
*   **`web/`**: Preact/TypeScript/Vite source. It has a status tab, a
    configuration tab, and live VG/class panels. No router or component library.
*   **`Makefile`**: `ui` builds the frontend; `build` builds the full binary;
    `build-go` builds the Go binary with the fallback page and no Node.
*   **`.github/workflows/ci.yml`**: Node 22.12 for `make build`, plus the
    existing Go checks.
*   **`cmd/kouja-agent/main.go`**: wires the admin server, the shared VG
    service, and the live class registry. Admin listener failures are
    non-fatal. No restart path.
*   **`internal/agent/startup.go`**: not changed. It keeps the first-start VG
    seed and VG verification.

The local UI does not call the mTLS gRPC services. It calls the shared VG
service and the new class registry, and reads the state store and LVM backend
directly. Volume lifecycle mutations remain on the mTLS services. Class
mutations are local-UI-only; remote automation can edit `storage.classes` and
restart, or use a future mTLS class RPC.

### Configuration schema

The configuration file gains one optional section:

```yaml
admin_ui:
  enabled: false
  address: 127.0.0.1:9444
  client_cert_dir: /etc/kouja/tls/clients
```

Rules:

*   `enabled` defaults to `false`. When false, no admin listener is created,
    no `.last-good` is written, and no UI goroutine runs.
*   `address` defaults to `127.0.0.1:9444`. The host must be the exact IP
    literal `127.0.0.1` or `::1`. The rest of `127.0.0.0/8` and hostnames are
    rejected as bind addresses. The port must be numeric from 1 through 65535.
*   `client_cert_dir` defaults to `<directory of listen.tls.cert_file>/clients`
    and must be an absolute path when set. The local UI may list and download
    regular `.crt`, `.key`, and `.pem` files directly inside this directory;
    symlinks and files outside it are never served. Do not point it at the TLS
    root, CA key, or server key directory.
*   The loopback rule is validated whenever the section is present, even when
    `enabled` is false. An invalid `admin_ui` section is a configuration error
    and fails startup. The "non-fatal admin listener" rule applies only after
    configuration parsing.
*   The file path comes from `--config`; it is not a configuration field. The
    admin server receives the same path.
*   `InitializeVGs` seeds `cfg.EffectiveVGs()` on the first start:
    `storage.vgs` plus every VG referenced by `storage.classes`. After that,
    `storage.vgs` is not a live registration source. `storage.classes` is the
    initial class map, loaded on every start. VG register/deregister is
    available on the admin UI and the mTLS admin API; class set/remove is
    local-UI-only. Removing a VG from the YAML does not deregister it.
*   Existing configuration files remain valid; the new section is optional.
*   The accepted deployment precondition is a single-administrator host with no
    untrusted local users, no host-network containers, and no SSRF-capable
    local service. The installer and README must state it.

### Binding socket detection

`restart_required` is true only when one of these fields differs between the
running and parsed disk configurations:

*   `listen.address`;
*   `admin_ui.enabled`;
*   `admin_ui.address`, but only when the admin listener is enabled in the
    running or candidate configuration;
*   `targets.iscsi.enabled`;
*   `targets.iscsi.portals` (address and port values, order-insensitive), but
    only when iSCSI is enabled in the running or candidate configuration;
*   `targets.nvme_tcp.enabled`;
*   `targets.nvme_tcp.portals` (address and port values, order-insensitive), but
    only when NVMe/TCP is enabled in the running or candidate configuration.

`config_changed` is true when any parsed field differs, after normalizing
`storage.vgs` as a set (sort and deduplicate) and treating map keys
order-insensitively. Comments, indentation, and mapping key order do not count;
target portal order is meaningful and counts as a change. The response includes
one deterministic `restart_reason` when multiple socket fields change, using
this precedence: `listen_address_changed`, `admin_enabled_changed`,
`admin_address_changed`, `iscsi_enabled_changed`, `iscsi_portals_changed`,
`nvme_tcp_enabled_changed`, `nvme_tcp_portals_changed`. If a listener is
disabled in both running and candidate configurations, its address/portal
change does not produce a reason. No other field sets `restart_required`.

### HTTP interface

| Method | Path | Success | Error statuses | Notes |
|---|---|---|---|---|
| `GET` | `/` | `200` | `405`, `503` | Embedded `index.html`, or the fallback page with `503`. |
| `GET` | `/assets/*` | `200` | `404`, `405` | Hashed Vite bundle only. Directory and dot-file requests return `404`. |
| `GET` | `/favicon.ico` | `204` | `405` | Empty response. |
| `GET` | `/api/status` | `200` | `500`, `503` | `503` only when SQLite cannot be read; `500` only if the running configuration cannot be marshaled. Partial VG/config/metrics errors stay `200`. |
| `GET` | `/api/config` | `200` | `500` | Disk problems return `200` with `disk_valid:false` and `disk_error_code`; `500` only if the running configuration cannot be marshaled. |
| `PUT` | `/api/config` | `200` | `400`, `409`, `413`, `415`, `422`, `428`, `500` | Writes the file. Returns `config_changed`, `restart_required`, and the new ETag. Never restarts. |
| `GET` | `/api/vgs` | `200` | `500` | Registered and configured VGs with capacity and errors. |
| `POST` | `/api/vgs` | `201` | `400`, `409`, `422`, `500` | Body `{"name":"vg"}`. Registers live. |
| `DELETE` | `/api/vgs/{name}` | `204` | `404`, `409`, `500` | Deregisters live. `409` when volumes remain or a class mapping references the VG. |
| `GET` | `/api/classes` | `200` | `500` | Current class-to-VG map. |
| `PUT` | `/api/classes/{name}` | `200` | `400`, `409`, `422`, `500` | Body `{"vg":"vg"}`. Sets a class mapping live and persists it. |
| `DELETE` | `/api/classes/{name}` | `204` | `404`, `409`, `500` | Removes a class mapping live and persists it; `409` on config-lock conflict. |
| `POST` | `/api/targets/disable` | `204` | `400`, `404`, `409`, `415`, `422`, `500`, `503` | Body `{"owner":"...","volume_id":"..."}`. Force-unexports only an iSCSI target, drops active sessions, and retains the SQLite volume/export definition. |
| `GET` | `/api/certificates` | `200` | `500` | Lists regular `.crt`, `.key`, and `.pem` files in `admin_ui.client_cert_dir` and, separately, the public `listen.tls.client_ca_file` as `ca`. A missing/unsafe directory is reported in the body, not as a crash. |
| `GET` | `/api/certificates/{name}` | `200` | `404`, `405` | Downloads one allowed file with `Content-Disposition: attachment`. Path traversal, symlinks, and non-allowlisted names are `404`. |
| `GET` | `/api/ca` | `200` | `404`, `405` | Downloads `listen.tls.client_ca_file` (the public CA certificate only; the CA private key is never served). |

All API errors use `Content-Type: application/json` and:

```json
{"error": "human-readable message", "code": "invalid_config"}
```

Codes: `bad_request`, `forbidden`, `not_found`, `method_not_allowed`,
`conflict`, `too_large`, `unsupported_media_type`, `invalid_config`,
`precondition_required`, `internal_error`, `unavailable`, `vg_unavailable`,
`vg_in_use`, `vg_conflict`, `class_conflict`, `invalid_target`, `volume_in_drift`,
`session_active`, `session_unknown`, `operation_in_progress`.

Global Host and mutation rules:

*   `Host` must be `127.0.0.1:<port>`, `[::1]:<port>`, or
    `localhost:<port>` with the actual listener port (including an injected
    ephemeral port). Otherwise `403`. The `localhost` form is a Host-header
    alias only; it is not a valid bind address.
*   Mutations require exact Host and a non-simple content type: config `PUT`
    uses `Content-Type: application/yaml`; all other `PUT`/`POST`/`DELETE`
    mutations use `Content-Type: application/json`.
*   If `Origin` is present, it must be `http://` plus an allowed Host value.
    `Origin: null` is rejected. `Sec-Fetch-Site: cross-site` is rejected with
    `403`. `GET`/`HEAD` may omit `Origin`; mutations may omit it only when
    their content type is non-simple.
*   No CORS headers. `OPTIONS` returns `405`.
*   Global `403` (Host/Origin), `404` (unknown route), and `405` (wrong method
    or `OPTIONS`) apply to every route. API responses use the JSON error body;
    the HTML fallback uses plain HTML.

### Status response

```json
{
  "agent": {
    "version": "dev",
    "api_version": "v1",
    "host_uuid": "6f1c...",
    "ready": true,
    "started_at": "2026-11-07T07:00:00Z",
    "uptime_seconds": 1234,
    "config_path": "/etc/kouja/config.yaml",
    "config_changed": false,
    "restart_required": false,
    "restart_reason": "",
    "last_good_error": "",
    "listen_address": "0.0.0.0:9443",
    "admin_address": "127.0.0.1:9444",
    "protocols": ["iscsi"]
  },
  "iscsi": {
    "enabled": true,
    "iqn_prefix": "iqn.2026-11.io.kouja",
    "portals": ["0.0.0.0:3260"]
  },
  "metrics": {
    "sampled_at": "2026-11-07T07:00:05Z",
    "available": true,
    "rates_available": true,
    "error": "",
    "read_bytes": 8309504,
    "write_bytes": 1110503424,
    "read_bytes_per_second": 1048576,
    "write_bytes_per_second": 1572864,
    "commands": 888,
    "commands_per_second": 12.5,
    "error_count": 0,
    "connections": [
      {
        "client_address": "192.0.2.10",
        "client_port": 40000,
        "local_address": "192.0.2.1",
        "local_port": 3260,
        "target": "iqn.2026-11.io.kouja:host:uuid",
        "initiator": "iqn.2026-11.io.example:consumer",
        "ambiguous": false
      }
    ],
    "connections_error": "",
    "targets": [
      {
        "target": "iqn.2026-11.io.kouja:host:uuid",
        "volume_owner": "rookery",
        "volume_id": "example-1",
        "sessions": 1,
        "initiators": [
          {
            "initiator": "iqn.2026-11.io.example:consumer",
            "rx_bytes": 1110503424,
            "tx_bytes": 8309504,
            "command_pdus": 888,
            "response_pdus": 311,
            "connection_timeout_errors": 0,
            "connection_digest_errors": 0
          }
        ],
        "luns": [
          {
            "lun": 0,
            "resource_uuid": "uuid",
            "commands": 888,
            "read_bytes": 7340032,
            "write_bytes": 1125122048,
            "resets": 0,
            "aborts_complete": 0,
            "aborts_no_task": 0,
            "status": "available",
            "state_bit": "exposed",
            "busy_count": 0,
            "in_commands": 888,
            "high_priority_in_commands": 0,
            "error": ""
          }
        ],
        "client_addresses": ["192.0.2.10"],
        "read_bytes": 8309504,
        "write_bytes": 1110503424,
        "read_bytes_per_second": 1048576,
        "write_bytes_per_second": 1572864,
        "commands": 888,
        "commands_per_second": 12.5,
        "rates_available": true,
        "error_count": 0,
        "errors": {
          "login_failures": 0,
          "authentication_failures": 0,
          "authorization_failures": 0,
          "negotiation_failures": 0,
          "other_login_failures": 0,
          "abnormal_logouts": 0,
          "format_errors": 0,
          "connection_errors": 0,
          "digest_errors": 0,
          "connection_timeouts": 0,
          "connection_digest_errors": 0,
          "aborts_complete": 0,
          "aborts_no_task": 0,
          "resets": 0
        },
        "error": ""
      }
    ]
  },
  "vgs": [
    {
      "name": "kouja",
      "uuid": "vg-uuid",
      "state": "active",
      "storage_class": "hdd",
      "configured": true,
      "total_bytes": 107374182400,
      "free_bytes": 53687091200,
      "allocatable_bytes": 48318382080,
      "extent_size_bytes": 4194304,
      "max_volume_bytes": 1099511627776,
      "observed_at": "2026-11-07T07:00:00Z",
      "error": ""
    }
  ],
  "volumes": [
    {
      "owner": "rookery",
      "volume_id": "example-1",
      "resource_uuid": "uuid",
      "lv_name": "kouja-uuid",
      "vg_name": "kouja",
      "storage_class": "hdd",
      "requested_size_bytes": 1073741824,
      "actual_size_bytes": 1073741824,
      "state": "exported",
      "serving": true,
      "drift_reasons": [],
      "updated_at": "2026-11-07T07:00:00Z",
      "export": {
        "protocol": "iscsi",
        "initiator": "iqn.2026-11.io.example:consumer",
        "target_name": "iqn.2026-11.io.kouja:host:uuid",
        "lun": 0,
        "nsid": 0,
        "portal": "192.0.2.10:3260",
        "state": "exported"
      },
      "observation": {
        "lv_present": true,
        "observed_size_bytes": 1073741824,
        "device_path": "/dev/kouja/kouja-uuid",
        "observed_at": "2026-11-07T07:00:00Z",
        "source": "lvm",
        "stale": false
      },
      "export_observation": {
        "target_present": true,
        "namespace_present": true,
        "acl_present": true,
        "active_sessions": 1,
        "session_state": "active",
        "observed_initiator": "iqn.2026-11.io.example:consumer",
        "initiator_source": "acl",
        "observed_at": "2026-11-07T07:00:00Z",
        "stale": false
      }
    }
  ],
  "counts": {
    "volumes": 1,
    "by_state": {"exported": 1},
    "truncated_volumes": false,
    "operations": 1,
    "truncated_operations": false
  },
  "operations": [
    {
      "id": "op-1",
      "owner": "rookery",
      "volume_id": "example-1",
      "resource_uuid": "uuid",
      "action": "create",
      "state": "failed",
      "attempt": 3,
      "max_attempts": 3,
      "deadline_at": "2026-11-07T07:05:00Z",
      "last_error_code": "UNAVAILABLE",
      "last_error_retryable": true,
      "created_at": "2026-11-07T07:00:00Z",
      "updated_at": "2026-11-07T07:01:00Z"
    }
  ],
  "errors": [
    {"code": "capacity_unavailable", "message": "vg kouja-hdd: vgs failed"}
  ]
}
```

Rules:

*   `serving` is true only when the volume and export states are `exported`,
    `observation.lv_present` is true, the target and ACL are present, the
    namespace is present for `nvme-tcp`, and both observations are fresh. If
    either observation is missing or `stale`, `serving` is false and the UI
    shows `unknown`/stale rather than asserting healthy.
*   `export`, `observation`, and `export_observation` are `null` when the
    corresponding row has not been observed. The UI renders `unknown`, and
    `serving` is false. A volume before its first reconcile therefore shows
    observation `unknown`, not `lv_present:false`.
*   `active_sessions` is `null` when `session_state` is `unknown`. iSCSI
    reports a count when the kernel exposes it; nvmet reports `unknown` on the
    supported kernel.
*   `observed_initiator` is the reconciler's stored
    `export_observations.observed_initiator` when non-empty, and
    `initiator_source` is `"acl"` because the current adapters copy the
    configured ACL initiator; it is not a kernel session identity. When the
    `export_observation` row exists but the stored value is empty,
    `observed_initiator` is null and `initiator_source:"none"`. When the row is
    absent, `export_observation` itself is null and there is no
    `initiator_source` field. The configured initiator is `export.initiator`.
    A future session-verified value would use `initiator_source:"session"`.
*   `stale` is a boolean in the API, true when `observed_at` is missing or
    older than `2 * state.reconcile_interval_seconds` (default 120s). The UI
    renders a missing or stale observation as `unknown`, with a stale badge
    where useful.
*   Every array (`vgs`, `volumes`, `operations`, `drift_reasons`, `errors`,
    `protocols`, `metrics.connections`, `metrics.targets`,
    `metrics.targets[].initiators`, and `metrics.targets[].luns`) is `[]`,
    never `null`.
*   `metrics` is best-effort. `available:false` with a non-empty `error` means
    the collector could not read LIO configfs; the status response remains
    `200`. A missing iSCSI target tree is `available:true` with no targets.
    `rates_available` is false on the first sample or when samples are closer
    than one second; rates are computed from cumulative counters between
    successful samples. A counter reset makes that target's
    `rates_available:false` and leaves its rates at zero.
*   Target-level `read_bytes` is the sum of initiator `tx_bytes` (target sent
    to initiators), and `write_bytes` is the sum of initiator `rx_bytes`
    (target received from initiators). LUN `read_bytes`/`write_bytes` are the
    coarse whole-MiB `scsi_lu` counters and are shown for LUN detail.
    `commands` is the sum of LUN command counters. Error counts are cumulative
    and never auto-reset; an operator can compare them across refreshes.
*   Per-initiator counters come from the ACL `iscsi_sess_stats` directory.
    They are aggregated per initiator when an ACL has more than one session;
    the current kouja model uses one target and one session per volume.
*   `metrics.connections` lists ESTABLISHED TCP sockets whose local port is a
    configured target portal port from the LIO `np` directory. `client_address`
    and `client_port` are the initiator endpoint; `local_address` and
    `local_port` are the target portal endpoint. Target/initiator matching uses
    the active session `Address` lines in ACL `info`. When several targets
    share the same portal port and initiator address, the connection is marked
    `ambiguous:true` instead of guessing. A connection with no matching ACL is
    shown as unmatched. `/proc/net/tcp(6)` is namespace-local and best-effort;
    if it is unreadable, `connections_error` reports the error and the rest of
    the metrics still render.
*   `counts.by_state` contains zero values for `creating`, `available`,
    `exporting`, `exported`, `unexporting`, `resizing`, `deleting`, `failed`,
    and `drift`. The JSON example elides zero keys for brevity; the server
    emits all nine. Deleted rows are tombstones and are not in the live volume
    table.
*   `counts.volumes` is the total live volume rows. `truncated_volumes` is
    true when the total exceeds the returned rows. `counts.operations` is the
    total problem operations; `truncated_operations` compares it with the
    returned rows.
*   `errors` may include `config_missing`, `config_invalid`, `config_unsafe`,
    or `config_unreadable` with the disk error message. These are status
    diagnostics; the status response remains `200` unless SQLite fails
    (`503`) or the running configuration cannot be marshaled (`500`).
*   A failed VG capacity probe sets `vgs[].state` to `unavailable`, zeros the
    capacity fields, sets `error`, and appends a `capacity_unavailable` entry
    to `errors`. A probe that exceeds the time budget sets `state:"unknown"`
    and appends `capacity_unknown`. A successful probe uses
    `capacity.VGState`/`capacity.VGUUID` for display; a UUID that differs from
    the registered row is `unavailable` with an error. The persisted VG row is
    not changed.

### Status data flow

`GET /api/status` performs one SQLite transaction and then live capacity
probes:

1.  `Store.Transact`:
    *   `Tx.ListVGs` (existing method) for registered VGs;
    *   `Tx.ListVolumeStatuses(256)` for at most 256 volume rows;
    *   `Tx.VolumeStateCounts()` for the counts map;
    *   `Tx.ListRecentOperations(64)` for at most 64 problem operations;
    *   `Tx.OperationCount()` for the total problem-operation count.
2.  Build an effective config copy with
    `Storage.Classes = Classes.Snapshot()`, then merge its `EffectiveVGs()` with
    registered VGs. A configured but unregistered VG gets a row with
    `state: "not_registered"`, `configured: true`, empty UUID, zero capacity,
    and an `error` string. The same helper backs `GET /api/vgs`.
3.  Call `lvm.Backend.Capacity` for each registered VG. The handler has a total
    budget of 10 seconds, a 3-second timeout per VG, and a concurrency limit of
    4. A VG that exceeds the budget is marked `unknown` with a
    `capacity_unknown` error; a probe that returns an LVM error is marked
    `unavailable` with `capacity_unavailable`. If the probe succeeds, the
    capacity's `VGState`/`VGUUID` take precedence for display; a UUID mismatch
    is surfaced as `unavailable` with an error, and the persisted VG row is not
    changed.
4.  Compute `config_changed`, `restart_required`, and `restart_reason` with the
    same helper used by `GET /api/config`; append the matching `config_missing`,
    `config_invalid`, `config_unsafe`, or `config_unreadable` entry when the
    disk config is unavailable.
5.  Read live iSCSI metrics through `Options.TargetMetrics` when configured.
    Join target names to the volume read model for owner/volume labels. Read
    target, ACL, and LUN counters from LIO configfs; read established TCP client
    endpoints from `/proc/net/tcp(6)` using the target portal ports and ACL
    session addresses; keep metrics collection errors in `metrics.error` or
    `metrics.connections_error` instead of failing the status request. Derive
    rates from the previous successful sample held in the server.
6.  Return the response.

The status handler uses a non-blocking in-flight guard. If a request is already
running, a second request waits for the first result instead of starting another
LVM probe. The shared computation uses a detached 10-second context, so one
client abort does not cancel a result another waiter needs; each waiter still
honors its own request context while waiting. The page polls every 5 seconds and
aborts the previous fetch before starting a new one. There is no cache. The
status path does not call `Admission.AcquireCapacity`; it is local and read-only.

### State query additions

Add four methods to the existing `Tx` interface. No schema migration and no new
indexes are required at the supported 256-volume/64-operation scale:

*   `ListVolumeStatuses(limit int) ([]VolumeStatus, error)`: one `LEFT JOIN`
    from `volumes` to `exports`, `volume_observations`, and
    `export_observations`, joining on `owner` and `volume_id`. Select every
    volume column plus the related columns with explicit aliases. Decode
    `drift_reasons` and `legacy_identity` with the existing JSON decoders. A
    limit of zero or less becomes 256; larger values are capped at 256. Order
    by `volumes.updated_at DESC, volumes.owner, volumes.volume_id`.
*   `VolumeStateCounts() (map[string]int, error)`: group the live volume table
    by state. Return an empty map when there are no rows.
*   `ListRecentOperations(limit int) ([]Operation, error)`: filter `state IN
    ('running', 'failed', 'needs_attention')`, order by `updated_at DESC,
    id DESC`, normalize zero or less to 64, cap at 64.
*   `OperationCount() (int, error)`: count the same problem-operation set.

`VolumeStatus` embeds `Volume` and carries pointers to `Export`,
`Observation`, and `ExportObservation`. The joined tables have at most one row
per `(owner, volume_id)` by primary key. The existing `operations_by_state`
index supports the state filter. Add an index only after profiling, not in this
feature.

### Configuration read flow

The agent reads the file once at startup with `config.LoadWithData` and keeps:

*   `Config`: the parsed effective configuration;
*   `ConfigData`: the exact raw bytes for `.last-good`.

`configEquivalent(running, candidate *config.Config, classes ClassRegistry) bool`
compares a deterministic YAML marshal after normalizing `storage.vgs` as a
sorted, deduplicated set and replacing the running class map with the live
`Classes.Snapshot()`. Comments, indentation, and mapping key order do not
count; `storage.vgs` order/duplicates do not count. Target portal order is
meaningful and remains order-sensitive for `config_changed`.
`rawETag(data []byte)` returns the quoted string `"sha256:<hex>"`.

`GET /api/config`:

1.  Read the file with a display reader that does not reject symlinks; it
    reports the path type.
2.  Compute `disk_etag` from the raw bytes, or `"*"` if missing, or `""` if the
    path is unsafe/unreadable.
3.  Parse the bytes with `config.Parse`.
4.  Set `disk_valid`, `disk_error`, and `disk_error_code`:
    *   `missing`: file does not exist; `disk_yaml: ""`, `disk_etag: "*"`.
    *   `parse_error`: file exists but does not parse; raw YAML is preserved,
        `disk_etag` is the raw hash; the UI can fix and save with `If-Match`.
    *   `unsafe_path`: symlink or non-regular; raw YAML is empty,
        `disk_etag: ""`; `PUT` returns `422`.
    *   `read_error`: permission or I/O failure; `disk_etag: ""`; `PUT`
        returns `500`.
5.  Compute `config_changed = !disk_valid || parsedDisk != effectiveRunning`.
    The effective running configuration is a copy of the startup configuration
    whose `Storage.Classes` is replaced by `Classes.Snapshot()`; this makes live
    class changes visible and avoids a false `config_changed` after a class
    action. When `disk_valid` is false, set `restart_required:false`,
    `restart_reason:""`, and put the diagnostic in `disk_error`; skip the
    binding comparison because there is no parsed candidate. When valid,
    compare binding sockets as defined above.
6.  Return `disk_yaml`, `running_yaml` (canonical effective with the live class
    map), ETag, validity, error code, `config_changed`, `restart_required`,
    `restart_reason`, `last_good_error`, and `warnings`. `GET` warnings are
    always `[]`; preflight warnings are produced by `PUT` only. Class mutations
    return durability/audit warnings from their own write path, not preflight.

### Configuration write flow

`PUT /api/config`:

1.  Enforce Host, Origin, `Sec-Fetch-Site`, `Content-Type: application/yaml`,
    and the 1 MiB body limit.
2.  Read the current file and ETag. For an existing regular file, require
    `If-Match` equal to `disk_etag`; a mismatch returns `409`. For a missing
    file, require `If-None-Match: *`. If the supplied precondition form does
    not match the current state, return `409`. A symlink or non-regular file
    returns `422`. Missing `If-Match`/`If-None-Match` returns `428`; malformed
    returns `400`.
3.  Parse with `config.Parse`; invalid input returns `422` without a write.
4.  Build preflight warnings with
    `preflight(ctx, candidate, running, vgs)` under a 5-second context. Warnings
    never block. The VG check uses `VGs.Inspect` (read-only). This runs before
    the file lock so a slow LVM/bind probe cannot block class updates or other
    saves.
5.  Acquire the exclusive `<config>.lock` file (`unix.Flock`). If the lock is
    not acquired within 2 seconds, return `409`.
6.  Re-read the current file's size, modification time, and raw hash. If it
    changed after step 2, return `409` and leave the original intact.
7.  If a current file exists, write its bytes to `<config>.bak` with
    `internal/fsutil.WriteFileAtomic` (mode `0600`). If this fails, abort with
    `500`; the original is unchanged. A missing-file create skips `.bak`
    because there is no previous file.
8.  Write the new YAML with `fsutil.WriteFileAtomic`. If temp write or rename
    fails, the original remains. If the directory sync fails after rename,
    `fsutil.ErrDurabilityUncertain` is converted to a successful response with
    a `durability_uncertain` warning, because the new file is visible.
9.  Release `<config>.lock` immediately after the rename/recheck critical
    section. Then compute `config_changed` and `restart_required` from the new
    parsed config; `configState` may take the live class snapshot without
    holding the file lock. This preserves the `ClassMap.mu` -> file-lock order
    used by class writes.
10. Write a `config_save` audit row (best effort; failure adds
    `audit_write_failed` warning). Build and flush the response, including the
    new `disk_etag`, `config_changed`, `restart_required`, and
    `restart_reason`.
11. Do not restart, schedule a restart, or exit the process.

`preflight(ctx, candidate, running *config.Config, vgs VGService)` warnings:

*   TLS cert/key/CA file missing or unreadable;
*   `secret_key_file` changed (existing credentials may become unreadable);
*   data/backup directory parent not writable;
*   a changed mTLS listen address cannot be bound (skip if unchanged);
*   a changed admin address cannot be bound (skip if unchanged; skip when
    disabled);
*   configured VG missing or ineligible (uses `VGs.Inspect`; read-only);
*   `admin_ui.enabled` changed to false (UI disappears after restart);
*   `admin_ui.address` changed (UI moves after restart);
*   identity or state paths changed;
*   target portals or protocol enable flags changed;
*   target portal order changed (future exports after restart use the new
    first portal; this is a warning, not a binding-socket change);
*   `listen.tls` changed (startup-bound; may require a manual restart to load
    new material);
*   operations, admission, storage headroom, or state intervals changed
    (startup-bound; may require a manual restart to apply);
*   `storage.vgs` changed in raw YAML (part of the first-start
    `EffectiveVGs()` seed; use the live VG panel);
*   `storage.classes` changed in raw YAML (loaded every start; a class for a
    never-registered VG still needs the live VG panel; use the class panel for
    immediate effect);
*   any other parsed difference (`config_changed`).

Warnings have the shape `{"code": "...", "message": "..."}`. `PUT` returns
`200` with warnings; they never block a valid save. Only the binding socket
fields set `restart_required`; startup-bound warnings may advise a manual
restart but do not set it.

### Configuration responses

`GET /api/config` returns:

```json
{
  "path": "/etc/kouja/config.yaml",
  "backup_path": "/etc/kouja/config.yaml.bak",
  "last_good_path": "/etc/kouja/config.yaml.last-good",
  "disk_yaml": "host_id_file: /var/lib/kouja/host-id\n...",
  "running_yaml": "host_id_file: /var/lib/kouja/host-id\n...",
  "disk_etag": "\"sha256:...\"",
  "disk_valid": true,
  "disk_error": "",
  "disk_error_code": "",
  "config_changed": false,
  "restart_required": false,
  "restart_reason": "",
  "last_good_error": "",
  "warnings": [],
  "tls": {
    "subject": "kouja-agent",
    "server_names": ["storage.example", "localhost"],
    "ip_addresses": ["127.0.0.1", "192.0.2.10"],
    "not_after": "2028-11-07T00:00:00Z",
    "error": ""
  }
}
```

`PUT /api/config` returns:

```json
{
  "saved": true,
  "path": "/etc/kouja/config.yaml",
  "backup_path": "/etc/kouja/config.yaml.bak",
  "disk_etag": "\"sha256:...\"",
  "config_changed": true,
  "restart_required": true,
  "restart_reason": "listen_address_changed",
  "warnings": [
    {"code": "durability_uncertain", "message": "directory sync failed after rename"}
  ]
}
```

`GET /api/vgs` returns `{"vgs":[VGStatus...]}` using the same VG object as
`/api/status`. `POST /api/vgs` returns `201` with
`{"vg": VGStatus, "warnings": []}`. `DELETE /api/vgs/{name}` returns `204`;
audit failure is logged only. `GET /api/classes` returns
`{"classes":{"class":"vg"}}`. `PUT /api/classes/{name}` returns
`{"classes":{"class":"vg"},"config_changed":false,"restart_required":false,"warnings":[]}`;
`config_changed` is recomputed after the write and may be true if a concurrent
raw edit left the disk different. `DELETE /api/classes/{name}` returns `204`;
audit failure is logged only. These mutations never restart. The class file
write and the live map swap are one logical action; the live map is updated
before the response, so the running and disk views match.

`POST /api/targets/disable` accepts an owner and volume ID for an existing
iSCSI export. The server verifies the export protocol, then calls the shared
operation manager with a forced unexport. The target adapter disables the TPG
and removes the target/ACL, dropping active sessions; the manager retains the
volume and export rows and records `volume.state:"available"` /
`export.state:"unexported"`. The endpoint returns `204` with no body. It is
idempotent per target generation and creates a distinct operation after a
later re-export because the admin action carries a unique request ID in the
unexport intent.

### Last known-good configuration

When the admin UI is enabled, after the agent has loaded the configuration,
both listeners are serving, and readiness is set, the admin server writes the
startup bytes to `<config>.last-good` with mode `0600` using
`fsutil.WriteFileAtomic`. Neither a raw configuration save nor a live class
write refreshes it. It is the startup configuration that last reached
readiness, so recovery never promotes a disk-only edit that was not tested by a
successful start. Live class changes after startup are in the current (possibly
failed) config; `.bak` holds the revision before the last write and may not
contain the latest class change. CUJ 4 tells the operator to reapply them if
recovery is needed.

The write is best effort. If it fails, the server logs the error, reports
`last_good_error` in `GET /api/config`, and still allows saves; `.bak` is the
immediate undo. If the write fails, recovery is manual. On first enablement,
`.last-good` does not exist yet; CUJ 4 requires a prior successful
admin-enabled startup. The operator can use `.bak`, the installer starter, or a
shell backup. When `admin_ui.enabled` is false, no `.last-good` is written.

### Configuration audit

`writeAudit(ctx context.Context, store Store, action, operationID, preState,
postState, result, errorCode, remote string) error` writes one row through
`state.PutAudit`. It assigns `caller: "admin-ui"`, `source_ip` as the host part
of `RemoteAddr`, and the supplied action/result/error. The operation ID is a
UUID. `preState`/`postState` map to `state.Audit.PreState`/`PostState`; there
are no hash columns. Actions:

*   `config_save` — successful save only; `result: succeeded`;
*   `vg_register`, `vg_deregister`;
*   `class_set`, `class_remove`.

Rejected config saves are logged with a request ID, not audited. The audit write
happens after the file/VG/class mutation and before the JSON response is built.
If it fails, the mutation still succeeds; a `200`/`201` response includes
`audit_write_failed`, and the server logs the error. A `204` delete cannot carry
a body, so its audit failure is log-only. There is no restart audit because the
UI never restarts the service.

### Live VG service

`internal/agent/vg_service.go` owns:

```go
type VGService interface {
    Register(ctx context.Context, name, expectedUUID string, replace bool) (lvm.Capacity, error)
    Deregister(ctx context.Context, name string) error
    List(ctx context.Context) ([]state.VG, error)
    Inspect(ctx context.Context, name string) (lvm.Capacity, error)
}
```

`Inspect` runs `lvm.Eligibility` and `lvm.Capacity` read-only; it never writes
state. `Register` validates the name, calls `Inspect`, and first checks the
requested UUID: if `expectedUUID` is non-empty and differs from the observed
`Capacity.VGUUID`, return `ErrVGUUIDMismatch` before any row logic. This
preserves the existing mTLS check for new and existing rows. Then:

*   no existing row: insert the observed UUID, `active` state, and timestamps;
*   existing row with the same observed UUID: idempotent success; refresh
    `state:"active"` and `updated_at`;
*   existing row with a different observed UUID and `replace=false`: return
    `ErrVGConflict` (the local UI passes false and returns `409`);
*   existing row with a different observed UUID and `replace=true`: update the
    row. An empty `expectedUUID` skips the UUID check and preserves the legacy
    upsert behavior. The mTLS `AdminService.RegisterVG` passes `replace=true`
    and `request.GetVgUuid()`; the local UI passes `replace=false` and an empty
    UUID.

`Deregister` rejects a VG that still has live volumes (`ErrVGInUse`) or a VG
referenced by a class mapping (`ErrVGClassMapped`) before deleting it. `List`
returns state rows. The mTLS `AdminService.RegisterVG`/`DeregisterVG` delegate
to this service; `ListVGs` uses `VGService.List` for state rows and keeps its
existing capacity/class loop. The local UI uses the shared `buildVGStatus`
helper for capacity and configured state. The local UI does not use a synthetic
admin identity. Exact gRPC mapping: `ErrVGInUse`, `ErrVGUnavailable`, and
`ErrVGClassMapped` -> `FailedPrecondition`; `ErrVGUUIDMismatch` ->
`FailedPrecondition`; `ErrVGNotFound` -> `NotFound`; `ErrVGConflict` ->
`AlreadyExists`. Local HTTP mapping is in the error table.

### Live class registry

`internal/agent/class_registry.go` owns a mutex-protected map. It does not depend
on `VGService`; the local handler validates registration before calling it, and
`VGManager` uses it for the deregistration guard. This avoids a construction
cycle.

```go
type ClassRegistry interface {
    Snapshot() map[string]string
    ClassName(vg string) string
    Resolve(class, vg string) (resolvedClass, resolvedVG string, err error)
    Set(ctx context.Context, class, vg string) (warnings []string, err error)
    Remove(ctx context.Context, class string) (warnings []string, err error)
}
```

The registry is initialized from `cfg.Storage.Classes`. `VolumeService` and
`AdminService` use it instead of a static map. `Set`/`Remove` hold the registry
write lock across the complete config read/modify/write and the map swap, so a
concurrent `Set`/`Remove` cannot commit the file and map in different orders.
The config file lock is acquired inside the registry lock; the lock order is
`ClassMap.mu` then `<config>.lock`, never the reverse. A successful write does
not refresh `.last-good`; `durability_uncertain` on post-rename fsync failure
is a warning. The local handler checks that the VG is registered before `Set`;
a missing VG returns `vg_unavailable`. A class name must match the existing
name pattern. A class cannot map to a VG that another class already uses.
Changes persist across restarts and do not restart the service. The
`PUT /api/classes` response recomputes `config_changed`/`restart_required` via
`configState` after the write; a class-only change normally has
`config_changed:false` and `restart_required:false`. `Remove` returns
`config.ErrClassNotFound` for a missing class.

`storage.classes` is the initial class map and is loaded on every start; raw
YAML edits to it apply on restart. `cfg.EffectiveVGs()` is the first-start seed;
`storage.vgs` alone is not. The live class panel applies a mapping immediately
and updates the file; adding a class for a VG that was never registered still
requires the VG panel.

### Embedded assets and build

The browser application lives under `web/` and is built with Preact,
TypeScript, and Vite. `npm run build` runs from `web/` and writes
`../internal/adminui/assets`, which resolves to
`internal/adminui/assets` from the repository root. The Go package embeds the
directory with `//go:embed all:assets`; `fallback.html` is embedded separately
with `//go:embed fallback.html`. A committed `.gitkeep` and nested `.gitignore`
keep the generated directory present; generated files are ignored by Git.

Build contracts:

*   `web/package.json` pins `preact@11.2.0`, `vite@8.3.6`, `typescript@7.0.2`,
    and `@preact/preset-vite@2.10.6`. Node `22.12.0` is the build runtime.
*   Scripts: `dev: vite`, `build: tsc --noEmit && vite build`,
    `typecheck: tsc --noEmit`, `preview: vite preview`.
*   `vite.config.ts`: `base: "/"`, `build.target: "es2022"`,
    `build.modulePreload.polyfill: false`,
    `build.outDir: "../internal/adminui/assets"`, `emptyOutDir: false`.
    The dev proxy sends `/api` to `http://127.0.0.1:9444`, sets
    `changeOrigin: true`, and rewrites `Origin` to `http://127.0.0.1:9444`.
*   `make clean-assets`: remove generated files in
    `internal/adminui/assets` except `.gitkeep` and `.gitignore`.
*   `make ui`: `clean-assets`, `cd web && npm ci`, `cd web && npm run build`,
    then verify `internal/adminui/assets/index.html` exists.
*   `make build`: depends on `ui`, then `go build ./...`.
*   `make build-go`: depends on `clean-assets`, then `go build ./...`; the
    binary serves the fallback page even if `make build` ran previously. This
    is the Node-free path.
*   CI sets up Node 22.12.0, caches `web/package-lock.json`, runs `make build`,
    and runs the Go checks. The quality job runs `make build-go` from a clean
    asset directory and checks the fallback path.

The Preact/Vite toolchain is a product constraint from the design review. The
fallback page keeps `go build` usable without Node; the full UI requires
`make build` and Node. The ongoing cost is a second language ecosystem and a
generated asset directory.

### Security controls

*   Listener binds only `127.0.0.1` or `::1`; the bound address is checked after
    `net.Listen`.
*   No authentication. Any local process that can reach the port is
    root-equivalent through the config editor, VG registration, and class
    changes. The accepted precondition is a single-administrator host with no
    untrusted local users, no host-network containers, and no SSRF-capable
    local service. SSRF clients can set Host/Origin headers; the loopback
    location is the only control.
*   Host allowlist as above; `403` on mismatch.
*   `Origin` same-origin when present; `Origin: null` rejected;
    `Sec-Fetch-Site: cross-site` rejected.
*   `PUT /api/config` requires `application/yaml`; VG/class mutations require
    `application/json`. A cross-site HTML form cannot send these non-simple
    types without a preflight, and no CORS/preflight is supported.
*   CSP: `default-src 'self'; script-src 'self'; style-src 'self'; img-src
    'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none';
    form-action 'self'`.
*   Headers: `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`,
    `Referrer-Policy: no-referrer`; API/index `Cache-Control: no-store`;
    hashed assets `public, max-age=31536000, immutable`.
*   Timeouts: `ReadHeaderTimeout: 5s`, `ReadTimeout: 15s`, `WriteTimeout: 30s`,
    `IdleTimeout: 60s`, `MaxHeaderBytes: 1 MiB`. Status has a 10s total budget.
*   Config body limit 1 MiB. Config, backup, last-good, and lock files are mode
    `0600`. The admin writer rejects symlinks and non-regular files.
*   Status and config responses never include credentials, grants, the secret
    key, or the credential table. The raw config is returned because it contains
    paths and policy, not secrets.
*   No restart endpoint, no `exec`, and no systemd command from the UI.

### Process lifecycle

1.  Load the configuration with `config.LoadWithData`.
2.  Run the existing directory, dependency, lock, store, host identity,
    credential, LVM, operation manager, reconciler, and first-start VG seed.
    `InitializeVGs` is unchanged.
3.  Create the mTLS server.
4.  If `admin_ui.enabled`, create the admin server. A bind or construction
    failure is logged, sets `kouja_admin_ui_up` to 0, and leaves
    `adminServer = nil`; the mTLS server and storage path continue.
5.  Start the backup and reconcile goroutines.
6.  Set readiness.
7.  Start the admin `Serve` goroutine and call `MarkRunning` after readiness.
    If `MarkRunning` fails, log it, set `last_good_error`, and continue; startup
    is not aborted. A later admin `Serve` error is logged, sets the gauge to 0,
    calls `adminServer.Shutdown(ctx)` with a 5-second context, and disables the
    admin server. It never calls the process stop callback.
8.  Wait on the signal context and the mTLS server error. The admin error is
    handled in its own goroutine.
9.  On shutdown, stop the admin server with a 5-second bounded context, stop the
    mTLS server, wait for the reconciler and backup goroutines, and return.
10. When disabled, do not create the admin server, write `.last-good`, or set
    the admin gauge to 1; set `kouja_admin_ui_up` to 0 at startup.

`startAdminServer(cfg *config.Config, options adminui.Options) *adminui.Server`
returns nil when disabled, uses `options.Logger` to log and return nil on `New`
failure, and otherwise returns the server. `main_test.go` covers disabled, bind-failure, and free-port
cases. There is no restart sentinel or exit-code mapping.

### Package API

```go
type Store interface {
    Transact(context.Context, func(state.Tx) error) error
}

type CapacityProvider interface {
    Capacity(context.Context, string) (lvm.Capacity, error)
}

type Options struct {
    ConfigPath    string
    Config        *config.Config
    ConfigData    []byte
    Store         Store
    Capacity      CapacityProvider
    VGs           agent.VGService
    Classes       agent.ClassRegistry
    Ready         func() bool
    HostUUID      string
    AgentVersion  string
    APIVersion    string
    StartedAt     time.Time
    Logger        *slog.Logger
    Now           func() time.Time
    Assets        fs.FS
    Listener      net.Listener
    Metrics       *observability.Metrics
    TargetMetrics target.ISCSIMetricsProvider
    DisableTarget TargetDisabler
    VGClassGate   *sync.Mutex
}

type Server struct { /* unexported */ }

func New(options Options) (*Server, error)
func (server *Server) Handler() http.Handler
func (server *Server) Serve() error
func (server *Server) Shutdown(ctx context.Context) error
func (server *Server) Address() net.Addr
func (server *Server) MarkRunning() error
```

Defaults: `Logger` = `slog.Default()`, `Now` = `time.Now`, `StartedAt` =
`time.Now()`, `Assets` = embedded root, `Ready` = always true, `VGClassGate` =
a new `sync.Mutex`. `New` returns an error when `ConfigPath` is empty, when
`Config`, `ConfigData`, `Store`,
`Capacity`, `VGs`, or `Classes` is nil, or when
`Config.AdminUI.Enabled` is false. It binds `Config.AdminUI.Address` unless
`Listener` is non-nil, verifies the bound address is loopback, derives the
allowed Host set from the actual listener port, stores the parsed running
configuration, and creates the HTTP server. `Serve` sets
`kouja_admin_ui_up` to 1 immediately before calling `http.Serve` (after the
listener is bound), resets it to 0 when `http.Serve` returns, and returns nil
for `http.ErrServerClosed`. `Shutdown` also sets it to 0. `MarkRunning` writes
`.last-good` from `ConfigData` and records `last_good_error`.

Handler ownership: `server.go` owns routes, middleware, and the HTTP handlers.
`status.go` builds the status view model. `config_file.go` owns read/save file
operations. `vg.go` owns VG/class handlers. `audit.go` owns audit writes.
`types.go` owns JSON types. No file owns restart logic because the UI never
restarts.

### Error handling and failure modes

| Condition | Behavior |
|---|---|
| Invalid YAML or invalid field | `422`, no write, running process unchanged. |
| File changed since page load | `409`, reload the editor. |
| Config file is a symlink/non-regular | `GET` 200 with `disk_valid:false`, `disk_error_code:"unsafe_path"`; `PUT` `422`. |
| Disk full or temp/rename failure | `500`, original intact. |
| Directory fsync fails after rename | `200` with `durability_uncertain` warning; new file visible, `.bak` recovery. |
| `.bak` write fails (existing file) | `500`, new file not written; missing-file create has no `.bak` step. |
| `.last-good` write fails | Logged and reported via `last_good_error`; startup and saves continue. |
| Admin listener bind fails | Logged; gauge 0; mTLS and storage continue. |
| Admin `Serve` fails | Logged; gauge 0; `adminServer.Shutdown`; mTLS and storage continue. |
| Invalid `admin_ui` config | Startup fails during config parsing; this is a config error, not a listener failure. |
| VG register invalid/ineligible | `422` with `vg_unavailable`; no state change. |
| VG register existing with same UUID | `201` idempotent success. |
| VG register existing with different UUID | `409` with `vg_conflict`; no overwrite. |
| VG deregister with volumes | `409` with `vg_in_use`; no state change. |
| VG deregister with a class mapping | `409` with `class_conflict`; no state change. |
| Class VG not registered | `422` with `vg_unavailable`; no config or registry change. |
| Class VG already mapped by another class | `409` with `class_conflict`; no config or registry change. |
| Class remove missing | `404` with `not_found`; no config change. |
| Class lock/unsafe/I/O failure | `409` for lock; `422` for unsafe path; `500` for I/O; live registry unchanged on failure. |
| Target disable with active sessions | Forced unexport; `204`; target/ACL removed, sessions dropped, volume/export rows retained. |
| Target disable on a non-iSCSI export | `422` with `invalid_target`; no target or state change. |
| Target disable on a missing definition | `404` with `not_found`; no target or state change. |
| Config changed, binding socket unchanged | `200`, `config_changed:true`, `restart_required:false`. |
| Binding socket changed | `200`, `config_changed:true`, `restart_required:true`; operator restarts manually. |
| LVM capacity probe fails for one VG | VG `unavailable` + `errors` entry; page still renders. |
| LVM capacity probe exceeds budget | VG `unknown` + `capacity_unknown`; page still renders. |
| Disk config missing/invalid/unsafe/unreadable | Status `200` with `config_missing`/`config_invalid`/`config_unsafe`/`config_unreadable`; config GET `200` with `disk_error_code`. |
| Running config cannot be marshaled | `GET /api/config`/status `500`; this is an internal invariant failure. |
| SQLite status read fails | `503`. |
| UI assets not built | `/` fallback `503`; API routes work. |
| Admin listener disabled | No port, no `.last-good`, gauge 0; existing behavior. |

### Observability

Structured `slog` lines:

*   admin listener start, failure, and stop;
*   startup warning when the admin UI is enabled (loopback, no authentication,
    root-equivalent);
*   config save (path, client, config_changed, restart_required, reason,
    warnings);
*   config rejection (error, client);
*   VG register/deregister (name, result);
*   class set/remove (class, VG, result);
*   audit write failure and last-good write failure.

Never log the config body, credentials, or volume secrets. The existing
`observability.Metrics` gains `kouja_admin_ui_up` (1 serving, 0 disabled or
unavailable). No restart metrics.

### Compatibility, rollout, and rollback

*   Configuration is additive. Existing files remain valid; the UI is disabled
    by default.
*   No schema migration. No protobuf, CSI, or mTLS API shape change. mTLS VG
    deregistration now rejects a VG referenced by a class mapping, preventing a
    dangling mapping; volume guards are unchanged.
*   `InitializeVGs` is unchanged: first-start `EffectiveVGs()` seed, then
    verification. Live VG and class changes are additive.
*   Runtime is one binary. `make build` requires Node; `make build-go` does not.
*   Rollout: enable `admin_ui`, restart once, open the URL. VG/class changes are
    live after that.
*   Rollback: disable `admin_ui`, remove the `admin_ui` section before running
    an older binary (which uses `KnownFields(true)`), and restart. No database
    migration to reverse.

### Risks and constraints

*   Any local process that can reach the port is root-equivalent through the
    config editor and live VG/class actions. SSRF-capable local services and
    host-network containers are inside the trust boundary. Single-administrator
    host precondition.
*   The UI does not restart. Binding socket changes require an operator
    `systemctl restart`; non-socket startup-bound fields may require an operator
    restart as well, but the UI does not set `restart_required` for them;
    preflight warnings may advise a restart. This is an accepted policy risk.
*   `.last-good` is absent on first enablement and when disabled; recovery then
    uses `.bak`, the installer starter, or a shell backup.
*   Status caps are 256 volumes and 64 problem operations. Above that, the page
    truncates and shows totals.
*   nvmet session counts are unknown on the supported kernel; `active_sessions`
    is null and `session_state` is `unknown`. `observed_initiator` is the
    reconciler's stored ACL value (`initiator_source:"acl"`), not a kernel
    session identity; it is null only when the observation value is absent.
*   Observation freshness is bounded by the reconcile interval; the UI marks
    stale observations.
*   The Preact/Vite build requires Node for the full UI. `make build-go` is the
    Node-free path.
*   Class changes write the config file; a concurrent raw editor can create a
    conflict, which the ETag/lock path detects.

--------------------------------------------------------------------------------

## Alternatives Considered

### Alternative A: Separate admin binary and static file server

*   **Pros**: Independent lifecycle and upgrade.
*   **Cons**: Second binary, second config parser, second state/LVM path, larger
    installer and systemd unit. It would still need root for config and LVM, so
    it does not create a useful privilege boundary.
*   **Reason for Rejection**: The requirement is one embedded UI in the agent.

### Alternative B: Admin UI on the existing mTLS listener

*   **Pros**: One listener, existing certificate roles.
*   **Cons**: Browser client certificates and enrollment; UI exposed on the
    caller network.
*   **Reason for Rejection**: The requirement is a separate loopback listener
    with no application authentication.

### Alternative C: Read-only config view

*   **Pros**: Smallest write surface.
*   **Cons**: Administrator still needs a shell; does not meet the config goal.
*   **Reason for Rejection**: Full YAML editing was selected.

### Alternative D: Live subset editor

*   **Pros**: Common changes apply without restart.
*   **Cons**: Only VGs have an existing live admin service; class mappings need
    a new registry. The listener, TLS, LVM backend, targets, admission, store,
    and reconciler are startup-bound.
*   **Reason for Rejection**: The UI uses the existing live VG service plus a
    new class registry and reports binding socket changes; it does not invent a
    second config model for the remaining fields.

### Alternative E: Unix socket or token boundary

*   **Pros**: Real local user boundary.
*   **Cons**: SSH forwarding or a token; conflicts with the no-auth requirement.
*   **Reason for Rejection**: Loopback TCP without auth was selected; the
    single-administrator precondition is documented.

### Alternative F: Vendored Preact with `htm`, no build

*   **Pros**: No Node, `go build` works.
*   **Cons**: No TypeScript or standard Preact tooling.
*   **Reason for Rejection**: Preact/Vite is a product constraint; the full UI
    build keeps TypeScript checking. `make build-go` remains the Node-free
    path.

### Alternative G: Commit the Vite output

*   **Pros**: Node-free `go build`.
*   **Cons**: Generated files in Git and drift.
*   **Reason for Rejection**: One source of truth; the fallback and
    `make build-go` cover Node-free builds.

### Alternative H: Vanilla JavaScript

*   **Pros**: No framework or build.
*   **Cons**: Manual DOM and asynchronous state handling; TypeScript checking
    is lost.
*   **Reason for Rejection**: Preact is the selected UI framework.

### Alternative I: Restart from the UI

*   **Pros**: One-click apply.
*   **Cons**: Root process restarts itself; broken config takes the UI down;
    systemd/non-systemd behavior differs; requires exit-code and restart
    tracking.
*   **Reason for Rejection**: The operator restarts manually. The UI only
    reports `restart_required` for binding socket changes.

### Alternative J: Structured form editor

*   **Pros**: Guided fields and checks.
*   **Cons**: Duplicates the config schema and lags new fields.
*   **Reason for Rejection**: Raw YAML with server validation covers every
    field; live VG/class controls cover the common administrative changes.

--------------------------------------------------------------------------------

## Non-Functional Requirements

The Technical Plan is canonical for behavior and failure handling; this section
states the non-functional targets and constraints.

### Security and privacy

*   Loopback-only listener; Host/Origin/`Sec-Fetch-Site` and content-type
    checks; no CORS; strict CSP; 1 MiB limits.
*   No authentication. Any local process that can reach the port is
    root-equivalent through the config editor and live VG/class actions. The
    single-administrator precondition is a deployment requirement, not a
    technical control.
*   Config, backup, last-good, and lock files are `0600`; symlinks are rejected
    by the writer.
*   Status/config responses exclude credentials, grants, and the secret key.
*   SSRF-capable local services and host-network containers are inside the trust
    boundary; do not expose the port via a proxy or port forward.

### Performance and latency

*   Status uses one SQLite transaction, at most 256 volumes, 64 problem
    operations, and one capacity probe per VG with a 10-second total budget, a
    3-second per-VG timeout, and concurrency 4. No cache. A single in-flight
    status request is coalesced.
*   Config saves parse once, write `.bak` and the new file, and fsync. The body
    is 1 MiB. `.last-good` is one startup write; saves and class writes do not
    refresh it.
*   VG/class changes are one state transaction plus, for classes, one config
    write and one live map swap.
*   The embedded UI is a one-time binary-size cost; no runtime file reads or
    CDN.

### Reliability and recovery

*   Invalid config is rejected before writing.
*   `.bak` failure aborts the save. A post-rename directory-fsync failure is
    reported as `durability_uncertain` with `.bak` recovery.
*   `.last-good` is written when the UI is enabled and readiness is reached;
    saves and class writes do not refresh it. First enablement has no recovery
    file.
*   Admin listener failures are non-fatal. Admin `Serve` errors call
    `adminServer.Shutdown`, not the process stop callback.
*   The UI never restarts, exits, or executes systemctl. Binding socket changes
    set `restart_required`; the operator restarts manually.
*   VG deregistration with volumes is rejected. Class changes validate the VG
    and persist before the live map changes.

### Compatibility and rollout

*   Additive config; UI disabled by default. No schema migration, no protobuf,
    CSI, or mTLS API shape change. The class-mapped VG deregistration rejection
    is an intentional behavior change.
*   `InitializeVGs` remains first-start `EffectiveVGs()` seed plus
    verification.
*   Runtime single binary. `make build` needs Node; `make build-go` does not.
*   Rollout: enable, restart once, use the UI. VG/class changes are live.
*   Rollback: disable, remove `admin_ui` before an older binary (KnownFields),
    restart. No database migration to restore.

## Additional Resources

*   `docs/designs/kouja.md`: existing architecture, state model, API, and
    failure behavior.
*   `README.md`: install, build, API, and operations guidance.
*   `proto/kouja/v1`: existing mTLS API contract.
*   `internal/state/schema.go`: volume, export, observation, operation, and
    audit tables.
*   `internal/agent/server.go`: listener, TLS, and shutdown patterns.
*   `internal/agent/admin_service.go`: existing VG/admin operations.
*   Preact and Vite documentation for the build setup.

## Future Improvements

*   **NVMe/TCP traffic counters**: Add equivalent nvmet per-session or
    per-subsystem statistics if the kernel exposes them.
*   **Config diff preview**: Show a parsed difference before saving.
*   **Frontend tests**: Add component or browser tests.
*   **Automatic class persistence alternatives**: If structured YAML edits
    become too complex, store class mappings in SQLite in a future schema.
*   **Authentication**: Add a local token or root-only Unix socket if the host
    stops being single-administrator.

--------------------------------------------------------------------------------

## Detailed Implementation Plan

The order below is the recommended implementation order. Every file created or
modified is listed. Phase checkpoints are included.

### Target Files to Create/Modify

**Phase 0: Live iSCSI metrics**

#### 0a. `internal/target/iscsi_metrics.go` (Create)

*   **Type / Scope**: Read-only LIO statistics.
*   **Proposed Changes**: Add `ISCSIMetricsProvider`, `ISCSIMetrics`,
    `ISCSITargetMetrics`, initiator/LUN/error types, and
    `(*ISCSI).Metrics(context.Context)`. Read `target/iscsi/*/fabric_statistics`,
    per-ACL `iscsi_sess_stats`, LUN `statistics/scsi_lu`, `scsi_tgt_dev`, and
    `scsi_tgt_port`. Read target portal ports from `np`, active client IPs from
    ACL `info`, and established TCP endpoints from `/proc/net/tcp(6)`. Missing
    optional attributes are zero; target/LUN parse errors are recorded in the
    corresponding `Error` field. The method never mutates target state and a
    missing `target/iscsi` tree is an empty success.

#### 0b. `internal/target/iscsi_metrics_test.go` (Create)

*   **Type / Scope**: Go unit tests.
*   **Proposed Changes**: Test full target/ACL/LUN parsing, aggregate reset and
    abort counters, empty target tree, cancelled context, invalid stat values,
    LUN index parsing, missing optional fields, `/proc/net/tcp` IPv4/IPv6
    parsing, portal-port filtering, and client-to-target matching.

**Phase 1: Configuration and shared helpers**

#### 1. `internal/config/config.go` (Modify)

*   **Type / Scope**: Go configuration schema and validation.
*   **Proposed Changes**: Add `AdminUI AdminUIConfig` with tag
    `yaml:"admin_ui"`, and `AdminUIConfig{Enabled bool yaml:"enabled"; Address
    string yaml:"address"}`. Defaults: `enabled:false`,
    `address:127.0.0.1:9444`. `applyDefaults` fills an empty address. `Validate`
    parses the address with `net.SplitHostPort`, accepts only exact host
    `127.0.0.1` or `::1`, rejects hostnames and the rest of `127.0.0.0/8`, and
    requires numeric port 1-65535. Validate even when disabled. Add
    `func LoadWithData(path string) (*Config, []byte, error)` retaining `Load`'s
    empty-path check and `Parse`; `Load` wraps it and discards bytes.

#### 2. `internal/config/config_test.go` (Modify)

*   **Type / Scope**: Go unit tests.
*   **Proposed Changes**: Test disabled default, default address,
    `127.0.0.1:9444`/`[::1]:9444` acceptance, rejection of `0.0.0.0`, `127.0.0.2`,
    `localhost`, port 0, out-of-range and named ports, unknown `admin_ui` field,
    validation while disabled, and `LoadWithData` byte fidelity. Add named
    `bddTestAdminUIConfig` and `bddTestLoadWithData` functions.

#### 3. `internal/config/ginkgo_suite_test.go` (Modify)

*   **Type / Scope**: Ginkgo suite registration.
*   **Proposed Changes**: Register `TestAdminUIConfig` and
    `TestLoadWithData`.

#### 4. `internal/config/storage_class.go` (Create)

*   **Type / Scope**: Shared helper.
*   **Proposed Changes**: Add
    `func StorageClassForVG(classes map[string]string, vg string) string`.

#### 5. `internal/config/storage_class_test.go` (Modify)

*   **Type / Scope**: Go unit tests.
*   **Proposed Changes**: Test mapped class, raw VG fallback, nil/empty map.

#### 6. `internal/config/storage.go` (Create)

*   **Type / Scope**: Structured config updater for live class changes.
*   **Proposed Changes**: Add
    `func SetClass(path, class, vg string) (warnings []string, err error)` and
    `func RemoveClass(path, class string) (warnings []string, err error)`.
    Acquire `<path>.lock` with `unix.Flock` and a 2-second timeout
    (`ErrLocked` on timeout), read the file into a `yaml.Node`, navigate/create
    `storage.classes`, set/delete the key, marshal, and write through
    `internal/fsutil.WriteFileAtomic` with mode `0600`. Preserve comments and
    unrelated keys. Before the final write, copy the current bytes to
    `<path>.bak` atomically; if the backup fails, abort with the live class map
    unchanged. Validate the marshaled result with `config.Parse` before the
    write; reject symlinks and non-regular files (`ErrUnsafePath`). Define
    `ErrLocked`, `ErrClassNotFound`, and `ErrUnsafePath` sentinels. A
    post-rename directory-sync failure is a successful write with a
    `durability_uncertain` warning, not an error. `.bak` always holds the
    immediately previous bytes, including after a class write; `.last-good` is
    not refreshed. Add tests for add, replace, remove, missing class, missing
    storage section, comment preservation, lock conflict, backup failure,
    durability warning, and atomic failure.

#### 6a. `internal/fsutil/atomic.go` (Create)

*   **Type / Scope**: Atomic file writer.
*   **Proposed Changes**: Add
    `func WriteFileAtomic(path string, data []byte, mode fs.FileMode) error`.
    It creates a temp file in the target directory, applies the mode, writes,
    syncs, closes, renames, and syncs the directory. It removes the temp file on
    failure. Define `var ErrDurabilityUncertain = errors.New(...)` and return
    it only when the rename succeeded but the directory sync failed; callers
    treat that as success with a warning. The admin config writer, `.bak`,
    `.last-good`, and structured class updater use it. The existing
    hostid/credential/backup writers are not changed; migrating them is a
    future cleanup.

#### 6b. `internal/fsutil/atomic_test.go` (Create)

*   **Type / Scope**: Go unit tests.
*   **Proposed Changes**: Test success, mode `0600`, replacement, cleanup on
    temp-write failure, rename failure with a target that is a non-empty
    directory (deterministic even as root), post-rename directory-sync
    uncertainty where the file is visible and `ErrDurabilityUncertain` is
    returned, and original-file behavior. This package uses plain Go tests; it
    has no shared Ginkgo test infrastructure.

#### 6c. `internal/config/storage_test.go` (Create)

*   **Type / Scope**: Go unit tests.
*   **Proposed Changes**: Test set/replace/remove class, missing class,
    missing `storage.classes`, comment preservation, invalid YAML rejection,
    `.bak` creation, `.last-good` unchanged, lock timeout, durability warning,
    and atomic write failure. Use temporary files.

**Phase 2: Live VG and class services**

#### 7. `internal/agent/vg_service.go` (Create)

*   **Type / Scope**: Shared VG service.
*   **Proposed Changes**: Define:
    ```go
    type VGService interface {
        Register(ctx context.Context, name, expectedUUID string, replace bool) (lvm.Capacity, error)
        Deregister(ctx context.Context, name string) error
        List(ctx context.Context) ([]state.VG, error)
        Inspect(ctx context.Context, name string) (lvm.Capacity, error)
    }
    type VGManager struct { store *state.Store; lvm lvm.Backend; classes ClassRegistry }
    func NewVGManager(store *state.Store, lvmBackend lvm.Backend, classes ClassRegistry) *VGManager
    ```
    `Inspect` runs `Eligibility` and `Capacity` without writing state; preflight
    uses it. `Register` validates the name, calls `Inspect`, checks a non-empty
    `expectedUUID` against the observed UUID before any row logic
    (`ErrVGUUIDMismatch`), then applies the create-only/replace rule: absence
    inserts; same UUID is idempotent and refreshes `updated_at`; a different
    UUID with `replace=false` is `ErrVGConflict`; a different UUID with
    `replace=true` updates, and an empty `expectedUUID` skips the check to
    preserve the legacy upsert. `Deregister` checks classes
    (`ErrVGClassMapped`)
    and live volumes (`ErrVGInUse`) before `tx.DeleteVG`. `List` uses
    `tx.ListVGs`. Define sentinel errors `ErrVGInUse`, `ErrVGUnavailable`,
    `ErrVGConflict`, `ErrVGUUIDMismatch`, `ErrVGNotFound`, and
    `ErrVGClassMapped`.

#### 8. `internal/agent/vg_service_test.go` (Create)

*   **Type / Scope**: Go unit tests.
*   **Proposed Changes**: Test register success, idempotent same-UUID
    `updated_at` refresh, create-only duplicate conflict, new-row UUID
    mismatch, same-UUID row UUID mismatch, empty-UUID replace preserving the
    legacy upsert, replace/UUID match/mismatch, ineligible VG, read-only
    `Inspect`, deregister empty, deregister with volume, deregister with a
    class mapping, list, and gRPC/HTTP error mapping. Use the existing state
    test store, a fake LVM backend, and a fake class registry.

#### 9. `internal/agent/admin_service.go` (Modify)

*   **Type / Scope**: mTLS AdminService.
*   **Proposed Changes**: Replace the inline RegisterVG/DeregisterVG logic with
    calls to `VGService.Register`/`Deregister`; pass
    `request.GetVgUuid()` as `expectedUUID` and `replace=true` to preserve the
    existing upsert/UUID check. Map `ErrVGUUIDMismatch`, `ErrVGInUse`,
    `ErrVGUnavailable`, and `ErrVGClassMapped` to `FailedPrecondition`;
    `ErrVGNotFound` to `NotFound`; `ErrVGConflict` to `AlreadyExists`. Use
    `VGService.List` for `ListVGs` state rows and keep the existing
    capacity/class loop. Keep the existing auth checks, request validation,
    response protos, and storage-class labels. Add `WithVGService(VGService)`
    and `WithVGClassGate(*sync.Mutex)` to the admin options; `DeregisterVG`
    holds the gate across the class/volume guard and delete. Construct from
    main. `WithStorageClasses` is replaced by
    `WithClassRegistry(ClassRegistry)`. Default `VGService` to
    `NewVGManager(store, lvmBackend, classes)` when no option is supplied; a nil
    `ClassRegistry` falls back to raw VG labels.

#### 10. `internal/agent/class_registry.go` (Create)

*   **Type / Scope**: Live class registry.
*   **Proposed Changes**: Define:
    ```go
    type ClassRegistry interface {
        Snapshot() map[string]string
        ClassName(vg string) string
        Resolve(class, vg string) (string, string, error)
        Set(ctx context.Context, class, vg string) (warnings []string, err error)
        Remove(ctx context.Context, class string) (warnings []string, err error)
    }
    type ClassMap struct { mu sync.RWMutex; classes map[string]string; configPath string }
    func NewClassMap(classes map[string]string, configPath string) *ClassMap
    ```
    Initialize from the map. `Snapshot`/`ClassName`/`Resolve` take `RLock`;
    `Set`/`Remove` hold the write lock across
    `config.SetClass`/`RemoveClass` and the map swap; the config updater takes
    the file lock inside. The binding `VGClassGate` (held by the local handler
    and `AdminService` deregistration) is outside the class lock; the lock order
    is `VGClassGate` -> `ClassMap.mu` -> `<config>.lock`, never the reverse.
    `Set` validates class-name and duplicate-VG rules; a VG already used by
    another class returns the agent `ErrClassConflict`, which the handler maps
    to `409`/`class_conflict`; define that sentinel in `class_registry.go`.
    The handler validates VG registration before calling. A successful write
    does not refresh `.last-good` and returns any `durability_uncertain`
    warning. `Remove` returns `config.ErrClassNotFound` when absent. `Resolve` mirrors
    the existing `resolveStorageClass`; `ClassName` mirrors
    `storageClassForVG`. The local handler and tests cover `Set`/`Remove`,
    duplicate class, unknown VG at the handler boundary, config write failure
    (map unchanged), concurrent `Set`/`Remove`, and concurrent snapshots.

#### 10a. `internal/agent/class_registry_test.go` (Create)

*   **Type / Scope**: Class registry tests.
*   **Proposed Changes**: Use a temporary config file. Test set/remove/
    persistence, duplicate class, missing class (`ErrClassNotFound`), config
    write failure leaving the map unchanged, durability warning, raw VG
    fallback, interleaved `Set`/`Remove`, and concurrent reads during a set. The
    unknown-VG check lives in the handler test. Register with
    `TestClassRegistry`.

#### 11. `internal/agent/volume_service.go` (Modify)

*   **Type / Scope**: Volume service.
*   **Proposed Changes**: Replace `ServiceOptions.StorageClasses
    map[string]string` with `Classes ClassRegistry`. Use `Classes.Resolve` in
    `CreateVolume` only; `ExportVolume` has no class request and uses
    `Classes.ClassName(volume.VGName)` for response labels. Use
    `Classes.ClassName` in `volumeProto` and `capacityProto`. If `Classes` is
    nil, class equals VG (the existing raw-VG behavior). Keep the mTLS behavior
    identical when the registry is initialized from the same maps. Add a test
    that a live class change relabels a new export response without changing the
    volume's VG.

#### 12. `internal/agent/storage_class.go` (Modify)

*   **Type / Scope**: Compatibility wrappers.
*   **Proposed Changes**: Keep `storageClassForVG` and `resolveStorageClass` as
    wrappers that use `config.StorageClassForVG` or accept a class map for
    existing tests. The live services use `ClassRegistry`; do not change the
    mTLS API semantics.

#### 12a. `internal/agent/admin_service_test.go` (Modify)

*   **Type / Scope**: Existing admin tests.
*   **Proposed Changes**: Update construction to provide a `VGService` (default
    `NewVGManager(store, backend, classes)`) and a class registry or nil
    fallback. Keep
    the auth, grant, drift, and VG tests. Add a test that `RegisterVG` and
    `DeregisterVG` delegate to the shared service.

#### 12b. `internal/agent/volume_service_test.go` (Modify)

*   **Type / Scope**: Existing volume tests.
*   **Proposed Changes**: Update `ServiceOptions` to use `Classes` when a test
    needs class mappings. A nil `Classes` behaves as an empty map (class equals
    VG), so tests without classes remain valid. Keep behavior assertions.

#### 12c. `internal/agent/storage_class_test.go` (Modify)

*   **Type / Scope**: Class tests.
*   **Proposed Changes**: Replace `ServiceOptions{StorageClasses: ...}` with a
    live `ClassRegistry` test double or a `ClassMap` built from the map. Test
    `ListStorageClasses`, `CreateVolume` class resolution, and raw VG fallback.

#### 12d. `internal/agent/ginkgo_suite_test.go` (Modify)

*   **Type / Scope**: Suite registration.
*   **Proposed Changes**: Register `TestVGService` and
    `TestClassRegistry` from the new test files.

**Phase 3: State read model**

#### 13. `internal/state/types.go` (Modify)

*   **Type / Scope**: State types.
*   **Proposed Changes**: Add
    `VolumeStatus{Volume; Export *Export; Observation *Observation; ExportObservation *ExportObservation}`.

#### 14. `internal/state/store.go` (Modify)

*   **Type / Scope**: Tx interface.
*   **Proposed Changes**: Add `ListVolumeStatuses(limit int)`,
    `VolumeStateCounts()`, `ListRecentOperations(limit int)`, and
    `OperationCount()`. No `schemaVersion` change. `Tx.ListVGs` remains the VG
    read method.

#### 15. `internal/state/status.go` (Create)

*   **Type / Scope**: Status queries.
*   **Proposed Changes**: Implement the four methods. `ListVolumeStatuses`
    joins exports/volume_observations/export_observations on owner and volume_id,
    selects all volume columns plus explicit aliases. Volume columns use the
    same nullable handling as the existing `scanVolume` helper (or reuse it),
    including `actual_size_bytes`, `last_operation_id`, `error_code`,
    `drift_reasons`, and `legacy_identity`, which may be NULL. Every joined
    column is scanned through `sql.NullString`/`sql.NullInt64`/`sql.NullBool`
    (or equivalent) because the LEFT JOIN produces NULL for a missing related
    row; construct the `Export`/`Observation`/`ExportObservation` pointers only
    when the related row exists (for example `exports.owner IS NOT NULL`),
    decode
    `drift_reasons` and `legacy_identity` with
    `decodeStrings`/`decodeMap`, normalizes `limit<=0` to 256 and caps at 256,
    and orders by `updated_at DESC, owner, volume_id`. `VolumeStateCounts`
    groups by state. `ListRecentOperations` filters
    `running|failed|needs_attention`, normalizes/caps at 64, orders
    `updated_at DESC, id DESC`. `OperationCount` counts the same set. No new
    indexes.

#### 16. `internal/state/status_test.go` (Create)

*   **Type / Scope**: Go unit tests.
*   **Proposed Changes**: Test joined rows with export/observations, no related
    rows, NULL `actual_size_bytes`/`last_operation_id`/`error_code`,
    drift_reasons/legacy_identity round-trip, multiple owners with duplicate
    volume IDs (manual inserts with distinct resource UUID/LV/updated time, not
    `addVolume`), ordering, limits 0/negative/over-cap, state counts,
    problem-operation filter, operation count. Use `openTestStore` only where
    it can create the current schema; no migration test.

#### 17. `internal/state/ginkgo_suite_test.go` (Modify)

*   **Type / Scope**: Suite registration.
*   **Proposed Changes**: Register `TestVolumeStatusQueries` calling
    `bddTestVolumeStatusQueries`.

**Phase 4: Admin UI backend**

#### 18. `internal/adminui/server.go` (Create)

*   **Type / Scope**: HTTP server, routes, middleware, handlers.
*   **Proposed Changes**: Implement the package API below. Routes:
    `GET /`, `GET /assets/`, `GET /favicon.ico`, `GET /api/status`,
    `GET /api/config`, `PUT /api/config`, `GET/POST /api/vgs`,
    `DELETE /api/vgs/{name}`, `GET /api/classes`,
    `PUT/DELETE /api/classes/{name}`, `POST /api/targets/disable`. Methods:
    ```go
    func New(options Options) (*Server, error)
    func (server *Server) Handler() http.Handler
    func (server *Server) Serve() error
    func (server *Server) Shutdown(ctx context.Context) error
    func (server *Server) Address() net.Addr
    func (server *Server) MarkRunning() error
    ```
    `Options{ConfigPath, Config, ConfigData, Store, Capacity, VGs, Classes,
    Ready, HostUUID, AgentVersion, APIVersion, StartedAt, Logger, Now, Assets,
    Listener, Metrics, TargetMetrics, DisableTarget, VGClassGate}`. `New` validates nil dependencies, a non-empty
    `ConfigPath`, disabled state, binds loopback, verifies the address, stores
    the parsed startup configuration, and derives the allowed Host set from the
    actual listener port (so injected ephemeral listeners work). Middleware
    order: recover, security headers, Host guard, route; mutations also check
    Origin, `Sec-Fetch-Site`, and content type. Handlers: `handleStatus`,
    `handleGetConfig`, `handlePutConfig`, `handleListVGs`, `handleRegisterVG`,
    `handleDeregisterVG`, `handleListClasses`, `handleSetClass`,
    `handleRemoveClass`. VG/class mutation handlers hold `options.VGClassGate`
    across the registration/class check and the mutation; `AdminService`
    deregistration takes the same gate. The save sequence is
    `server.saveConfig(ctx, body, ifMatch, ifNoneMatch, remote)`. `Server`
    state is guarded by a mutex: `lastGoodError`, `serving`, `inFlight` status,
    and `allowedHosts`. `Serve` sets the gauge 1; `Shutdown` sets 0.

#### 19. `internal/adminui/types.go` (Create)

*   **Type / Scope**: JSON wire types.
*   **Proposed Changes**: Define `StatusResponse`, `AgentStatus`, `ISCSIInfo`, `VGStatus`,
    `VolumeStatus`, `ExportView`, `ObservationView`, `ExportObservationView`,
    `OperationView`, `Counts`, `ErrorView`, `MetricsView`, `TargetMetricsView`,
    `InitiatorMetricsView`, `LUNMetricsView`, `MetricsErrorsView`,
    `ConfigResponse`, `TLSInfo`,
    `SaveConfigResponse`, `VGListResponse`, `RegisterVGResponse`,
    `ClassListResponse`, `SetClassResponse`, `CertificateFile`,
    `CertificatesResponse`, `WarningView{Code,Message}`, and
    `APIError{Error,Code}`. Fields match the Technical Plan exactly. All slices
    are initialized non-nil and all maps (including `classes` and
    `counts.by_state`) are initialized to `{}`, never `null`. Time fields are
    RFC3339Nano UTC strings. `counts.by_state` uses the exact known states.
    `active_sessions` is `*int32`; `observed_initiator` is `*string`.

#### 20. `internal/adminui/status.go` (Create)

*   **Type / Scope**: Status assembly.
*   **Proposed Changes**: `buildStatus(ctx)` uses one `Store.Transact` with
    `ListVGs`, `ListVolumeStatuses(256)`, `VolumeStateCounts`,
    `ListRecentOperations(64)`, `OperationCount`. Seed `counts.by_state` with
    zero for all nine volume states before merging `VolumeStateCounts`. Build
    the effective running config with `Storage.Classes = Classes.Snapshot()` and merge its
    `EffectiveVGs()` with registered VGs. Probe capacity with a 10s total
    budget, 3s per VG, concurrency 4. Call shared `buildVGStatus` for status,
    `GET /api/vgs`, and `POST /api/vgs`; use capacity `VGState`/`VGUUID` for
    display, mark budget-exceeded probes `unknown`, and surface UUID mismatch
    or probe error as `unavailable` with an `ErrorView`. Compute
    `config_changed/restart_required/reason` via `configState()` and append the
    matching `config_missing`/`config_invalid`/`config_unsafe`/
    `config_unreadable` entry for unavailable disk config.
    Compute `serving` (false when observations are missing or stale), `stale`
    (2x `state.reconcile_interval_seconds`), stored ACL observed initiator,
    counts, and truncation. Return `503` on store failure and `500` if the
    effective running configuration cannot be marshaled. Use a non-blocking
    in-flight guard.

#### 20a. `internal/adminui/metrics.go` (Create)

*   **Type / Scope**: Status metrics view and rate sampler.
*   **Proposed Changes**: Add `buildMetrics(ctx, volumes) MetricsView`,
    target-to-volume joining, conversion to wire views, cumulative error
    counts, and per-target read/write/command rates from the previous successful
    sample. Store the last sample on `Server` under `metricsMu`; samples closer
    than one second do not replace the baseline. A provider error sets
    `available:false` and `error` without failing status.

#### 21. `internal/adminui/config_file.go` (Create)

*   **Type / Scope**: Config read/save and state comparison.
*   **Proposed Changes**: Define:
    ```go
    type Warning struct { Code, Message string }
    type configFile struct { Raw []byte; Parsed *config.Config; ETag string; DiskError string; DiskErrorCode string; Valid bool }
    type ConfigState struct {
        DiskYAML, RunningYAML, DiskETag string
        DiskValid bool
        DiskError, DiskErrorCode string
        ConfigChanged, RestartRequired bool
        RestartReason, LastGoodError string
        Warnings []Warning
    }
    type SaveResult struct {
        Saved bool
        Path, BackupPath, DiskETag string
        ConfigChanged, RestartRequired bool
        RestartReason string
        Warnings []Warning
    }
    func readConfig(path string) (configFile, error)
    func configEquivalent(running, candidate *config.Config, classes agent.ClassRegistry) bool
    func rawETag(data []byte) string
    func bindingSocketChanged(running, candidate *config.Config) (bool, string)
    func (server *Server) configState() (ConfigState, error)
    func (server *Server) saveConfig(ctx context.Context, body []byte, ifMatch, ifNoneMatch, remote string) (SaveResult, error)
    func preflight(ctx context.Context, candidate, running *config.Config, vgs agent.VGService) []Warning
    func configPaths(path string) (backup, lastGood, lock string)
    ```
    `readConfig` returns the display model for missing/parse_error/unsafe_path/
    read_error. `configState` uses `server.options.Config`, `ConfigPath`,
    `Classes`, and the mutex-guarded `lastGoodError`; it returns a `ConfigState`
    with `ConfigChanged:true`, `DiskValid:false`, and `DiskErrorCode` for disk
    problems; it returns an error only for a running-config marshal failure.
    `saveConfig` runs preflight (5-second bounded context) before acquiring the
    lock, then does lock/recheck/backup/atomic-write, releases the lock, and
    only then computes `config_changed`/`config_equivalent` and writes the
    audit/response. It returns the new ETag, `config_changed`,
    `restart_required`, reason, and warnings; no restart. `bindingSocketChanged` compares the exact fields listed in the
    Technical Plan. `configEquivalent` sorts/deduplicates `storage.vgs` and
    uses the live class map; portal order is meaningful for `config_changed`.
    `preflight` uses `VGs.Inspect` (read-only), skips bind probes for unchanged
    addresses, and applies a 5-second total context. `writeAtomic` uses
    `internal/fsutil.WriteFileAtomic`; `ErrDurabilityUncertain` becomes a
    `durability_uncertain` warning.

#### 22. `internal/adminui/audit.go` (Create)

*   **Type / Scope**: Audit writer.
*   **Proposed Changes**: Implement
    `writeAudit(ctx context.Context, store Store, action, operationID, preState, postState, result, errorCode, remote string) error`.
    It creates a UUID if `operationID` is empty, calls `store.Transact` with
    `tx.PutAudit(state.Audit{...})`, sets `caller:"admin-ui"`, `source_ip` as
    the host part, and writes the supplied fields; `preState`/`postState` map
    to `state.Audit.PreState`/`PostState` (no hash columns exist). Used for
    `config_save`, `vg_register`, `vg_deregister`, `class_set`, `class_remove`.
    No restart action. A 204 delete cannot carry a warning: log audit failure
    only. A 200/201 mutation includes `warnings` in its JSON body.

#### 23. `internal/adminui/vg.go` (Create)

*   **Type / Scope**: VG/class handlers.
*   **Proposed Changes**: Implement `handleListVGs`, `handleRegisterVG`,
    `handleDeregisterVG`, `handleListClasses`, `handleSetClass`,
    `handleRemoveClass`. Validate JSON, class/VG names, and content type. Map
    errors: invalid `400`/`422`, duplicate/conflict/lock `409`, in-use/class-
    mapped `409`, unavailable `422`, not-found `404`, internal `500`. Hold
    `options.VGClassGate` across each VG/class check and mutation. VG register
    calls `VGs.Register(ctx, name, "", false)` and returns `201` with
    `{"vg": buildVGStatus(...), "warnings": [...]}`. VG deregister/class remove
    return `204`. Class set checks the VG is registered (`VGs.List`), calls
    `Classes.Set`, recomputes `config_changed`/`restart_required` via
    `configState`, and returns `200` with the live map and warnings. Class
    remove calls `Classes.Remove`; a missing class is `404`.
    Write audit rows after successful mutations; map warning codes
    (`durability_uncertain`, `audit_write_failed`) to `WarningView` messages.
    Include them in 200/201 bodies; 204 deletes log only. No restart.

#### 24. `internal/adminui/assets.go` (Create)

*   **Type / Scope**: Embedded assets.
*   **Proposed Changes**: `//go:embed all:assets` for the generated output and
    `//go:embed fallback.html` separately. `Options.Assets` is the resolved
    output root by default (`fs.Sub(embeddedAssets, "assets")`); injected assets
    are used as the resolved root. Serve `index.html` at `/`, hashed files under
    `/assets/`, reject directory/dot/`..` paths, set exact cache headers, serve
    fallback `503` when `index.html` is missing.

#### 25. `internal/adminui/fallback.html` (Create)

*   **Type / Scope**: Static fallback.
*   **Proposed Changes**: Plain HTML with the `make build` instruction; no
    script, external asset, or inline event handler.

#### 26. `internal/adminui/assets/.gitkeep` (Create)

*   **Type / Scope**: Placeholder.

#### 27. `internal/adminui/assets/.gitignore` (Create)

*   **Type / Scope**: Generated-asset ignore.
*   **Proposed Changes**: Ignore everything except `.gitkeep` and the ignore
    file.

#### 28. `internal/adminui/ginkgo_suite_test.go` (Create)

*   **Type / Scope**: Suite.
*   **Proposed Changes**: Register `TestAdminUIHandlers`,
    `TestAdminUIConfigFile`, `TestAdminUIStatus`, `TestAdminUIVGClasses`, and
    `TestAdminUIAudit`.

#### 29. `internal/adminui/server_test.go` (Create)

*   **Type / Scope**: HTTP tests.
*   **Proposed Changes**: Test Host, Origin, `Sec-Fetch-Site`, content types,
    body limit, status/error codes, CSP/cache headers, fallback/assets,
    `/favicon.ico` 204, unknown route 404, wrong method 405, panic recovery,
    empty `ConfigPath`, injected ephemeral-listener Host validation, and that
    no restart route exists. Add a `-race` test with concurrent status requests
    plus `MarkRunning`/`Shutdown` to cover the shared server state. Main wiring
    is covered by `main_test.go` and the manual host check.

#### 30. `internal/adminui/config_file_test.go` (Create)

*   **Type / Scope**: Config file tests.
*   **Proposed Changes**: Test regular/missing/invalid/symlink/non-regular/
    unreadable read models and codes; `configState` returns disk errors without
    a Go error; ETag formats; `If-Match` and `If-None-Match`; `.bak`
    success/failure; `.last-good`; `.lock` timeout; directory-fsync warning; two
    sequential saves with new ETag; comment-only and VG-order saves set no
    `restart_required`; a pure target-portal reorder sets `config_changed` but
    no `restart_required`, with a warning; binding socket changes set
    `restart_required` with deterministic reasons; preflight runs before the
    lock (stalled LVM does not block a class update), skips unchanged
    addresses, checks changed addresses, VG eligibility via `Inspect`, secret
    key changes, and non-blocking warnings; non-cooperating writer hook before
    final recheck returns `409`; concurrent save + class `Set` completes without
    a lock-order deadlock (timeout guard).

#### 31. `internal/adminui/status_test.go` (Create)

*   **Type / Scope**: Status tests.
*   **Proposed Changes**: Assert exact JSON fields, arrays never null, serving
    true/false including `lv_present` and stale/missing observations, active
    sessions null when unknown, stored ACL observed initiator with
    `initiator_source:"acl"`, configured VGs recomputed from the live class map,
    capacity budget-exceeded `unknown`, capacity error `unavailable`, UUID
    mismatch, `config_missing`/`config_invalid` for disk config, counts/truncation
    at 256/64, `503` on store failure, `500` on running-config marshal failure,
    and no cache (two requests see a changed row).

#### 32. `internal/adminui/vg_test.go` (Create)

*   **Type / Scope**: VG/class handler tests.
*   **Proposed Changes**: Test register 201, same-UUID idempotent 201,
    different-UUID 409, ineligible 422, `201` body `{"vg":...,"warnings":[]}`,
    deregister 204, in-use 409, class-mapped 409, list via `buildVGStatus`,
    class set/remove, unknown VG 422, duplicate class 409, missing class 404,
    config write failure leaves registry unchanged, lock timeout 409, audit rows
    for each mutation, 204 audit failure log-only, and concurrent VG/class
    mutations under `VGClassGate`.

#### 33. `internal/adminui/audit_test.go` (Create)

*   **Type / Scope**: Audit tests.
*   **Proposed Changes**: Test fields, UUID, source host, error code, and
    audit failure warning.

**Phase 5: Preact frontend**

#### 34. `web/package.json` (Create)

*   **Type / Scope**: npm manifest.
*   **Proposed Changes**: `kouja-admin-ui`, private, ESM. Scripts `dev: vite`,
    `build: tsc --noEmit && vite build`, `typecheck: tsc --noEmit`,
    `preview: vite preview`. Exact `preact@11.2.0`, `vite@8.3.6`,
    `typescript@7.0.2`, `@preact/preset-vite@2.10.6`. No router/UI kit.

#### 35. `web/package-lock.json` (Create)

*   **Type / Scope**: Lockfile.

#### 36. `web/tsconfig.json` (Create)

*   **Type / Scope**: TypeScript config.
*   **Proposed Changes**: `target: "ES2022"`, `module: "ESNext"`,
    `moduleResolution: "Bundler"`, `lib: ["ES2022","DOM","DOM.Iterable"]`,
    `jsx: "react-jsx"`, `jsxImportSource: "preact"`, `strict: true`,
    `noEmit: true`, `types: ["vite/client"]`; include `src` and
    `vite.config.ts`.

#### 37. `web/vite.config.ts` (Create)

*   **Type / Scope**: Vite config.
*   **Proposed Changes**: Preact preset; `base:"/"`; `build.target:"es2022"`;
    `build.modulePreload.polyfill:false`; `build.outDir:"../internal/adminui/assets"`;
    `build.emptyOutDir:false`; dev proxy `/api` to `http://127.0.0.1:9444`,
    `changeOrigin:true`, `configure` rewrites `Origin` to
    `http://127.0.0.1:9444`.

#### 38. `web/index.html` (Create)

*   **Type / Scope**: Vite entry.
*   **Proposed Changes**: Root element `id="app"` and
    `<script type="module" src="/src/main.tsx"></script>`. No inline script or
    external asset.

#### 39. `web/.nvmrc` (Create)

*   **Type / Scope**: Node version.
*   **Proposed Changes**: `22.12.0`.

#### 40. `web/src/main.tsx` (Create)

*   **Type / Scope**: Entry.
*   **Proposed Changes**: `render(<App />, document.getElementById("app")!)`;
    import `style.css`.

#### 41. `web/src/api.ts` (Create)

*   **Type / Scope**: API client.
*   **Proposed Changes**: `getStatus(signal?)`, `getConfig(signal?)`,
    `saveConfig(yaml, etag, ifNoneMatch)`, `getVGs()`, `registerVG(name)`,
    `deregisterVG(name)`, `getClasses()`, `setClass(name, vg)`,
    `removeClass(name)`. Config save uses `If-Match` for an existing file and
    `If-None-Match: *` for a missing file, `application/yaml`; other mutations
    use `application/json`. Import `ApiError{status,code,message}` from
    `types.ts`; do not redeclare it. Caller owns `AbortController`.

#### 42. `web/src/types.ts` (Create)

*   **Type / Scope**: Types.
*   **Proposed Changes**: Export `StatusResponse`, `AgentStatus`, `ISCSIInfo`, `VGStatus`,
    `VolumeStatus`, `ExportView`, `ObservationView`,
    `ExportObservationView`, `OperationView`, `Counts`, `ErrorView`,
    `MetricsView`, `TargetMetricsView`, `InitiatorMetricsView`,
    `LUNMetricsView`, `MetricsErrorsView`, `ClientConnectionView`,
    `ConfigResponse`, `TLSInfo`, `SaveConfigResponse`, `VGListResponse`,
    `RegisterVGResponse`, `ClassListResponse`, `SetClassResponse`,
    `CertificateFile`, `CertificatesResponse`, `WarningView`, `ApiError`. Match the Technical Plan JSON exactly. Define
    `ApiError` only here; `api.ts` imports it. Define the component callback
    interfaces here:
    ```ts
    export interface StatusPageProps {
        status: StatusResponse | null;
        loading: boolean;
        error: string;
        onRefresh: () => void;
    }
    export interface ConfigPageProps {
        config: ConfigResponse;
        vgs: VGStatus[];
        classes: Record<string, string>;
        onSaved: (result: SaveConfigResponse) => void;
        onDirtyChange: (dirty: boolean) => void;
        onRefreshVGs: () => Promise<void>;
        onReloadConfig: () => Promise<void>;
    }
    export interface VgsPanelProps {
        vgs: VGStatus[];
        classes: Record<string, string>;
        classActionsDisabled: boolean;
        onRegister: (name: string) => Promise<void>;
        onDeregister: (name: string) => Promise<void>;
        onSetClass: (name: string, vg: string) => Promise<void>;
        onRemoveClass: (name: string) => Promise<void>;
    }
    ```
    Export these interfaces and import them in `app.tsx` and the components.
    `ConfigPage` calls `onReloadConfig` after a class action and passes
    `classActionsDisabled={dirty}` to `VgsPanel`. `onSaved` receives the save
    response; `onDirtyChange` feeds the `beforeunload` guard.

#### 43. `web/src/format.ts` (Create)

*   **Type / Scope**: Formatting.
*   **Proposed Changes**: `formatBytes` binary units with 2 decimals
    (`0 B`, `1.00 KiB`, ...; negative `-1.00 KiB`), `formatTime` using
    `Intl.DateTimeFormat(undefined,{dateStyle:"medium",timeStyle:"medium"})`,
    `formatUptime` as `0s`, `59s`, `1m 5s`, `2h 3m`, `2d 3h`. No state labels.

#### 44. `web/src/style.css` (Create)

*   **Type / Scope**: Styles.
*   **Proposed Changes**: Layout, tabs, cards, tables, badges, forms, error and
    warning blocks, config textarea. System fonts; no external assets. Table
    scroll at `max-width: 900px`. Class names shared with components.

#### 45. `web/src/vite-env.d.ts` (Create)

*   **Type / Scope**: Vite types.

#### 46. `web/src/app.tsx` (Create)

*   **Type / Scope**: App shell.
*   **Proposed Changes**: Own tabs (`status`, `config`), status fetch/poll every
    5000ms when visible and `document.visibilityState === "visible"`, abort the
    previous request, and fetch `/api/config` for the banner. Fetch
    `/api/vgs`/`/api/classes` for the config tab and refresh them after live
    actions. Props: `StatusPage{status,loading,error,onRefresh}`,
    `ConfigPage{config,vgs,classes,onSaved,onDirtyChange,onRefreshVGs,onReloadConfig}`.
    The config banner uses `config_changed`/`restart_required`; no restart
    action.

#### 47. `web/src/components/StatusPage.tsx` (Create)

*   **Type / Scope**: Status view.
*   **Proposed Changes**: Props `{status, loading, error, onRefresh}`. Render
    agent summary, config-changed/restart-required banner (message only), a
    live iSCSI traffic card, a read-only VG table, volume table, operation
    table, and errors. The traffic card shows cumulative read/write/command
    totals, sampled rates, per-target volume/initiator rows, client IP/port
    connections, LUN status, and error/reset counters; render `—` when
    `rates_available` is false. The `TrafficCard` child component owns the
    tables, the Disable action, and error-detail formatting.
    Pass truncation flags. No restart button and no VG/class mutation controls;
    those live in `ConfigPage`.

#### 47a. `web/src/components/TrafficCard.tsx` (Create)

*   **Type / Scope**: Live iSCSI traffic view.
*   **Proposed Changes**: Render aggregate read/write/command rates and totals,
    session count, per-target volume/initiator rows, initiator RX/TX and PDU
    counts, LUN status/busy/reset information, established client IP/port
    connections, target mapping, and error details. Add a per-target Disable
    button with a warning that active sessions will be dropped and that the
    database definition remains. Show `—` for unavailable rates and explicit
    unavailable/error messages when metrics or connection collection fails.

#### 48. `web/src/components/ConfigPage.tsx` (Create)

*   **Type / Scope**: Config view.
*   **Proposed Changes**: Props `{config, vgs, classes, onSaved, onDirtyChange, onRefreshVGs, onReloadConfig}`. Load disk
    YAML, track dirty state, show running YAML in a read-only panel, ETag,
    disk error code, warnings, `config_changed`/`restart_required` banner, and
    a TLS card with the server certificate subject, DNS server names, IP
    addresses, expiry, and parse error from `config.tls`.
    Show a note that `storage.vgs` is part of the first-start `EffectiveVGs()`
    seed and `storage.classes` is loaded every start; live VG/class changes
    belong in `VgsPanel`.
    Save posts YAML, updates the ETag from the response, and calls `onSaved`.
    On `409` reload; on `422` show parser error. Render `VgsPanel` and
    `CertificatesPanel` below the editor; after a successful VG/class action call
    `onRefreshVGs` and `onReloadConfig`. Disable class set/remove while the YAML
    editor is dirty, because those actions write the same config file; VG
    register/deregister are state-only and may remain enabled. A pending raw
    class edit is handled by the live class panel or by saving the raw file;
    `.last-good` is not refreshed. No restart
    control, no polling. Disable save when clean/in-flight; for
    unsafe/missing/read-error, follow the error code. Add `beforeunload` dirty
    warning.

#### 49. `web/src/components/VgsPanel.tsx` (Create)

*   **Type / Scope**: Live VG/class controls.
*   **Proposed Changes**: Props `{vgs, classes, classActionsDisabled, onRegister, onDeregister, onSetClass, onRemoveClass}`. Show registered/configured VG rows with capacity
    and error. Add VG name input; remove button disabled when state is not
    `active`; a VG with volumes returns `409` and the error is shown. Class
    mapping input with VG select; remove button. Show per-action errors and
    success messages. This panel is the live path for VG/class changes;
    `storage.vgs` in raw YAML remains part of the first-start
    `EffectiveVGs()` seed, and raw `storage.classes` edits apply on the next
    start. No restart.

#### 50. `web/src/components/VolumesTable.tsx` (Create)

*   **Type / Scope**: Volume table.
*   **Proposed Changes**: Props `{volumes, truncated, total}`. Render identity,
    owner, VG/class, sizes, state, serving, drift reasons, export, target/ACL/
    namespace presence, active sessions (`unknown` when null), session state,
    ACL/observed initiator, observation times/stale, update time. A null
    export/observation/export_observation renders `unknown`, not `false`.
    Problems-only filter predicate:
    `state === "drift" || state === "failed" || drift_reasons.length > 0`.
    Horizontal scroll.

#### 51. `web/src/components/OperationsTable.tsx` (Create)

*   **Type / Scope**: Operations table.
*   **Proposed Changes**: Props `{operations, truncated, total}`. Render exact
    operation fields; highlight failed/needs_attention. Show truncation.

#### 52. `web/src/components/StateBadge.tsx` (Create)

*   **Type / Scope**: State badge.
*   **Proposed Changes**: Props `{state, kind}`. Mapping for volume states
    (`creating`, `available`, `exporting`, `exported`, `unexporting`,
    `resizing`, `deleting`, `failed`, `drift`), export states (`exporting`,
    `exported`, `unexporting`,
    `unexported`, `failed`, `drift`), session states, operation states, and VG states
    (`active`, `unavailable`, `unknown`, `not_registered`); unknown neutral
    class. Exact labels/classes shared with `style.css`.

#### 53. `web/README.md` (Create)

*   **Type / Scope**: Developer docs.
*   **Proposed Changes**: `npm ci`, `npm run dev`, `npm run build`, typecheck,
    output path, dev proxy, `make build` vs `make build-go`.

#### 53a. `web/src/components/CertificatesPanel.tsx` (Create)

*   **Type / Scope**: Client certificate browser.
*   **Proposed Changes**: Fetch `/api/certificates`; show directory, file name,
    kind, role, size, modification time, and a download link. Provide a separate
    “Download client CA” link from the same response's `ca` field. Warn when
    private keys are present. The server restricts files to the configured
    client certificate directory and rejects symlinks, sensitive CA/server
    names, and path traversal.

**Phase 6: Process wiring and build**

#### 54. `cmd/kouja-agent/main.go` (Modify)

*   **Type / Scope**: Entry point.
*   **Proposed Changes**: Load with `LoadWithData`; create state/lvm, a shared
    `vgClassGate := &sync.Mutex{}`, the class map from `cfg.Storage.Classes`,
    then the VG manager with that class map; pass the gate to `AdminService`,
    `VolumeService` does not need it, and the admin `Options`; create the mTLS
    server; create the admin server if enabled
    (non-fatal); log a warning that the loopback admin UI has no authentication
    and is root-equivalent; set gauge 0 on disabled; start backup/reconcile; set
    readiness; start admin `Serve` and `MarkRunning` after readiness; wait on
    signal/mTLS error; shut down admin with 5s, mTLS, wait goroutines. No
    restart path. Add `startAdminServer(cfg, options) *adminui.Server` helper.
    On admin Serve error call `adminServer.Shutdown(ctx)`, never a process stop
    callback. Initialize `kouja_admin_ui_up` to 0.

#### 55. `cmd/kouja-agent/main_test.go` (Create)

*   **Type / Scope**: Main tests.
*   **Proposed Changes**: Test `startAdminServer` disabled, bind-failure, and
    free-port cases. No exit-code/restart tests.

#### 56. `Makefile` (Modify)

*   **Type / Scope**: Build.
*   **Proposed Changes**: `NPM ?= npm`, `UI_DIR ?= web`. The Vite
    `build.outDir` (`../internal/adminui/assets` from `web/`) is the single
    output definition; the Makefile does not duplicate it. `clean-assets`
    target removes the fixed asset directory except `.gitkeep`/`.gitignore`.
    `ui: clean-assets` runs `cd $(UI_DIR) && $(NPM) ci`,
    `cd $(UI_DIR) && $(NPM) run build`, then verifies
    `internal/adminui/assets/index.html` exists. `build: ui` then
    `go build ./...`. `build-go: clean-assets` then `go build ./...`; this
    guarantees the fallback page after a prior `make build`. Add all targets to
    `.PHONY`.

#### 57. `.gitignore` (Modify)

*   **Type/Scope**: Ignore `web/node_modules/`; generated assets use nested
    ignore; no TypeScript build info.

#### 58. `.github/workflows/ci.yml` (Modify)

*   **Type/Scope**: Node 22.12.0 with npm cache; `make build`; Go tests/lint;
    required `make build-go` fallback check.

#### 59. `deploy/install/install.sh` (Modify)

*   **Type/Scope**: Add disabled `admin_ui` section; state the single-admin/
    root-equivalent/SSRF precondition; no port-forwarding.

#### 60. `README.md` (Modify)

*   **Type/Scope**: Add admin UI capability, Node build vs `make build-go`,
    config example, live VG actions and local-only class actions,
    config-changed/restart-required policy (manual restart only for binding
    socket changes), `.bak`/`.last-good` recovery, security precondition,
    `kouja_admin_ui_up`, `web/` and `internal/adminui`/`internal/fsutil` layout
    entries, and the design link.

#### 61. `docs/designs/kouja.md` (Modify)

*   **Type/Scope**: Base design relationship.
*   **Proposed Changes**: Update the Alternative N supersession note and the
    compatibility bullet to say the loopback listener is limited to the local
    configuration/status UI and live VG/class actions, while the mTLS admin
    service remains the authenticated API for volume lifecycle and other
    administrative operations. Do not change the base VG seed model.

#### Phase checkpoints

*   After section 1: `go test -tags=unit ./internal/config ./internal/fsutil`.
*   After section 2: `go test -tags=unit ./internal/agent`.
*   After section 3: `go test -tags=unit ./internal/state`.
*   After section 4: `go test -tags=unit ./internal/adminui`.
*   After section 5: `cd web && npm run typecheck && npm run build`.
*   After section 6: `go test -tags=unit ./cmd/kouja-agent`, `make build`,
    `make build-go`, `make test`, `make coverage-check`, `make lint`.

#### Verification Plan

*   **Frontend**: `make ui` with Node 22.12; typecheck/build pass; assets
    contain `index.html` + hashed JS/CSS; no inline script.
*   **Builds**: `make build` and `make build-go`; `make build-go` after a
    prior `make build` cleans the generated assets and serves the fallback page;
    `go build ./...` on a clean tree.
*   **Unit**: config admin_ui/LoadWithData; live VG/class services; state joins,
    limits 256/64, arrays; config read/save/ETag/lock/backup/last-good;
    HTTP statuses 400/403/404/405/409/413/415/422/428/500/503; Host/Origin/
    Sec-Fetch-Site; VG/class handlers; audit; status serving/stale/unknown;
    main admin disabled/failure.
*   **No-restart verification**: assert no `/api/restart` route, no systemctl
    invocation, no exit-75 path; config save returns messages only.
*   **Live VG/class**: add/remove VG, add/remove class, persistence across
    restart, VG-in-use rejection, config write failure rollback.
*   **BDD/coverage/lint**: `make test-bdd`, `make coverage-check`, `make lint`.
*   **Manual host**: enable UI; status per-volume; add/remove VG live; class
    live; save non-socket config (config_changed, no restart); save socket
    change (restart_required, manual systemctl); admin port occupied (mTLS
    continues); disabled (no port, no `.last-good`); failed manual restart
    recovery; no external network exposure.
*   **Residual gaps**: no browser automation; full `run()` shutdown ordering is
    manual. Documented.

--------------------------------------------------------------------------------

<!-- Provenance: Indicates initial structural drafting via EGM. -->

*Drafted with EGM*
