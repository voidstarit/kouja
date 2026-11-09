# kouja: Per-Host Block Storage Provisioning and Export Agent

* **Author:** kouja maintainers
* **Status:** Approved for implementation (Phase 0 tracked)
* **Date:** 2026-11-05

---

## Overview

kouja is a per-host service that creates logical volumes in administrator-registered LVM volume groups and exports them as network block devices. An administrator registers the volume groups kouja may use. A caller then asks that host's kouja instance to create a volume and, by default, export it to one initiator over iSCSI or NVMe-oF/TCP. The callers are Rookery, a VM system, and a Kubernetes CSI driver that is part of this project.

Each host is independent. There is no cluster membership, no peer communication, and no global placement service. The caller selects the host, connects the initiator, mounts the filesystem, and rescans after a resize. kouja creates the thick LV, configures the target side, records ownership and state in local SQLite, and reports allocatable capacity. mTLS authenticates callers, and a generated credential controls access to the one allowed initiator for that volume.

## Problem Statement

Today a storage host is prepared by hand: create an LV, configure LIO or nvmet, add an ACL or CHAP secret, and remember which caller owns which device. This leaves no stable API, no ownership boundary between Rookery and Kubernetes, no durable record of an interrupted operation, and no way to restore exports after a reboot. A caller that times out cannot tell whether a volume was created. A host restart can lose nvmet configfs state. An operator cannot tell which volumes are still in use or whether a VG has room.

Existing storage control planes solve this by adding a cluster, a central database, and a scheduler. kouja does not need that. The callers already know which host a volume belongs to, and the data path is a network block export, not a replicated cluster volume. What is missing is a host-local agent that exposes a narrow API, records intent and ownership, serializes operations on a volume, and reports what exists without automatic repair.

## Goals

* Provide one versioned API, using protobuf over gRPC with a generated HTTP/JSON gateway, for create, grow, shrink, delete, export, unexport, credential retrieval, capacity reporting, and admin import.
* Create a thick LV in an administrator-registered VG and export it to exactly one initiator in one call by default. Allow an unexported create for the CSI controller.
* Support iSCSI with CHAP and NVMe-oF/TCP with TLS 1.3 pre-shared keys.
* Authenticate callers with mTLS, derive a stable caller identity from the certificate SAN and issuer, and enforce per-volume ownership.
* Survive a process or host restart: restore exports from SQLite, re-run interrupted operations up to the configured retry and timeout bound, and report a `drift` state when the database and the host disagree.
* Report allocatable capacity per VG, including extent size and maximum volume size, so callers can place volumes without a central scheduler.
* Provide a CSI driver that selects a host from a StorageClass SRV record, creates a volume, publishes it to the scheduled node, expands it, and deletes it.
* Record every mutation in an audit trail with the caller, volume, protocol, initiator, pre-state, post-state, and result.
* Fail closed under the startup predicate: missing or corrupt SQLite, a missing database with existing `kouja-*` LVs, a host identity file that does not match the database while volumes exist, or a missing credential key while credentials exist.
* On the reference host defined in the verification plan, complete 99% of create, export, and delete calls within 5 seconds for volumes up to 1 TiB, excluding an explicit wipe.
* Restore all non-drifted exports before `/readyz` reports ready, within 60 seconds for 100 exported volumes on the reference host.

## Non-Goals

* Thin provisioning, raw partitions, plain files, ZFS, or any backend other than LVM.
* Snapshots, clones, replication, migration, backup of volume data, or encryption of volume data at rest. TLS-PSK protects the NVMe-oF transport, not the stored data.
* Multi-initiator access, shared-disk clusters, or CSI `RWX`.
* Cross-host placement or scheduling inside kouja. The caller or the CSI driver chooses the host.
* Consumer-side discovery, login, mount, filesystem resize, or rescan. The caller or the CSI node plugin performs those steps.
* Preventing a caller from exercising its own destructive intent through authorization. kouja still enforces `allow_shrink` for shrink, `SESSION_ACTIVE` and `SESSION_STATE_UNKNOWN` for delete and unexport, and `VOLUME_IN_DRIFT` for mutations on a drifted volume.
* Wiping old data by default. A wipe happens only when explicitly requested.
* Replacing LIO, nvmet, or LVM with a new data path.
* A central control plane, a fleet-wide database, or a cluster membership protocol.
* Automatic repair of drift. kouja records the mismatch and requires an administrator to resolve it.

## Open Questions

* **Configfs and kernel coverage**: The exact LIO and nvmet attributes for active-session visibility and TLS-PSK vary by kernel. The supported kernel matrix, capability probes, and fallback behavior are tracked in Risks & Mitigations. Owner: platform team; decide before implementation.
* **Target-manager validation (decision: direct configfs)**: v1 uses direct configfs. A five-day spike validates it before item 3; if it fails, the fallback is `targetd`/`rtslib` for iSCSI only, with nvmet direct. Decision criteria: session observation, TLS-PSK support, dependency footprint, failure behavior, and implementation effort. Owner: storage team; record the evidence in `docs/decisions/target-manager.md`.

## Critical User Journeys (CUJs)

### CUJ 1: Rookery creates and exports an iSCSI disk

* **Given**: A storage host has a registered VG with free extents, an iSCSI portal, and a Rookery client certificate.
* **When**: Rookery calls `CreateVolume` with a volume ID, size, `export=true`, protocol `ISCSI`, and the VM host's IQN.
* **Then**: kouja creates a thick LV, creates the LIO target and ACL, sets the CHAP credential, and returns the target IQN, portal, LUN, and credential. The VM host logs in and sees the disk. The volume is `exported`, and an audit row records the caller and the result.

### CUJ 2: Kubernetes provisions and attaches a PV

* **Given**: A StorageClass names an SRV record and a protocol. A PVC exists, and a pod is scheduled to a node.
* **When**: The external-provisioner calls `CreateVolume` with `export=false`; the external-attacher later calls `ControllerPublishVolume` with the node ID.
* **Then**: The CSI driver resolves the SRV record, selects a host with capacity, creates an unexported LV, and returns `v1:<host-uuid>:<resource-uuid>`. At publish, it maps the node to its IQN or NQN, calls `CreateVolume` with `export=true`, and returns the portal, target, and LUN or NSID. The node plugin fetches the credential, logs in, stages, formats if needed, mounts, and the pod sees the filesystem.

### CUJ 3: Kubernetes grows a mounted volume

* **Given**: An exported volume is mounted on a node.
* **When**: The PVC is resized. The external-resizer calls `ControllerExpandVolume`; the kubelet calls `NodeExpandVolume`.
* **Then**: kouja extends the LV, updates the LIO backstore or nvmet namespace, and returns `rescan_required=true`. The node plugin rescans the device, grows the filesystem, and the pod sees the new size. The CSI driver never advertises shrink.

### CUJ 4: Host reboot restores exports

* **Given**: The host rebooted. SQLite and its backups are intact. nvmet configfs is empty, and LIO persistence is disabled.
* **When**: kouja starts.
* **Then**: It verifies the database, detects that the kernel boot ID changed, recreates the recorded exports, observes every managed LV, marks any unresolved mismatch as `drift`, and then reports ready. Consumers reconnect to the restored targets.

### CUJ 5: Administrator imports an existing LV and export

* **Given**: A host contains a hand-built LV and an LIO target with an active initiator.
* **When**: An administrator calls `ImportVolume` with the owner, VG, LV UUID, size, protocol, initiator, target name, LUN or NSID, and credential.
* **Then**: kouja validates the LV, VG registration, target binding, and absence of conflicting state. It adopts the objects without renaming them, stores any legacy names in `legacy_identity`, marks the volume `exported`, and writes an audit row. Any validation failure rejects the import with no state change.

### CUJ 6: Delete with an active session

* **Given**: A volume is exported and the target reports an active session.
* **When**: The owner calls `DeleteVolume` without `force`.
* **Then**: kouja returns `FailedPrecondition` with reason `SESSION_ACTIVE`; no target or LV is changed. With `force=true`, kouja removes the target and the LV, marks the volume `deleted`, and records the forced teardown in the audit trail.

## Proposed Architecture

### Architecture map

1. System context
2. Components and deployment
3. API contract
4. State model
5. Operation execution
6. Volume and export state machine
7. LVM backend
8. iSCSI/LIO adapter
9. NVMe-oF/nvmet adapter
10. Credentials
11. Capacity and placement
12. CSI driver
13. Security and authorization
14. Startup and reconciliation
15. Observability and audit
16. Configuration
17. Failure handling summary

### System context

```text
                       control traffic (mTLS)
+----------------------+   gRPC / HTTP-JSON   +---------------------------+
| Rookery VM service   | --------------------> | kouja-agent (host A)      |
+----------------------+                       |                           |
                                               |  API -> operation manager |
+----------------------+                       |       -> LVM backend      |
| kouja-csi-controller | --------------------> |       -> LIO adapter      |
| (Kubernetes)         |                       |       -> nvmet adapter    |
+----------+-----------+                       |       -> SQLite state     |
           |                                   +-------------+-------------+
           | CSI                                           |
           v                                               | configfs
+----------------------+   iSCSI / NVMe-oF/TCP             v
| kouja-csi-node       | <---------------------------------+
| (one per Kubernetes node)   |
+----------------------+
```

Each storage host runs one `kouja-agent`. The agent owns the host's LVM operations, target configuration, SQLite state, and API. Rookery talks to the agent directly. The CSI controller and node plugins talk to the same API. There is no peer communication, no shared database, and no scheduler inside the agent.

### Components and deployment

* **`kouja-agent`**: A Go binary installed as a systemd service. It runs as root because it writes configfs, creates LVs, and manages credentials. It serves gRPC and the generated HTTP/JSON gateway on one mTLS listener. It owns SQLite at `/var/lib/kouja/state.db`.
* **`kouja-csi-controller`**: A Kubernetes Deployment that implements the CSI Identity and Controller services. It uses the CSI external-provisioner, external-attacher, and external-resizer sidecars. It resolves SRV records, selects hosts, and calls the agent.
* **`kouja-csi-node`**: A privileged DaemonSet on every Kubernetes node. It implements the CSI Node service, connects iSCSI or NVMe-oF sessions with the host's `iscsiadm` or `nvme-cli`, stages and mounts devices, and performs node-side rescan and filesystem growth.
* **`kouja-proto`**: The protobuf package `kouja.v1` and generated Go and HTTP gateway code. It is the single source for both transports.

The repository layout is:

```text
cmd/kouja-agent/            agent entry point and flags
cmd/kouja-csi-controller/   CSI controller entry point
cmd/kouja-csi-node/         CSI node entry point
proto/kouja/v1/             protobuf definitions
gen/kouja/v1/               generated gRPC and gateway code
internal/agent/             service wiring and request validation
internal/state/             SQLite store, migrations, audit
internal/lvm/               LVM commands and observation
internal/target/            LIO and nvmet configfs adapters
internal/credential/        CHAP and TLS-PSK generation and storage
internal/operations/        per-volume locks, journal, retry
internal/reconcile/         startup observation and export restore
internal/capacity/          VG capacity and host selection data
internal/auth/              mTLS identity and role mapping
internal/observability/     metrics, structured logs, readiness
internal/config/            YAML configuration
internal/csi/               CSI controller, node, inventory, and client
deploy/systemd/             agent unit and tmpfiles
deploy/kubernetes/          CSIDriver, RBAC, controller, node, StorageClass
```

### API contract

The API is defined in protobuf. gRPC serves typed clients, and a generated in-process `grpc-gateway` handler serves HTTP/JSON on the same TLS listener with the same messages. The gateway reads the TLS peer from the inbound connection; it never trusts a forwarded client-identity header. The package is `kouja.v1`; HTTP paths use `/v1`. Changes are additive within the version.

```proto
service VolumeService {
  rpc CreateVolume(CreateVolumeRequest) returns (CreateVolumeResponse);
  rpc ExportVolume(ExportVolumeRequest) returns (ExportVolumeResponse);
  rpc UnexportVolume(UnexportVolumeRequest) returns (UnexportVolumeResponse);
  rpc ResizeVolume(ResizeVolumeRequest) returns (ResizeVolumeResponse);
  rpc DeleteVolume(DeleteVolumeRequest) returns (DeleteVolumeResponse);
  rpc GetVolume(GetVolumeRequest) returns (GetVolumeResponse);
  rpc ListVolumes(ListVolumesRequest) returns (ListVolumesResponse);
  rpc GetCredential(GetCredentialRequest) returns (GetCredentialResponse);
  rpc RotateCredential(RotateCredentialRequest) returns (RotateCredentialResponse);
  rpc GetCapacity(GetCapacityRequest) returns (GetCapacityResponse);
  rpc GetInfo(GetInfoRequest) returns (GetInfoResponse);
}

service AdminService {
  rpc RegisterVG(RegisterVGRequest) returns (RegisterVGResponse);
  rpc DeregisterVG(DeregisterVGRequest) returns (DeregisterVGResponse);
  rpc ListVGs(ListVGsRequest) returns (ListVGsResponse);
  rpc ImportVolume(ImportVolumeRequest) returns (ImportVolumeResponse);
  rpc ResolveDrift(ResolveDriftRequest) returns (ResolveDriftResponse);
  rpc ListOrphans(ListOrphansRequest) returns (ListOrphansResponse);
  rpc WipeVolume(WipeVolumeRequest) returns (WipeVolumeResponse);
  rpc TransferOwnership(TransferOwnershipRequest) returns (TransferOwnershipResponse);
  rpc AdminDeleteVolume(AdminDeleteVolumeRequest) returns (AdminDeleteVolumeResponse);
  rpc GrantCredential(GrantCredentialRequest) returns (GrantCredentialResponse);
  rpc RevokeGrant(RevokeGrantRequest) returns (RevokeGrantResponse);
}
```

`TransferOwnership` and `AdminDeleteVolume` require the admin role. `GrantCredential` and `RevokeGrant` require the controller role or the admin role.

Key request fields:

* `CreateVolumeRequest`: `volume_id`, `size_bytes`, `vg` (required), `export` (optional, default true), `protocol`, `initiator`, `include_credential` (optional, default true), `wipe` (default false), `request_id` (caller correlation ID).
* `ExportSpec`: `protocol` (`ISCSI` or `NVME_TCP`), `initiator` (IQN or NQN), `include_credential` (default true).
* `UnexportVolumeRequest`: `force` (default false).
* `ResizeVolumeRequest`: `size_bytes`, `allow_shrink` (default false).
* `DeleteVolumeRequest`: `force` (default false).
* `GetCapacityResponse`: one entry per registered VG with `vg_name`, `vg_uuid`, `vg_state`, `total_bytes`, `free_bytes`, `allocatable_bytes`, `extent_size_bytes`, `max_volume_bytes`, and `observed_at`.
* `ListStorageClassesResponse`: one entry per configured class with `name`, backing VG, state, and the same capacity fields.
* `ImportVolumeRequest`: `volume_id`, `owner`, `vg`, `lv_uuid` (LVM UUID) or `lv_name`, `size_bytes`, `export_spec`, `target_name`, `lun` or `nsid`, `credential` (oneof CHAP or TLS-PSK), `allow_active_sessions`.
* `GrantCredentialRequest`: `volume_id`, `node_id`, `node_caller_id`, `expires_at`.

The API is an ensure API, not a strict create. A `CreateVolume` call with the same owner, volume ID, and request hash returns the current volume and export. A call that adds an export to an existing unexported volume performs the export. A call that changes the initiator of an exported volume returns `FailedPrecondition` with reason `VOLUME_ALREADY_EXPORTED`; the caller unexports first. The conflict rules are:

| Existing state | Request difference | Result |
| --- | --- | --- |
| same request hash | none | Return the existing volume and export. |
| `available` | `export=true` with an initiator | Export the volume and return the details. |
| `exported` | same initiator, `export=true` | Return the existing export and credential. |
| `exported` | different initiator | `FailedPrecondition`, `VOLUME_ALREADY_EXPORTED`. |
| any | different size | `FailedPrecondition`, `VOLUME_REQUEST_MISMATCH`; use `ResizeVolume`. |
| any | different VG | `FailedPrecondition`, `VOLUME_REQUEST_MISMATCH`. |
| any | different protocol | `FailedPrecondition`, `VOLUME_REQUEST_MISMATCH`. |
| any | `wipe=true` on an existing volume | `FailedPrecondition`, `VOLUME_REQUEST_MISMATCH`; use `WipeVolume`. |
| `exported` | `export=false` | Return the existing exported volume; do not unexport. |
| `deleted` tombstone | same `volume_id` | Create a new volume with a new `resource_uuid`; an old handle returns `STALE_HANDLE`. |

`ExportVolume` is a thin alias for `CreateVolume` with `export=true`. Both paths use the same operation action key, idempotency rules, and credential semantics.

Errors use gRPC status codes plus `google.rpc.ErrorInfo` details. `ErrorInfo.domain` is `kouja.v1`. `ErrorInfo.metadata` carries `retryable` (`"true"` or `"false"`), `operation_id`, and optional `allocatable_bytes`, `lvm_exit_code`, `configfs_path`, and `errno`. The HTTP gateway maps the code to an HTTP status and a JSON body with the same fields.

| Reason | gRPC code | Retryable | HTTP |
| --- | --- | --- | --- |
| `INVALID_ARGUMENT` | `InvalidArgument` | no | 400 |
| `NOT_FOUND` | `NotFound` | no | 404 |
| `PERMISSION_DENIED` | `PermissionDenied` | no | 403 |
| `VOLUME_IN_DRIFT` | `FailedPrecondition` | no | 409 |
| `VOLUME_ALREADY_EXPORTED` | `FailedPrecondition` | no | 409 |
| `VOLUME_REQUEST_MISMATCH` | `FailedPrecondition` | no | 409 |
| `STALE_HANDLE` | `FailedPrecondition` | no | 409 |
| `SESSION_ACTIVE` | `FailedPrecondition` | yes | 409 |
| `SESSION_STATE_UNKNOWN` | `FailedPrecondition` | no | 409 |
| `OPERATION_NEEDS_ATTENTION` | `FailedPrecondition` | no | 409 |
| `DEVICE_NOT_READY` | `Unavailable` | yes | 503 |
| `RESOURCE_EXHAUSTED` | `ResourceExhausted` | yes | 429 |
| `UNAVAILABLE` | `Unavailable` | yes | 503 |
| `DEADLINE_EXCEEDED` | `DeadlineExceeded` | yes | 504 |
| `INTERNAL` | `Internal` | no | 500 |

The response messages are:

* `GetInfoResponse`: `host_uuid`, `agent_version`, `api_version`, `protocols`, `auth_modes`, `ready`.
* `GetVolumeResponse`: `desired` (the stored volume and export), `observed` (LV presence, size, device path, target presence, namespace presence, ACL presence, active sessions, session state, observed initiator), `drift_reasons`, and `operation_id`.
* `GetCredentialResponse`: `protocol`, `credential` (the secret), `expires_at`, and `operation_id`. The secret is returned only to the owning caller or an authorized node grant.

HTTP paths:

* `POST /v1/volumes`, `GET /v1/volumes/{volume_id}`, `GET /v1/volumes`
* `POST /v1/volumes/{volume_id}:export`, `POST /v1/volumes/{volume_id}:unexport`
* `POST /v1/volumes/{volume_id}:resize`, `DELETE /v1/volumes/{volume_id}`
* `GET /v1/volumes/{volume_id}/credential`, `POST /v1/volumes/{volume_id}:rotate-credential`
* `GET /v1/capacity`, `GET /v1/storage-classes`, `GET /v1/info`
* `POST /v1/admin/vgs`, `DELETE /v1/admin/vgs/{vg_name}`, `GET /v1/admin/vgs`
* `POST /v1/admin/import`, `POST /v1/admin/drift/{volume_id}:resolve`, `GET /v1/admin/orphans`
* `POST /v1/admin/ownership:transfer`, `DELETE /v1/admin/volumes/{volume_id}`
* `POST /v1/admin/volumes/{volume_id}:wipe`
* `POST /v1/admin/grants`, `DELETE /v1/admin/grants/{volume_id}/{node_id}`

