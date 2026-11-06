# Phase 0 Readiness

* **Date:** 2026-11-05
* **Status:** Kernel, configfs, and data-path checks pass on a disposable Debian 13 VM. The full target-manager spike and the two-host test harness remain pending.

## Completed

* Created the Go module `github.com/voidstarit/kouja`.
* Added the core `state`, `lvm`, `target`, `auth`, `credential`, `csi`, `observability`, and `operations` contract types.
* Added the `KoujaVolumePlacement` CRD types.
* Added the protobuf sources under `proto/kouja/v1`.
* Added the buf workspace and generation configuration.
* Generated `gen/kouja/v1/*.pb.go`, the gRPC clients, the HTTP gateways, and `deploy/kubernetes/crd-koujavolumeplacement.yaml`.
* Installed `buf`, `protoc-gen-go`, `protoc-gen-go-grpc`, `protoc-gen-grpc-gateway`, and `controller-gen`.
* `go build ./...`, `go test ./...`, and `go vet ./...` pass on the development host.
* Resolved the current dependency set: Go 1.26.0, gRPC 1.85.0, protobuf 1.36.12, and grpc-gateway 2.33.0. The design pins were updated to match.
* Added a disposable QEMU validation path and extended `hack/phase0-probe.sh` to separate runtime requirements from build-host-only tooling.
* Implemented the SQLite state store with versioned migrations, WAL, `synchronous=FULL`, foreign keys, volume/export/observation/tombstone/VG/grant/credential/operation/audit persistence, and unit tests.
* Implemented the LVM command backend with JSON reporting, capacity and eligibility checks, tag handling, device readiness polling, and a loop-backed integration test.
* Implemented the direct-configfs iSCSI/LIO adapter with CHAP, session observation, idempotent export/unexport/delete, forced teardown, and real-configfs integration tests.
* Implemented YAML configuration loading and validation, host identity reconciliation with SQLite, and the backup bundle (`VACUUM INTO`, key, host ID, restore manifest, retention rotation).
* Implemented the operation manager with per-volume and per-VG locks, request hashing, durable operation rows, create/export/unexport/delete, session guards, audit rows, and unit tests.
* Implemented the AES-256-GCM credential manager with key-file generation, authenticated encryption, reveal, rotation, owner/grant authorization, and unit tests.
* Implemented the gRPC volume and admin services, the HTTP/JSON gateway on the same mTLS listener, mTLS identity and request-ID propagation, error mapping, service tests, startup dependency/VG checks, first-start VG seeding, periodic reconciliation and drift reporting, all admin drift actions (`RETRY`, `RECREATE_EXPORT`, and mark actions), readiness, admission, metrics, and the `cmd/kouja-agent` entry point.
* Added the direct-configfs NVMe/TCP adapter with TLS port attributes, allowed-host binding, namespace management, and `.nvme` keyring installation.
* Validated a two-QEMU iSCSI flow: mTLS API, create/export, CHAP login, 8 MiB I/O, active-session refusal, logout, delete, and clean host state. NVMe/TCP TLS passed the Go adapter integration test.

## Disposable VM validation

The validation host was a Debian 13 genericcloud image running under QEMU/KVM with 4 vCPU, 4 GiB RAM, a 20 GiB overlay disk, and a loop-backed VG for the test LVs. The test scripts are committed under `hack/`; the VM disk and seed image are disposable and not part of the repository.

### Kernel matrix

| Host | Kernel | LIO/configfs | nvmet/configfs | TLS-PSK kernel support | TLS-PSK login | nvmet session visibility | Verdict |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Debian 13 | `6.12.115+deb13.1-cloud-amd64` | pass | pass | `CONFIG_NVME_TARGET_TCP_TLS=y`, `CONFIG_NVME_TARGET_AUTH=y`, `CONFIG_NVME_TCP_TLS=y` | pass | absent | **Validated reference** |
| Debian 12 | `6.1.0-54-cloud-amd64` | pass | pass | missing | not available | not tested | **Unsupported for v1**; TLS is mandatory |

