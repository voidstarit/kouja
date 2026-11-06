# Kernel and Host Support

* **Status:** Validated on Debian 13 with Linux 6.12.115. Debian 12 with Linux 6.1 is not supported for v1.
* **Scope:** Host requirements for LVM, LIO, nvmet, and the TLS-PSK data path. Client-side discovery, login, and mount remain caller-owned.

## Supported matrix

| Host | Kernel | LIO | nvmet | TLS-PSK | nvmet sessions | Verdict |
| --- | --- | --- | --- | --- | --- | --- |
| Debian 13 genericcloud | `6.12.115+deb13.1-cloud-amd64` | pass | pass | pass | unknown | Reference host |
| Debian 12 genericcloud | `6.1.0-54-cloud-amd64` | pass | pass | unavailable | unknown | Unsupported for v1 |

The Debian 13 reference test ran under QEMU/KVM with 4 vCPU, 4 GiB RAM, and a loop-backed VG. The TLS test performed a real loopback NVMe/TCP TLS-PSK login and an 8 MiB write/read. The iSCSI test performed a real CHAP login and read the LIO session count.

Other kernels are not certified by this test. A host must pass the capability probe and the two data-path checks before it is added to the supported matrix.

## Kernel configuration

The target requires:

```text
CONFIG_TARGET_CORE=m
CONFIG_ISCSI_TARGET=m
CONFIG_TARGET_CORE_IBLOCK=m
CONFIG_NVME_TARGET=m
CONFIG_NVME_TARGET_TCP=m
CONFIG_NVME_TARGET_TCP_TLS=y
CONFIG_NVME_TARGET_AUTH=y
CONFIG_TLS=y
```

The initiator-side validation host additionally requires `CONFIG_NVME_TCP_TLS=y`. The probe reports that symbol as optional for a storage-only host.

The tested module set was:

```text
target_core_mod
target_core_iblock
iscsi_target_mod
nvmet
nvmet_tcp
```

A host that lacks `CONFIG_NVME_TARGET_TCP_TLS` cannot serve the v1 NVMe-oF path because v1 requires TLS 1.3 PSK. It may still pass the basic LIO checks, but it must be rejected as a storage host.

## Packages

Required runtime packages:

* `lvm2` for `lvs`, `vgs`, `pvs`, `lvcreate`, `lvextend`, `lvreduce`, and `lvremove`.
* `ktls-utils` for `tlshd`; `tlshd` must be active and must include the `.nvme` keyring.
* The kernel modules listed above.

Initiator-side requirement for NVMe/TCP:

* `ktls-utils` and an active `tlshd` are required on every initiator node as
  well as on the storage host. Without the initiator-side handshake daemon, the
  kernel fails with `failed to start TLS: -3` before the target can match a PSK.
* `nvme-cli` is required on initiator nodes for `nvme connect` and
  `nvme check-tls-key`.

Validation and test-only packages:

* `nvme-cli` for the loopback NVMe/TCP TLS check.
* `open-iscsi` for the loopback CHAP check.
* `targetcli-fb`/`rtslib` for test setup, cross-checking, and cleanup only.
* `nvmetcli` is optional; the adapter uses configfs directly.

`targetcli` and `nvmetcli` are not production dependencies. The adapters must not shell out to them for normal create, export, unexport, or delete operations.

## LIO configfs contract

The Stage 1 iSCSI adapter must encode these behaviors:

* Configure an `iblock` backstore with `control`:

  ```sh
  mkdir -p /sys/kernel/config/target/core/iblock_0/<name>
  printf 'udev_path=%s\n' /dev/<vg>/<lv> \
    > /sys/kernel/config/target/core/iblock_0/<name>/control
  printf '1' > /sys/kernel/config/target/core/iblock_0/<name>/enable
  ```

  Writing the informational `udev_path` attribute is not sufficient; `enable=1` then fails with `EINVAL`. The adapter uses the shared `iblock_0` HBA. A UUID-shaped HBA name such as `iblock_<resource_uuid>` is rejected by LIO with `EINVAL`; the per-volume identity belongs in the backstore name, target, LUN, and ACL.

* Create `tpgt_1`, the network portal under `np/<address>:<port>`, and the LUN under `lun/lun_0`. The LUN entry is a symlink to the backstore.

* Create the initiator ACL and its mapped LUN:

  ```sh
  mkdir -p .../tpgt_1/acls/<initiator-iqn>
  mkdir -p .../tpgt_1/acls/<initiator-iqn>/lun_0
  ln -s /sys/kernel/config/target/iscsi/<target-iqn>/tpgt_1/lun/lun_0 \
    .../tpgt_1/acls/<initiator-iqn>/lun_0/<link-name>
  printf '0' > .../tpgt_1/acls/<initiator-iqn>/lun_0/write_protect
  ```

  The target path must resolve at symlink time. Use the absolute configfs path or the exact relative path from the intended resolver context.

* Write CHAP credentials without a trailing newline:

  ```sh
  printf '%s' "$CHAP_USER" > .../acls/<initiator-iqn>/auth/userid
  printf '%s' "$CHAP_PASS" > .../acls/<initiator-iqn>/auth/password
  printf '1' > .../tpgt_1/attrib/authentication
  ```

  LIO preserves the bytes, including a trailing newline. A newline becomes part of the CHAP name and causes `CHAP_N values do not match` at login.