Admin import validates the LVM LV UUID, size, VG registration, target binding, and absence of conflicting state. If `allow_active_sessions=false` and a session is active, it returns `SESSION_ACTIVE`. The `credential` field is a oneof CHAP secret or NVMe TLS PSK; kouja encrypts it and adopts the existing target binding. Import creates or updates both the volume and export rows and records an observation. It never deletes or reconfigures an existing target; it adopts the binding as-is or rejects the import. Import accepts nonconforming LV and target names; it generates a `resource_uuid` and stores the legacy `lv_name` and `target_name` in `legacy_identity`. It does not rename an active LV or target.

`TransferOwnership` and `AdminDeleteVolume` are admin-only. `ListOrphans` is a read-only admin view of observed `kouja-*` LVs with no live row; `ImportVolume` is the only adoption path. `TransferOwnership` updates the volume, export, observations, grants, credentials, and operations rows and the `kouja.owner` LVM tag in one transaction. These actions are audited and require the caller to acknowledge data loss where applicable.

`ListVolumes` is owner-scoped for callers and controllers. It returns a page of volume summaries with `page_size` (default 50, maximum 100), `page_token`, and `next_page_token`; it does not include initiators or target names. The admin role may request a global listing.

Admin request fields: `RegisterVG` and `DeregisterVG` use `vg_name` and `vg_uuid`; `ImportVolume` uses the fields above; `ResolveDrift` uses `volume_id` and `action`; `WipeVolume` and `AdminDeleteVolume` require `acknowledge_data_loss=true`; `TransferOwnership` requires `new_owner`; `GrantCredential` uses `volume_id`, `node_id`, `node_caller_id`, and `expires_at`; `RevokeGrant` uses `volume_id` and `node_id`.

Every mutation returns the current `Volume`, `Export`, `operation_id`, and `request_id`; `ListVolumes` returns a page of `Volume` summaries; admin mutations return the affected object and the audit row ID.

### State model

SQLite is the record of intent, ownership, credentials, grants, operations, and audit. LVM and configfs are the record of what exists. A mismatch is a `drift` state, not a repair trigger. The database enables `PRAGMA foreign_keys=ON` and uses WAL with `synchronous=FULL`.

```sql
CREATE TABLE schema_migrations (
  version INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);

CREATE TABLE settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
); -- boot_id, host_uuid, schema metadata

CREATE TABLE vgs (
  name TEXT PRIMARY KEY,
  uuid TEXT NOT NULL UNIQUE,
  state TEXT NOT NULL, -- active, unavailable
  registered_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE volumes (
  owner TEXT NOT NULL,
  volume_id TEXT NOT NULL,
  resource_uuid TEXT NOT NULL UNIQUE,
  lv_name TEXT NOT NULL,
  vg_name TEXT NOT NULL REFERENCES vgs(name) ON DELETE RESTRICT,
  requested_size_bytes INTEGER NOT NULL,
  actual_size_bytes INTEGER,
  state TEXT NOT NULL, -- creating, available, exporting, exported, resizing, deleting, deleted, failed, drift
  drift_reasons TEXT,
  last_operation_id TEXT,
  error_code TEXT,
  legacy_identity TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (owner, volume_id),
  UNIQUE(vg_name, lv_name)
);

CREATE TABLE volume_tombstones (
  owner TEXT NOT NULL,
  volume_id TEXT NOT NULL,
  resource_uuid TEXT NOT NULL,
  lv_name TEXT NOT NULL,
  vg_name TEXT NOT NULL,
  size_bytes INTEGER NOT NULL,
  deleted_at TEXT NOT NULL,
  last_operation_id TEXT,
  reason TEXT,
  PRIMARY KEY (owner, volume_id, resource_uuid)
);

CREATE TABLE volume_observations (
  owner TEXT NOT NULL,
  volume_id TEXT NOT NULL,
  lv_present INTEGER NOT NULL,
  observed_size_bytes INTEGER,
  device_path TEXT,
  observed_at TEXT NOT NULL,
  source TEXT NOT NULL,
  PRIMARY KEY (owner, volume_id),
  FOREIGN KEY (owner, volume_id) REFERENCES volumes(owner, volume_id) ON DELETE CASCADE
);

CREATE TABLE exports (
  owner TEXT NOT NULL,
  volume_id TEXT NOT NULL,
  protocol TEXT NOT NULL,
  initiator TEXT NOT NULL,
  target_name TEXT NOT NULL,
  lun INTEGER,
  nsid INTEGER,
  portal TEXT NOT NULL, -- v1: one portal per export
  state TEXT NOT NULL, -- exporting, exported, unexporting, unexported, failed, drift
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (owner, volume_id),
  FOREIGN KEY (owner, volume_id) REFERENCES volumes(owner, volume_id) ON DELETE CASCADE
);

CREATE TABLE export_observations (
  owner TEXT NOT NULL,
  volume_id TEXT NOT NULL,
  target_present INTEGER NOT NULL,
  namespace_present INTEGER NOT NULL,
  acl_present INTEGER NOT NULL,
  active_sessions INTEGER NOT NULL,
  session_state TEXT NOT NULL, -- none, active, unknown
  observed_initiator TEXT,
  observed_at TEXT NOT NULL,
  source TEXT NOT NULL,
  PRIMARY KEY (owner, volume_id),
  FOREIGN KEY (owner, volume_id) REFERENCES volumes(owner, volume_id) ON DELETE CASCADE
);

CREATE TABLE grants (
  owner TEXT NOT NULL,
  volume_id TEXT NOT NULL,
  node_id TEXT NOT NULL,
  node_caller_id TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (owner, volume_id, node_id),
  FOREIGN KEY (owner, volume_id) REFERENCES volumes(owner, volume_id) ON DELETE CASCADE
);

CREATE TABLE credentials (
  id TEXT PRIMARY KEY,
  owner TEXT NOT NULL,
  volume_id TEXT NOT NULL,
  protocol TEXT NOT NULL,
  secret_ciphertext BLOB NOT NULL,
  nonce BLOB NOT NULL,
  key_id TEXT NOT NULL,
  key_fingerprint TEXT NOT NULL,
  state TEXT NOT NULL, -- active, superseded
  created_at TEXT NOT NULL,
  rotated_at TEXT,
  FOREIGN KEY (owner, volume_id) REFERENCES volumes(owner, volume_id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX credentials_active
  ON credentials(owner, volume_id) WHERE state = 'active';

CREATE TABLE operations (
  id TEXT PRIMARY KEY,
  owner TEXT NOT NULL,
  volume_id TEXT NOT NULL,
  resource_uuid TEXT NOT NULL,
  action TEXT NOT NULL,
  request_id TEXT,
  request_hash TEXT NOT NULL,
  intent_json TEXT NOT NULL,
  state TEXT NOT NULL, -- running, succeeded, failed, needs_attention, superseded
  attempt INTEGER NOT NULL,
  max_attempts INTEGER NOT NULL,
  deadline_at TEXT NOT NULL,
  last_error_code TEXT,
  last_error_retryable INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(owner, volume_id, action, request_hash)
);

CREATE TABLE audit (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  timestamp TEXT NOT NULL,
  caller TEXT NOT NULL,
  operation_id TEXT NOT NULL,
  request_id TEXT,
  volume_id TEXT,
  action TEXT NOT NULL,
  protocol TEXT,
  initiator TEXT,
  pre_state TEXT,
  post_state TEXT,
  result TEXT NOT NULL,
  error_code TEXT,
  source_ip TEXT
);
```

The observation, export, grant, and credential tables declare `FOREIGN KEY (owner, volume_id) REFERENCES volumes(owner, volume_id) ON DELETE CASCADE`. `vgs` uses `ON DELETE RESTRICT` while volumes exist. `operations` and `audit` are retained as history and are not cascaded. The default is database-enforced integrity; application checks supplement it.

`volumes.state` is one of `creating`, `available`, `exporting`, `exported`, `resizing`, `deleting`, `deleted`, `failed`, or `drift`. On a successful delete, the live row moves to `volume_tombstones` in the same transaction; `deleted` is the tombstone state, and `absent` means there is no live row. `exports.state` is one of `exporting`, `exported`, `unexporting`, `unexported`, `failed`, or `drift`. `vgs.state` is `active` or `unavailable`. `operations.state` is `running`, `succeeded`, `failed`, `needs_attention`, or `superseded`.

`volumes.state` is the user-visible state. `exports.state` is the stored target-binding state. A volume with an export row in `exported` is `exported`; a volume with no export or an `unexported` row is `available`. `UnexportVolume` sets the export row to `unexported` and the volume to `available`. `ImportVolume` creates or updates both rows.

The identity terms are:

| Term | Meaning | Used in |
| --- | --- | --- |
| `volume_id` | Caller-supplied, owner-scoped identifier. | API paths and owner-scoped lookup. |
| `resource_uuid` | Kouja-generated UUID, never reused. | LV name, target IQN or NQN, credential username, LVM tags, and CSI volume handle. |
| `lv_uuid` | LVM logical-volume UUID. | Observation and import validation only. |
| `owner` | Stable caller identity from the certificate SAN and issuer. | Authorization. |
| `owner_hash` | SHA-256 of `owner`, truncated to 16 hex characters. | LVM owner tag. |
| `legacy_identity` | JSON map of imported `lv_name` and `target_name` when they do not follow the canonical pattern. | Import and audit only. |

The LV name is `kouja-<resource_uuid>`. The LV carries tags `kouja.managed=true`, `kouja.volume=<resource_uuid>`, and `kouja.owner=<owner_hash>`. Tags are ownership markers and drift evidence, not a recovery source.

`drift` is computed when any of these is true:

* The volume row says `available` or `exported`, but `volume_observations.lv_present` is false.
* `observed_size_bytes` differs from `actual_size_bytes`.
* The export row says `exported`, but the target, namespace, or ACL is absent, or `observed_initiator` differs from the recorded initiator.
* A target session exists that SQLite does not attribute to the export.

`session_state=unknown` is not drift. `GetVolume` exposes it. Delete and unexport require `force=true` unless a live probe returns a definitive `none`. The periodic reconciler writes the last known state and never converts `unknown` to `none`.

`volumes.drift_reasons` stores the list. The agent does not repair these conditions. `GetVolume` returns both `desired` and `observed` objects and the drift reasons. Mutations return `FailedPrecondition` with `VOLUME_IN_DRIFT` until an administrator calls `ResolveDrift`.

| `ResolveDrift` action | Required observed state | Side effects | Result |
| --- | --- | --- | --- |
| `MARK_FAILED` | Any | None | `failed` |
| `MARK_AVAILABLE` | LV present and no target | None | `available` |
| `MARK_EXPORTED` | LV and target present | None | `exported` |
| `MARK_DELETED` | LV and target absent | Tombstone the row | `deleted` |
| `RETRY` | A `failed` or `needs_attention` operation row exists | Reuse the row, reset attempts and deadline, set it running | State follows the operation |
| `RECREATE_EXPORT` | LV present, target absent | Recreate the target from the recorded export | `exported` |

Each `ResolveDrift` action runs in one transaction. It writes `volumes.state`, clears or updates `drift_reasons`, updates the export row (`exported`, `unexported`, or `failed`), updates the operation row for `RETRY`, and writes an audit row. `RETRY` accepts both `failed` and `needs_attention` operation rows. `MARK_AVAILABLE` requires the export row to be `unexported` or absent. `MARK_DELETED` requires both the LV and target to be absent and tombstones both rows. If a precondition fails, no row changes. The agent never recreates a missing LV from `ResolveDrift`; a missing LV with data loss requires a separate administrator decision and a fresh volume.

### Operation execution

Every mutation follows the same sequence:

1. Authenticate the caller and derive the owner from the certificate SAN and issuer.
2. Validate the request and compute a request hash over the action and all input fields except `request_id`.
3. Look up `operations` by `(owner, volume_id, action, request_hash)`.
   * `succeeded`: check `GetTombstone(owner, volume_id)`. If a tombstone exists, do not replay; mark the old operation `superseded` and create a new volume with a new `resource_uuid`. Otherwise return the current volume and export state.
   * `failed`: return the last typed error from `last_error_code` and `last_error_retryable`. If the error is retryable, the caller may re-arm the existing row; the manager updates `state='running'`, resets attempts, and sets a new deadline. It never inserts a second row for the same key.
   * `needs_attention`: return `FailedPrecondition` with `OPERATION_NEEDS_ATTENTION` and the operation ID. An administrator must call `ResolveDrift` with `RETRY` or a `MARK_*` action.
   * `running`: continue to the per-volume lock below, wait for the in-process operation to finish, re-read the row, and return the terminal result without inserting a new row. The host `flock` prevents a second agent process, so the cross-process case is not supported.
4. Acquire the in-process per-volume lock keyed by `(owner, volume_id)`.
5. Re-check the operation row and the current volume state under the lock.
6. Insert a `running` operation row with `intent_json`, `request_id`, the attempt count, maximum attempts, and deadline. Write the intended volume state. fsync before any external side effect.
7. Run the operation steps. Before each step, observe the host. If the step already happened, skip it.
8. On success, update the volume and export state, mark the operation `succeeded`, write the audit row, and return.
9. On failure, classify the failure:
   * Guard or precondition failures return immediately without a journal row: invalid input, ineligible VG, `VOLUME_IN_DRIFT`, `SESSION_ACTIVE` at entry, or `RESOURCE_EXHAUSTED` at the pre-check.
   * Journaled execution failures retry up to three attempts or five minutes. On exhaustion, mark the operation `failed`, preserve `last_error_code` and `last_error_retryable`, set the volume to `failed` or the previous stable state, and return the typed reason.
   * Ambiguous failures mark the operation `needs_attention` and require administrator action.
10. Release the per-volume lock.

| Failure class | Examples | Journal row | Retry | Terminal state | Caller sees |
| --- | --- | --- | --- | --- | --- |
| Guard or precondition | Invalid size, ineligible VG, `VOLUME_IN_DRIFT`, `SESSION_ACTIVE` at entry, `RESOURCE_EXHAUSTED` at pre-check | No | Caller decides; no side effect | No change | Typed reason with `retryable` |
| Execution, retryable | `UNAVAILABLE`, `DEADLINE_EXCEEDED`, `DEVICE_NOT_READY`, `SESSION_ACTIVE` during teardown | Yes | Auto to 3 attempts or 5 minutes; caller may re-arm | `failed` with `last_error_code` | Typed reason, not a generic timeout |
| Execution, ambiguous | Partial target state, unknown session state that affects safety, database failure after a side effect | Yes | No automatic retry | `needs_attention` | `OPERATION_NEEDS_ATTENTION` and the operation ID |
| Success | Completed operation | Yes | Idempotent replay | `succeeded` | Result |

The `intent_json` contains the normalized request: resource UUID, requested size, VG, protocol, initiator, export target, wipe/force/allow flags, and import fields. It is written in the same transaction as the operation row. A restart re-runs the normal path from the intent, not from a stored shell command.

The per-volume lock is acquired after the idempotency lookup and before the state re-check. `CreateVolume` and `ResizeVolume` also acquire a per-VG lock around the capacity check and the LVM allocation. The lock order is per-volume, then per-VG, and it is never reversed. A VG lock is not held for the whole operation.

The default limits are three attempts and five minutes total. Step deadlines apply per attempt: 120 seconds for create, 60 seconds for export, 120 seconds for delete, and 120 seconds for resize. The total deadline wins when the sum of attempts would exceed it. A wipe has its own configurable deadline, default 30 minutes, and is not counted against the create/delete total.

If a database write fails after an external side effect, the agent cannot record the failure in SQLite. It writes a best-effort, fsynced record to `/var/lib/kouja/emergency.log` with the operation ID, resource UUID, step, and observed state, then exits. Startup reads the emergency log, marks the affected volume `drift`, and requires an administrator to resolve it. The emergency log is root-only and follows the audit retention policy.

Retries use the normal path, not a replay of stored commands. The agent observes the LV, the backstore, the target, the namespace, and the ACL before each step. A completed step is a no-op. A partially created object is completed or adopted only if it matches the operation's recorded identifiers; otherwise the operation ends in `drift`.

### Volume and export state machine

`volumes.state` is the user-visible state; `exports.state` is the target-binding state. The table uses `volumes.state`.

| Current state | Action | Next state | Notes |
| --- | --- | --- | --- |
| `absent` | `CreateVolume` with `export=false` | `creating` -> `available` | LV created, no target. |
| `absent` | `CreateVolume` with `export=true` | `creating` -> `exporting` -> `exported` | LV and target created in one call. |
| `available` | `ExportVolume` or `CreateVolume` with `export=true` | `exporting` -> `exported` | Same initiator is idempotent and returns the existing credential. |
| `exported` | `ExportVolume` with the same initiator | `exported` | Returns the existing details and credential; does not rotate. |
| `exported` | `ExportVolume` with a different initiator | `exported` | `FailedPrecondition`, `VOLUME_ALREADY_EXPORTED`; unexport first. |
| `exported` | `UnexportVolume` | `unexporting` -> `available` | Active session requires `force`. Export row becomes `unexported`; credential becomes `superseded`. |
| `available` | `ResizeVolume` grow | `resizing` -> `available` | `rescan_required=true`. |
| `exported` | `ResizeVolume` grow | `resizing` -> `exported` | `rescan_required=true` or `reconnect_required=true`. |
| `available` | `ResizeVolume` shrink | `resizing` -> `available` | Requires `allow_shrink=true` and no active session. |
| `exported` | `ResizeVolume` shrink | `exported` | Refused; unexport first. |
| `available` or `exported` | `DeleteVolume` | `deleting` -> `deleted` | Active session requires `force`. |
| `available` or `exported` | observation mismatch | `drift` | Admin resolves; no automatic repair. `deleted` rows are not drift. |
| `drift` | `ResolveDrift` | `failed`, `available`, `exported`, or `deleted` | Admin only; preconditions are in the state model. |
| `failed` | retry or delete | `available`, `exported`, or `deleted` | Admin `ResolveDrift RETRY` reuses the failed operation row; a new caller request returns `OPERATION_NEEDS_ATTENTION`. |

A re-export is an `UnexportVolume` followed by an `ExportVolume`; only that path rotates the credential. A repeated `ExportVolume` to the same initiator returns the existing credential unchanged.

Resize semantics:

* Grow with no export: `lvextend`, wait for the device, return `rescan_required=true`.
* Grow while exported: extend the LV, update the LIO backstore LUN size and return `rescan_required=true`. For nvmet, update the namespace `device_size` if the kernel supports it; otherwise disable the namespace, update the size, enable it, and return `reconnect_required=true`. The export is not torn down and the credential does not rotate.
* Shrink: requires `allow_shrink=true`, no active session, and an unexported volume. Run `lvreduce`, wait for the device, and return `rescan_required=true`.
* The CSI driver never shrinks a volume.

### LVM backend

The agent executes LVM commands with `exec.Command` and explicit argument arrays. It never builds a shell string. It uses JSON reporting:

* `vgs --reportformat json --units b --nosuffix`
* `lvs --reportformat json --units b --nosuffix`
* `pvs --reportformat json --units b --nosuffix`

Eligibility checks for `RegisterVG`:

* The VG exists and is not partial.
* `vg_attr` does not set the clustered or shared flag.
* The VG contains no thin pool or thin volume (`lv_attr` first character `t` or `T`).
* The VG does not back the root filesystem, `data_dir`, `backup_dir`, or any critical host filesystem. Resolve each mount source to its backing LV and VG with `findmnt -no SOURCE`, `lvs -o vg_name`, and `lsblk -s`; reject a match by VG name or UUID. This catches root-on-LVM, where the mount source is `/dev/mapper/<vg>-<lv>` rather than the underlying PV.
* The VG UUID is recorded and rechecked on every startup. A UUID mismatch marks the VG unavailable and the affected volumes `drift`.

Commands:

* Create: `lvcreate --yes --size <bytes>b --name kouja-<resource_uuid> --addtag kouja.managed=true --addtag kouja.volume=<resource_uuid> --addtag kouja.owner=<owner_hash> <vg>`
* Grow: `lvextend --yes --size <bytes>b <vg>/kouja-<resource_uuid>`
* Shrink: `lvreduce --yes --size <bytes>b <vg>/kouja-<resource_uuid>`
* Delete: `lvremove --yes <vg>/kouja-<resource_uuid>`
* Activate: `lvchange --activate y <vg>/kouja-<resource_uuid>`

After a create or resize, the agent runs `udevadm settle --timeout=30` and polls `/dev/<vg>/<lv>` and `/dev/mapper/<vg>-<lv>` until the device appears and reports the expected size. It returns the extent-rounded size, not the requested size. LVM may satisfy a linear LV from multiple free extents; the agent relies on LVM's allocator and returns `ResourceExhausted` only when the actual allocation fails.

`DeregisterVG` is rejected while any non-deleted volume row references the VG. Tombstones are purged after the audit retention period.

An explicit `WipeVolume` runs `blkdiscard` when the LV supports discard; otherwise it writes zeroes with `dd`. These methods reduce but do not guarantee physical erasure on all media. The volume must be unexported unless the admin sets `force`. The operation is audited and has a size-aware deadline.

### iSCSI/LIO adapter

The adapter uses one target and TPG per volume. This restricts each ACL to one initiator and keeps delete and restore local to one object tree.

* Backstore: `/sys/kernel/config/target/core/iblock_0/<lv_name>/` with `udev_path=/dev/<vg>/<lv_name>` and `enable=1`. LIO rejects UUID-shaped HBA names such as `iblock_<resource_uuid>`; v1 uses one shared `iblock_0` HBA and keeps per-volume identity in the backstore, target, LUN, and ACL.
* Target: `/sys/kernel/config/target/iscsi/<iqn>/tpgt_1/`, where the IQN is `<configured-prefix>:<host-uuid>:<resource_uuid>`.
* LUN: `tpgt_1/lun/lun_0` symlink to the backstore.
* ACL: `tpgt_1/acls/<initiator-iqn>/` with `auth/userid` and `auth/password`. Set `tpgt_1/attrib/authentication=1`; the authentication flag is a TPG attribute, not an ACL attribute.
* Portal: `tpgt_1/np/<address>:<port>/`.
* Enable: `tpgt_1/enable=1`; the target has no separate writable `enable` attribute.

Active sessions are read from `fabric_statistics/iscsi_instance/sessions`, which is the authoritative per-target count. The `tpgt_1/dynamic_sessions` file lists only sessions whose node ACL is dynamic, which is the demo-mode path; an empty `dynamic_sessions` does not mean there are no sessions. Read `acls/<initiator>/info` to confirm the initiator when the kernel exposes it. If the authoritative count is unavailable, the adapter returns `SESSION_STATE_UNKNOWN`; delete and unexport then require `force=true`. It does not infer session state by attempting a destructive operation.

Restore waits for the LV device, then creates the backstore, target, LUN, ACL, and portal in that order. It adopts an existing object only when its attributes match the recorded target name, LUN, and initiator; otherwise the volume becomes `drift`.

Delete removes the portal, ACL, LUN, target, and backstore in that order. If the kernel reports the target busy, the operation returns `SESSION_ACTIVE` unless `force=true`.

The agent is the only writer to LIO. The installer disables the distribution's `target.service` and any `targetcli saveconfig` persistence. Existing unmanaged LIO state is adopted only through `ImportVolume`; kouja-owned exports are restored from SQLite.

### NVMe-oF/nvmet adapter

The adapter uses one subsystem per volume and one namespace with NSID 1.

* Subsystem: `/sys/kernel/config/nvmet/subsystems/<nqn>/`, where the NQN is `<configured-prefix>:<host-uuid>:<resource_uuid>`.
* Attributes: `attr_allow_any_host=0`, `attr_serial=<resource_uuid>`, `attr_model=kouja`.
* Namespace: `namespaces/1/device_path=/dev/<vg>/<lv_name>`, then `enable=1`.
* Allowed host: `allowed_hosts/<initiator-nqn>/` symlink.
* Port: a host-level port per protocol. For v1, TLS is required for NVMe-oF/TCP: set `addr_trtype=tcp`, `addr_traddr=<address>`, `addr_trsvcid=<port>`, `addr_adrfam=ipv4`, `addr_tsas=tls1.3`, and `addr_treq=required`. A non-TLS port is not supported in v1.
* Link: `ports/<id>/subsystems/<nqn>/`.

Mainline nvmet configfs does not expose a `controllers/` group. The adapter probes for a supported kernel interface, either a merged `controllers/` group or the debugfs nvmet view. If the interface is present, it reads `hostnqn`, `traddr`, and `trsvcid`. If the interface is absent, the adapter returns `SESSION_STATE_UNKNOWN`. On kernels without a session interface, delete and unexport require an audited `force=true`; the CSI driver must not force automatically. The supported kernel or backport is documented in the deployment guide.

TLS-PSK configuration requires:

* Kernel `CONFIG_NVME_TARGET_TCP_TLS` and `CONFIG_NVME_TARGET_AUTH`, plus a supported kernel or backport for session visibility.
* `ktls-utils` with `tlshd` running on the target and on every initiator node.
* A keyring entry in `.nvme` for the subsystem PSK. The key is generated in the `NVMeTLSkey-1:` format, stored encrypted in SQLite, and inserted with `keyctl` or a Go keyring library.
* The port must be created with `addr_tsas=tls1.3` and `addr_treq=required`. The port is disabled while its attributes change.

The CSI node plugin connects with `nvme connect -t tcp -a <address> -s <port> -n <nqn> --tls --tls-key=<key-file>`. The key must be passed through a keyring entry or a 0600 file, not as a long-lived argument visible in the process table. The node plugin waits for `/dev/nvme*n*` and uses the namespace path returned by the controller.

Restore waits for the LV device, creates the subsystem, namespace, allowed host, and port link, reinstalls the PSK into the `.nvme` keyring, and verifies `tlshd` before `/readyz`. It adopts an existing subsystem only when the NQN, namespace device, and allowed host match; otherwise the volume becomes `drift`. Delete removes the port link, allowed host, namespace, and subsystem in reverse order, then revokes the PSK.

### Credentials

iSCSI uses CHAP. The username is `kouja-<resource_uuid>`, and the secret is 32 random bytes encoded with base64url. The adapter writes the username and password into the LIO ACL and sets the TPG `attrib/authentication=1`.

NVMe-oF uses TLS 1.3 pre-shared keys. kouja generates a key in the `NVMeTLSkey-1:` format for each subsystem, stores it encrypted, and installs it in the target `.nvme` keyring. The initiator receives the same key through a keyring entry or a 0600 file.

Credentials are encrypted at rest with AES-256-GCM. Each row stores a unique nonce and binds the ciphertext to the owner, volume ID, protocol, and `key_id` as additional authenticated data. The key is `/var/lib/kouja/secret.key`, mode 0600, generated on first start. The database stores a key fingerprint and fails closed if the key file is missing or does not match while credentials exist. The database file and backups are mode 0600. The key is backed up separately from the database; if a restore lacks the key, the administrator must rotate the credentials. Secrets are never written to logs, audit rows, list responses, or process arguments.

`GetCredential` returns the active credential to the owning caller or to a node with a valid grant, and writes an audit row. `RotateCredential` generates a new secret, updates the target, marks the old row `superseded`, and returns the new secret. iSCSI rotation applies to new logins; existing sessions continue until they reconnect. NVMe TLS rotation applies to new connections; the caller reconnects. A re-export, defined as unexport followed by export, rotates the credential. A repeated `ExportVolume` to the same initiator returns the existing credential unchanged.

### Capacity and placement

`GetCapacity` computes per VG at request time:

* `total_bytes`, `free_bytes`, and `extent_size_bytes` from `vgs`, `pvs`, and `lvs` at request time.
* `allocatable_bytes = max(0, free_bytes - max(headroom_percent% of total_bytes, headroom_min_bytes))`.
* `max_volume_bytes = min(allocatable_bytes, 1 TiB)`. LVM may satisfy a linear LV from multiple extents; no contiguous-extent admission rule applies.
* `vg_state` and `observed_at`.
* The agent queries LVM on each request and does not persist a capacity cache.

`CreateVolume` requires either a configured `storage_class` or a raw `vg`. The agent resolves a class to its VG and serializes the capacity check and the LVM allocation with a per-VG lock. The per-VG lock prevents racing kouja allocations. `ResourceExhausted` is returned when the actual LVM allocation fails or the request exceeds the current allocatable size. The CSI driver tries the next eligible host up to three times.

The CSI driver selects a host by:

1. Resolving the StorageClass SRV record.
2. Calling `GetInfo` and `GetCapacity` on every endpoint in parallel, with a 2-second timeout.
3. Filtering to hosts that are ready, support the requested protocol, have the requested class or VG, have `vg_state=active`, and have `allocatable_bytes >= requested size` and `max_volume_bytes >= requested size`.
4. Choosing the host with the largest `allocatable_bytes`; ties break by host UUID.
5. Calling `CreateVolume`; on `ResourceExhausted`, removing that host and trying the next.
6. Caching the inventory for at most 10 seconds; the cache is invalidated on an unreachable host or a `ResourceExhausted` response.

The CSI `GetCapacity` sums `allocatable_bytes` across ready hosts as `available_capacity`. It returns the maximum of the per-host `max_volume_bytes` as `maximum_volume_size`, not the sum.

### CSI driver

The CSI driver is part of the project but ships after the agent core and iSCSI path. Stage 1 is the agent with iSCSI, create/export/delete, crash restore, mTLS, and capacity. Stage 2 adds NVMe-oF/TLS-PSK and the CSI controller and node. Stage 3 adds grow, shrink, import, wipe, and operational tooling. The CSI contract is defined now so the API and state model do not need to change later.

The driver name is `kouja.csi.voidstarit.com`. It advertises these capabilities:

* Controller: `CREATE_DELETE_VOLUME`, `PUBLISH_UNPUBLISH_VOLUME`, `EXPAND_VOLUME`, `GET_CAPACITY`. `EXPAND_VOLUME` is enabled only after the grow path passes the integration and CSI conformance suites.
* Node: `STAGE_UNSTAGE_VOLUME` and `EXPAND_VOLUME`. `GET_VOLUME_STATS` is not advertised.
* Access modes: `SINGLE_NODE_WRITER` only. `RWX` and multi-node modes are not advertised.
* No snapshots, clones, or volume statistics.

`CreateVolume`:

1. Parse the StorageClass parameters: `srvRecord`, `class` or `vg` (one required), `protocol`, optional `wipe`.
2. Derive a stable placement key from the CSI request name and StorageClass.
3. Look up a `KoujaVolumePlacement` custom resource with `resourceVersion` compare-and-swap; the external-provisioner supplies leader election. If it exists and is `Bound`, use that host. If it is `Pending`, query the recorded host; if the volume exists, bind it; if not, resume the create on that host. If it is `Failed`, follow the recorded retry path. If the host is unreachable, return `Unavailable` and do not create on another host.
4. If no record exists, resolve the SRV record, select a host by capacity, and write a `Pending` placement record with the selected host and an idempotency key before calling `CreateVolume` with `export=false`, the size, the class or VG, and `wipe`.
5. On success, update the record to `Bound` with the returned `host_uuid` and `resource_uuid`. On failure, delete the pending record or mark it `Failed` with the error; a retry resolves the record before creating.
6. Return `v1:<host-uuid>:<resource-uuid>` as the volume handle and the actual capacity. The volume context includes the host UUID, resource UUID, protocol, class, VG, control endpoint, and portal.

If the pending record cannot be written, the driver does not call `CreateVolume`. If the response is lost, the pending record identifies the host and idempotency key, and a retry queries that host before creating anywhere else. The driver never returns a handle that is not durably recorded.

`KoujaVolumePlacement` fields are `spec.placementKey`, `spec.csiVolumeName`, `spec.storageClass`, `spec.hostUuid`, `spec.volumeId`, `spec.resourceUuid`, `spec.idempotencyKey`, and `status.phase` (`Pending`, `Bound`, `Failed`) with `status.error` and `status.updatedAt`. `DeleteVolume` deletes the record after the volume delete succeeds. A controller reconcile loop deletes `Failed` records older than 24 hours after confirming no volume exists.

`ControllerPublishVolume`:

1. Parse the handle to get the host UUID and resource UUID; read the placement record to get the caller `volume_id`; verify that the record's `resource_uuid` matches the handle and return `FailedPrecondition` with `STALE_HANDLE` on a mismatch. Resolve the current endpoint for that host from the SRV record or the placement record.
2. Read the node's IQN or NQN and its caller ID from `CSINode` annotations written by the node plugin: `kouja.csi/iscsi-iqn` or `kouja.csi/nvme-nqn`, and `kouja.csi/node-caller-id`. If any annotation is missing, return `FailedPrecondition`.
3. Call `AdminService.GrantCredential` with the controller role, granting the node caller ID access to the volume until unpublish.
4. Call `CreateVolume` with the same owner and `volume_id`, `export=true`, the protocol, and the initiator.
5. Return a `PublishContext` with `protocol`, `targetName`, `portal`, `lun` or `nsid`, `resourceUuid`, `volumeId`, `owner`, and `controlEndpoint`. The credential is not placed in `PublishContext`.

`NodeStageVolume`:

1. Read the `PublishContext`.
2. Authenticate to the control endpoint with the node's own client certificate, with SAN `spiffe://kouja/node/<node-id>`, and call `GetCredential`. The grant authorizes the node for this volume.
3. Run `iscsiadm` discovery and login with CHAP, or `nvme connect` with TLS. Pass secrets through a 0600 file or a keyring entry, never as a long-lived argument.
4. Wait for the device, verify its size, and format it with the requested filesystem only if it has no filesystem and the CSI request asks for one.
5. Mount the device to the staging path.

`NodePublishVolume` bind-mounts the staged path into the pod target. `NodeUnpublishVolume` unmounts the pod target. `NodeUnstageVolume` unmounts the staging path and logs out or disconnects the session.

`ControllerUnpublishVolume` calls `UnexportVolume` with `force=false`; if the target still reports an active session, it returns a retryable `FailedPrecondition` and the external-attacher retries. A stale session is cleaned up by an administrator with `force=true`, and the grant is revoked after a successful unexport.

`ControllerExpandVolume` calls `ResizeVolume` with the new size and `allow_shrink=false`. It returns the new capacity and `node_expansion_required=true`. `NodeExpandVolume` rescans the device and grows the filesystem: `resize2fs` for ext4, `xfs_growfs` for XFS. The driver never shrinks a volume through CSI.

`DeleteVolume` calls `DeleteVolume` with `force=false`. `SESSION_ACTIVE` is returned as a retryable error. An administrator can force-delete through the API if the consumer is gone.

`GetCapacity` sums `allocatable_bytes` across ready hosts that match the StorageClass SRV record, protocol, and required class or VG. It returns the sum as `available_capacity` and the maximum of `max_volume_bytes` as `maximum_volume_size`. It does not reserve capacity.

Node topology is empty. The first version assumes every Kubernetes node can reach every portal returned by the SRV record. An operator who needs network zones scopes the SRV record per zone and creates one StorageClass per zone.

The node plugin publishes its IQN or NQN and its caller ID (`kouja.csi/node-caller-id`) in `CSINode` annotations at startup. If the host's initiator name does not match, the node plugin reports a fatal event rather than silently changing the host's global initiator identity. The node DaemonSet mounts the node client certificate, the CA, and the control endpoint configuration.

### Security and authorization

The control listener requires mTLS with TLS 1.3. Client and server certificates are issued by the operator's CA. Identity is carried in a URI SAN:

* `spiffe://kouja/caller/<caller-id>` for volume callers.
* `spiffe://kouja/controller/<controller-id>` for a CSI controller.
* `spiffe://kouja/node/<node-id>` for a CSI node.
* `spiffe://kouja/admin` for administrators.

The agent verifies the chain and parses the URI SAN plus issuer. A caller can act only on volumes whose `owner` equals its caller ID. A non-owner receives `NotFound` for volume reads and mutations. A node can call `GetCredential` only with a valid grant for that volume. An admin method called with the wrong role receives `PermissionDenied`.

The admin service shares the listener but is a separate gRPC service and HTTP path prefix (`/v1/admin`). Admin actions are audited with the caller identity. Ownership transfer, admin delete, drift resolution, VG registration, and import require the admin role. `ListOrphans` is read-only. `GrantCredential` and `RevokeGrant` require the controller or admin role.

Per-node certificates are issued by the operator's CA through a per-node Secret or a CSR flow. The node DaemonSet mounts only its own Secret, and the agent verifies that the SAN equals the node ID. The CSI controller uses a controller certificate whose owner is `controller/<controller-id>`; CSI volumes are owned by that controller identity. The controller reads the node caller ID from the `kouja.csi/node-caller-id` CSINode annotation written by the node plugin and passes it to `GrantCredential`, which validates that it matches the node ID.

| RPC group | Caller | Controller | Node | Admin |
| --- | --- | --- | --- | --- |
| `GetInfo`, `GetCapacity` | yes | yes | yes | yes |
| Volume mutations and reads | owner | owner | no | no |
| `ListVolumes` | own only | own only | no | global |
| `GetCredential` | owner | owner | grant only | no |
| `GrantCredential`, `RevokeGrant` | no | yes | no | yes |
| `ListVGs`, `ListOrphans` | no | no | no | yes |
| `RegisterVG`, `DeregisterVG`, `ImportVolume`, `ResolveDrift`, `WipeVolume` | no | no | no | yes |
| `TransferOwnership`, `AdminDeleteVolume` | no | no | no | yes |

The gateway is an in-process handler that reads the TLS peer from the inbound connection. The server certificate includes `spiffe://kouja/host/<host-uuid>`; clients verify the expected host UUID. Certificates are short-lived, rotate with overlapping validity, and are checked against a CRL or OCSP responder. A compromised host cannot impersonate another host selected via SRV.

The control API belongs on a management network. iSCSI and NVMe-oF portals belong on a storage network. The firewall rules name the control listener, iSCSI 3260, NVMe-oF 4420, and the TLS handshake port. The data path is protected by CHAP or TLS-PSK, not by mTLS. CHAP authenticates but does not encrypt iSCSI traffic; iSCSI requires an isolated storage network or IPsec.

The agent fails closed if SQLite is missing or corrupt, if the host identity file is missing or does not match the database while volumes exist, or if the credential key is missing while encrypted credentials exist. A missing database with existing `kouja-*` LVs is not a fresh host. It never recreates an empty LV to satisfy a stale row. If the database is lost, the administrator restores the latest backup and runs reconciliation; any volume created after the backup becomes an unowned observation and must be imported.

### Startup and reconciliation