The Debian 12 host accepted basic LIO and nvmet objects, but its kernel does not expose writable `addr_tsas`; `echo tls1.3 > .../addr_tsas` fails with `EACCES`. The v1 NVMe-oF/TCP path is TLS-only, so a 6.1 kernel is not a supported storage host.

The tested Debian 13 userspace was:

* `lvm2` 2.03.31-2 (LVM 2.03.31).
* `targetcli-fb` 2.1.53-1.3 with `rtslib` 2.1.76 (test and cleanup helper only; not a production dependency).
* `nvme-cli` 2.13-2.
* `open-iscsi` 2.1.11-1+deb13u2 (initiator-side validation only).
* `ktls-utils` 1.0.0-1 with `tlshd` active.
* `tlshd` configured with `keyrings=.nvme`.

### Commands and results

Run as root on the storage VM:

```sh
./hack/phase0-probe.sh
./hack/phase0-kernel-check.sh
./hack/phase0-tls-check.sh
./hack/phase0-lio-check.sh
./hack/phase0-failure-check.sh
```

Observed results on Debian 13:

```text
Phase 0 storage host probe passed.

lio=1 nvmet=1 tls_psk_port=1 tls_treq_required=1
tls_target_config=1 tls_host_config=1 tls_target_auth_config=1
nvmet_controllers_interface=0

tls_login=1
io_check=ok device=/dev/nvme0n1
nvmet_session_interface=none

iscsi_login=1
lio_sessions=1
```

The TLS check performs a real loopback NVMe/TCP TLS-PSK connection and an 8 MiB write/read. The iSCSI check performs a real CHAP login and reads the authoritative LIO session count.

### Direct-configfs findings

These findings are implementation requirements for the Stage 1 adapters and are documented in [docs/kernel.md](kernel.md):

* An `iblock` backstore is configured by writing `udev_path=<device>` to `control`, not by writing the informational `udev_path` attribute. The latter makes `enable=1` fail with `EINVAL`.
* LIO ACL `auth/userid` and `auth/password` preserve the written bytes verbatim. A trailing newline becomes part of the CHAP name and produces `CHAP_N values do not match`. Write these values without a newline.
* The ACL mapped-LUN entry must be created under `acls/<initiator>/lun_0/` as an absolute symlink to the TPG's `lun/lun_0` group. Remove every mapped-LUN link before removing the ACL.
* The LIO session count is exposed as `fabric_statistics/iscsi_instance/sessions`. This is the authoritative per-target count for the Stage 1 session observer.
* nvmet port attributes must be set while the port is disabled and before the address attributes are populated. Set `addr_trtype=tcp`, then `addr_tsas=tls1.3` and `addr_treq=required`, then `addr_adrfam`, `addr_traddr`, and `addr_trsvcid`.
* Mainline 6.12 nvmet exposes no `controllers/` group and no `debugfs/nvmet` session view after a connection. The adapter must return `SESSION_STATE_UNKNOWN`; delete and unexport then require the audited `force=true` path.
* `targetcli` is useful for validation and cleanup in the test scripts, but the production adapters must use direct configfs.

### Failure and reboot validation

A second disposable pass exercised the failure paths:

```text
session_count=1
delete_with_active_session_rc=0 target_after_delete=no
lvremove_with_active_session_rc=5 lv_after_remove=yes
duplicate_portal_mkdir_rc=0 enable_rc=0
partial_target_cleanup_rc=0
```

* `targetcli` deletes a target even while a session is active. The adapter must read the authoritative session count and reject destructive operations first; it does.
* `lvremove` refuses an in-use LV with exit code 5 and leaves the LV in place.
* Two iSCSI targets can share the same IP:port portal; this is not an iSCSI conflict.
* A partially created target with a TPG but no LUN/ACL can be removed without residue.
* After a VM reboot, LIO and nvmet configfs were empty. The loop-backed LVM LV persisted on disk; reattaching the loop device and activating the VG recovered it. The kernel boot ID changed.

The direct-configfs iSCSI adapter passed real integration tests on the VM: export, CHAP login, active-session observation, refusal without `force`, logout, unexport, delete, and forced teardown while the session was still active. See `test/integration/iscsi_test.go` and `test/integration/iscsi_force_test.go`.

