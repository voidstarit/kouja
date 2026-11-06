# CSI Driver

The CSI driver is `kouja.csi.voidstarit.com`. It is a client of the same
per-host agent API that Rookery uses. The controller chooses an agent; the node
plugin connects the resulting block device to the Kubernetes node.

## Current scope

Implemented:

* CSI Identity, Controller, and Node services.
* Dynamic provisioning with `CreateVolume`/`DeleteVolume`.
* `ControllerPublishVolume`/`ControllerUnpublishVolume` through the external
  attacher.
* iSCSI CHAP login, filesystem formatting, mount, unmount, and logout.
* NVMe-oF/TCP TLS 1.3 PSK connect, namespace discovery, filesystem formatting,
  mount, unmount, and disconnect. The node-side key install, target PSK lookup,
  and both endpoint handshakes were validated with two direct QEMU nodes and
  with the k3s CSI data path.
* Online volume expansion with LV extension, target resize, block-device
  rescan, and filesystem growth.
* Node volume stats.
* Versioned volume handles of the form `v1:<host-uuid>:<resource-uuid>`.
* mTLS from the controller to the agent; a controller certificate is mounted
  from a Kubernetes Secret.
* Durable `KoujaVolumePlacement` records and optional SRV discovery. The
  controller still accepts a static `--hosts` list as a fallback, but a
  `--srv-record` replaces it with discovered agent endpoints. A CRD is written
  before the first agent create call, so a lost response cannot move a volume
  to another host.
* Per-node credential grants. The controller grants the node caller access to
  the volume and the publish context carries only the endpoint and volume
  identity. The node plugin authenticates with its own certificate and fetches
  the CHAP secret or NVMe PSK through `GetCredential`.

## Placement and discovery

`CreateVolume` uses the CSI request name as a durable placement key. Before
calling the agent it writes a `KoujaVolumePlacement` object with the selected
host UUID and agent volume ID; on success it records the resource UUID and
marks the object `Bound`. A retry reads that record and resumes on the same
host, even if another host has more free capacity.

The controller uses `--srv-record` to resolve agent endpoints when configured;
otherwise it uses the comma-separated `--hosts` list. The CRD and controller
RBAC are in `deploy/kubernetes/crd-koujavolumeplacement.yaml` and
`deploy/kubernetes/csi/kouja-csi.yaml`. If the CRD is absent, the controller
logs a warning and falls back to the legacy static selection path.

## Per-node grants

The controller does not place a secret in the CSI publish context when the
Kubernetes policy lookup is available. It creates a grant for the node caller
ID, then the node plugin uses its own mTLS certificate to call
`GetCredential`. The grant is revoked after a successful unpublish. The node
DaemonSet must mount a certificate whose SAN is
`spiffe://kouja/node/<node-id>`; if an operator uses a different SAN, it must
write the `kouja.csi/node-caller-id` annotation on the node's `CSINode`.
Without a node certificate, set `--agent-endpoint` on the node plugin to empty
to retain the legacy publish-context credential fallback.

## Credential rotation

`ControllerPublishVolume` asks the agent to rotate the credential and recreate
the target for every new attachment. The controller reads the Kubernetes
`VolumeAttachment` UID and passes it to the agent as the attachment generation.
A retry of the same attachment uses the same UID and operation hash and is
idempotent; it does not rotate again. The controller must be able to list
PersistentVolumes and VolumeAttachments; the manifest grants those permissions.
If Kubernetes client configuration is unavailable at startup, the controller
falls back to `node|initiator` and logs a warning; that fallback does not provide
per-attachment generation guarantees.
A normal `ControllerUnpublishVolume` supersedes the current credential, and the
next publish generates a new one. If an old session is still active, the
publish returns `SESSION_ACTIVE` unless the StorageClass sets
`forceReattach: "true"`. The forced path tears down the old target before
creating the new credential. The parameter applies when the controller is
already publishing a new attachment; it does not detect a dead node or release
its old pod and VolumeAttachment automatically. `nodeFailurePolicy` controls
the unpublish side: with `force`, the controller checks the old node's Ready
condition and out-of-service taint, then forces the agent unexport. A Kubernetes
out-of-service taint or another explicit pod-release mechanism is still needed
to release a StatefulSet pod that cannot be deleted by its dead kubelet.

Changing the CHAP secret alone does not disconnect an already logged-in iSCSI
session. The forced rotation path therefore removes the old target/session;
the normal path relies on the old node being unmounted first.

On the supported nvmet kernels the target cannot observe a session count, so
`UnexportVolume` reports `SESSION_UNKNOWN` unless forced. During a normal
`ControllerUnpublishVolume` on a Ready node, the controller therefore passes
`force=true` for NVMe volumes: the node plugin has already unmounted and
disconnected the namespace. If the node is NotReady or out of service, the
`nodeFailurePolicy` still decides whether force is authorized: `wait` leaves
the export in place, while `force` tears it down. `DeleteVolume` uses the same
NVMe force path after detachment; iSCSI deletion remains conservative and
continues to return `SESSION_ACTIVE` until the session is gone.

## Expansion

The controller and node advertise `EXPAND_VOLUME`; the controller expansion
call resizes the LV through the agent and returns
`node_expansion_required=true`. The node plugin rescans the NVMe namespace or
iSCSI session and grows ext4 with `resize2fs` or XFS with `xfs_growfs`. The
manifest includes the external-resizer sidecar. Shrinking is not supported.
Set `allowVolumeExpansion: true` on the StorageClass.

## StorageClass

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: kouja-e2e
provisioner: kouja.csi.voidstarit.com
parameters:
  vg: kouja-e2e
  protocol: iscsi
  initiator: iqn.2026-11.io.kouja:kind-node
  csi.storage.k8s.io/fstype: ext4
  forceReattach: "false"
  nodeFailurePolicy: wait