1. Load `/etc/kouja/config.yaml`.
2. Acquire `flock` on `/var/lib/kouja/lock`. A second agent exits. Before opening the database, check whether `/var/lib/kouja/state.db` exists. If it is absent, scan all host VGs for `kouja.managed=true` tags or `kouja-*` LVs. If any exist, fail closed and require a restore or import. Only a host with no managed LVs may create a new database.
3. Open SQLite with WAL, `synchronous=FULL`, `PRAGMA foreign_keys=ON`, and a busy timeout. Run `PRAGMA integrity_check`. On failure, exit and require a restore.
4. Apply schema migrations. Refuse to start if the database schema is newer than the binary.
5. Load the host UUID from the `settings` table and compare it with `/var/lib/kouja/host-id`. If the file is missing or differs while the database has volumes, fail closed. On a fresh host with no managed LVs, create the file and the database row.
6. Check `/var/lib/kouja/secret.key`. If it is missing or its fingerprint differs while encrypted credentials exist, fail closed. On a fresh host with no credentials, generate it.
7. Read the kernel boot ID from `/proc/sys/kernel/random/boot_id`. Store it in `settings`.
8. Check dependencies: configfs mounted, LIO and nvmet modules loaded, `nvmet_tcp` loaded, `tlshd` active when TLS-PSK is enabled, and LVM tools present.
9. Start the gRPC/HTTP and metrics listener. `/healthz` is live, `/readyz` returns 503 with `Retry-After`, and mutations return `Unavailable`.
10. Verify each registered VG: exists, UUID matches, eligible, not partial, and does not contain `data_dir` or `backup_dir`. A missing or changed VG marks its volumes `drift`.
11. Activate every managed LV with `lvchange --activate y`, run `udevadm settle`, and wait for `/dev/<vg>/<lv>` or `/dev/mapper/<vg>-<lv>`.
12. Observe every volume and export. If the boot ID changed and LIO persistence is disabled, configfs is empty; recreate every export recorded as `exported` from SQLite. If the boot ID is unchanged and an export is missing, mark it `drift`; do not recreate it. Reinstall NVMe TLS PSKs into the `.nvme` keyring and verify `tlshd` before serving. Restore exports with a worker pool of 8 and a 30-second per-volume timeout; continue on a failed volume and mark it `drift`.
13. Re-run every non-terminal operation under its per-volume lock, using the normal path and the remaining attempt and deadline budget. A `running` row from a previous process is resumed from `intent_json` before the normal request path accepts mutations; the request path waits on it and returns the existing result rather than inserting a new row. Read `/var/lib/kouja/emergency.log` and mark affected volumes `drift`.
14. Write observations and drift reasons. `/readyz` becomes true, even if some volumes are in `drift`; the drift metric and `GetVolume` expose the degraded state.

A periodic reconciliation runs every 60 seconds. It updates observations, detects drift, and never repairs it. The 60-second interval is configurable.

### Observability and audit

The agent writes structured JSON logs to stderr for journald. Each line includes `timestamp`, `level`, `component`, `caller`, `operation_id`, `request_id`, `volume_id`, `protocol`, `error_code`, and `duration_ms`. Secrets are redacted.

Prometheus metrics are exposed at `/metrics` on the control listener and require mTLS. A network ACL may add a second layer:

* `kouja_up`, `kouja_build_info`, `kouja_ready`
* `kouja_volume_state{state}`
* `kouja_operation_total{action,result}`
* `kouja_operation_duration_seconds{action}`
* `kouja_vg_free_bytes{vg}`, `kouja_vg_allocatable_bytes{vg}`
* `kouja_active_sessions{protocol}`
* `kouja_session_state{protocol,state}`
* `kouja_active_grants`
* `kouja_drift_volumes`
* `kouja_sqlite_integrity_ok`, `kouja_sqlite_write_failures_total`
* `kouja_reconcile_duration_seconds`, `kouja_reconcile_result{result}`
* `kouja_credential_rotations_total`
* `kouja_backup_last_success_timestamp`
* `kouja_backup_failures_total`
* `kouja_admission_rejected_total`

Labels stay aggregate. Per-volume detail is available through `GetVolume` and `ListVolumes`. The audit table stores one row per mutation, credential retrieval, and grant change. Audit rows are pruned after 90 days by default; the retention is configurable. The audit log is mirrored to structured logs for external shipping.

`/healthz` returns process liveness without dependency checks. `/readyz` returns 200 only after startup reconciliation and returns 503 with `Retry-After` before that. The listener starts before reconciliation, so the 503 path is reachable; mutations return `Unavailable` until readiness. A degraded host with `drift` volumes is ready but reports the drift metric.

### Configuration

The agent reads `/etc/kouja/config.yaml`:

```yaml
host_id_file: /var/lib/kouja/host-id
data_dir: /var/lib/kouja
secret_key_file: /var/lib/kouja/secret.key
emergency_log: /var/lib/kouja/emergency.log
listen:
  address: 0.0.0.0:9443
  tls:
    cert_file: /etc/kouja/tls/server.crt
    key_file: /etc/kouja/tls/server.key
    client_ca_file: /etc/kouja/tls/client-ca.crt
    min_version: TLS1.3
storage:
  headroom_percent: 5
  headroom_min_bytes: 1073741824
  wipe_timeout_seconds: 1800  # base; deadline is max(base, 1 second per GiB)
  vgs: []  # names to seed on first start; the admin API is authoritative afterward
  classes: {}  # optional class -> VG mappings, e.g. nvme: vg-nvme
targets:
  iscsi:
    enabled: true
    iqn_prefix: iqn.2026-11.io.kouja
    portals:
      - address: 10.0.0.10
        port: 3260
  nvme_tcp:
    enabled: false  # stage 2; v1 ships iSCSI
    nqn_prefix: nqn.2026-11.io.kouja
    portals:
      - address: 10.0.0.10
        port: 4420
    tls:
      enabled: true
      keyring: .nvme
      tlshd_service: tlshd
operations:
  max_attempts: 3
  total_timeout_seconds: 300
  step_timeout_seconds:  # per attempt; the total deadline wins
    create: 120
    export: 60
    delete: 120
    resize: 120
admission:
  max_concurrent_operations: 64
  per_caller_operations: 10
  capacity_requests_per_second: 10
state:
  backup_dir: /var/lib/kouja/backups
  backup_target: ""  # required writable off-host mount path in production
  backup_interval_seconds: 900
  backup_retention_short: 96  # 15-minute backups for 24 hours
  backup_retention_daily: 7
  backup_retention_weekly: 4
  integrity_check_interval_seconds: 86400
  reconcile_interval_seconds: 60
  audit_retention_days: 90
```

The admin API registers VGs at runtime. The `storage.vgs` list seeds VGs on first start only. After first start, the admin API is authoritative; a config entry that differs is a warning, not a fatal error. A configured VG that no longer exists marks its volumes `drift`. Portal changes require an agent restart in the first version. The backup retention keys control the 15-minute, daily, and weekly backup tiers.

The backup job requires `backup_target` to be a writable off-host mount path. It writes `VACUUM INTO` to a temporary file, fsyncs it, and atomically renames it into the target. It validates the target before each run and exposes last-success and failure metrics.

### Failure handling summary

* **LVM command fails**: return `Unavailable` or `ResourceExhausted` with the stderr summary and exit code; redact secrets. Retry up to the attempt limit.
* **Device does not appear**: return `Unavailable` with `DEVICE_NOT_READY`; the operation remains running until the step deadline.
* **configfs write fails**: return `Unavailable` with the attribute path and errno. If the object was partially created, observe and complete it on the next attempt or mark drift.
* **Active session**: return `FailedPrecondition` with `SESSION_ACTIVE`; delete and unexport require `force`.
* **Session state unavailable**: return `FailedPrecondition` with `SESSION_STATE_UNKNOWN`; delete and unexport require an audited `force`.
* **Database write fails before a side effect**: fail the operation with no host change.
* **Database write fails after a side effect**: write the emergency log, exit, and require an administrator to resolve the drift.
* **Restart with a non-terminal operation**: re-run the normal path with the remaining budget.
* **Database lost or corrupt**: exit; do not guess. The administrator restores a backup and reconciles.
* **Host identity file missing or mismatched**: fail closed if the database has volumes. A new UUID would orphan every CSI volume handle.
* **Credential key missing or mismatched**: fail closed if encrypted credentials exist; the administrator restores the key or rotates every credential.

## Alternatives Considered

### Alternative A: One central control plane

* **Pros**: a single inventory, global placement, one database, and one place to see every host.
* **Cons**: adds cluster membership, a central database, a single point of failure, and a new set of distributed-state problems.
* **Reason for Rejection**: the two callers already know which host a volume belongs to. The data path is a network block export, not a replicated cluster volume. A central plane would add the complexity the project is trying to avoid.

### Alternative B: Placement inside kouja

* **Pros**: callers would not need to choose a host, and a global view could optimize capacity.
* **Cons**: requires peer communication or a shared inventory, which contradicts the per-host, no-cluster design. A single host cannot see the other hosts.
* **Reason for Rejection**: placement stays with Rookery and the CSI driver. kouja reports capacity and accepts or rejects the allocation.

### Alternative C: Thin LVs and non-LVM backends

* **Pros**: thin provisioning allows overcommit and more efficient use of capacity. Raw partitions, files, and ZFS would support more existing workloads.
* **Cons**: thin pools introduce pool exhaustion, overcommit, and ambiguous capacity. Every additional backend has its own capacity, resize, restore, and safety rules.
* **Reason for Rejection**: thick LVs on LVM are the only backend in the first version. A full VG fails the request instead of hiding a pool problem, and the implementation stays small.

### Alternative D: Multi-initiator or RWX

* **Pros**: shared-disk clusters and CSI `RWX` volumes would be possible.
* **Cons**: shared block devices need a cluster filesystem, coordinated resize, a more complex ACL model, and careful fencing. A mistake corrupts data on more than one host.
* **Reason for Rejection**: every volume is bound to one initiator. The CSI driver does not advertise `RWX`, and a node replacement requires an unexport and re-export, not a second simultaneous initiator.

### Alternative E: Consumer-side connect inside kouja

* **Pros**: one service would provision and connect the volume, simplifying the caller.
* **Cons**: kouja would need initiator credentials and host access on every consumer, handle different operating systems and initiator tools, and take on a much larger trust boundary.
* **Reason for Rejection**: kouja configures the target only. The caller or the CSI node plugin performs discovery, login, mount, and rescan.

### Alternative F: Snapshots, clones, replication, and migration

* **Pros**: data protection, fast provisioning from a template, and host mobility.
* **Cons**: each feature adds a new data path, a new state model, and new failure modes. None of them is required by the two current callers.
* **Reason for Rejection**: they are explicit non-goals for the first version. The state model leaves room to add them later without changing the volume identity.

### Alternative G: LVM tags or a host manifest as a second recovery source

* **Pros**: ownership and export intent would survive SQLite loss without depending on a backup.
* **Cons**: a second source of truth creates drift between SQLite, LVM tags or the manifest, and the target configuration. Tags are size-limited, and an atomic manifest adds another write path.
* **Reason for Rejection**: SQLite plus tested backups is the chosen recovery model. The host is still observed for drift, and a missing or corrupt database fails closed instead of guessing. Tags remain as ownership markers, not as a recovery source.

### Alternative H: Asynchronous mutations with operation IDs

* **Pros**: long operations and caller timeouts are easier to handle, and a status endpoint can report progress.
* **Cons**: adds a worker, a durable status API, polling, and two response paths. The callers are two systems that can wait on a synchronous call.
* **Reason for Rejection**: mutations are synchronous. A durable journal still exists for crash recovery, and a retry with the same owner and volume ID attaches to the in-flight operation.

### Alternative I: Reservation ledger or per-owner quotas

* **Pros**: prevents allocation races and stops one caller from consuming a whole VG.
* **Cons**: adds a second state machine, expiry sweeps, stale reservations, and reconciliation between the reservation and the LV.
* **Reason for Rejection**: allocation is optimistic, serialized per VG, and returns a typed `ResourceExhausted` error. The caller retries or chooses another host. No quota is enforced in the first version.

### Alternative J: Caller-supplied credentials

* **Pros**: the caller controls secret generation and rotation and can match its existing credential store.
* **Cons**: weak secrets, inconsistent formats, and kouja still has to store and validate them. Rotation would depend on every caller implementing it correctly.
* **Reason for Rejection**: kouja generates random credentials, stores them encrypted, configures the target, and returns them to the owning caller. Rotation is a kouja operation.

### Alternative K: DH-HMAC-CHAP or NQN allowlist for NVMe-oF

* **Pros**: DH-HMAC-CHAP is native in-band authentication with no TLS daemon. An NQN allowlist is simple and works on older kernels.
* **Cons**: DH-HMAC-CHAP authenticates but does not encrypt the transport. An NQN is not a secret and can be spoofed.
* **Reason for Rejection**: NVMe-oF/TCP uses TLS 1.3 with a per-subsystem PSK. It encrypts the connection and authenticates the host. The kernel and `ktls-utils` requirements are documented as deployment prerequisites.

### Alternative L: Strict create plus separate export calls

* **Pros**: a clean CSI lifecycle, an explicit `available` state, and no guessing about the initiator.
* **Cons**: an extra call for every volume, and Rookery would have to perform two calls even when it knows the VM host at create time.
* **Reason for Rejection**: create accepts an `export` flag that defaults to true. Rookery creates and exports in one call. CSI creates with `export=false` and calls the same endpoint with `export=true` at publish. The endpoint is an ensure operation, not a strict create.

### Alternative M: Static host list or Kubernetes registry for CSI discovery

* **Pros**: a static list is simple and a registry is dynamic.
* **Cons**: a static list becomes stale and needs a reload. A registry is another service to run, watch, and keep healthy.
* **Reason for Rejection**: the StorageClass carries an SRV record name. The driver resolves it, queries the endpoints for host UUID and capacity, and selects a host. The SRV record is the discovery mechanism, and the StorageClass scopes it.

### Alternative N: Separate admin listener or local-only admin socket

* **Pros**: a separate listener gives stronger network isolation. A local socket keeps admin traffic off the network entirely.
* **Cons**: a separate listener adds certificates, ports, and firewall rules. A local socket makes remote automation depend on SSH or a jump host.
* **Reason for Rejection**: the admin service shares the listener and is authorized by an admin role in the client certificate. The volume and admin APIs are separate services and URL prefixes, but one mTLS endpoint.

> **Superseded for the local admin UI (2026-11-06):** A separate,
> disabled-by-default loopback listener now serves the local admin UI described
> in [admin-ui.md](admin-ui.md). The mTLS admin service remains the
> authenticated API for volume lifecycle and other administrative operations.
> The loopback listener is limited to the local configuration/status UI and
> live VG/class actions. This note does not change the rejection of a
> network-exposed second admin API.

### Alternative O: CLI wrappers or `targetd`

* **Pros**: `targetcli` and `nvmetcli` are existing tools, and `targetd` already manages LVM and LIO.
* **Cons**: the CLIs are Python programs with brittle output and slow startup. `targetd` would become a second state manager next to SQLite, and the agent would have to reconcile two models.
* **Reason for Rejection**: the Go agent writes LIO and nvmet configfs directly. This removes the Python dependency, makes ordering explicit, and keeps SQLite as the only state record.

### Alternative P: Rust or Python

* **Pros**: Rust offers memory safety and static binaries. Python offers direct access to `rtslib`, `targetd`, and `nvmetcli`.
* **Cons**: Rust has a thinner Kubernetes CSI ecosystem. Python requires an interpreter and dependency management on every host and has weaker static guarantees.
* **Reason for Rejection**: Go provides static binaries, mature Kubernetes and CSI libraries, and strong gRPC support. The direct configfs choice removes the main reason to use Python.

### Alternative Q: Delete always, or require unexport first

* **Pros**: deleting always is simple. Requiring unexport first is the safest ordering for a live consumer.
* **Cons**: deleting always can interrupt a live filesystem. Requiring unexport first adds a mandatory step and can block cleanup on an orphaned export.
* **Reason for Rejection**: delete refuses with `SESSION_ACTIVE` when the target reports an active session. `force=true` overrides and performs the teardown. The caller gets a typed error and an explicit override.

### Alternative R: Missing LV as `failed` or `deleted`

* **Pros**: `failed` keeps the record, and `deleted` releases the name and capacity.
* **Cons**: `failed` can leak capacity forever if nobody cleans it up. `deleted` can destroy ownership on a transient boot-time invisibility.
* **Reason for Rejection**: a mismatch becomes `drift`, which keeps ownership, the name, and the reservation, and blocks new operations until an administrator resolves it. This matches the no-repair rule.

### Alternative S: Import unsupported, or LVs only

* **Pros**: unsupported is the smallest surface. LVs-only avoids adopting target state.
* **Cons**: unsupported forces a manual rebuild. LVs-only requires a re-export outage for every existing volume.
* **Reason for Rejection**: administrators can import existing LVs and exports. Import validates the LV, VG, target binding, and conflicts before adopting anything.

### Alternative T: Full wipe on allocation, or no wipe

* **Pros**: a full wipe gives the strongest data isolation. No wipe gives the fastest allocation.
* **Cons**: a full wipe can take hours and saturate host I/O. No wipe can expose the previous tenant's data.
* **Reason for Rejection**: the default is no wipe, and a wipe happens only on explicit request. The design documents the remanence risk and gives the administrator a deliberate operation.

### Alternative U: HTTP/JSON only, or native dual APIs

* **Pros**: HTTP/JSON is the simplest runtime and debugging path. Native dual APIs avoid a gateway.
* **Cons**: HTTP-only loses the typed gRPC ecosystem. Dual native APIs mean two contracts to keep in sync.
* **Reason for Rejection**: protobuf is the only schema. gRPC serves typed clients, and a generated HTTP/JSON gateway serves Rookery and curl without a second contract.

### Alternatives accepted rather than rejected

* Both iSCSI and NVMe-oF/TCP are in scope. v1 ships iSCSI; stage 2 ships NVMe-oF/TLS-PSK behind the same export interface.
* The CSI driver is part of the project and ships in stage 2, so the Kubernetes path has a supported client. It is a client of the same API, not a second control plane inside kouja.
* SQLite is the only state store, with WAL, full synchronous writes, tested backups, and fail-closed behavior.
* Mutations are synchronous and serialized per volume, with a per-VG lock around capacity and allocation.
* The admin API shares the mTLS listener and uses an admin role in the certificate. The separate loopback listener in [admin-ui.md](admin-ui.md) is limited to the local configuration/status UI and live VG/class actions, and is disabled by default.
* The agent is written in Go and writes configfs directly.

## Risks & Mitigations