### State and LVM validation

The state package tests cover schema migration, reopening an existing database, WAL/foreign-key/full-synchronous settings, transactions and rollback, volume/export/observation/grant/credential persistence, delete cascades, tombstone retrieval, pagination, audit insertion, operation uniqueness, validation, and rejection of a newer schema.

The LVM adapter passed a loop-backed lifecycle test against LVM 2.03.31 on the Debian 13 VM:

```text
Eligibility
Capacity (total, free, extent size, active state)
Create a 64 MiB tagged LV
Set the owner tag and observe the updated tag
ListManaged
Remove and observe ErrNotFound
```

The test is `test/integration/lvm_test.go` and runs only with the `integration` build tag on root hosts. It creates a temporary loop-backed VG and removes it in cleanup.

### Startup foundation validation

The configuration loader applies the design defaults, rejects unknown YAML fields, and validates absolute paths, TLS 1.3, portals, admission limits, operation timeouts, and backup retention. The host identity manager creates a 0600 host-ID file, records `host_uuid` in SQLite, repairs a missing file when no volumes exist, and fails closed when the identity is missing or mismatched while volumes exist. It also reads and records the kernel boot ID.

The backup manager writes a `VACUUM INTO` database copy, the credential key when present, the host ID, and a `restore.json` manifest to the target directory using temporary files and atomic renames. It validates the target is writable and rotates short-interval, daily, and weekly backups. The state store's backup copy reopens and passes integrity/migration checks.

The operation manager tests cover unexported and exported create, idempotent replay, request mismatch rejection, active-session guards, forced unexport/delete, credential supersession, tombstone creation, stale-tombstone retirement, retry re-arm, and journaled execution failure. The credential manager tests cover key-file mode and size, reveal after reopen, wrong-key rejection, owner/admin/grant authorization, expiry, and rotation. The agent tests cover canonical create intent construction, unexported create, owner scoping, pagination, capacity mapping, error mapping, credential reveal, VG registration/deregistration, first-start VG seeding, unavailable/changed UUID drift marking, observation/drift reconciliation, admin drift mark actions, export recreation and retry, mTLS identity/request-ID propagation, admission concurrency/rate limits, and a real TLS listener serving both gRPC and HTTP/JSON. A two-QEMU iSCSI flow and an NVMe/TCP TLS loopback adapter test also passed; see [docs/e2e.md](e2e.md).

These packages are unit-tested on the development host; they do not require the storage VM.

### Development container

The development container used for the scaffold is still not a storage host:

* Non-root user (uid 1000).
* Go 1.27.2 and protoc 3.21.12 are present.
* `buf`, `nvmetcli`, `targetcli`, `nvme`, `tlshd`, and `controller-gen` are missing.
* LIO and nvmet configfs are absent.
* `target_core_mod`, `iscsi_target_mod`, `nvmet`, and `nvmet_tcp` are not loaded.
* `iscsiadm` is present.

`hack/phase0-probe.sh` now reports build-host tools as optional and fails only on missing storage-host requirements. It correctly fails on the development container.

## Pending

* Automate per-node certificate issuance and the remaining production deployment hardening. Durable placement records, optional SRV discovery, per-node credential grants, iSCSI/NVMe/TCP TLS data paths, and online expansion passed live Kubernetes tests; see [docs/csi.md](csi.md) and [docs/e2e.md](e2e.md).
* Automate the validated two-QEMU flow under `hack/` and CI; add failpoint hooks and the reference-host benchmark.
* Validate `deploy/install/install.sh` on a clean disposable host, including firewall and systemd behavior.
* Add operational dashboards and complete the remaining deployment/recovery documentation.
* Continue tracking nvmet session visibility for future kernels; mainline 6.12 remains `SESSION_STATE_UNKNOWN` and requires audited `force=true`.

The kernel/configfs/data-path gate, direct-configfs target manager, iSCSI two-VM flow, NVMe/TCP TLS adapter, API, mTLS, reconciliation, drift actions, admission, readiness, metrics, installer skeleton, agent binary, and the iSCSI CSI controller/node path are implemented. The remaining Stage 2 items are listed above.
