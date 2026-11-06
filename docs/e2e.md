# End-to-End Validation

* **Date:** 2026-11-06
* **Result:** iSCSI two-VM end-to-end passed; NVMe/TCP TLS passed on two direct
  QEMU nodes and through the k3s CSI data path.

## Two-VM iSCSI topology

Two Debian 13 QEMU/KVM VMs were connected with a QEMU socket network on `10.10.0.0/24`:

* Storage host: `10.10.0.1`, agent on `9443`, LIO portal `3261`.
* Consumer: `10.10.0.2`, open-iscsi initiator.

The agent used SQLite state, an mTLS gRPC/HTTP listener, a loop-backed LVM VG, the direct-configfs iSCSI adapter, and the CHAP credential manager.

## Flow

1. The host agent seeded the registered `kouja-e2e` VG and started serving mTLS.
2. The consumer called `GET /v1/info` and `/healthz` through the HTTP/JSON gateway.
3. The consumer called `POST /v1/volumes` with `export=true` and the consumer IQN.
4. The agent created the LV, target, ACL, CHAP credential, and portal.
5. The consumer logged in with `iscsiadm`; `/dev/sda` appeared with the expected 64 MiB size.
6. The consumer completed an 8 MiB write and read at roughly 74-78 MB/s.
7. `DELETE /v1/volumes/e2e-final` with an active session returned HTTP `400` with `SESSION_ACTIVE`.
8. After logout, the same delete returned HTTP `200`.
9. The host had no remaining LV, backstore, or iSCSI target.

The final two-VM test used the agent binary built after the operation-journal tombstone-retirement fix, so a stale delete operation from a previous volume cannot replay against a new volume.

## NVMe/TCP TLS

`TestNVMeTLSAdapterLifecycle` passed on the storage VM:

* Direct nvmet subsystem, namespace, allowed-host link, TLS port attributes, and subsystem link.
* `nvme gen-tls-key` PSK generation and `.nvme` keyring insertion.
* `nvme connect --tls` over loopback.
* 8 MiB namespace write/read.
* Forced unexport because the mainline kernel has no nvmet session-interface group.

The nvmet adapter therefore has the same behavior validated earlier by the shell probe, now through the Go adapter.

A second direct-OS test used the storage host and the consumer as separate QEMU
VMs, with no Kubernetes involved. The agent created a 64 MiB LV, exported it as
an NVMe/TCP TLS subsystem, and the consumer connected with `nvme connect
--tls`, wrote and read 8 MiB, and disconnected cleanly.

The first attempt failed with:

```text
nvme nvme0: queue 0: failed to start TLS: -3
```

The PSK values and NQNs were identical on both sides, and a local initiator on
the storage host worked. The missing piece was the initiator-side `tlshd`
service on the consumer. The consumer had `nvme-cli` but not `ktls-utils`, so
its kernel had no user-space TLS handshake service and the target saw a
terminated handshake. Installing `ktls-utils`, starting `tlshd`, and connecting
again succeeded:

```text
tlshd: Handshake with kouja-phase0-debian13 (10.10.0.1) was successful
connecting to device: nvme0
```

This is a node prerequisite, not a PSK mismatch or a target keyring defect.
Both the storage host and every NVMe initiator must run `tlshd`.

## Commands

```sh
# Host/consumer manual flow, from the repository worktree:
# build the agent
CGO_ENABLED=0 go build -o kouja-agent ./cmd/kouja-agent

# On a root storage host:
sudo ./kouja-agent --config /etc/kouja/config.yaml

# On the consumer:
curl --http2 --cacert ca.crt --cert admin.crt --key admin.key \
  https://10.10.0.1:9443/v1/info

# Installer syntax and full automated checks:
bash -n deploy/install/install.sh
make build
make test
make lint
```

## CSI in Kubernetes

The CSI controller and node were validated inside the consumer QEMU VM.

* Kind `v0.26.0` with Kubernetes `v1.32.0` was used first. Dynamic provisioning,
  `ControllerPublishVolume`, and attacher bookkeeping worked.
* The kind node could not create the iSCSI session: `iscsid` connected to
  `10.10.0.1:3261` but the kernel rejected session creation from the kind
  container's network namespace (`sendmsg: bug? ctrl_fd 4`). This is a
  namespace limitation, not an agent or controller failure.
* The same controller/node manifests were then run on k3s `v1.37.2` installed
  directly in the consumer VM. The node plugin used the VM's host `iscsid`,
  `/dev`, and initial network namespace.

The successful k3s flow was:

1. `kouja.csi.voidstarit.com` provisioned a 64 MiB thick LV through the agent
   and wrote a `KoujaVolumePlacement` record before the create call.
2. The external attacher called `ControllerPublishVolume`; the controller
   granted the node caller access and the agent created an iSCSI target. The
   publish context contained target, portal, LUN, endpoint, owner, and volume
   identity, but no secret.
3. The node plugin authenticated with its own certificate, fetched the CHAP
   secret through the grant, logged in, formatted the device with ext4, and
   mounted it through `/var/lib/kubelet` with bidirectional mount propagation.