| Risk | Impact | Likelihood | Mitigation | Residual risk |
| --- | --- | --- | --- | --- |
| Data remanence after delete or default no-wipe | A new tenant can read previous contents. | Medium | Explicit `WipeVolume`, `blkdiscard` or zeroing, documented limits, consumer-side encryption or isolated tenancy. | Media-level remanence and operator error remain. |
| SQLite loss or corruption | Ownership, export mapping, and credentials are lost while LVs survive. | Medium | WAL, full synchronous writes, integrity checks, 15-minute backups to an off-host target, fail-closed startup. | Volumes created after the last backup require import. |
| Credential key loss | Encrypted credentials cannot be decrypted. | Low | Key fingerprint, fail-closed startup, separate key backup, bulk rotation procedure. | Rotation causes a reconnect outage. |
| mTLS certificate theft or host impersonation | Unauthorized control or data-path access. | Low | Short-lived certificates, server SAN bound to the host UUID, CRL or OCSP, per-node identities, credential grants. | CA compromise remains a fleet-wide risk. |
| CHAP or TLS-PSK exposure | An attacker can connect to an exported device. | Medium | Root-only storage, no secrets in argv or logs, rotation, isolated storage network or IPsec. | Host compromise remains a risk. |
| Kernel and configfs variance | Session observation or TLS-PSK is unavailable. | High | Capability probe, documented kernel matrix, fail-closed behavior, audited force path. | Older kernels require force or are unsupported. |
| Crash mid-mutation | A partial LV, target, or credential remains. | Medium | Durable operation intent, idempotent retry, emergency log, drift state. | Some cases require manual resolution. |
| Capacity allocation race | A create loses to another caller. | Medium | Per-VG lock around the capacity check and allocation, allocatable capacity, typed `ResourceExhausted`. | Fragmentation can still reject a large request. |
| Scale limit (256 volumes, 64 operations) | Requests fail or the host degrades under load. | Medium | Limits section, `ResourceExhausted`, rate limits, and a scale test. | Beyond the limit requires another host. |
| Target-manager choice is unvalidated | The configfs layer is larger or more fragile than expected. | Medium | Five-day spike comparing direct configfs, `targetd`/`rtslib`, and LINSTOR; decision record before item 3. | The spike can invalidate the iSCSI adapter plan. |
| Single-host failure | Volumes on that host are unavailable. | Medium | Caller retries and placement, backups, documented single point of failure. | No high availability in v1. |
| LVM or udev failure | The device is missing or not settled. | Medium | LV activation, `udevadm settle`, device polling, drift classification. | Hardware failure still requires operator action. |
| Schema upgrade or rollback | An older binary cannot read a newer schema. | Low | Schema version table, refuse newer schemas, pre-migration backup, drain procedure. | Rollback may be blocked until the schema is compatible. |
| CSI identity or host mismatch | A node receives a credential for a volume it does not serve. | Low | Per-node certificates, volume grants, server SAN verification, cross-node tests. | A compromised node with a valid grant can use that volume. |
| iSCSI traffic is not encrypted by CHAP | Block data is readable on the storage network. | Medium | Isolated storage network or IPsec, documented firewall rules. | A misconfigured network exposes data. |
| Local-only backup target | A host disk loss also loses the database, key, and backups. | Medium | Off-host backup target for the database, key, host ID, and a non-authoritative restore report. | Backup lag remains. |

## Non-Functional Requirements

### Security and privacy

* The control listener uses mTLS with TLS 1.3. Caller, controller, node, and admin identities come from URI SANs and the issuer.
* The HTTP/JSON gateway runs in-process and reads the TLS peer; it never trusts a forwarded identity header.
* iSCSI uses CHAP. NVMe-oF uses TLS 1.3 with a per-subsystem pre-shared key. iSCSI requires an isolated storage network or IPsec because CHAP does not encrypt block data.
* Credentials are encrypted at rest with AES-256-GCM, a unique nonce, and authenticated data. The database, key, and backups are root-only.
* Secrets never appear in logs, audit rows, list responses, or process arguments. `GetCredential` is owner-only or grant-only and audited.
* The control API belongs on a management network; the data portals belong on a storage network. Firewall rules are documented.
* The CSI controller and each node use distinct certificates. A node can fetch a credential only with a volume grant.

### Performance and latency

* On the reference host defined in the verification plan, 99% of create, export, and delete calls complete within 5 seconds for volumes up to 1 TiB, excluding an explicit wipe.
* Reference host: 8 vCPU, 16 GiB RAM, local NVMe, one 4 TiB VG with 4 MiB extents, Linux 6.12, and 10 concurrent clients. The benchmark creates, exports, and deletes a 1 TiB LV and restores 100 exported volumes.
* A restart with 100 exported volumes reaches readiness within 60 seconds on the reference host, using bounded parallel restore and a per-volume timeout.
* Capacity queries return within 1 second per host on a host with 100 volumes.
* The agent supports at least 10 concurrent volume operations across different volumes, with per-volume and per-VG serialization. Each operation has a five-minute total deadline.
* A wipe has a separate, size-aware deadline and is the only operation expected to exceed five minutes.

### Reliability and availability

* Each host is a single point of failure for its volumes. The callers own retries and placement.
* The agent restores exports after a host reboot, reactivates LVs, reinstalls NVMe PSKs, and re-runs interrupted operations up to the retry and timeout bound.
* A `drift` state keeps ownership and capacity reserved and blocks mutations until an administrator resolves it.
* The agent fails closed when SQLite is missing or corrupt, when the host identity file does not match the database while volumes exist, or when the credential key is missing while credentials exist.

### Observability

* Structured JSON logs carry request and operation IDs.
* Prometheus metrics cover capacity, operations, failures, drift, sessions, grants, and SQLite health.
* `/healthz` and `/readyz` separate liveness from reconciliation readiness.
* Every mutation, credential retrieval, and grant change writes an audit row.

### Limits

* v1 supports up to 256 exported volumes and 512 LVs per host, 256 iSCSI targets and TPGs, 256 NVMe subsystems, and 64 concurrent operations, with 10 per caller.
* Volume sizes are validated up to 1 TiB; VG sizes up to 16 TiB.
* `GetCapacity` is limited to 10 requests per second per host.
* Audit rows are pruned daily with 90-day retention. The SQLite database is expected to stay below 1 GiB at the supported limits.
* At any limit, the agent returns `ResourceExhausted` or `Unavailable` with a reason. It does not degrade silently.
* A token-bucket admission controller enforces the concurrency and capacity-query limits. Excess requests return `ResourceExhausted` or `Unavailable`, and `kouja_admission_rejected_total` counts them.

## Rollout and Operations

* The installer disables `target.service` and `targetcli saveconfig`, creates the directories and firewall rules, and documents uninstall.
* The agent is a systemd service. Upgrades replace the binary and restart the service. Schema migrations run before the listener starts.
* An older binary refuses to start against a newer schema.
* The CSI driver upgrades independently. It reads every volume-handle version it has emitted and supports the previous minor agent API (N-1).
* Rolling back the agent is allowed only when the schema is compatible. Destructive operations are not reversible.
* Uninstall unexports volumes and keeps the data LVs. It does not delete them automatically.
* The database, key, host ID, and a non-authoritative `restore.json` report are backed up to an off-host target, and a restore drill is part of the release checklist.

## Additional Resources

