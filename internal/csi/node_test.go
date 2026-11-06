package csi

import (
	"context"
	"crypto/tls"
	"os"
	"path/filepath"
	"testing"

	csipb "github.com/container-storage-interface/spec/lib/go/csi"
)

func fakeCommands(t *testing.T) {
	t.Helper()
	directory := t.TempDir()
	scripts := map[string]string{
		"mountpoint": "#!/bin/sh\nexit ${FAKE_MOUNTPOINT:-1}\n",
		"findmnt":    "#!/bin/sh\ncase \"$*\" in *missing*|*not-mounted*) exit 1;; esac\ncase \"$*\" in *SOURCE,FSTYPE*) echo '/dev/sda ext4';; *) echo \"${FAKE_FSTYPE:-ext4}\";; esac\n",
		"mount":      "#!/bin/sh\nexit 0\n",
		"umount":     "#!/bin/sh\nexit 0\n",
		"resize2fs":  "#!/bin/sh\nexit 0\n",
		"xfs_growfs": "#!/bin/sh\nexit 0\n",
		"blkid":      "#!/bin/sh\nexit 1\n",
		"mkfs.ext4":  "#!/bin/sh\nexit 0\n",
		"mkfs.xfs":   "#!/bin/sh\nexit 0\n",
		"blockdev":   "#!/bin/sh\nexit 0\n",
		"nvme": `#!/bin/sh
case "$1" in
  list) printf '%s\n' '{"Devices":[{"DevicePath":"/dev/null","SubsystemNQN":"nqn.target"}]}' ;;
  list-subsys) exit 0 ;;
  *) exit 0 ;;
esac
`,
		"iscsiadm": `#!/bin/sh
if [ "$1" = "-m" ] && [ "$2" = "session" ] && [ "$3" = "-P" ]; then
  echo "Attached scsi disk null"
fi
exit 0
`,
	}
	for name, body := range scripts {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(body), 0o755); err != nil { //nolint:gosec // executable test fixture
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", directory+":"+os.Getenv("PATH"))
}

func TestNodeLifecycleAndExpand(t *testing.T) {
	fakeCommands(t)
	node := NewNode("node-1", "nqn.test", "ext4")
	target := filepath.Join(t.TempDir(), "target")
	fakeKey := "NVMeTLSkey-1:01:" + "test:"
	request := &csipb.NodePublishVolumeRequest{
		VolumeId: "vol-1", TargetPath: target,
		PublishContext: map[string]string{
			"protocol": "nvme-tcp", "target": "nqn.target", "portal": "127.0.0.1:4420",
			"nsid": "1", "credential": fakeKey,
		},
		VolumeCapability: &csipb.VolumeCapability{
			AccessType: &csipb.VolumeCapability_Mount{Mount: &csipb.VolumeCapability_MountVolume{FsType: "ext4"}},
		},
	}
	if _, err := node.NodePublishVolume(context.Background(), request); err != nil {
		t.Fatalf("NodePublishVolume: %v", err)
	}
	if _, err := node.NodeExpandVolume(context.Background(), &csipb.NodeExpandVolumeRequest{
		VolumeId: "vol-1", VolumePath: target,
	}); err != nil {
		t.Fatalf("NodeExpandVolume: %v", err)
	}
	t.Setenv("FAKE_MOUNTPOINT", "0")
	if _, err := node.NodeUnpublishVolume(context.Background(), &csipb.NodeUnpublishVolumeRequest{
		VolumeId: "vol-1", TargetPath: target,
	}); err != nil {
		t.Fatalf("NodeUnpublishVolume: %v", err)
	}
	if _, err := node.NodeGetVolumeStats(context.Background(), &csipb.NodeGetVolumeStatsRequest{VolumePath: target}); err != nil {
		t.Fatalf("NodeGetVolumeStats: %v", err)
	}
	if _, err := node.NodeStageVolume(context.Background(), &csipb.NodeStageVolumeRequest{}); err != nil {
		t.Fatalf("NodeStageVolume: %v", err)
	}
	if _, err := node.NodeUnstageVolume(context.Background(), &csipb.NodeUnstageVolumeRequest{}); err != nil {
		t.Fatalf("NodeUnstageVolume: %v", err)
	}
	info, _ := node.NodeGetInfo(context.Background(), &csipb.NodeGetInfoRequest{})
	if info.GetNodeId() != "node-1" {
		t.Fatalf("node info = %#v", info)
	}
	capabilities, _ := node.NodeGetCapabilities(context.Background(), &csipb.NodeGetCapabilitiesRequest{})
	if len(capabilities.GetCapabilities()) != 2 {
		t.Fatalf("node capabilities = %#v", capabilities)
	}
}

func TestNodeISCSIPublishAndFailures(t *testing.T) {
	fakeCommands(t)
	previous := iscsiInitiatorPath
	iscsiInitiatorPath = filepath.Join(t.TempDir(), "initiatorname.iscsi")
	t.Cleanup(func() { iscsiInitiatorPath = previous })
	node := NewNode("node-1", "iqn.test", "ext4")
	target := filepath.Join(t.TempDir(), "target")
	request := &csipb.NodePublishVolumeRequest{
		VolumeId: "vol-iscsi", TargetPath: target,
		PublishContext: map[string]string{
			"protocol": "iscsi", "target": "iqn.target", "portal": "127.0.0.1:3260",
			"lun": "0", "username": "user", "credential": "secret",
		},
		VolumeCapability: &csipb.VolumeCapability{
			AccessType: &csipb.VolumeCapability_Mount{Mount: &csipb.VolumeCapability_MountVolume{FsType: "ext4"}},
		},
	}
	if _, err := node.NodePublishVolume(context.Background(), request); err != nil {
		t.Fatalf("iSCSI NodePublishVolume: %v", err)
	}
	if _, err := node.NodePublishVolume(context.Background(), &csipb.NodePublishVolumeRequest{}); err == nil {
		t.Fatal("empty publish request did not fail")
	}
	if _, err := node.NodePublishVolume(context.Background(), &csipb.NodePublishVolumeRequest{
		VolumeId: "bad", TargetPath: target, PublishContext: map[string]string{"protocol": "unsupported"},
		VolumeCapability: &csipb.VolumeCapability{AccessType: &csipb.VolumeCapability_Mount{Mount: &csipb.VolumeCapability_MountVolume{}}},
	}); err == nil {
		t.Fatal("unsupported protocol did not fail")
	}
	if _, err := node.NodeUnpublishVolume(context.Background(), &csipb.NodeUnpublishVolumeRequest{}); err == nil {
		t.Fatal("empty unpublish request did not fail")
	}
	if _, err := node.NodeExpandVolume(context.Background(), &csipb.NodeExpandVolumeRequest{}); err == nil {
		t.Fatal("empty expand request did not fail")
	}
}

func TestNodeMiscellaneousPaths(t *testing.T) {
	fakeCommands(t)
	node := NewNode("node-1", "nqn.test", "ext4")
	if err := ensureDeviceNode("definitely-missing-kouja-device"); err == nil {
		t.Fatal("missing device node did not fail")
	}
	if iscsiSessionPresent(context.Background(), "iqn.target") {
		t.Fatal("empty fake session should not match")
	}
	if err := node.SetAgent("127.0.0.1:1", &tls.Config{}); err != nil {
		t.Fatalf("SetAgent: %v", err)
	}
	_ = node.agent.close()
	device, fsType, err := node.mountedDevice(context.Background(), "missing", "/tmp")
	if err != nil || device != "/dev/sda" || fsType != "ext4" {
		t.Fatalf("mountedDevice fallback = %q %q %v", device, fsType, err)
	}
	node.connections["xfs"] = connection{device: "/dev/null"}
	t.Setenv("FAKE_FSTYPE", "xfs")
	if _, err := node.NodeExpandVolume(context.Background(), &csipb.NodeExpandVolumeRequest{
		VolumeId: "xfs", VolumePath: "/tmp",
	}); err != nil {
		t.Fatalf("xfs expand: %v", err)
	}
	if _, _, err := node.mountedDevice(context.Background(), "no-connection", "/not-mounted"); err == nil {
		t.Fatal("unmounted lookup did not fail")
	}
	if _, err := node.credential(context.Background(), map[string]string{"volume_id": "vol"}); err == nil {
		t.Fatal("unconfigured credential did not fail")
	}
	if _, err := node.NodePublishVolume(context.Background(), &csipb.NodePublishVolumeRequest{
		VolumeId: "vol", TargetPath: filepath.Join(t.TempDir(), "target"),
	}); err == nil {
		t.Fatal("publish without capability did not fail")
	}
	if _, err := node.NodePublishVolume(context.Background(), &csipb.NodePublishVolumeRequest{
		VolumeId: "vol", TargetPath: filepath.Join(t.TempDir(), "target"),
		PublishContext:   map[string]string{"protocol": "isscsi"},
		VolumeCapability: &csipb.VolumeCapability{AccessType: &csipb.VolumeCapability_Mount{Mount: &csipb.VolumeCapability_MountVolume{}}},
	}); err == nil {
		t.Fatal("missing iSCSI fields did not fail")
	}

}

func TestNodeCredentialAndHelpers(t *testing.T) {
	fakeCommands(t)
	node := NewNode("node-1", "nqn.test", "ext4")
	node.agentEndpoint = "agent-1:9443"
	node.agentTLS = &tls.Config{}
	node.agent = &hostClient{volume: &fakeVolumeClient{credential: "secret"}}
	credential, err := node.credential(context.Background(), map[string]string{
		"endpoint": "agent-1:9443", "volume_id": "vol-1", "owner": "owner",
	})
	if err != nil || credential != "secret" {
		t.Fatalf("credential = %q, %v", credential, err)
	}
	if _, err := node.credential(context.Background(), map[string]string{}); err == nil {
		t.Fatal("missing volume ID did not fail")
	}
	values := map[string]string{"credential": "inline"}
	if credential, err := node.credential(context.Background(), values); err != nil || credential != "inline" {
		t.Fatalf("inline credential = %q, %v", credential, err)
	}
	if err := node.rescanDevice(context.Background(), "/dev/nvme0n1"); err != nil {
		t.Fatalf("rescan nvme: %v", err)
	}
	if err := node.rescanDevice(context.Background(), "/dev/sda"); err != nil {
		t.Fatalf("rescan scsi: %v", err)
	}
	if err := run(context.Background(), "sh", "-c", "exit 0"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if output, err := commandOutput(context.Background(), "sh", "-c", "printf ok"); err != nil || output != "ok" {
		t.Fatalf("commandOutput = %q, %v", output, err)
	}
	if mounted, err := isMounted(context.Background(), filepath.Join(t.TempDir(), "missing")); err != nil || mounted {
		t.Fatalf("isMounted = %v, %v", mounted, err)
	}
	if _, _, err := node.mountedDevice(context.Background(), "missing", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing mounted device did not fail")
	}
}

func TestNodeFilesystemAndWaitEdges(t *testing.T) {
	fakeCommands(t)
	node := NewNode("node-1", "nqn.test", "ext4")
	if err := ensureFilesystem(context.Background(), "/dev/null", "xfs"); err != nil {
		t.Fatalf("ensure xfs: %v", err)
	}
	if err := ensureFilesystem(context.Background(), "/dev/null", "btrfs"); err == nil {
		t.Fatal("unsupported filesystem did not fail")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := node.NodeGetVolumeStats(context.Background(), &csipb.NodeGetVolumeStatsRequest{VolumePath: file}); err != nil {
		t.Fatalf("file stats: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := waitForNVMeDevice(ctx, "127.0.0.1:4420", "nqn.other", "1"); err == nil {
		t.Fatal("canceled NVMe wait did not fail")
	}
	if err := run(context.Background(), "definitely-missing-command"); err == nil {
		t.Fatal("missing command did not fail")
	}
}

func TestNodeSetAgentAndNoop(t *testing.T) {
	node := NewNode("node-1", "nqn.test", "")
	if node.FsType != "ext4" {
		t.Fatalf("default fs type = %q", node.FsType)
	}
	if err := node.SetAgent("", nil); err != nil {
		t.Fatalf("empty SetAgent: %v", err)
	}
	if _, err := node.NodeExpandVolume(context.Background(), &csipb.NodeExpandVolumeRequest{}); err == nil {
		t.Fatal("empty expand request did not fail")
	}
	if _, err := node.NodeGetVolumeStats(context.Background(), &csipb.NodeGetVolumeStatsRequest{}); err == nil {
		t.Fatal("empty stats request did not fail")
	}
	if _, _, err := node.mountedDevice(context.Background(), "vol", "/not-mounted"); err == nil {
		t.Fatal("unmounted device lookup did not fail")
	}
}
