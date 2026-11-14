# Target-Manager Decision

* **Date:** 2026-11-05
* **Status:** Direct configfs selected and confirmed for v1. Both the iSCSI and NVMe/TCP adapters are implemented and passed integration tests on Debian 13, including forced iSCSI teardown and NVMe/TCP TLS I/O.

## Decision

v1 implements the iSCSI and NVMe-oF target adapters directly against configfs in Go. The shared `target.Exporter` interface keeps the two protocols behind one boundary.

## Validation evidence

The disposable QEMU host was Debian 13 with Linux `6.12.115+deb13.1-cloud-amd64`, LVM 2.03.31, `nvme-cli` 2.13, `open-iscsi` 2.1.11, and `ktls-utils` 1.0.0. The tests used loop-backed LVs. The committed scripts are `hack/phase0-lio-check.sh` and `hack/phase0-tls-check.sh`.

### iSCSI/LIO

A direct-configfs target was created with:

* An `iblock` backstore configured through `control` with `udev_path=<lv>`.
* `tpgt_1`, a `127.0.0.1:3262` network portal, and a `lun_0` symlink to the backstore.
* An explicit initiator ACL, an ACL mapped-LUN symlink to `tpgt_1/lun/lun_0`, and CHAP values written without trailing newlines.
* `tpgt_1/attrib/authentication=1`.

An `iscsiadm` CHAP login succeeded and produced an initiator block device. The authoritative count was `fabric_statistics/iscsi_instance/sessions=1`; `targetcli sessions` independently reported `LOGGED_IN`.

Notable implementation constraints:

* The `udev_path` attribute is informational. The backend requires the `udev_path=` command in `control`; otherwise `enable=1` returns `EINVAL`.
* LIO rejects a UUID-shaped HBA group name such as `iblock_<resource_uuid>` with `EINVAL`. The adapter uses the shared `iblock_0` HBA and a per-volume backstore name.
* LIO ACL auth attributes preserve bytes. A shell `echo` adds a newline to the CHAP name and causes `CHAP_N values do not match`. The adapter must write exact byte strings.
* The ACL mapped-LUN symlink must resolve at creation time. Removing only some mapped-LUN links prevents `rmdir` of the ACL and leaves a partial target.
* LIO session observation is definitive: `fabric_statistics/iscsi_instance/sessions` gives the active per-target count.
* `targetcli /iscsi delete` succeeds even with an active session and tears the target down. The production adapter must read the session count and reject the operation before calling any delete path. The adapter implements this pre-check.
* `lvremove` correctly refuses an LV that is still in use (exit 5); the operation layer must remove the target first or require the force path.
* Multiple iSCSI targets can share the same IP:port portal; a duplicate portal is not an iSCSI error. The host-level port constraint applies to nvmet, where ports are shared objects with multiple subsystem links.
* After reboot, LIO and nvmet configfs were empty. The LVM LV on a loop-backed test VG persisted, but the loop device was not auto-attached; reattaching the backing device and activating the VG recovered it. The boot ID changed from the pre-reboot value. This matches the restore-from-SQLite design.

### Adapter implementation and integration test

`internal/target/configfs.go` provides safe relative-path operations, byte-exact attribute writes, idempotent symlinks, and removal. `internal/target/iscsi.go` implements the shared `target.Exporter` interface with one target and TPG per volume, a shared `iblock_0` backstore HBA, an explicit ACL and mapped LUN, CHAP credentials written without trailing newlines, a portal, and TPG enablement.

`test/integration/iscsi_test.go` passed on the Debian 13 VM. It:

1. Created a loop-backed LV and exported it through the adapter.
2. Logged in from the same VM with `iscsiadm` and CHAP.
3. Observed `SessionActive` from the authoritative session count.
4. Received `ErrSessionActive` from `Unexport` without `force`.
5. Logged out, observed `SessionNone`, unexported, and deleted the target and backstore.

A second integration test called `Unexport(force=true)` with the session still active. The adapter disabled the TPG, removed the target, and left the backstore available for `Delete`. The nvmet adapter is implemented in `internal/target/nvmet.go` and `TestNVMeTLSAdapterLifecycle` passed on the storage VM: subsystem, namespace, allowed host, TLS port, keyring installation, `nvme connect --tls`, 8 MiB I/O, and teardown.

### NVMe-oF/nvmet

A direct-configfs subsystem and namespace were created with an allowed-host link and `attr_allow_any_host=0`. The TLS port was configured with `addr_trtype=tcp`, `addr_tsas=tls1.3`, `addr_treq=required`, and the address attributes. The PSK was inserted into the `.nvme` keyring and `tlshd` was active.

A real loopback `nvme connect ... --tls` succeeded, produced `/dev/nvme0n1`, and completed an 8 MiB write/read. The target also had no `controllers/` group and no `debugfs/nvmet` view, so nvmet session observation remains `SESSION_STATE_UNKNOWN`.

The key ordering constraint is that `addr_tsas` and `addr_treq` must be written before the address attributes activate the port. A port that is already active rejects security-attribute changes.

### Dependency footprint

The production adapters need only Go, LVM tools, configfs, kernel target support (built in or loaded as modules), and `tlshd` for NVMe TLS. `targetcli`, `rtslib`, and `nvmetcli` were used for validation and cross-checking. They are not required at runtime and must not become implicit state managers. The test cleanup uses `targetcli` for robustness after failed partial setup; the production adapter must reproduce the direct teardown ordering.

### Failure behavior

The following are still unmeasured:

* Reboot restoration of a target with an active session.
* Crash after partial target creation.
* Active-session teardown when the initiator does not log out.
* Port-in-use and conflicting target-name failures on a busy host.
* Crash injection against a target mid-create.

The normal create/login/delete and forced-teardown paths are now covered. Direct configfs remains selected; the `targetd`/`rtslib` fallback is not needed for v1.

## Fallback

If the remaining spike work fails the session-observation or TLS-PSK criteria, the iSCSI path falls back to `targetd` or `rtslib`; the nvmet path remains direct configfs. The affected files are `internal/target/iscsi.go`, `session.go`, and the dependency pins in the design.

The tested subset does not trigger the fallback:

* iSCSI session observation is definitive.
* The iSCSI CHAP login works with direct configfs.
* The nvmet TLS-PSK login works with direct configfs.
* The nvmet session-interface gap is already an explicit design fallback to `SESSION_STATE_UNKNOWN`, not a reason to switch the iSCSI path to `rtslib`.

## Rejected alternatives

* `targetd` for both LVM and LIO in v1: it would become a second state manager next to SQLite.
* `targetcli` or `nvmetcli` wrappers: they require Python and make ordering and error handling implicit.
* LINSTOR: it adds a controller and a cluster model that the per-host design avoids.