* [LIO configfs documentation](https://linux-iscsi.org/wiki/ConfigFS).
* [ktls-utils for NVMe-oF/TLS](https://github.com/oracle/ktls-utils).
* [Container Storage Interface specification](https://github.com/container-storage-interface/spec).
* [LVM documentation](https://man7.org/linux/man-pages/man8/lvm.8.html).
* [csi-sanity](https://github.com/kubernetes-csi/csi-test).

## Future Improvements

* **Topology-aware CSI placement**: expose storage topology domains instead of the flat-network assumption.
* **Multipath**: return multiple portals and document multipath configuration on the consumer.
* **Snapshots and clones**: add LVM snapshot or thin-snapshot support with a separate state model.
* **Thin provisioning**: support thin pools with an explicit overcommit policy and pool-exhaustion handling.
* **Asynchronous operations**: add an operation status endpoint if a caller needs to poll long operations.
* **Second recovery source**: add LVM tags or an atomic manifest if backup-only recovery proves insufficient.
* **Containerized agent**: package the agent as a privileged container for immutable host images.
* **Helm chart**: replace raw Kubernetes manifests with a chart once the install surface stabilizes.
* **Data-at-rest encryption**: add per-volume encryption if the data remanence risk becomes unacceptable.
* **Multi-initiator and RWX**: add shared-disk support only with a cluster filesystem and fencing model.

## Phase 0 Readiness

The design is approved for implementation, starting with Phase 0. Phase 0 is a gate, not a feature stage.

| Item | Owner | Exit criterion |
| --- | --- | --- |
| Kernel and configfs probe | Platform team | Supported kernel matrix documented; LIO and nvmet session-visibility probes return a definitive state or `SESSION_STATE_UNKNOWN`; TLS-PSK path verified. |
| Target-manager spike | Storage team | Five-day comparison recorded in `docs/decisions/target-manager.md`; direct configfs confirmed or the `targetd`/`rtslib` iSCSI fallback selected. |
| Proto and CRD generation | Agent team | `buf lint`, `buf generate`, `buf breaking`, and `controller-gen` pass; generated code compiles. |
| Skeleton compile | Agent team | `state.Tx`, `operations.Options`, `credential.Manager`, and `target.Exporter` compile together with no package cycles. |
| Test harness | Platform team | Two-host mTLS smoke test, kind and e2e environment, failpoint hooks, and reference-host benchmark are runnable. |
| Deployment contract | Platform team | Exact manifests and installer are written and installed on a disposable host. |

Phase 0 exit is required before the Stage 1 iSCSI adapter work begins. The rest of Stage 1 can proceed in parallel where it does not depend on the kernel or target-manager outcome.

## Detailed Implementation Plan

This section is the implementation appendix and the Stage 9 review range. The architecture altitude standard does not apply to it. The file table is deliberately exhaustive so an implementer can execute it without design decisions.

### Implementation sequence

1. **Foundation**: repository scaffolding, protobuf definitions, generated clients, configuration, version metadata, and CI. The Stage 1 build produces a static agent binary; Stage 2 adds the two CSI binaries.
2. **State and LVM**: SQLite schema and migrations, volume and export stores, LVM command adapter, eligibility checks, host identity, and backup.
3. **iSCSI path**: configfs helpers, LIO adapter, CHAP credentials, operation manager, create/export/delete flows, and unit tests.
4. **NVMe-oF path**: nvmet adapter, TLS-PSK generation and keyring integration, and the shared export interface.
5. **Recovery**: startup and periodic reconciliation, boot-ID handling, drift detection, and restore tests.
6. **CSI**: controller and node services, SRV inventory, host selection, initiator discovery, connection and mount helpers, volume handles, and conformance tests.
7. **Admin and operations**: Stage 1 includes drift resolution, audit, metrics, readiness, and the VG admin surface. Stage 3 adds import, wipe, ownership transfer, admin delete, documentation, and the remaining operational tooling.
8. **Packaging and end-to-end**: systemd unit, Kubernetes manifests, integration lab, crash injection, and release automation.

Stage 1 (v1) includes items 1, 2, 3, 5, the Stage 1 subset of item 7 (drift resolution, audit, metrics, readiness, and VG administration), and the iSCSI parts of item 8. Stage 2 adds item 4 and the CSI driver in item 6. Stage 3 adds grow, shrink, import, wipe, and the remaining operational tooling in items 7 and 8.

Decision: direct configfs is the v1 implementation. Before item 3, a five-day spike validates it; if it fails, the fallback is `targetd`/`rtslib` for iSCSI only, with nvmet direct. The affected files are `internal/target/iscsi.go`, `session.go`, and the dependency pins. The decision evidence is recorded in `docs/decisions/target-manager.md`.

### Dependencies

* `google.golang.org/grpc`, `google.golang.org/protobuf`, and `github.com/grpc-ecosystem/grpc-gateway/v2` provide the API and HTTP gateway.
* `modernc.org/sqlite` is a pure-Go SQLite driver, so the agent builds with `CGO_ENABLED=0`.
* `github.com/google/uuid` generates resource UUIDs and host IDs.
* `gopkg.in/yaml.v3` parses the agent configuration.
* `github.com/prometheus/client_golang` exposes metrics.
* `golang.org/x/sync/errgroup` runs parallel capacity queries.
* `golang.org/x/sys/unix` performs keyring and system-call helpers without a `keyctl` binary; the LVM adapter invokes `udevadm` for device settling.
* Root-VG detection uses `findmnt`, `lvs`, and `lsblk`; no extra mountinfo dependency is required.
* `github.com/container-storage-interface/spec` provides the CSI protobuf types.
* `k8s.io/client-go` and `k8s.io/apimachinery` read and write `CSINode` annotations.
* `github.com/kubernetes-csi/csi-test/v5` and `github.com/stretchr/testify` are test-only dependencies.

### Implementation contract

This appendix removes the remaining design choices from the implementation plan. The target-file table is the index; this section defines the contracts the files must implement.

#### Phase map

| Stage | Scope | Exit gate |
| --- | --- | --- |
| Stage 1 (v1, iSCSI) | Foundation, `common.proto`, `volume.proto`, `admin.proto` definitions, generated core code, config, version, state, LVM, shared exporter, LIO, CHAP, operation manager with create/export/delete, reconciliation, host ID, backup, audit, metrics, readiness, auth, agent core, VG registration, drift resolution, systemd, installer, unit and integration tests, fault harness. | `make build`, `make test`, `make integration` for iSCSI, `make fault`, and a two-host mTLS smoke test pass. |
| Stage 2 (NVMe-oF and CSI) | Grant handlers, nvmet, TLS-PSK keyring, CSI controller and node, inventory, initiator annotations, mount helpers, handles, placement CRD, Kubernetes manifests, e2e. | `make csi-sanity`, `make e2e`, and NVMe-oF integration pass. |
| Stage 3 (growth and migration) | Resize, wipe, import, ownership transfer, admin delete, performance benchmarks, upgrade suite, final docs. | `make perf`, `make upgrade`, and the full verification matrix pass. |

Files in a later stage are compiled but not wired; later-stage RPC methods return `Unimplemented`. Stage 1 omits the CSI binaries from the build target; it does not ship stubs. The phase map is authoritative; the target table does not need a per-row stage column.

Stage 1 does not build the CSI binaries or run `csi-sanity`. The CSI commands are stubs that exit with a clear "stage 2" message, or are omitted from the Stage 1 build target. VG registration is Stage 1; import, wipe, transfer, and admin delete are Stage 3.

#### Package contracts

```go
package state

type Store struct { /* ... */ }
func Open(ctx context.Context, path string) (*Store, error)
func (s *Store) Close() error
func (s *Store) Transact(ctx context.Context, fn func(Tx) error) error

type Tx interface {
    GetVolume(owner, volumeID string) (Volume, error)
    GetVolumeByResourceUUID(resourceUUID string) (Volume, error)
    ListVolumes(owner string, page Page) ([]Volume, error)
    PutVolume(Volume) error
    DeleteVolume(owner, volumeID string) error
    PutTombstone(Tombstone) error
    GetTombstone(owner, volumeID string) (Tombstone, error)
    ListTombstones(owner string) ([]Tombstone, error)
    GetExport(owner, volumeID string) (Export, error)
    PutExport(Export) error
    DeleteExport(owner, volumeID string) error
    GetObservation(owner, volumeID string) (Observation, error)
    PutObservation(Observation) error
    GetExportObservation(owner, volumeID string) (ExportObservation, error)
    PutExportObservation(ExportObservation) error
    GetVG(name string) (VG, error)
    ListVGs() ([]VG, error)
    PutVG(VG) error
    DeleteVG(name string) error
    GetSetting(key string) (string, error)
    PutSetting(key, value string) error
    GetOperation(owner, volumeID, action, requestHash string) (Operation, error)
    GetOperationByID(id string) (Operation, error)
    ListOperations(owner, volumeID string) ([]Operation, error)
    ListRunningOperations() ([]Operation, error)
    PutOperation(Operation) error
    PutGrant(Grant) error
    GetGrant(owner, volumeID, nodeID string) (Grant, error)
    ListGrants(owner, volumeID string) ([]Grant, error)
    DeleteGrant(owner, volumeID, nodeID string) error
    PutCredential(Credential) error
    GetActiveCredential(owner, volumeID string) (Credential, error)
    DeleteCredential(owner, volumeID string) error
    PutAudit(Audit) error
}
```

`internal/state/types.go` defines `Volume`, `Export`, `Observation`, `ExportObservation`, `Tombstone`, `VG`, `Grant`, `Credential`, `Operation`, `Audit`, and `Page` (limit plus cursor).

```go
package lvm

type Backend interface {
    Create(ctx context.Context, vg, lvName string, bytes int64, tags map[string]string) (LV, error)
    Extend(ctx context.Context, vg, lvName string, bytes int64) (LV, error)
    Shrink(ctx context.Context, vg, lvName string, bytes int64) (LV, error)
    Remove(ctx context.Context, vg, lvName string) error
    Activate(ctx context.Context, vg, lvName string) error
    Observe(ctx context.Context, vg, lvName string) (LV, error)
    ListManaged(ctx context.Context) ([]LV, error)
    ListCandidates(ctx context.Context) ([]LV, error)
    SetOwnerTag(ctx context.Context, vg, lvName, ownerHash string) error
    Capacity(ctx context.Context, vg string) (Capacity, error)
    Eligibility(ctx context.Context, vg string) error
}
```

```go
package target

type Exporter interface {
    Export(ctx context.Context, spec ExportSpec, credential Credential) (ExportResult, error)
    Unexport(ctx context.Context, spec ExportSpec, force bool) error
    Observe(ctx context.Context, spec ExportSpec) (ExportObservation, error)
    ListSessions(ctx context.Context) ([]Session, error)
    Restore(ctx context.Context, spec ExportSpec, credential Credential) error
    Delete(ctx context.Context, spec ExportSpec, force bool) error
}

type Session struct {
    Initiator string
    Target    string
    State     SessionState
}

type ExportObservation struct {
    TargetPresent    bool
    NamespacePresent bool
    ACLPresent       bool
    ActiveSessions   int
    SessionState     SessionState
    ObservedInitiator string
    ObservedAt       time.Time
}

type SessionState string
const (
    SessionNone    SessionState = "none"
    SessionActive  SessionState = "active"
    SessionUnknown SessionState = "unknown"
)
```

```go
package operations

type Action string
const (
    ActionCreate   Action = "create"
    ActionExport   Action = "export"
    ActionUnexport Action = "unexport"
    ActionResize   Action = "resize"
    ActionDelete   Action = "delete"
    ActionWipe     Action = "wipe"
    ActionImport   Action = "import"
)

type Options struct {
    MaxAttempts  int
    TotalTimeout time.Duration
    StepTimeouts map[Action]time.Duration
    Failpoints   Failpoints
    Metrics      *observability.Metrics
    Audit        AuditWriter
    Clock        Clock
}
type Request struct {
    Owner     string
    VolumeID  string
    Action    Action
    Intent    json.RawMessage
    RequestID string
}
type Result struct {
    Volume      state.Volume
    Export      state.Export
    OperationID string
}
type Manager struct { /* ... */ }
func New(store *state.Store, lvm lvm.Backend, iscsi target.Exporter, nvmet target.Exporter, creds credential.Manager, opts Options) *Manager
func (m *Manager) Run(ctx context.Context, req Request) (Result, error)
func (m *Manager) Resume(ctx context.Context) error
```

```go
package credential

type Manager interface {
    Generate(ctx context.Context, owner, volumeID, resourceUUID string, protocol Protocol) (Credential, error)
    // Reveal decrypts a stored credential only for target installation.
    Reveal(ctx context.Context, credential Credential) (target.Credential, error)
    Get(ctx context.Context, caller auth.Identity, owner, volumeID string) (Credential, error)
    Rotate(ctx context.Context, caller auth.Identity, owner, volumeID string) (Credential, error)
    Install(ctx context.Context, spec target.ExportSpec, credential Credential) error
    Revoke(ctx context.Context, spec target.ExportSpec) error
}
```

`credential.Manager` owns the AES-GCM key, nonce, and key fingerprint. The key file is read or created only after SQLite and the host-ID checks. `Install` writes the LIO ACL or the `.nvme` keyring entry. `Get` returns a decrypted credential only when `caller.Owner == owner` or when a matching grant exists for a node caller. `Rotate` is owner-only and rejects node callers. The `target` package's `Credential` is a plain secret value; `credential.Credential` is the stored record. `Install` accepts the plain value, so the target package does not import the credential package.

```go
package auth

type Role string
const (
    RoleCaller     Role = "caller"
    RoleController Role = "controller"
    RoleNode       Role = "node"
    RoleAdmin      Role = "admin"
)

type Identity struct {
    Owner string // full SAN URI
    Role  Role
}
func Parse(peer *x509.Certificate) (Identity, error)
```

`owner` is the full SAN URI: `spiffe://kouja/caller/<id>` or `spiffe://kouja/controller/<id>`. The issuer is verified but not part of the key. The node SAN `spiffe://kouja/node/<id>` is not an owner. `owner_hash = hex(SHA-256(owner))[:16]`. Rotation keeps the same SAN and owner.

```go
package csi

type Inventory interface {
    Resolve(ctx context.Context, srvRecord string) ([]Host, error)
    Capacity(ctx context.Context, host Host, vg string) (Capacity, error)
    Select(ctx context.Context, hosts []Host, request SelectRequest) (Host, error)
}
type Placement interface {
    Get(ctx context.Context, key string) (PlacementRecord, error)
    List(ctx context.Context) ([]PlacementRecord, error)
    Pending(ctx context.Context, key string, host Host, volumeID string) error
    Bind(ctx context.Context, key string, resourceUUID string) error
    Fail(ctx context.Context, key string, err error) error
    Delete(ctx context.Context, key string) error
}
type Mounter interface {
    Connect(ctx context.Context, spec ConnectSpec) (Device, error)
    Disconnect(ctx context.Context, spec ConnectSpec) error
    Format(ctx context.Context, device Device, fsType string) error
    HasFilesystem(ctx context.Context, device Device) (bool, error)
    Mount(ctx context.Context, device Device, target string) error
    IsMounted(ctx context.Context, target string) (bool, error)
    Unmount(ctx context.Context, target string) error
    Resize(ctx context.Context, device Device, fsType string) error
}
```

#### Validation rules

| Field | Rule |
| --- | --- |
| `volume_id` | `[A-Za-z0-9][A-Za-z0-9._-]{0,127}` |
| IQN | `iqn\.[0-9]{4}-[0-9]{2}\.[A-Za-z0-9.-]+(:[A-Za-z0-9._-]+)+`, maximum 223 characters |
| NQN | `nqn\.[0-9]{4}-[0-9]{2}\.[A-Za-z0-9.-]+(:[A-Za-z0-9._-]+)+`, maximum 223 characters |
| VG name | `[A-Za-z0-9+_.-]{1,127}`, must exist and be eligible |
| `size_bytes` | greater than 0, at most 1 TiB; the response returns the extent-rounded size |
| `request_id` | 1-128 printable ASCII characters, opaque to kouja |
| `force`, `allow_shrink`, `acknowledge_data_loss` | explicit booleans; no caller default is inferred |
| Timeouts | 1 second to 3600 seconds; the total deadline is at least the step deadline |

#### Protobuf contract

The following is the authoritative field-number assignment. It is reduced to the messages required by the design; comments mark the HTTP annotations.

```proto
syntax = "proto3";
package kouja.v1;
option go_package = "github.com/voidstarit/kouja/gen/kouja/v1;koujav1";

import "google/api/annotations.proto";
import "google/protobuf/timestamp.proto";

enum Protocol { PROTOCOL_UNSPECIFIED = 0; PROTOCOL_ISCSI = 1; PROTOCOL_NVME_TCP = 2; }
enum VolumeState { VOLUME_STATE_UNSPECIFIED = 0; VOLUME_STATE_CREATING = 1; VOLUME_STATE_AVAILABLE = 2; VOLUME_STATE_EXPORTING = 3; VOLUME_STATE_EXPORTED = 4; VOLUME_STATE_RESIZING = 5; VOLUME_STATE_DELETING = 6; VOLUME_STATE_DELETED = 7; VOLUME_STATE_FAILED = 8; VOLUME_STATE_DRIFT = 9; }
enum ExportState { EXPORT_STATE_UNSPECIFIED = 0; EXPORT_STATE_EXPORTING = 1; EXPORT_STATE_EXPORTED = 2; EXPORT_STATE_UNEXPORTING = 3; EXPORT_STATE_UNEXPORTED = 4; EXPORT_STATE_FAILED = 5; EXPORT_STATE_DRIFT = 6; }
enum SessionState { SESSION_STATE_UNSPECIFIED = 0; SESSION_STATE_NONE = 1; SESSION_STATE_ACTIVE = 2; SESSION_STATE_UNKNOWN = 3; }
enum DriftReason { DRIFT_REASON_UNSPECIFIED = 0; DRIFT_REASON_LV_ABSENT = 1; DRIFT_REASON_SIZE_MISMATCH = 2; DRIFT_REASON_TARGET_ABSENT = 3; DRIFT_REASON_NAMESPACE_ABSENT = 4; DRIFT_REASON_ACL_ABSENT = 5; DRIFT_REASON_INITIATOR_MISMATCH = 6; DRIFT_REASON_UNATTRIBUTED_SESSION = 7; }
enum ResolveAction { RESOLVE_ACTION_UNSPECIFIED = 0; RESOLVE_ACTION_MARK_FAILED = 1; RESOLVE_ACTION_MARK_AVAILABLE = 2; RESOLVE_ACTION_MARK_EXPORTED = 3; RESOLVE_ACTION_MARK_DELETED = 4; RESOLVE_ACTION_RETRY = 5; RESOLVE_ACTION_RECREATE_EXPORT = 6; }

message Volume {
  string owner = 1;
  string volume_id = 2;
  string resource_uuid = 3;
  string lv_name = 4;
  string vg_name = 5;
  int64 requested_size_bytes = 6;
  int64 actual_size_bytes = 7;
  VolumeState state = 8;
  repeated DriftReason drift_reasons = 9;
  string lv_uuid = 10;
  google.protobuf.Timestamp created_at = 11;
  google.protobuf.Timestamp updated_at = 12;
  map<string,string> legacy_identity = 13;
}

message Export {
  Protocol protocol = 1;
  string initiator = 2;
  string target_name = 3;
  int32 lun = 4;
  int32 nsid = 5;
  string portal = 6;
  ExportState state = 7;
  SessionState session_state = 8;
  string credential = 9; // owner-only; omitted from list responses
}

message CreateVolumeRequest {
  string volume_id = 1;
  int64 size_bytes = 2;
  string vg = 3;
  optional bool export = 4;
  Protocol protocol = 5;
  string initiator = 6;
  bool wipe = 7;
  string request_id = 8;
  optional bool include_credential = 9;
}
message CreateVolumeResponse { Volume volume = 1; Export export = 2; string operation_id = 3; string request_id = 4; }
message ExportVolumeRequest { string volume_id = 1; Protocol protocol = 2; string initiator = 3; string request_id = 4; }
message ExportVolumeResponse { Volume volume = 1; Export export = 2; string operation_id = 3; string request_id = 4; }
message UnexportVolumeRequest { string volume_id = 1; bool force = 2; string request_id = 3; }
message UnexportVolumeResponse { Volume volume = 1; Export export = 2; string operation_id = 3; string request_id = 4; }
message ResizeVolumeRequest { string volume_id = 1; int64 size_bytes = 2; bool allow_shrink = 3; string request_id = 4; }
message ResizeVolumeResponse { Volume volume = 1; Export export = 2; bool rescan_required = 3; bool reconnect_required = 4; string operation_id = 5; string request_id = 6; }
message DeleteVolumeRequest { string volume_id = 1; bool force = 2; string request_id = 3; }
message DeleteVolumeResponse { string operation_id = 1; string request_id = 2; }
message GetVolumeRequest { string volume_id = 1; }
message Observation {
  bool lv_present = 1;
  int64 observed_size_bytes = 2;
  string device_path = 3;
  bool target_present = 4;
  bool namespace_present = 5;
  bool acl_present = 6;
  int32 active_sessions = 7;
  SessionState session_state = 8;
  string observed_initiator = 9;
  google.protobuf.Timestamp observed_at = 10;
  string source = 11;
}
message GetVolumeResponse { Volume desired = 1; Observation observed = 2; Export export = 3; repeated DriftReason drift_reasons = 4; string operation_id = 5; }
message ListVolumesRequest { int32 page_size = 1; string page_token = 2; }
message ListVolumesResponse { repeated Volume volumes = 1; string next_page_token = 2; }
message GetCredentialRequest { string volume_id = 1; string owner = 2; }
message GetCredentialResponse { Protocol protocol = 1; string credential = 2; string operation_id = 3; }
message RotateCredentialRequest { string volume_id = 1; string request_id = 2; }
message RotateCredentialResponse { string credential = 1; string operation_id = 2; string request_id = 3; }
message Capacity { string vg_name = 1; string vg_uuid = 2; string vg_state = 3; int64 total_bytes = 4; int64 free_bytes = 5; int64 allocatable_bytes = 6; int64 extent_size_bytes = 7; int64 max_volume_bytes = 8; google.protobuf.Timestamp observed_at = 9; }
message GetCapacityRequest {}
message GetCapacityResponse { repeated Capacity vgs = 1; }
message ListStorageClassesRequest {}
message StorageClass { string name = 1; string vg_name = 2; string vg_uuid = 3; string vg_state = 4; int64 total_bytes = 5; int64 free_bytes = 6; int64 allocatable_bytes = 7; int64 extent_size_bytes = 8; int64 max_volume_bytes = 9; google.protobuf.Timestamp observed_at = 10; }
message ListStorageClassesResponse { repeated StorageClass classes = 1; }
message GetInfoRequest {}
message GetInfoResponse { string host_uuid = 1; string agent_version = 2; string api_version = 3; repeated Protocol protocols = 4; repeated string auth_modes = 5; bool ready = 6; }

message GrantCredentialRequest { string volume_id = 1; string node_id = 2; string node_caller_id = 3; google.protobuf.Timestamp expires_at = 4; string owner = 5; string request_id = 6; }
message GrantCredentialResponse { string request_id = 1; }
message RevokeGrantRequest { string volume_id = 1; string node_id = 2; string owner = 3; string request_id = 4; }
message RevokeGrantResponse { string request_id = 1; }
message RegisterVGRequest { string vg_name = 1; string vg_uuid = 2; string request_id = 3; }
message RegisterVGResponse { Capacity capacity = 1; string request_id = 2; }
message DeregisterVGRequest { string vg_name = 1; string request_id = 2; }
message DeregisterVGResponse { string request_id = 1; }
message ListVGsRequest {}
message ListVGsResponse { repeated Capacity vgs = 1; }
message ImportVolumeRequest { string volume_id = 1; string owner = 2; string vg = 3; string lv_uuid = 4; string lv_name = 5; int64 size_bytes = 6; Protocol protocol = 7; string initiator = 8; string target_name = 9; int32 lun = 10; int32 nsid = 11; bytes credential = 12; bool allow_active_sessions = 13; string request_id = 14; }
message ImportVolumeResponse { Volume volume = 1; Export export = 2; string operation_id = 3; string request_id = 4; }
message ResolveDriftRequest { string volume_id = 1; ResolveAction action = 2; string reason = 3; string owner = 4; string request_id = 5; }
message ResolveDriftResponse { Volume volume = 1; string operation_id = 2; string request_id = 3; }
message ListOrphansRequest {}
message ListOrphansResponse { repeated Volume orphans = 1; }
message WipeVolumeRequest { string volume_id = 1; bool acknowledge_data_loss = 2; bool force = 3; string request_id = 4; string owner = 5; }
message WipeVolumeResponse { string operation_id = 1; string request_id = 2; }
message TransferOwnershipRequest { string volume_id = 1; string new_owner = 2; string owner = 3; string request_id = 4; }
message TransferOwnershipResponse { Volume volume = 1; string operation_id = 2; string request_id = 3; }
message AdminDeleteVolumeRequest { string volume_id = 1; bool acknowledge_data_loss = 2; string owner = 3; string request_id = 4; }
message AdminDeleteVolumeResponse { string operation_id = 1; string request_id = 2; }

service VolumeService {
  rpc CreateVolume(CreateVolumeRequest) returns (CreateVolumeResponse) { option (google.api.http) = { post: "/v1/volumes" body: "*" }; }
  rpc ExportVolume(ExportVolumeRequest) returns (ExportVolumeResponse) { option (google.api.http) = { post: "/v1/volumes/{volume_id}:export" body: "*" }; }
  rpc UnexportVolume(UnexportVolumeRequest) returns (UnexportVolumeResponse) { option (google.api.http) = { post: "/v1/volumes/{volume_id}:unexport" body: "*" }; }
  rpc ResizeVolume(ResizeVolumeRequest) returns (ResizeVolumeResponse) { option (google.api.http) = { post: "/v1/volumes/{volume_id}:resize" body: "*" }; }
  rpc DeleteVolume(DeleteVolumeRequest) returns (DeleteVolumeResponse) { option (google.api.http) = { delete: "/v1/volumes/{volume_id}" }; }
  rpc GetVolume(GetVolumeRequest) returns (GetVolumeResponse) { option (google.api.http) = { get: "/v1/volumes/{volume_id}" }; }
  rpc ListVolumes(ListVolumesRequest) returns (ListVolumesResponse) { option (google.api.http) = { get: "/v1/volumes" }; }
  rpc GetCredential(GetCredentialRequest) returns (GetCredentialResponse) { option (google.api.http) = { get: "/v1/volumes/{volume_id}/credential" }; }
  rpc RotateCredential(RotateCredentialRequest) returns (RotateCredentialResponse) { option (google.api.http) = { post: "/v1/volumes/{volume_id}:rotate-credential" body: "*" }; }
  rpc GetCapacity(GetCapacityRequest) returns (GetCapacityResponse) { option (google.api.http) = { get: "/v1/capacity" }; }
  rpc ListStorageClasses(ListStorageClassesRequest) returns (ListStorageClassesResponse) { option (google.api.http) = { get: "/v1/storage-classes" }; }
  rpc GetInfo(GetInfoRequest) returns (GetInfoResponse) { option (google.api.http) = { get: "/v1/info" }; }
}

service AdminService {
  rpc RegisterVG(RegisterVGRequest) returns (RegisterVGResponse) { option (google.api.http) = { post: "/v1/admin/vgs" body: "*" }; }
  rpc DeregisterVG(DeregisterVGRequest) returns (DeregisterVGResponse) { option (google.api.http) = { delete: "/v1/admin/vgs/{vg_name}" }; }
  rpc ListVGs(ListVGsRequest) returns (ListVGsResponse) { option (google.api.http) = { get: "/v1/admin/vgs" }; }
  rpc ImportVolume(ImportVolumeRequest) returns (ImportVolumeResponse) { option (google.api.http) = { post: "/v1/admin/import" body: "*" }; }
  rpc ResolveDrift(ResolveDriftRequest) returns (ResolveDriftResponse) { option (google.api.http) = { post: "/v1/admin/drift/{volume_id}:resolve" body: "*" }; }
  rpc ListOrphans(ListOrphansRequest) returns (ListOrphansResponse) { option (google.api.http) = { get: "/v1/admin/orphans" }; }
  rpc WipeVolume(WipeVolumeRequest) returns (WipeVolumeResponse) { option (google.api.http) = { post: "/v1/admin/volumes/{volume_id}:wipe" body: "*" }; }
  rpc TransferOwnership(TransferOwnershipRequest) returns (TransferOwnershipResponse) { option (google.api.http) = { post: "/v1/admin/ownership:transfer" body: "*" }; }
  rpc AdminDeleteVolume(AdminDeleteVolumeRequest) returns (AdminDeleteVolumeResponse) { option (google.api.http) = { delete: "/v1/admin/volumes/{volume_id}" }; }
  rpc GrantCredential(GrantCredentialRequest) returns (GrantCredentialResponse) { option (google.api.http) = { post: "/v1/admin/grants" body: "*" }; }
  rpc RevokeGrant(RevokeGrantRequest) returns (RevokeGrantResponse) { option (google.api.http) = { delete: "/v1/admin/grants/{volume_id}/{node_id}" }; }
}
```

`GetVolumeResponse.drift_reasons` is authoritative; `Volume.drift_reasons` is a convenience copy used by exported summaries.

#### Dependency and toolchain pins

| Dependency or tool | Version |
| --- | --- |
| Go | 1.26.0 |
| `google.golang.org/grpc` | v1.85.0 |
| `google.golang.org/protobuf` | v1.36.12 |
| `github.com/grpc-ecosystem/grpc-gateway/v2` | v2.33.0 |
| `google.golang.org/genproto/googleapis/api` | v0.0.0-20261104224025-d1e00e9f33ab |
| `google.golang.org/genproto/googleapis/rpc` | v0.0.0-20261019180522-ce908d29299b |
| `modernc.org/sqlite` | v1.34.4 |
| `github.com/google/uuid` | v1.6.0 |
| `gopkg.in/yaml.v3` | v3.0.1 |
| `github.com/prometheus/client_golang` | v1.20.5 |
| `golang.org/x/sync` | v0.10.0 |
| `golang.org/x/sys` | v0.29.0 |
| `golang.org/x/time` | v0.10.0 |
| `github.com/container-storage-interface/spec` | v1.10.0 |
| `k8s.io/client-go` | v0.32.1 |
| `k8s.io/apimachinery` | v0.32.1 |
| `k8s.io/api` | v0.32.1 |
| `github.com/kubernetes-csi/csi-test/v5` | v5.3.1 |
| `github.com/stretchr/testify` | v1.10.0 |
| buf | v1.47.0 |
| protoc-gen-go | v1.36.0 |
| protoc-gen-go-grpc | v1.5.1 |
| protoc-gen-grpc-gateway | v2.24.0 |
| `controller-gen` | v0.16.5 |
| golangci-lint | v2.15.0 |
| kind | v0.26.0 |
| csi-sanity | v5.3.1 |
| build image | `golang:1.24-bookworm` |
| controller runtime image | `gcr.io/distroless/static-debian12:nonroot` |
| node runtime image | `debian:bookworm-slim` with `iscsiadm`, `nvme-cli`, `ktls-utils`, `e2fsprogs`, and `xfsprogs`; runs as root |
| csi-provisioner | v5.2.0 |
| csi-attacher | v4.8.0 |
| csi-resizer | v1.13.1 |
| csi-node-driver-registrar | v2.12.0 |

`google.golang.org/genproto/googleapis/rpc` and `google.golang.org/genproto/googleapis/api` are resolved by `go mod tidy` to the versions required by gRPC and grpc-gateway; the resolved versions are pinned in `go.sum`. `go.mod` also records the module path `github.com/voidstarit/kouja` and `go 1.26.0`.

#### Idempotency and intent format

* Canonical JSON follows RFC 8785: UTF-8, lexicographically sorted keys, no insignificant whitespace, explicit `false` and `0` preserved, `null` and empty optional fields omitted, and a top-level `"version":"v1"`.
* The hash input is the canonical JSON itself. The action is a field inside the canonical JSON. The hash is SHA-256, encoded as lowercase hex.
* `request_id`, trace headers, credential secrets, and the generated `resource_uuid` are excluded from the hash. The intent stores the resolved `resource_uuid` after it is generated.
* `ExportVolume` uses action `create` in the canonical JSON, so it and `CreateVolume` with `export=true` share one idempotency key.
* `intent_json` stores the canonical JSON plus the resolved `resource_uuid`, owner, and action. It is written in the same transaction as the operation row.
* Re-arm updates the existing operation row: `state='running'`, `attempt=0`, `deadline_at=now+total_timeout`, and keeps the same `id`. It never inserts a second row for the same key.
* Test vector: canonical JSON `{"action":"create","export":true,"initiator":"iqn.2026-11.io.example:host1","protocol":"ISCSI","requested_size_bytes":1073741824,"version":"v1","vg":"vg0","volume_id":"vol-1","wipe":false}`. SHA-256: `cfe9c314108a38761c20d1d16df56d44ecda3e1c05a82cf32057280fe23bb6c3`.

#### Deployment contract

| File | Required content |
| --- | --- |
| `deploy/kubernetes/rbac-controller.yaml` | `get/list/watch/create/update/patch/delete` on PVs, PVCs, VolumeAttachments, CSINodes, StorageClasses, Events, Leases, Nodes, Pods, and `kouja.io` placements; `create` on Events; `update` on PVC and VolumeAttachment status. |
| `deploy/kubernetes/rbac-node.yaml` | `get/patch` on CSINodes; `create` on Events. |
| `deploy/kubernetes/crd-koujavolumeplacement.yaml` | API group `kouja.io`, version `v1alpha1`, kind `KoujaVolumePlacement`, scope Cluster, status subresource, printer columns for host and phase. |
| `deploy/kubernetes/controller-deployment.yaml` | Service account, three sidecars (provisioner v5.2.0, attacher v4.8.0, resizer v1.13.1), socket `/csi/csi.sock`, leader-election IDs, liveness and readiness probes, non-root security context. |
| `deploy/kubernetes/node-daemonset.yaml` | Privileged, host-network, `csi-node-driver-registrar v2.12.0`, socket `/csi/csi.sock`, host paths `/var/lib/kubelet`, `/dev`, `/sys`, `/etc/iscsi`, `/etc/nvme`, node TLS secret, tolerations for control-plane nodes. |
| `deploy/kubernetes/csidriver.yaml` | `attachRequired: true`, `podInfoOnMount: true`, `volumeLifecycleModes: [Persistent]`, `fsGroupPolicy: None`, `requiresRepublish: false`. |
| `deploy/systemd/kouja-agent.service` | `ExecStart=/usr/local/bin/kouja-agent --config /etc/kouja/config.yaml`, `Restart=on-failure`, `LimitNOFILE=1048576`, `After=network-online.target`, `Wants=network-online.target`, `User=root`, and `[Install] WantedBy=multi-user.target`. |
| `deploy/systemd/kouja-agent.conf` | Installed as `/etc/tmpfiles.d/kouja-agent.conf`; creates `/var/lib/kouja` 0700 root:root and `/var/lib/kouja/backups` 0700 root:root. |
| `deploy/install/install.sh` | Installs the binary at `/usr/local/bin/kouja-agent`, config at `/etc/kouja/config.yaml`, TLS material at `/etc/kouja/tls` (directory 0700, files 0600), the systemd unit, and tmpfiles; disables `target.service` and `targetcli saveconfig`; creates the data and backup directories; enables and starts the service; applies firewall rules; prints an uninstall procedure. |

Sidecar arguments:

* `csi-provisioner`: `--csi-address=/csi/csi.sock --leader-election --leader-election-namespace=kouja-system --timeout=30s`.
* `csi-attacher`: `--csi-address=/csi/csi.sock --leader-election --leader-election-namespace=kouja-system --timeout=30s`.
* `csi-resizer`: `--csi-address=/csi/csi.sock --leader-election --leader-election-namespace=kouja-system --timeout=30s`.
* `csi-node-driver-registrar`: `--csi-address=/csi/csi.sock --kubelet-registration-path=/var/lib/kubelet/plugins/kouja.csi.voidstarit.com/csi.sock --drivername=kouja.csi.voidstarit.com`.

The controller and node pods use the `kouja-csi` service account, the `/csi` emptyDir socket, the `/var/lib/kubelet` host path with `mountPropagation: Bidirectional`, and the `kouja-csi-client-tls` secret. Each node receives its own certificate from a per-node Secret named `kouja-csi-node-<node-name>`; the DaemonSet projects the matching Secret with a downward-API `NODE_NAME` field.

CRD schema: `spec.placementKey` (string, required), `spec.csiVolumeName` (string, required), `spec.storageClass` (string, required), `spec.hostUuid` (string, required), `spec.volumeId` (string, required), `spec.resourceUuid` (string, optional), `spec.idempotencyKey` (string, required); `status.phase` (`Pending`, `Bound`, `Failed`), `status.error`, `status.updatedAt`. The status subresource is enabled.

#### Test matrix

| Test file | Required cases | Stage | Command |
| --- | --- | --- | --- |
| `internal/state/*_test.go` | Schema, FKs, tombstone cascade, session state, grants, re-arm, `needs_attention`, audit retention. | 1-3 | `make test` |
| `internal/operations/*_test.go` | Guard vs execution failure, retry, re-arm, `needs_attention`, lock order, per-VG serialization, idempotency vectors. | 1-3 | `make test` |
| `internal/agent/server_test.go` | mTLS roles, gateway identity, owner scoping, pagination, redaction, admission limits. | 1-2 | `make test` |
| `internal/agent/admin_service_test.go` | VG registration, de-registration guards, drift actions, wipe, transfer, admin delete, orphan listing. | 1-3 | `make test` |
| `internal/agent/credential_service_test.go` | Owner retrieval, node-grant retrieval, node rotation denial, audit rows. | 1-2 | `make test` |
| `internal/credential/*_test.go` | CHAP and PSK formats, nonce, key fingerprint, missing key, rotation, grant access. | 1-2 | `make test` |
| `internal/csi/*_test.go` | Handle versions, placement lifecycle, node annotations, inventory paths `/v1/info` and `/v1/capacity`, credential grant, mount, secret redaction, resize. | 2-3 | `make test` |
| `internal/backup/*_test.go` | Backup rotation, key, host ID, `restore.json`, restore without the key. | 1 | `make test` |
| `test/integration/agent_test.go` | Stage 1 create, export, login, read, write, delete, restart restore, drift, VG registration, backup/restore; Stage 3 import, wipe, transfer, admin delete, orphan listing. | 1-3 | `make integration` |
| `test/integration/mtls_test.go` | Two-host mTLS, role mapping, gateway identity, server SAN, cross-node denial. | 1 | `make integration` |
| `test/integration/lvm_test.go` | Multi-extent allocation, exact sizes, eligibility, root-on-LVM rejection. | 1 | `make integration` |
| `test/integration/configfs_test.go` | LIO and nvmet object lifecycle, session active/unknown, restore. | 1-2 | `make integration` |
| `test/fault/crash_test.go` | Kill at every journaled step, post-side-effect DB failure, emergency log, resume. | 1 | `make fault` |
| `test/e2e/csi_test.go` | CSI Stage 2 install, iSCSI and NVMe-oF PVs, mount, delete; Stage 3 expansion. | 2-3 | `make e2e` |
| `test/perf/benchmark_test.go` | 1 TiB create/export/delete p99, 100-volume restore. | 3 | `make perf` |
| `test/upgrade/older_binary_test.go` | Older binary refuses newer schema; handle compatibility. | 3 | `make upgrade` |

`make test` uses `go test -tags=unit ./...`; integration, e2e, fault, perf, and upgrade files carry `//go:build integration`, `//go:build e2e`, `//go:build fault`, `//go:build perf`, and `//go:build upgrade`. Coverage uses `go test -coverprofile` statement coverage: at least 80% for `internal/state`, `internal/operations`, `internal/lvm`, `internal/target`, and `internal/csi`, and at least 80% overall. Fault injection uses a build-tag `fault` failpoint interface in `internal/operations/failpoint.go`; the e2e environment is a prepared VM with the agent installed, a DNS SRV record, and the TLS secrets mounted.

The integration and fault suites run on a self-hosted Linux runner labeled `kouja-storage` with root, loop devices, LIO, nvmet, and `ktls-utils`. The e2e suite runs on a kind cluster with the agent VM, DNS SRV record, per-node certificates, and TLS secrets. The performance and upgrade suites run on the reference host with the previous release artifact.

### Target Files to Create/Modify

| Path | Action | Type | Rationale |
| --- | --- | --- | --- |
| `go.mod` | Create | Build | Declares the Go module and the dependency versions above. |
| `go.sum` | Create | Build | Records module checksums; generated by `go mod tidy`. |
| `Makefile` | Create | Build | Single entry point for `generate`, `build`, `test`, `lint`, `proto`, `integration`, `e2e`, `fault`, `perf`, `upgrade`, `coverage`, and `csi-sanity`. |
| `README.md` | Modify | Documentation | Updates the existing design-phase README with the project overview, build steps, agent install, and CSI install. |
| `.gitignore` | Create | Build | Excludes binaries, coverage, generated test artifacts, and local config. |
| `.golangci.yml` | Create | Build | Enables the linters used by CI, including `govet`, `staticcheck`, `errcheck`, and `gosec`. |
| `buf.yaml` | Create | Build | Defines the protobuf module and lint rules. |
| `buf.gen.yaml` | Create | Build | Configures Go, gRPC, and gateway generation into `gen/`. |
| `buf.lock` | Create | Build | Pins the protobuf dependencies used by generation. |
| `.github/workflows/ci.yml` | Create | CI | Stage 1 runs `buf lint`, `buf generate` drift, build, unit tests, lint, coverage, integration, and fault. Stage 2 adds `csi-sanity` and e2e. Stage 3 adds perf and upgrade. |
| `.github/workflows/release.yml` | Create | CI | Builds static agent binaries and CSI container images on tags. |
| `Dockerfile.csi` | Create | Build | Stage 2 multi-stage build that produces a distroless controller image and a Debian-slim node image with the host tools. The agent ships as a systemd binary, not a container. |
| `proto/kouja/v1/common.proto` | Create | Protobuf | Defines shared enums, `Volume`, `Export`, `Capacity`, and `DriftReason`; error details use `google.rpc.ErrorInfo` metadata. |
| `proto/kouja/v1/volume.proto` | Create | Protobuf | Defines `VolumeService` and its request/response messages. |
| `proto/kouja/v1/admin.proto` | Create | Protobuf | Defines `AdminService`, import, VG registration, drift resolution, and wipe messages. |
| `gen/kouja/v1/common.pb.go` | Create | Generated | Generated message types for `common.proto`. |
| `gen/kouja/v1/volume.pb.go` | Create | Generated | Generated message types for `volume.proto`. |
| `gen/kouja/v1/volume_grpc.pb.go` | Create | Generated | Generated gRPC client and server for `VolumeService`. |
| `gen/kouja/v1/volume.pb.gw.go` | Create | Generated | Generated HTTP/JSON gateway for `VolumeService`. |
| `gen/kouja/v1/admin.pb.go` | Create | Generated | Generated message types for `admin.proto`. |
| `gen/kouja/v1/admin_grpc.pb.go` | Create | Generated | Generated gRPC client and server for `AdminService`. |
| `gen/kouja/v1/admin.pb.gw.go` | Create | Generated | Generated HTTP/JSON gateway for `AdminService`. |
| `cmd/kouja-agent/main.go` | Create | Go entry point | Loads configuration, acquires the host `flock`, performs the pre-open managed-LV scan, runs dependency checks, seeds VGs, starts the schedulers and server, and handles graceful shutdown. |
| `internal/version/version.go` | Create | Go package | Holds version, commit, build date, and API version for `GetInfo` and metrics. |
| `internal/config/config.go` | Create | Go package | Parses and validates the YAML configuration, applies defaults, and rejects unsafe values. |
| `internal/config/config_test.go` | Create | Test | Covers defaults, missing fields, invalid TLS paths, invalid portal values, and timeout bounds. |
| `internal/agent/server.go` | Create | Go package | Builds the mTLS listener, gRPC server, in-process HTTP gateway, health endpoints, metrics endpoint, and graceful shutdown. |
| `internal/agent/interceptors.go` | Create | Go package | Adds request IDs, caller identity, logging, panic recovery, and error mapping to gRPC. |
| `internal/agent/interceptors_test.go` | Create | Test | Verifies identity propagation, request IDs, and error mapping. |
| `internal/agent/errors.go` | Create | Go package | Defines the stable reason codes and maps internal errors to gRPC status and `ErrorInfo`. |
| `internal/agent/errors_test.go` | Create | Test | Covers every reason code and its retryable flag. |
| `internal/agent/server_test.go` | Create | Test | Covers mTLS roles, gateway identity propagation, owner scoping, pagination, redaction, and server SAN verification. |
| `internal/agent/admission.go` | Create | Go package | Implements the token-bucket admission controller for concurrency and capacity-query limits. |
| `internal/agent/admission_test.go` | Create | Test | Covers burst, sustained rate, rejection reasons, and `kouja_admission_rejected_total`. |
| `internal/agent/volume_service.go` | Create | Go package | Implements the Stage 1 methods `CreateVolume`, `ExportVolume`, `UnexportVolume`, `DeleteVolume`, owner-scoped `GetVolume` and `ListVolumes`, `GetCapacity`, and `GetInfo`. It delegates credential RPCs to `credential_service.go`. `ResizeVolume` returns `Unimplemented` until Stage 3. |
| `internal/agent/volume_service_test.go` | Create | Test | Exercises ensure semantics, ownership, export conflicts, resize flags, delete force, and capacity responses. |
| `internal/agent/admin_service.go` | Create | Go package | Registers `AdminService`, implements Stage 1 VG registration, de-registration, listing, and drift resolution, and delegates `GrantCredential` and `RevokeGrant` to `grant_service.go`. Stage 3 adds import, orphan listing, wipe, ownership transfer, and admin delete; those methods return `Unimplemented` until then. |
| `internal/agent/admin_service_test.go` | Create | Test | Covers admin authorization, VG registration, de-registration guards, drift actions, wipe, ownership transfer, admin delete, and orphan listing. |
| `internal/agent/grant_service.go` | Create | Go package | Helper invoked by `AdminService` to implement `GrantCredential` and `RevokeGrant` for the controller and admin roles; it is not registered as a separate service. |
| `internal/agent/grant_service_test.go` | Create | Test | Covers controller and admin authorization, grant expiry, and node credential access. |
| `internal/agent/credential_service.go` | Create | Go package | Owns the `GetCredential` and `RotateCredential` registration. `GetCredential` authorizes the owner or a valid node grant; `RotateCredential` is owner-only. Both calls are audited. |
| `internal/agent/credential_service_test.go` | Create | Test | Covers owner access, non-owner `NotFound`, rotation, and audit rows. |
| `internal/agent/validation.go` | Create | Go package | Validates sizes, protocol enums, IQN and NQN syntax, VG names, volume IDs, and force flags. |
| `internal/agent/validation_test.go` | Create | Test | Covers malformed identifiers, zero and negative sizes, and unsupported protocols. |
| `internal/auth/auth.go` | Create | Go package | Verifies mTLS chains, parses URI SANs and issuers, distinguishes caller, controller, node, and admin roles, and puts the identity in the context. |
| `internal/auth/auth_test.go` | Create | Test | Covers valid callers, admin role, missing SAN, wrong CA, and rotation overlap. |
| `internal/state/store.go` | Create | Go package | Opens SQLite with WAL, full synchronous writes, foreign keys, integrity checks, and migrations. |
| `internal/state/types.go` | Create | Go package | Defines `Volume`, `Export`, `Observation`, `ExportObservation`, `Tombstone`, `VG`, `Grant`, `Credential`, `Operation`, `Audit`, and `Page`. |
| `internal/state/migrations.go` | Create | Go package | Embeds forward-only SQL migrations, records the schema version, takes a pre-migration backup, and applies each migration transactionally. |
| `internal/state/migrations/0001_init.sql` | Create | SQL migration | Creates the tables and indexes described in the State model, including grants, tombstones, session state, and the operation intent. |
| `internal/state/volumes.go` | Create | Go package | Volume reads, writes, state transitions, tombstones, and owner-scoped lookup. |
| `internal/state/volumes_test.go` | Create | Test | Covers create, transitions, owner isolation, and drift reads. |
| `internal/state/exports.go` | Create | Go package | Export reads, writes, state transitions, session-state observations, and observation updates. |
| `internal/state/exports_test.go` | Create | Test | Covers export, unexport, conflict reads, and observed session counts. |
| `internal/state/operations.go` | Create | Go package | Operation journal reads, writes, idempotency lookup, attempt updates, and startup scan. |
| `internal/state/operations_test.go` | Create | Test | Covers duplicate request hashes, running operations, succeeded replay, and failed scans. |
| `internal/state/vgs.go` | Create | Go package | Registered VG records, UUID checks, state, and de-registration guards. |
| `internal/state/vgs_test.go` | Create | Test | Covers registration, duplicate names, UUID mismatch, and de-registration refusal. |
| `internal/state/credentials.go` | Create | Go package | Encrypted credential storage with a unique nonce, key fingerprint, active-row uniqueness, supersession, and rotation timestamps. |
| `internal/state/credentials_test.go` | Create | Test | Covers encryption round trip, active uniqueness, supersession, and missing key behavior. |
| `internal/state/grants.go` | Create | Go package | Stores volume-to-node grants, enforces expiry, and supports revocation. |
| `internal/state/grants_test.go` | Create | Test | Covers grant creation, expiry, revocation, and cross-node denial. |
| `internal/state/emergency.go` | Create | Go package | Appends and reads the fsynced emergency log used when a database write fails after a side effect. |
| `internal/state/emergency_test.go` | Create | Test | Covers append durability, restart parsing, and mapping to `drift`. |
| `internal/state/audit.go` | Create | Go package | Append-only audit inserts, filtering, and retention pruning. |
| `internal/state/audit_test.go` | Create | Test | Covers audit fields, retention, and secret redaction. |
| `internal/state/store_test.go` | Create | Test | Covers integrity check failure, WAL settings, schema refusal, and restore behavior. |
| `internal/state/migrations_test.go` | Create | Test | Covers migration application, version ordering, and newer-schema refusal; the older-binary case is in `test/upgrade`. |
| `internal/backup/backup.go` | Create | Go package | Runs `VACUUM INTO` on a schedule, writes the database, credential key, host ID, and `restore.json` to an operator-mounted off-host directory with atomic rename, validates the target, rotates 15-minute, daily, and weekly backups, and exposes success and failure metrics. |
| `internal/backup/backup_test.go` | Create | Test | Covers backup rotation, integrity of the copy, the credential key, host ID, `restore.json`, restore without the key, and restore into a temporary directory. |
| `internal/hostid/hostid.go` | Create | Go package | Loads or creates `/var/lib/kouja/host-id`, compares it with SQLite, reads the kernel boot ID, and fails closed on a mismatch with existing volumes. |
| `internal/hostid/hostid_test.go` | Create | Test | Covers first start, existing file, missing file with volumes, and boot-ID change detection. |
| `internal/lvm/lvm.go` | Create | Go package | Runs `vgs`, `lvs`, `pvs`, `lvcreate`, `lvextend`, `lvreduce`, `lvremove`, and `lvchange` with argument arrays and JSON parsing; owns `udevadm settle` and device polling. |
| `internal/lvm/types.go` | Create | Go package | Defines `LV`, `Capacity`, and VG attribute types. |
| `internal/lvm/lvm_test.go` | Create | Test | Uses a fake command runner and JSON fixtures to cover success, failure, parse errors, and exact byte sizes. |
| `internal/lvm/eligibility.go` | Create | Go package | Rejects shared or clustered VGs, thin pools, partial VGs, VGs that back the root or OS disk, and VGs containing the data or backup directories. |
| `internal/lvm/eligibility_test.go` | Create | Test | Covers each rejection rule and the happy path. |
| `internal/target/configfs.go` | Create | Go package | Provides safe mkdir, attribute write, symlink, read, and remove helpers with errno classification and path validation. |
| `internal/target/configfs_test.go` | Create | Test | Uses a fake configfs root to cover ordering, missing attributes, and cleanup. |
| `internal/target/export.go` | Create | Go package | Defines the shared `Exporter` interface and the protocol-neutral `ExportSpec`, `ExportResult`, and `SessionState` types. |
| `internal/target/iscsi.go` | Create | Go package | Implements the per-volume LIO target, backstore, LUN, ACL, portal, CHAP, the authoritative session count, restore, and teardown. |
| `internal/target/iscsi_test.go` | Create | Test | Covers create, adopt, restore, teardown, and CHAP attribute writes. |
| `internal/target/nvmet.go` | Create | Go package | Implements the per-volume nvmet subsystem, namespace, allowed host, port link, TLS-PSK attributes, the session capability probe, restore, and teardown. |
| `internal/target/nvmet_test.go` | Create | Test | Covers create, adopt, restore, teardown, and TLS port attributes. |
| `internal/target/session.go` | Create | Go package | Defines the session observer interface, the authoritative LIO per-target count, and the nvmet capability probe with the `SESSION_STATE_UNKNOWN` fallback. |
| `internal/target/session_test.go` | Create | Test | Covers no sessions, active sessions, and the `SESSION_STATE_UNKNOWN` fallback. |
| `internal/credential/credential.go` | Create | Go package | Generates CHAP secrets and NVMe TLS PSKs, encrypts them with AES-256-GCM, and decrypts for retrieval. |
| `internal/credential/types.go` | Create | Go package | Defines `Credential`, `Protocol`, and key metadata. |
| `internal/credential/credential_test.go` | Create | Test | Covers format, length, encryption, decryption, wrong key, and redaction. |
| `internal/credential/keyring.go` | Create | Go package | Inserts and revokes `.nvme` keyring entries through `golang.org/x/sys/unix` and verifies `tlshd` via its systemd unit state. |
| `internal/credential/keyring_test.go` | Create | Test | Uses an interface fake and a keyring integration test gated on root. |
| `internal/credential/key.go` | Create | Go package | Loads, creates, fingerprints, and fails closed on the AES-GCM credential key; owns the nonce and key version. |
| `internal/credential/key_test.go` | Create | Test | Covers first start, existing key, missing key with credentials, fingerprint mismatch, and nonce uniqueness. |
| `internal/operations/manager.go` | Create | Go package | Implements the per-volume and per-VG lock maps, the intent journal, failure classes, retry and re-arm logic, ensure semantics, and emergency logging. |
| `internal/operations/types.go` | Create | Go package | Defines `Action` constants, `Options`, `Request`, `Result`, `Failpoints`, `AuditWriter`, and `Clock`. |
| `internal/operations/manager_test.go` | Create | Test | Covers concurrent calls, idempotent replay, retry exhaustion, crash-resume behavior, `needs_attention` routing, and re-arm. |
| `internal/operations/failpoint.go` | Create | Go package | Defines the build-tag `fault` failpoint hooks used by the crash-injection suite. |
| `internal/operations/failpoint_stub.go` | Create | Go package | Provides the no-op failpoint implementation for normal builds. |
| `internal/operations/create.go` | Create | Go package | Implements the create path: LV, wait, optional export, state updates. |
| `internal/operations/create_test.go` | Create | Test | Covers unexported create, create with export, existing volume, and LV already present. |
| `internal/operations/export.go` | Create | Go package | Implements export and unexport, including same-initiator idempotency without rotation, different-initiator rejection, rotation after unexport, and session checks. |
| `internal/operations/export_test.go` | Create | Test | Covers export, re-export, active session refusal, force unexport, and target restore. |
| `internal/operations/resize.go` | Create | Go package | Stage 3. Implements grow and shrink, `allow_shrink`, the unexported-shrink precondition, target updates, and `rescan_required` or `reconnect_required`. |
| `internal/operations/resize_test.go` | Create | Test | Covers grow, shrink refusal without the flag, same-size idempotency, and target re-export when live resize is unsupported. |
| `internal/operations/delete.go` | Create | Go package | Implements delete, active-session refusal, force teardown, LV removal, and idempotent absent handling. |
| `internal/operations/delete_test.go` | Create | Test | Covers normal delete, session refusal, force delete, already absent, and drift refusal. |
| `internal/operations/wipe.go` | Create | Go package | Stage 3. Implements the explicit wipe with `blkdiscard` or zeroing, the exported-state guard, and the size-aware deadline. |
| `internal/operations/wipe_test.go` | Create | Test | Covers discard, zero fallback, refusal while exported, and timeout. |
| `internal/operations/import.go` | Create | Go package | Stage 3. Validates and adopts existing LVs and exports without renaming the target; stores legacy names in `legacy_identity`. |
| `internal/operations/import_test.go` | Create | Test | Covers valid import, LV UUID mismatch, conflicting owner, target mismatch, and active-session import. |
| `internal/reconcile/reconcile.go` | Create | Go package | Runs startup and periodic observation, activates LVs, waits for devices, reinstalls PSKs, uses the boot ID from `internal/hostid` to apply the rule, and writes drift reasons. |
| `internal/reconcile/reconcile_test.go` | Create | Test | Covers host reboot restore, process restart with missing export, partial restore, and drift classification. |
| `internal/reconcile/bootid.go` | Create | Go package | Consumes the boot ID from `internal/hostid` and applies the reconciliation policy; it does not read or store the boot ID itself. |
| `internal/capacity/capacity.go` | Create | Go package | Computes total, free, allocatable, and maximum volume size from LVM at request time, with headroom. |
| `internal/capacity/capacity_test.go` | Create | Test | Covers headroom, extent rounding, fragmentation, and allocatable clamping. |
| `internal/observability/log.go` | Create | Go package | Configures `log/slog` JSON output with service fields and redaction. |
| `internal/observability/log_test.go` | Create | Test | Covers required fields and redaction. |
| `internal/observability/metrics.go` | Create | Go package | Registers and updates the Prometheus metrics listed in Proposed Architecture. |
| `internal/observability/metrics_test.go` | Create | Test | Covers metric registration and label cardinality. |
| `internal/observability/health.go` | Create | Go package | Implements `/healthz`, `/readyz`, and the readiness state machine. |
| `internal/observability/health_test.go` | Create | Test | Covers startup not-ready, ready after reconcile, and degraded-with-drift readiness. |
| `internal/observability/redact.go` | Create | Go package | Removes secrets and certificate material from logs and errors. |
| `internal/observability/redact_test.go` | Create | Test | Covers CHAP, TLS-PSK, and authorization header redaction. |
| `cmd/kouja-csi-controller/main.go` | Create | Go entry point | Stage 2. Starts the CSI controller service on `/csi/csi.sock` for the sidecars. |
| `cmd/kouja-csi-node/main.go` | Create | Go entry point | Stage 2. Starts the CSI node service on `/csi/csi.sock` and publishes the node's initiator and caller-ID annotations. |
| `internal/csi/controller.go` | Create | Go package | Implements CSI Identity and Controller methods, capability reporting, placement records, grants, and error mapping. |
| `internal/csi/controller_test.go` | Create | Test | Covers CreateVolume idempotency, publish, unpublish, expand, delete, and capacity. |
| `internal/csi/node.go` | Create | Go package | Implements NodeStage, NodePublish, NodeUnpublish, NodeUnstage, NodeExpand, and NodeGetInfo with the node certificate, control endpoint, and volume grant. |
| `internal/csi/node_test.go` | Create | Test | Covers stage, mount, unstage, expand, and missing `PublishContext` fields. |
| `internal/csi/client.go` | Create | Go package | Creates the mTLS kouja client for controller and node identities, maps errors, and implements the calls used by both plugins. |
| `internal/csi/types.go` | Create | Go package | Defines `Host`, `Capacity`, `SelectRequest`, `PlacementRecord`, `ConnectSpec`, and `Device`. |
| `internal/csi/client_test.go` | Create | Test | Covers TLS setup, error mapping, and retries. |
| `internal/csi/inventory.go` | Create | Go package | Resolves SRV records, queries `/v1/info` and `/v1/capacity`, caches for 10 seconds, invalidates on unreachable hosts and `ResourceExhausted`, and selects hosts by capacity. |
| `internal/csi/inventory_test.go` | Create | Test | Covers SRV resolution, unreachable hosts, capacity filtering, tie-breaking, and fallback. |
| `internal/csi/placement.go` | Create | Go package | Persists and manages the `KoujaVolumePlacement` CRD lifecycle: pending, bound, failed, delete, and garbage collection. |
| `internal/csi/placement_types.go` | Create | Generated | Generated typed client and informer for the `kouja.io/v1alpha1` `KoujaVolumePlacement` CRD. |
| `api/kouja.io/v1alpha1/types.go` | Create | Go package | Defines the `KoujaVolumePlacement` spec and status types; `controller-gen` generates deepcopy, CRD schema, clientset, and informers. |
| `internal/csi/placement_test.go` | Create | Test | Covers pending, bound, failed, delete, garbage collection, unreachable host, and duplicate prevention. |
| `internal/csi/credential.go` | Create | Go package | Fetches credentials with the node certificate and a volume grant; CHAP goes through a 0600 file and TLS keys through the keyring. |
| `internal/csi/credential_test.go` | Create | Test | Covers owner retrieval, node grant retrieval, node rotation denial, missing grant, and secret redaction. |
| `internal/csi/initiator.go` | Create | Go package | Reads and writes the `CSINode` IQN, NQN, and `kouja.csi/node-caller-id` annotations and validates the host initiator identity. |
| `internal/csi/initiator_test.go` | Create | Test | Covers annotation creation, missing annotation, and mismatch handling. |
| `internal/csi/mount.go` | Create | Go package | Runs `iscsiadm` and `nvme` login/logout, passes CHAP through a 0600 file and TLS keys through the keyring, waits for devices, formats when requested, mounts, rescans, and grows filesystems. |
| `internal/csi/mount_test.go` | Create | Test | Uses a command runner fake to cover login, logout, rescan, ext4, and XFS paths; asserts CHAP uses a 0600 file and no secret appears in argv. |
| `internal/csi/handle.go` | Create | Go package | Parses and formats versioned volume handles and rejects malformed or unknown versions. |
| `internal/csi/handle_test.go` | Create | Test | Covers current and future handle versions, length limits, and malformed values. |
| `internal/csi/metrics.go` | Create | Go package | Exposes CSI operation metrics and kouja client latency. |
| `deploy/systemd/kouja-agent.service` | Create | Deployment | Runs the agent as root with the config path, restart policy, and dependency ordering for LVM and configfs. |
| `deploy/systemd/kouja-agent.conf` | Create | Deployment | Installed as `/etc/tmpfiles.d/kouja-agent.conf`; creates `/var/lib/kouja` 0700 root:root and `/var/lib/kouja/backups` 0700 root:root. |
| `deploy/install/install.sh` | Create | Deployment | Installs the agent, disables `target.service` and `targetcli saveconfig`, creates directories, installs firewall rules, and documents uninstall. |
| `deploy/kubernetes/namespace.yaml` | Create | Deployment | Creates the `kouja-system` namespace. |
| `deploy/kubernetes/csidriver.yaml` | Create | Deployment | Registers the driver name, `attachRequired: true`, `podInfoOnMount: true`, `volumeLifecycleModes: [Persistent]`, and `fsGroupPolicy: None`. |
| `deploy/kubernetes/crd-koujavolumeplacement.yaml` | Create | Deployment | Defines `kouja.io/v1alpha1` `KoujaVolumePlacement`, cluster-scoped, with the status subresource, validation schema, and printer columns. |
| `deploy/kubernetes/rbac-controller.yaml` | Create | Deployment | Grants the controller access to PVs, PVCs, VolumeAttachments, CSINodes, StorageClasses, Events, Leases, and the placement CRD. |
| `deploy/kubernetes/rbac-node.yaml` | Create | Deployment | Grants the node plugin `get` and `patch` on CSINodes and `create` on Events. |
| `deploy/kubernetes/controller-deployment.yaml` | Create | Deployment | Deploys the controller with a service account, provisioner, attacher, and resizer sidecars, socket `/csi/csi.sock`, leader-election IDs, probes, and the client TLS secret. |
| `deploy/kubernetes/node-daemonset.yaml` | Create | Deployment | Deploys the node plugin as a privileged host-network DaemonSet with the node driver registrar, host paths, node client certificate, CA, control endpoint, and control-plane tolerations. |
| `deploy/kubernetes/storageclass-iscsi.yaml` | Create | Deployment | Example StorageClass with the SRV record, protocol `iscsi`, required VG, `allowVolumeExpansion: true`, `reclaimPolicy: Delete`, and `volumeBindingMode: Immediate`. |
| `deploy/kubernetes/storageclass-nvme-tcp.yaml` | Create | Deployment | Example StorageClass with the SRV record, protocol `nvme-tcp`, required VG, `allowVolumeExpansion: true`, `reclaimPolicy: Delete`, and `volumeBindingMode: Immediate`. |
| `deploy/kubernetes/secret-csi-client-tls.example.yaml` | Create | Deployment | Template for the CSI controller and node certificates, keys, and CA; the real secrets are supplied by the operator. |
| `deploy/kubernetes/kustomization.yaml` | Create | Deployment | Groups the namespace, CSIDriver, placement CRD, RBAC, controller, node, and example StorageClasses. |
| `docs/api.md` | Create | Documentation | Documents the gRPC and HTTP methods, request fields, error reasons, and example calls. |
| `docs/csi.md` | Create | Documentation | Documents CSI installation, StorageClass parameters, node prerequisites, and initiator annotations. |
| `docs/operations.md` | Create | Documentation | Documents the installer, systemd, configuration, TLS, portals, backups to an off-host target, metrics, and upgrades. |
| `docs/recovery.md` | Create | Documentation | Documents database restore, the emergency log, off-host backup, drift resolution, import, host reimage, and forced cleanup. |
| `docs/security.md` | Create | Documentation | Documents mTLS roles, node identities, grants, CHAP, TLS-PSK, keyring setup, secret storage, and certificate rotation. |
| `docs/kernel.md` | Create | Documentation | Documents the kernel config, modules, `ktls-utils`, the nvmet session-visibility requirement and capability-probe thresholds, LIO, nvmet, and supported versions. |
| `docs/decisions/target-manager.md` | Create | Documentation | Records the five-day spike result comparing direct configfs, `targetd`/`rtslib`, and LINSTOR. |
| `docs/designs/kouja.md` | Reference | Documentation | This document is the review target, not an implementation deliverable. |
| `internal/testutil/certs.go` | Create | Test infrastructure | Generates a test CA and client/server certificates for auth and mTLS tests. |
| `internal/testutil/configfs.go` | Create | Test infrastructure | Provides a temporary fake configfs tree for target adapter tests. |
| `internal/testutil/lvm.go` | Create | Test infrastructure | Provides a fake command runner and JSON fixtures for LVM tests. |
| `internal/testutil/sqlite.go` | Create | Test infrastructure | Opens a temporary SQLite database with migrations for state and operation tests. |
| `test/integration/lvm_test.go` | Create | Integration test | Creates a loopback VG and exercises create, remove, eligibility, exact sizes, and multi-extent allocation in Stage 1. Grow and shrink are Stage 3. Requires root and skips otherwise. |
| `test/integration/configfs_test.go` | Create | Integration test | Exercises LIO and nvmet object creation, session observation, restore, and teardown on a host with the modules. Requires root and skips otherwise. |
| `test/integration/agent_test.go` | Create | Integration test | Exercises the Stage 1 agent API end to end: create, export, login, read, write, delete, restart restore, drift, VG registration, and backup/restore. Stage 3 adds import, wipe, transfer, admin delete, and orphan listing. |
| `test/integration/mtls_test.go` | Create | Integration test | Exercises the two-host mTLS smoke test, role mapping, gateway identity propagation, server SAN verification, and cross-node denial. |
| `test/integration/README.md` | Create | Documentation | Explains how to prepare a disposable VM with loop devices, LIO, nvmet, `ktls-utils`, and the test certificates. |
| `test/e2e/csi_test.go` | Create | E2E test | Creates a kind cluster and installs the driver in Stage 2; provisions and mounts an iSCSI PV, then an NVMe-oF PV; expands and deletes in Stage 3. |
| `test/e2e/manifests/iscsi-storageclass.yaml` | Create | E2E fixture | StorageClass and PVC used by the iSCSI end-to-end case. |
| `test/e2e/manifests/nvme-storageclass.yaml` | Create | E2E fixture | StorageClass and PVC used by the NVMe-oF end-to-end case. |
| `test/e2e/manifests/test-pod.yaml` | Create | E2E fixture | Pod that writes, reads, and verifies the mounted volume. |
| `test/fault/crash_test.go` | Create | Fault injection | Kills the agent at each operation step through the `fault` failpoint hooks and verifies restart recovery, idempotency, and drift classification. |
| `test/perf/benchmark_test.go` | Create | Performance test | Measures 1 TiB create, export, and delete p99 and 100-volume restore readiness on the reference host. |
| `test/upgrade/older_binary_test.go` | Create | Upgrade test | Runs the previous release binary against the current schema and verifies refusal and handle compatibility. |
| `test/csi/sanity_test.go` | Create | Conformance test | Runs `csi-sanity` against the controller and node with the advertised capabilities; built only with the `csi_sanity` tag. |

### Verification Plan

* **Compilation/Build**: `make build` produces the Stage 1 `kouja-agent` with `CGO_ENABLED=0` and verifies static linkage with `file` or `ldd`. Stage 2 adds `kouja-csi-controller` and `kouja-csi-node`.
* **Protobuf**: `buf lint` passes, `buf generate` produces no diff, and `buf breaking` uses the committed Stage 1 baseline. It is enforced from the first release tag.
* **Unit Tests**: `make test` runs `go test -tags=unit ./...`. Table-driven tests cover state transitions, ensure semantics, error mapping, LVM parsing, configfs ordering, credential formats, grant expiry, placement records, admission, and CSI handlers.
* **Static Analysis & Linting**: `go vet ./...` and `golangci-lint run` pass with the configured linters.
* **Test Coverage**: `make coverage` uses `go test -coverprofile` statement coverage. The gate is at least 80% for `internal/state`, `internal/operations`, `internal/lvm`, `internal/target`, and `internal/csi`, and at least 80% overall.
* **Stage 1 Integration**: `make integration` runs on a disposable root VM with loop devices, LIO, and `ktls-utils`. It covers iSCSI create, export, login, read, write, delete, restart restore, drift, VG registration, and backup/restore. Import, wipe, grow, and shrink are Stage 3.
* **Stage 2 Integration and Conformance**: `make integration` adds NVMe-oF/TLS-PSK. `make csi-sanity` and `make e2e` pass for the advertised capabilities. The e2e test provisions, mounts, and deletes an iSCSI PV in stage 1 of the CSI work and an NVMe-oF PV in stage 2.
* **Failure Injection**: `make fault` runs the `fault` build-tag failpoints and kills the agent after each journaled step. Every case must either complete on restart or land in a deterministic `failed` or `drift` state with no duplicate LV, target, namespace, or credential. A post-side-effect database failure must produce an emergency-log record and a `drift` state.
* **Security**: Tests cover valid and invalid mTLS certificates, caller, controller, node, and admin roles, non-owner `NotFound`, cross-node credential denial without a grant, CHAP with the wrong secret, a second initiator rejection, TLS-PSK with the wrong key, gateway identity propagation, and server SAN host verification.
* **Capacity**: Tests cover the allocatable clamp, maximum-volume-size calculation as `max` across hosts, per-VG serialization, admission rejection, and a multi-extent LVM allocation that must succeed.
* **Host Reboot**: Tests cover LV activation, device wait, export restore, NVMe PSK keyring reinstallation, `tlshd` readiness, and a host-ID or key mismatch that must fail closed.
* **Stage 3 Performance**: `make perf` runs on the reference host. 99% of create, export, and delete calls complete within 5 seconds for volumes up to 1 TiB, excluding an explicit wipe. A restart with 100 exported volumes reaches readiness within 60 seconds using bounded parallel restore.
* **Backup and Restore**: Tests cover a missing database, a corrupt database, a stale backup, a restore without the credential key, a restore of the key, host ID, and `restore.json` from the off-host target, and reconciliation after restore. A missing database with existing `kouja-*` LVs must fail closed.
* **Upgrade**: `make upgrade` in Stage 3 runs the previous release binary against the current schema and confirms it refuses to start. The current binary must read every volume handle version it has emitted.
* **Definition of Done**: The agent and CSI driver pass the integration, conformance, failure, security, performance, and upgrade suites; the systemd unit, installer, and Kubernetes manifests install cleanly; and the API, CSI, operations, recovery, security, and kernel documents match the implementation.

---




