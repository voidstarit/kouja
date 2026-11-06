# kouja

[![CI](https://github.com/voidstarit/kouja/actions/workflows/ci.yml/badge.svg)](https://github.com/voidstarit/kouja/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.26%2B-00ADD8?logo=go)](https://go.dev/)
[![License: Dual Apache-2.0 / Commercial](https://img.shields.io/badge/license-Apache--2.0%20%2F%20Commercial-blue.svg)](#license)

**kouja is a per-host block-storage agent for Linux.** It creates thick LVM
logical volumes and exports them to one initiator over iSCSI with CHAP or
NVMe-oF/TCP with TLS 1.3 PSK. It exposes an mTLS gRPC and HTTP/JSON API and
includes a Kubernetes CSI driver.

kouja is deliberately small in scope: there is no cluster membership, no
peer-to-peer coordination, and no scheduler inside the agent. The caller owns
placement and decides which host should receive a volume. Each storage host
runs its own `kouja-agent`, its own SQLite database, and its own target
configuration.

> **Status:** early public preview (Stage 2). The data paths, CSI driver,
> placement records, credential grants, and online expansion are implemented
> and tested, but the API and deployment manifests may still change.

## Why kouja?

A storage host needs four things that are easy to get subtly wrong:

1. Allocate a real block device and remember who owns it.
2. Configure LIO or nvmet exactly once, including ACLs, LUNs, portals, and TLS.
3. Recover that state after a reboot without silently changing ownership.
4. Hand a caller a credential without putting the secret everywhere.

kouja treats the SQLite database as the authority for intent and ownership. The
kernel target state is observed and reported as `drift` when it disagrees; kouja
does not silently repair a disagreement. Changes are journaled, idempotent, and
serialized per volume and per volume group.

## What it does

| Capability | Behavior |
|---|---|
| Storage | Thick LVM logical volumes in administrator-registered volume groups. |
| iSCSI | LIO target with CHAP, one initiator, explicit ACLs, and session checks. |
| NVMe-oF | nvmet subsystem over TCP with TLS 1.3 PSK and one host NQN. |
| Control API | gRPC and generated HTTP/JSON on one mTLS listener. |
| State | SQLite with WAL, foreign keys, full synchronous writes, and backups. |
| Credentials | AES-256-GCM encrypted at rest; per-attachment rotation; node grants. |
| Recovery | Startup and periodic reconciliation, drift reporting, and admin actions. |
| Kubernetes | CSI controller and node plugin for provisioning, attach/detach, mount, expansion, and cleanup. |
| Observability | `/healthz`, `/readyz`, Prometheus metrics, structured logs, and audit rows. |

Not supported by v1: thin provisioning, snapshots, clones, replication,
multi-initiator/RWX volumes, automatic high availability, or encryption of data
at rest. A host is a single point of failure for the volumes on it.

## Architecture

```text
                        mTLS control API
  caller (VM control plane)  ──────────────────┐
  Kubernetes CSI    ──────────────────────────┤
                                               ▼
                                      ┌─────────────────┐
                                      │   kouja-agent   │
                                      │  (root, systemd)│
                                      └────────┬────────┘
                                               │
                 ┌─────────────────────────────┼───────────────────────────┐
                 ▼                             ▼                           ▼
          SQLite state                    LVM backend                 Target adapters
     intent, ownership, grants      thick LVs, capacity       LIO / nvmet configfs
                 │
                 ▼
          AES-GCM secret key

  iSCSI:     initiator  ── CHAP ──────────────► LIO portal :3260
  NVMe-oF:   initiator  ── TLS 1.3 PSK ───────► nvmet portal :4420
```

The CSI driver is deliberately outside the storage host. The controller selects
an agent from a static host list or DNS SRV record, records the placement in a
cluster-scoped `KoujaVolumePlacement` resource, and grants the scheduled node
access to the volume credential. The node plugin authenticates to the agent and
performs the iSCSI login or NVMe/TCP TLS connect.

## Requirements

### Storage host

The reference host is **Debian 13 with Linux 6.12**. Debian 12/Linux 6.1 is not
supported for the TLS-required NVMe path.

- `lvm2`
- kernel modules: `target_core_mod`, `target_core_iblock`,
  `iscsi_target_mod`, `nvmet`, `nvmet_tcp`
- `CONFIG_NVME_TARGET_TCP_TLS=y` for NVMe/TCP
- `ktls-utils` with `tlshd` active for NVMe/TCP
- `nvme-cli` for PSK generation and validation
- configfs mounted at `/sys/kernel/config`
- one or more LVM volume groups dedicated to kouja; do not point kouja at the root VG
- a server certificate, server private key, and client CA for mTLS

### Initiator nodes

- iSCSI: `open-iscsi`/`iscsid` and the `iscsi_tcp` module
- NVMe/TCP: `nvme-cli`, `ktls-utils`, an active `tlshd`, and the
  `nvme_tcp`/`nvme_fabrics` modules
- a client certificate trusted by the agent's client CA

`tlshd` is required on **both** the target and the initiator for NVMe/TCP.
Without it the kernel reports `failed to start TLS: -3` even when the PSK is
correct.

See [Kernel and Host Support](docs/kernel.md) for the tested matrix and
configfs details.

## Build from source

Go 1.26 or newer is required.

```sh
git clone https://github.com/voidstarit/kouja.git
cd kouja

make build              # compile all packages
make test               # unit tests
make test-bdd           # Ginkgo/Gomega BDD suite
make coverage-check     # enforce >= 80% coverage under ./internal
make lint               # go vet + golangci-lint
```

All unit test packages now run through [Ginkgo](https://onsi.github.io/ginkgo/)
suites; the existing case functions execute through Ginkgo's `GinkgoTB` adapter.
The BDD-style suite in `test/bdd` uses [Gomega](https://onsi.github.io/gomega/)
assertions to exercise the public volume lifecycle: create/export,
active-session refusal, credential rotation, and delete. GoMock is not required
yet because the core interfaces are covered by focused in-package test doubles.

`make tools` installs the optional protobuf, CRD, and lint tools. Add
`$(go env GOPATH)/bin` to `PATH` before running `make lint` or `make proto`.

To create standalone binaries explicitly:

```sh
mkdir -p bin
CGO_ENABLED=0 go build -trimpath -o bin/kouja-agent ./cmd/kouja-agent
CGO_ENABLED=0 go build -trimpath -o bin/kouja-csi-controller ./cmd/kouja-csi-controller
CGO_ENABLED=0 go build -trimpath -o bin/kouja-csi-node ./cmd/kouja-csi-node
```

Protocol generation and CRD generation require the tools installed by
`make tools`, followed by `make proto` and `make generate`.

## Install the agent

### 1. Prepare the host

Create or choose an LVM volume group, and load the target stack:

```sh
sudo vgcreate kouja /dev/disk/by-id/<your-disk>
sudo mountpoint -q /sys/kernel/config || sudo mount -t configfs none /sys/kernel/config

sudo modprobe target_core_mod
sudo modprobe target_core_iblock
sudo modprobe iscsi_target_mod
sudo modprobe nvmet
sudo modprobe nvmet_tcp

sudo systemctl enable --now tlshd
```

Make sure `/etc/tlshd.conf` includes the `.nvme` keyring:

```ini
[authenticate]
keyrings=.nvme
```

### 2. Install TLS material

The agent serves TLS 1.3 mTLS. Install the files before running the installer:

```text
/etc/kouja/tls/server.crt       # server certificate
/etc/kouja/tls/server.key       # server private key
/etc/kouja/tls/client-ca.crt    # CA used to verify API client certificates
```

The directory should be `0700` root-owned and the files `0600`. Client
certificate URI SANs determine roles:

- `spiffe://kouja/admin/<name>` — administrator
- `spiffe://kouja/caller/<name>` — volume caller
- `spiffe://kouja/controller/<name>` — CSI controller
- `spiffe://kouja/node/<name>` — CSI node

### 3. Run the installer

From the repository root:

```sh
sudo ./deploy/install/install.sh bin/kouja-agent
```

The installer:

- installs the binary at `/usr/local/bin/kouja-agent`;
- installs the systemd unit and tmpfiles configuration;
- creates `/etc/kouja`, `/var/lib/kouja`, and the backup directory;
- creates a starter `/etc/kouja/config.yaml` if one does not exist;
- disables the legacy `target.service`;
- enables and starts `kouja-agent.service`.

The installer does **not** create certificates or register volume groups. Edit
the configuration and restart:

```sh
sudoedit /etc/kouja/config.yaml
sudo systemctl restart kouja-agent
sudo journalctl -u kouja-agent -f
```

A minimal iSCSI configuration:

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
  wipe_timeout_seconds: 1800
  vgs: [kouja]

targets:
  iscsi:
    enabled: true
    iqn_prefix: iqn.2026-11.io.kouja
    portals:
      - address: 0.0.0.0
        port: 3260

operations:
  max_attempts: 3
  total_timeout_seconds: 300

admission:
  max_concurrent_operations: 64
  per_caller_operations: 10
  capacity_requests_per_second: 10

state:
  backup_dir: /var/lib/kouja/backups
  backup_target: ""
```

For NVMe/TCP, add:

```yaml
targets:
  iscsi:
    enabled: true
    iqn_prefix: iqn.2026-11.io.kouja
    portals:
      - address: 0.0.0.0
        port: 3260
  nvme_tcp:
    enabled: true
    nqn_prefix: nqn.2026-11.io.kouja
    portals:
      - address: 0.0.0.0
        port: 4420
    tls:
      enabled: true
      keyring: .nvme
      tlshd_service: tlshd
```

The VGs listed under `storage.vgs` are registered on startup. An administrator
can also register one through the admin API.

### Multiple storage classes on one host

A single agent can serve several LVM backends. Map a stable class name to each
volume group, for example the NVMe, SSD, and HDD pools on one physical machine:

```yaml
storage:
  headroom_percent: 5
  headroom_min_bytes: 1073741824
  wipe_timeout_seconds: 1800
  classes:
    nvme: kouja-nvme
    ssd: kouja-ssd
    hdd: kouja-hdd
```

The VGs referenced by `classes` are added to the first-start registration set.

```sh
sudo vgcreate kouja-nvme /dev/disk/by-id/<nvme-device>
sudo vgcreate kouja-ssd  /dev/disk/by-id/<ssd-device>
sudo vgcreate kouja-hdd  /dev/disk/by-id/<hdd-device>
```

Callers select a class in the API with `storage_class`, or in Kubernetes with
the StorageClass parameter `class`. The agent resolves the class to its VG and
reports the class on capacity and volume responses. A raw `vg` parameter still
works for existing callers; when no class map is configured, the VG name is
used as its own class. The VG remains the durable backend identity; the class
is the selection and presentation label.

## API quick start

All API calls require a client certificate trusted by the agent. A caller with
the `admin` role can create and inspect volumes.

Check readiness and capacity:

```sh
CURL=(curl --http2 --cacert ca.crt --cert admin.crt --key admin.key)

"${CURL[@]}" https://storage.example:9443/healthz
"${CURL[@]}" https://storage.example:9443/v1/info
"${CURL[@]}" https://storage.example:9443/v1/capacity
```

Create and export a 1 GiB iSCSI volume:

```sh
"${CURL[@]}" -X POST \
  -H 'Content-Type: application/json' \
  -d '{
    "volumeId": "example-1",
    "sizeBytes": 1073741824,
    "vg": "kouja",
    "export": true,
    "protocol": "PROTOCOL_ISCSI",
    "initiator": "iqn.2026-11.io.example:consumer",
    "wipe": false
  }' \
  "https://storage.example:9443/v1/volumes"
```

For NVMe/TCP, use `"protocol": "PROTOCOL_NVME_TCP"` and an NQN as
`initiator`.

Fetch the connection information and current credential:

```sh
"${CURL[@]}" "https://storage.example:9443/v1/volumes/example-1"
"${CURL[@]}" "https://storage.example:9443/v1/volumes/example-1/credential"
```

On the consumer, configure CHAP and log in to the returned target and portal.
For NVMe/TCP, install the PSK and connect with `nvme connect --tls`; the
end-to-end record in [docs/e2e.md](docs/e2e.md) shows the exact handshake and
commands.

Other useful endpoints:

```text
POST   /v1/volumes/{id}:export
POST   /v1/volumes/{id}:unexport
POST   /v1/volumes/{id}:resize
POST   /v1/volumes/{id}:rotate-credential
DELETE /v1/volumes/{id}
GET    /v1/volumes
GET    /v1/volumes/{id}/credential
POST   /v1/admin/vgs
GET    /v1/admin/vgs
POST   /v1/admin/drift/{id}:resolve
```

The protobuf contract is in [`proto/kouja/v1`](proto/kouja/v1).

## Kubernetes CSI

The in-project CSI driver name is `kouja.csi.voidstarit.com`. It provides:

- dynamic provisioning and deletion;
- controller attach/detach;
- iSCSI or NVMe/TCP node connection and mounting;
- durable `KoujaVolumePlacement` records;
- static host lists or DNS SRV discovery;
- per-node credential grants;
- online expansion with the external-resizer;
- `nodeFailurePolicy: wait` or `force` for node failures.

### Build the CSI images

The Dockerfiles expect the corresponding binary in their build context:

```sh
mkdir -p build/csi-controller build/csi-node

CGO_ENABLED=0 go build -trimpath -o build/csi-controller/kouja-csi-controller ./cmd/kouja-csi-controller
CGO_ENABLED=0 go build -trimpath -o build/csi-node/kouja-csi-node ./cmd/kouja-csi-node

cp deploy/kubernetes/csi/Dockerfile.controller build/csi-controller/Dockerfile
cp deploy/kubernetes/csi/Dockerfile.node build/csi-node/Dockerfile
cp deploy/kubernetes/csi/entrypoint-node.sh build/csi-node/

docker build -t kouja/csi-controller:latest build/csi-controller
docker build -t kouja/csi-node:latest build/csi-node
```

Push the images to a registry reachable by the cluster, or load them into the
node runtime.

### Prepare Kubernetes secrets

Create the namespace, the placement CRD, and two TLS Secrets. The controller
Secret contains the controller client certificate. Each node Secret contains a
node certificate whose URI SAN is `spiffe://kouja/node/<node-name>`.

```sh
kubectl apply -f deploy/kubernetes/crd-koujavolumeplacement.yaml
kubectl create namespace kouja-csi

kubectl -n kouja-csi create secret generic kouja-csi-tls \
  --from-file=ca.crt=controller-ca.crt \
  --from-file=tls.crt=controller.crt \
  --from-file=tls.key=controller.key

kubectl -n kouja-csi create secret generic kouja-csi-node-tls \
  --from-file=ca.crt=node-ca.crt \
  --from-file=tls.crt=node.crt \
  --from-file=tls.key=node.key
```

The sample manifest uses one node Secret. For a multi-node cluster, provide a
matching per-node Secret and project it into the DaemonSet, or use an external
certificate controller.

### Configure and install the driver

Edit `deploy/kubernetes/csi/kouja-csi.yaml` before applying it:

- set the controller `--hosts` list or `--srv-record`;
- set the node `--agent-endpoint`;
- set the StorageClass `initiator` to the node IQN/NQN;
- set the image names to the images built above;
- choose `protocol: iscsi` or `protocol: nvme-tcp`.

Then apply:

```sh
kubectl apply -f deploy/kubernetes/csi/kouja-csi.yaml
kubectl -n kouja-csi rollout status deploy/kouja-csi-controller
kubectl -n kouja-csi rollout status ds/kouja-csi-node
kubectl get storageclass kouja-e2e kouja-nvme
```

Each node using NVMe/TCP must have `tlshd` active. Each node using iSCSI must
have `iscsid` active. The node plugin mounts the host `/dev`, `/run`,
`/etc/iscsi`, and `/etc/nvme`; it does not create kernel sessions from a Docker
network namespace. A kind node is therefore not a supported full data-path
environment. Use a real VM or k3s installed directly on the host.

See [docs/csi.md](docs/csi.md) for the full StorageClass parameter reference,
credential-rotation behavior, placement, grants, and node-failure semantics.

## Testing

Fast, non-destructive checks:

```sh
make test
make coverage-check
make lint
```

`make coverage-check` enforces at least 80% statement coverage across
`./internal/...`; generated protobuf code and command entry points are excluded
from that gate. The CI workflow uploads `coverage.out` as an artifact.

Integration tests require root and disposable loop devices:

```sh
make integration
```

They create a temporary loop-backed VG and real LIO/nvmet objects. Do not run
them on a production host.

The CSI end-to-end test runs against an existing kubeconfig and installed
driver:

```sh
KOUJA_E2E=1 KOUJA_E2E_STORAGECLASS=kouja-e2e \
  go test -tags=e2e -v ./test/e2e
```

Set `KOUJA_E2E_STORAGECLASS=kouja-nvme` for the NVMe/TCP StorageClass. The
disposable VM probes under `hack/phase0-*` are destructive test aids; read
them before running.

The CI workflow is [`.github/workflows/ci.yml`](.github/workflows/ci.yml). It
runs unit tests, the coverage gate, formatting, `go vet`, and `golangci-lint`.

## Operations and recovery

- **State:** `/var/lib/kouja/state.db` is authoritative for volume ownership,
  exports, operations, credentials, grants, and audit records.
- **Credentials:** `/var/lib/kouja/secret.key` is separate from SQLite and must
  be backed up independently. Without it, encrypted credentials cannot be
  recovered.
- **Backups:** set `state.backup_target` to an off-host directory. Backups
  include the database, key, host ID, and a `restore.json` manifest.
- **Health:** `/healthz` is liveness; `/readyz` reflects reconciliation
  readiness; `/metrics` exposes Prometheus metrics.
- **Drift:** if an LV, target, ACL, namespace, size, or initiator disagrees with
  the database, the volume enters `drift`. Resolve it through the admin API
  (`RETRY`, `RECREATE_EXPORT`, or an explicit mark action) rather than editing
  configfs by hand.
- **Restart:** the agent restores SQLite intent, reactivates LVs, reinstalls
  NVMe PSKs, and reconciles exports. The host UUID is stable across reboots.
- **Node failure:** `nodeFailurePolicy: wait` preserves an unreachable node's
  export. `force` allows the controller to force-unexport after Kubernetes has
  marked the node NotReady or out-of-service; Kubernetes must still release the
  old pod and VolumeAttachment.

The agent runs as root because LVM and configfs require it. Put the control API
on a management network and the iSCSI/NVMe portals on a storage network. CHAP
does not encrypt iSCSI payloads; use an isolated storage network or IPsec for
iSCSI. NVMe/TCP uses TLS 1.3.

## Repository layout

```text
cmd/                         agent and CSI entry points
internal/agent/              API services, reconciliation, admission, startup
internal/lvm/                LVM command backend
internal/target/             direct configfs iSCSI and nvmet adapters
internal/operations/         journaled, idempotent volume operations
internal/state/              SQLite schema and persistence
internal/credential/         encrypted credential storage and rotation
internal/csi/                CSI controller and node implementation
proto/kouja/v1/              protobuf API contract
deploy/kubernetes/           CRD and CSI manifests
deploy/systemd/              agent service unit
test/                        unit, integration, e2e, fault, and CSI suites
docs/                        design, kernel, CSI, and validation records
```

## Documentation

- [Design](docs/designs/kouja.md) — architecture, state model, API, failure
  behavior, and implementation plan
- [CSI driver](docs/csi.md) — StorageClass parameters and Kubernetes behavior
- [Kernel and host support](docs/kernel.md) — kernel, package, and configfs
  requirements
- [End-to-end validation](docs/e2e.md) — tested iSCSI, NVMe/TCP, CSI, drain,
  node-failure, and expansion runs
- [Phase 0 readiness](docs/phase0.md) — kernel matrix and validation findings
- [Target-manager decision](docs/decisions/target-manager.md) — why kouja uses
  direct configfs instead of targetcli/nvmetcli

## Limitations

- No high availability: each volume is tied to one storage host.
- No snapshots, clones, replication, thin provisioning, or multi-initiator/RWX.
- No data-at-rest encryption; TLS-PSK protects transport only.
- nvmet does not expose session counts on the supported Linux 6.12 kernel, so
  NVMe teardown uses an explicit force path and reports `SESSION_UNKNOWN`.
- Per-node certificate issuance is not automated; the sample manifest uses one
  node Secret for a single-node test.
- The CSI controller needs a usable Kubernetes client for placement and
  attachment generation; without it, it falls back to a legacy static mode.
- The project is early; review the design and test record before production use.

## License

kouja is **dual-licensed**:

- **Apache License 2.0** — the public open-source license. The full text is in
  [`LICENSE`](LICENSE).
- **Commercial/proprietary license** — held by **Void Star IT Inc.** for
  commercial use. The Apache 2.0 license does not grant these commercial
  terms; contact Void Star IT Inc. for commercial licensing details.

The Apache 2.0 option applies to the source distributed in this repository.
The proprietary option is kept by Void Star IT Inc. and is not granted by
this repository.