4. A BusyBox pod wrote 8 MiB, read it back, and printed
   `kouja-csi-write-read-ok`.
5. The node plugin unmounted and logged out; the external attacher and
   provisioner removed the VolumeAttachment, PV, LV, target, and backstore.
6. A separate reattach test removed and recreated the VolumeAttachment. The
   second attachment received a new CHAP secret. A manual iSCSI login with the
   old secret failed with authorization error 24; the new secret logged in
   successfully.

### NVMe/TCP over k3s

After installing `ktls-utils` and starting `tlshd` on the consumer node, a
StorageClass with `protocol: nvme-tcp` completed the same flow:

1. `kouja.csi.voidstarit.com` provisioned a 64 MiB LV and created the nvmet
   subsystem, namespace, TLS port, and PSK-protected export.
2. The node plugin fetched the PSK through the per-node grant, inserted it,
   connected with `nvme connect --tls`, found `/dev/nvme1n1` through sysfs,
   formatted it as ext4, and mounted it.
3. A BusyBox pod wrote and read 8 MiB and printed
   `kouja-nvme-write-read-ok`.
4. Deleting the pod and PVC disconnected the namespace. The controller used
   the NVMe-specific force-unexport path for the Ready node because nvmet
   reports `SESSION_UNKNOWN` on the reference kernel, then deleted the LV and
   target.

### Online expansion

A mounted NVMe PVC was expanded from 64 MiB to 128 MiB through the
external-resizer. The agent extended the LV, the nvmet adapter refreshed the
namespace, and the node plugin rescanned the namespace and grew ext4 online.
Kubernetes reported `FileSystemResizeSuccessful`, the PVC capacity changed to
128 MiB, and `df` inside the pod showed the larger filesystem. The same flow
passed with an iSCSI PVC: `iscsiadm -m session --rescan` picked up the larger
LUN capacity and ext4 grew online.

The placement flow was verified through the CRD: the record was created with
the selected host and `Pending`/`Bound` status, and it was removed after the
PVC and VolumeAttachment were deleted. Both iSCSI and NVMe publishes succeeded
with a node certificate carrying `spiffe://kouja/node/kouja-consumer`; the
VolumeAttachment status contained `endpoint`, `owner`, and volume identity but
no credential. Revocation is exercised by the same deletion path.

The k3s cluster used controller certificate
`spiffe://kouja/controller/kind`, node certificate identity
`spiffe://kouja/node/kouja-consumer`, agent endpoint `10.10.0.1:9443`, and
StorageClass `kouja-e2e`. The agent and target both ran in the host QEMU VM; the
consumer and its kubelet ran in the second QEMU VM.

## CSI deployment notes

The CSI node path covers both iSCSI and NVMe/TCP, durable placement records,
optional SRV discovery, and per-node credential grants. The manifest ships a
single-node node certificate Secret for the test environment; production
deployments must provide one matching node certificate per node. The controller
still defaults to a static `--hosts` list unless `--srv-record` is set. See
[the CSI document](csi.md).

## Node failure and drain

A two-worker k3s run was added after the single-node CSI test. The storage host
was `10.10.0.1`; the k3s server was `10.10.0.2` and a worker was `10.10.0.3`.
A one-replica StatefulSet used a kouja PVC and was scheduled on the worker.

### Graceful drain

`kubectl drain kouja-worker --ignore-daemonsets --delete-emptydir-data` evicted
the StatefulSet pod. The node plugin unmounted and logged out, the
VolumeAttachment detached, and the replacement pod was placed on the server.
The application marker written before the drain was present on the replacement
pod. The test's StatefulSet had a worker node selector, so the selector was
changed to the server during the test; production scheduling should use normal
topology or affinity rules rather than pinning a StatefulSet to one node.

### Node down

Stopping `k3s-agent` on the worker made the Node `NotReady`. The StatefulSet pod
remained on the worker, the VolumeAttachment stayed attached, and the target
still reported one active iSCSI session. Applying
`node.kubernetes.io/out-of-service` force-deleted the pod and removed the
VolumeAttachment. With the default `nodeFailurePolicy: wait`, the replacement
pod on the server could not attach:
`FailedAttachVolume: SESSION_ACTIVE`. The old TCP/iSCSI session outlived the
kubelet. A manual forced unexport through the agent API (`force=true`) removed
the old target and session; the controller then rotated the credential and the
replacement pod attached and mounted the same LV successfully.

The same crash/out-of-service flow was repeated with a StorageClass carrying
`nodeFailurePolicy: force`. After the Node became NotReady and received the
out-of-service taint, the controller inspected the Node, called the agent
`UnexportVolume(force=true)`, removed the target/session, and allowed the
replacement pod on the server to attach. No manual force was needed. The data
marker persisted.

Conclusion: graceful drain works with the current node plugin. A crash under
`nodeFailurePolicy: wait` remains blocked by the old session until the node
returns or an administrator forces teardown. `nodeFailurePolicy: force`
performs the force-unexport automatically when Kubernetes has already marked
the node NotReady or out-of-service; Kubernetes still has to release the old
pod and VolumeAttachment (the out-of-service taint does both). Neither policy
itself detects the crash or force-deletes a StatefulSet pod.