* The authoritative LIO session count is:

  ```text
  /sys/kernel/config/target/iscsi/<target-iqn>/fabric_statistics/iscsi_instance/sessions
  ```

  `targetcli sessions` is a useful cross-check, but it is not the production interface. `dynamic_sessions` lists only dynamically generated ACL sessions and is empty for explicit ACLs; do not use it for the normal session count.

* The adapter must check the session count before any destructive path. `targetcli /iscsi delete` succeeds and tears down a target even with an active session; using it as a pre-check would silently disconnect a caller. The committed adapter returns `ErrSessionActive` unless `force=true`.

* Multiple iSCSI targets may share the same IPv4 portal. Do not treat a duplicate iSCSI portal as a conflict; the nvmet port model is the shared-port case.

* Teardown must remove every ACL mapped-LUN link before removing the ACL, then remove the LUN, portal, TPG, and target. The test script uses `targetcli` to remove leftover explicit ACLs because a single missed mapped-LUN link prevents `rmdir` of the ACL. The production adapter must implement the equivalent ordering directly.

## nvmet configfs contract

The Stage 2 NVMe-oF adapter must encode these behaviors:

* Create one subsystem and one namespace per volume:

  ```sh
  mkdir -p /sys/kernel/config/nvmet/subsystems/<nqn>
  printf '0' > /sys/kernel/config/nvmet/subsystems/<nqn>/attr_allow_any_host
  mkdir -p /sys/kernel/config/nvmet/subsystems/<nqn>/namespaces/1
  printf '%s' /dev/<vg>/<lv> \
    > /sys/kernel/config/nvmet/subsystems/<nqn>/namespaces/1/device_path
  printf '1' > /sys/kernel/config/nvmet/subsystems/<nqn>/namespaces/1/enable
  ```

* Allow the single initiator through the `hosts` group and the subsystem's `allowed_hosts` symlink. Do not enable `attr_allow_any_host`.

* Configure the TLS port while it is disabled and in this order:

  ```sh
  mkdir -p /sys/kernel/config/nvmet/ports/<id>
  printf 'tcp' > /sys/kernel/config/nvmet/ports/<id>/addr_trtype
  printf 'tls1.3' > /sys/kernel/config/nvmet/ports/<id>/addr_tsas
  printf 'required' > /sys/kernel/config/nvmet/ports/<id>/addr_treq
  printf 'ipv4' > /sys/kernel/config/nvmet/ports/<id>/addr_adrfam
  printf '%s' <address> > /sys/kernel/config/nvmet/ports/<id>/addr_traddr
  printf '%s' <port> > /sys/kernel/config/nvmet/ports/<id>/addr_trsvcid
  ln -s /sys/kernel/config/nvmet/subsystems/<nqn> \
    /sys/kernel/config/nvmet/ports/<id>/subsystems/<nqn>
  ```

  Writing `addr_tsas` or `addr_treq` after the address has activated the port fails with `EINVAL`/`EPERM` because the port is already enabled. Set the security attributes first.

* Install the PSK into the `.nvme` keyring and ensure `tlshd` is active. The validation flow was:

  ```sh
  PSK=$(nvme gen-tls-key --subsysnqn="$NQN")
  nvme check-tls-key --insert \
    --hostnqn="$HOSTNQN" --subsysnqn="$NQN" \
    --keydata="$PSK" --identity=1
  ```

  A loopback `nvme connect -t tcp -a 127.0.0.1 -s <port> -n "$NQN" --tls`
  completed and provided a working namespace. Production code must pass the PSK through the keyring or a mode-0600 file, never through a long-lived process argument.

* Mainline 6.12 exposes no nvmet `controllers/` group and no `debugfs/nvmet` session view. After a connection the adapter cannot determine the session count and must return `SESSION_STATE_UNKNOWN`. Delete and unexport require the audited `force=true` path; the CSI driver must not force automatically.

## Capability probe

Run as root on a disposable or candidate host:

```sh
./hack/phase0-probe.sh
./hack/phase0-kernel-check.sh
./hack/phase0-tls-check.sh
./hack/phase0-lio-check.sh
```

`hack/phase0-probe.sh` exits nonzero when a required storage-host item is missing. Build-only tools (`go`, `protoc`, `buf`, `controller-gen`) and test-only tools (`nvme`, `iscsiadm`, `targetcli`, `nvmetcli`) are reported but do not fail the storage host.

The kernel check prints the detected support:

```text
lio=1 nvmet=1 tls_psk_port=1 tls_treq_required=1
tls_target_config=1 tls_host_config=1 tls_target_auth_config=1
nvmet_controllers_interface=0
```

The TLS check additionally prints `tls_login=1`, `io_check=ok`, and `nvmet_session_interface=none`. The LIO check prints `iscsi_login=1` and `lio_sessions=1`.

## Deployment implications

* Reject Debian 12/6.1 storage hosts in v1. The kernel cannot create a TLS-only nvmet port.
* Treat nvmet session state as unknown on mainline 6.12. The API must expose the uncertainty and block destructive operations unless an administrator supplies `force=true`.
* Keep the LIO session count path in the readiness and reconciliation checks. A missing or unreadable count file is unknown, not zero.
* Keep `/var/lib/kouja/secret.key` and SQLite backups out of world-readable paths; the data-path credentials are encrypted at rest by the agent, not by the kernel.