reclaimPolicy: Delete
allowVolumeExpansion: true
volumeBindingMode: Immediate
```

Parameters:

| Parameter | Required | Meaning |
|---|---|---|
| `class` | one of `class`/`vg` | Storage class configured on the agent, for example `nvme`, `ssd`, or `hdd`. The agent maps it to a registered LVM volume group. |
| `vg` | one of `class`/`vg` | Registered LVM volume group on the selected kouja host. Use this for existing deployments or when no class mapping exists. |
| `protocol` | no | `iscsi` (default) or `nvme-tcp`. The NVMe path requires TLS 1.3 PSK support on the target and `tlshd` on the node. |
| `initiator` | yes for attach | IQN for iSCSI or NQN for NVMe-oF/TCP. It must match the node plugin's initiator. |
| `csi.storage.k8s.io/fstype` | no | `ext4` (default in the example). `xfs` is accepted by the node formatter only when `mkfs.xfs` is present in the node image. |
| `forceReattach` | no | `true` allows a new attachment to force-teardown an old active session before rotating the credential. Default `false`: an active old session blocks the new attachment. |
| `nodeFailurePolicy` | no | `wait` (default) keeps an unreachable node's export/session in place. `force` allows `ControllerUnpublishVolume` to force-unexport when the node is NotReady or marked `node.kubernetes.io/out-of-service`. It still requires Kubernetes to release the old pod/VolumeAttachment. |

The initiator value is passed through the PV's volume context. A production
deployment should derive it from the CSINode annotation described in the
design rather than storing a single cluster-wide IQN.

To expose multiple backends from the same host, map classes on the agent and
reference them from separate Kubernetes StorageClasses:

```yaml
# agent /etc/kouja/config.yaml
storage:
  classes:
    nvme: kouja-nvme
    ssd: kouja-ssd
    hdd: kouja-hdd
---
# Kubernetes
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: kouja-hdd
provisioner: kouja.csi.voidstarit.com
parameters:
  class: hdd
  protocol: iscsi
  initiator: iqn.2026-11.io.kouja:kind-node
reclaimPolicy: Delete
allowVolumeExpansion: true
volumeBindingMode: Immediate
```

The controller filters hosts by the capacity response's storage class, so a
PVC is only placed on an agent that advertises that class. A class that is not
mapped on the agent falls back to the raw VG name for backward compatibility.
Clients can inspect the available classes with `GET /v1/storage-classes`; the
response includes the backing VG, availability, and current capacity.

## Controller flags

```text
--endpoint=unix:///csi/csi.sock
--hosts=10.10.0.1:9443     # used when --srv-record is empty
--srv-record=_kouja._tcp.example.internal
--tls-cert=/etc/kouja/tls/tls.crt
--tls-key=/etc/kouja/tls/tls.key
--tls-ca=/etc/kouja/tls/ca.crt
--driver-name=kouja.csi.voidstarit.com
--kubeconfig=              # optional; in-cluster configuration is used by default
```

`--hosts` may contain one or more agent endpoints. The controller calls
`GetInfo` and `GetCapacity` to select a ready host that supports the requested
protocol and has enough allocatable space in the requested class or VG.

## Deployment notes

`deploy/kubernetes/csi/kouja-csi.yaml` contains the CSIDriver, StorageClass,
RBAC, controller Deployment, and node DaemonSet. It expects:

* a Secret named `kouja-csi-tls` in namespace `kouja-csi` with `ca.crt`,
  `tls.crt`, and `tls.key` for the controller;
* a Secret named `kouja-csi-node-tls` with a node client certificate whose URI
  SAN is `spiffe://kouja/node/<node-id>`, plus `ca.crt` and `tls.key`; the
  DaemonSet projection must use the matching per-node Secret in production;
* the `KoujaVolumePlacement` CRD from
  `deploy/kubernetes/crd-koujavolumeplacement.yaml`;
* images `kouja/csi-controller:e2e` and `kouja/csi-node:e2e`, or the equivalent
  production image names;
* an iSCSI initiator daemon on each node (`iscsid`) and the `iscsi_tcp` kernel
  module for the iSCSI path;
* `ktls-utils`/`tlshd` installed and active on every node that uses the
  `nvme-tcp` protocol, and the `nvme_tcp`/`nvme_fabrics` kernel modules loaded.
  `tlshd` is the initiator-side TLS 1.3 handshake service; without it the
  kernel reports `failed to start TLS: -3` even when the PSK is correct. The
  node plugin does not start a private handshake daemon, because the daemon
  must share the host network/kernel namespace;
* a node initiator name matching the StorageClass `initiator` parameter; for
  NVMe this is an NQN;
* `/var/lib/kubelet` mounted with `mountPropagation: Bidirectional` in the
  node plugin. Without this, the node pod's mounts do not propagate to the
  kubelet and the pod can write to the node's root filesystem.

The node DaemonSet mounts the host's `/run` and `/etc/iscsi` when the host
already runs `iscsid`, so that the CSI pod uses the host initiator daemon. If
`/run/iscsid.pid` is absent, the container starts a private daemon (useful in
isolated test environments, but not sufficient for kind nodes because iSCSI
sessions are created in the VM's initial network namespace).

## Kind limitation

A kind node is a Docker container with its own network namespace. The kernel
iSCSI transport cannot create a session from that namespace on the reference
kernel, so a kind cluster can validate provisioning and controller attach but
cannot complete the iSCSI node path. The full data-path test uses k3s directly
inside the consumer QEMU VM; see [the e2e record](e2e.md).
