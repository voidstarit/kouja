package csi

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	csipb "github.com/container-storage-interface/spec/lib/go/csi"
	koujav1 "github.com/voidstarit/kouja/gen/kouja/v1"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type connection struct {
	protocol string
	target   string
	portal   string
	lun      string
	device   string
}

type Node struct {
	csipb.UnimplementedNodeServer

	NodeID    string
	Initiator string
	FsType    string

	mu          sync.Mutex
	connections map[string]connection

	agentEndpoint string
	agentTLS      *tls.Config
	agent         *hostClient
}

func NewNode(nodeID, initiator, fsType string) *Node {
	if fsType == "" {
		fsType = "ext4"
	}
	return &Node{NodeID: nodeID, Initiator: initiator, FsType: fsType, connections: map[string]connection{}}
}

// SetAgent configures the node plugin's own mTLS client to the agent. The
// publish context supplies the current agent endpoint; this client is used to
// fetch the credential through a per-node grant.
func (node *Node) SetAgent(endpoint string, tlsConfig *tls.Config) error {
	node.agentEndpoint = strings.TrimSpace(endpoint)
	node.agentTLS = tlsConfig
	if node.agentEndpoint == "" || tlsConfig == nil {
		return nil
	}
	client, err := dialHost(node.agentEndpoint, tlsConfig)
	if err != nil {
		return err
	}
	node.agent = client
	return nil
}

func (node *Node) NodeGetInfo(context.Context, *csipb.NodeGetInfoRequest) (*csipb.NodeGetInfoResponse, error) {
	return &csipb.NodeGetInfoResponse{NodeId: node.NodeID}, nil
}

func (node *Node) NodeGetCapabilities(context.Context, *csipb.NodeGetCapabilitiesRequest) (*csipb.NodeGetCapabilitiesResponse, error) {
	return &csipb.NodeGetCapabilitiesResponse{
		Capabilities: []*csipb.NodeServiceCapability{
			{
				Type: &csipb.NodeServiceCapability_Rpc{Rpc: &csipb.NodeServiceCapability_RPC{
					Type: csipb.NodeServiceCapability_RPC_GET_VOLUME_STATS,
				}},
			},
			{
				Type: &csipb.NodeServiceCapability_Rpc{Rpc: &csipb.NodeServiceCapability_RPC{
					Type: csipb.NodeServiceCapability_RPC_EXPAND_VOLUME,
				}},
			},
		},
	}, nil
}

func (node *Node) NodeStageVolume(context.Context, *csipb.NodeStageVolumeRequest) (*csipb.NodeStageVolumeResponse, error) {
	return &csipb.NodeStageVolumeResponse{}, nil
}

func (node *Node) NodeUnstageVolume(context.Context, *csipb.NodeUnstageVolumeRequest) (*csipb.NodeUnstageVolumeResponse, error) {
	return &csipb.NodeUnstageVolumeResponse{}, nil
}

func (node *Node) NodePublishVolume(ctx context.Context, request *csipb.NodePublishVolumeRequest) (*csipb.NodePublishVolumeResponse, error) {
	if request.GetVolumeId() == "" || request.GetTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume_id and target_path are required")
	}
	if request.GetVolumeCapability().GetMount() == nil {
		return nil, status.Error(codes.InvalidArgument, "only mount volumes are supported")
	}
	protocol := request.GetPublishContext()["protocol"]
	if protocol == "" {
		protocol = request.GetVolumeContext()["protocol"]
	}
	mounted, err := isMounted(ctx, request.GetTargetPath())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if mounted {
		return &csipb.NodePublishVolumeResponse{}, nil
	}
	var attached connection
	switch strings.ToLower(protocol) {
	case "", "iscsi":
		attached, err = node.connectISCSI(ctx, request)
	case "nvme", "nvme-tcp", "nvme_tcp":
		attached, err = node.connectNVMe(ctx, request)
	default:
		return nil, status.Errorf(codes.Unimplemented, "node publish for protocol %q is not implemented", protocol)
	}
	if err != nil {
		return nil, err
	}
	fsType := request.GetVolumeCapability().GetMount().GetFsType()
	if fsType == "" {
		fsType = request.GetVolumeContext()["fsType"]
	}
	if fsType == "" {
		fsType = node.FsType
	}
	if err := ensureFilesystem(ctx, attached.device, fsType); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := os.MkdirAll(request.GetTargetPath(), 0o750); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("create target path: %v", err))
	}
	args := []string{"-t", fsType}
	if request.GetReadonly() {
		args = append(args, "-o", "ro")
	}
	args = append(args, attached.device, request.GetTargetPath())
	if err := run(ctx, "mount", args...); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("mount %s: %v", attached.device, err))
	}
	node.mu.Lock()
	node.connections[request.GetVolumeId()] = attached
	node.mu.Unlock()
	return &csipb.NodePublishVolumeResponse{}, nil
}

func (node *Node) NodeUnpublishVolume(ctx context.Context, request *csipb.NodeUnpublishVolumeRequest) (*csipb.NodeUnpublishVolumeResponse, error) {
	if request.GetVolumeId() == "" || request.GetTargetPath() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume_id and target_path are required")
	}
	if mounted, err := isMounted(ctx, request.GetTargetPath()); err == nil && mounted {
		if err := run(ctx, "umount", request.GetTargetPath()); err != nil {
			return nil, status.Error(codes.Internal, fmt.Sprintf("unmount target: %v", err))
		}
	}
	node.mu.Lock()
	connection, found := node.connections[request.GetVolumeId()]
	delete(node.connections, request.GetVolumeId())
	node.mu.Unlock()
	if found {
		switch connection.protocol {
		case "nvme-tcp", "nvme", "nvme_tcp":
			_ = run(ctx, "nvme", "disconnect", "-n", connection.target)
		default:
			_ = run(ctx, "iscsiadm", "-m", "node", "-T", connection.target, "-p", connection.portal, "--logout")
			_ = run(ctx, "iscsiadm", "-m", "node", "-T", connection.target, "-p", connection.portal, "-o", "delete")
		}
	}
	return &csipb.NodeUnpublishVolumeResponse{}, nil
}

func (node *Node) NodeExpandVolume(ctx context.Context, request *csipb.NodeExpandVolumeRequest) (*csipb.NodeExpandVolumeResponse, error) {
	if request.GetVolumeId() == "" || request.GetVolumePath() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume_id and volume_path are required")
	}
	device, fsType, err := node.mountedDevice(ctx, request.GetVolumeId(), request.GetVolumePath())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if err := node.rescanDevice(ctx, device); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	switch strings.ToLower(fsType) {
	case "xfs":
		if err := run(ctx, "xfs_growfs", request.GetVolumePath()); err != nil {
			return nil, status.Error(codes.Internal, fmt.Sprintf("grow XFS: %v", err))
		}
	case "ext4", "ext3", "ext2", "":
		if err := run(ctx, "resize2fs", device); err != nil {
			return nil, status.Error(codes.Internal, fmt.Sprintf("grow ext filesystem: %v", err))
		}
	default:
		return nil, status.Errorf(codes.InvalidArgument, "unsupported filesystem %q", fsType)
	}
	return &csipb.NodeExpandVolumeResponse{}, nil
}

func (node *Node) mountedDevice(ctx context.Context, volumeID, volumePath string) (string, string, error) {
	node.mu.Lock()
	attached, found := node.connections[volumeID]
	node.mu.Unlock()
	if found && attached.device != "" {
		fsType, _ := mountFilesystem(ctx, volumePath)
		return attached.device, fsType, nil
	}
	output, err := commandOutput(ctx, "findmnt", "-n", "-o", "SOURCE,FSTYPE", "-T", volumePath)
	if err != nil {
		return "", "", fmt.Errorf("find mounted device for %s: %w", volumePath, err)
	}
	fields := strings.Fields(output)
	if len(fields) < 1 {
		return "", "", fmt.Errorf("no mounted device found for %s", volumePath)
	}
	fsType := node.FsType
	if len(fields) > 1 {
		fsType = fields[1]
	}
	return fields[0], fsType, nil
}

func mountFilesystem(ctx context.Context, path string) (string, error) {
	output, err := commandOutput(ctx, "findmnt", "-n", "-o", "FSTYPE", "-T", path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func (node *Node) rescanDevice(ctx context.Context, device string) error {
	name := filepath.Base(device)
	if strings.HasPrefix(name, "nvme") {
		// nvme1n1 -> nvme1: the namespace number is the suffix after the
		// controller name.
		index := strings.LastIndex(name, "n")
		if index <= 0 || index == len(name)-1 {
			return fmt.Errorf("cannot determine NVMe controller for %s", device)
		}
		return run(ctx, "nvme", "ns-rescan", "/dev/"+name[:index])
	}
	if strings.HasPrefix(name, "sd") {
		return run(ctx, "iscsiadm", "-m", "session", "--rescan")
	}
	// SCSI and virtio devices are refreshed by the normal block-device
	// reread path.
	return run(ctx, "blockdev", "--rereadpt", device)
}

func (node *Node) NodeGetVolumeStats(ctx context.Context, request *csipb.NodeGetVolumeStatsRequest) (*csipb.NodeGetVolumeStatsResponse, error) {
	if request.GetVolumePath() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume_path is required")
	}
	info, err := os.Stat(request.GetVolumePath())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "volume path: %v", err)
	}
	if !info.IsDir() {
		return &csipb.NodeGetVolumeStatsResponse{}, nil
	}
	output, err := commandOutput(ctx, "stat", "-f", "-c", "%b %f %a %S", request.GetVolumePath())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "statfs: %v", err)
	}
	fields := strings.Fields(output)
	if len(fields) != 4 {
		return nil, status.Error(codes.Internal, "unexpected statfs output")
	}
	totalBlocks, _ := strconv.ParseInt(fields[0], 10, 64)
	availableBlocks, _ := strconv.ParseInt(fields[2], 10, 64)
	blockSize, _ := strconv.ParseInt(fields[3], 10, 64)
	total := totalBlocks * blockSize
	available := availableBlocks * blockSize
	return &csipb.NodeGetVolumeStatsResponse{Usage: []*csipb.VolumeUsage{{
		Total: total, Available: available, Used: total - available, Unit: csipb.VolumeUsage_BYTES,
	}}}, nil
}

func (node *Node) connectISCSI(ctx context.Context, request *csipb.NodePublishVolumeRequest) (connection, error) {
	contextValues := request.GetPublishContext()
	target := contextValues["target"]
	portal := contextValues["portal"]
	lun := contextValues["lun"]
	initiator := contextValues["initiator"]
	if initiator == "" {
		initiator = node.Initiator
	}
	if target == "" || portal == "" {
		return connection{}, status.Error(codes.InvalidArgument, "publish_context target and portal are required")
	}
	if initiator == "" {
		return connection{}, status.Error(codes.InvalidArgument, "an iSCSI initiator name is required")
	}
	if err := writeInitiator(initiator); err != nil {
		return connection{}, status.Error(codes.Internal, err.Error())
	}
	base := []string{"-m", "node", "-T", target, "-p", portal}
	_ = run(ctx, "iscsiadm", append(base, "-o", "new")...)
	if username := contextValues["username"]; username != "" {
		credential, credentialErr := node.credential(ctx, contextValues)
		if credentialErr != nil {
			return connection{}, credentialErr
		}
		if err := run(ctx, "iscsiadm", append(base, "-o", "update", "-n", "node.session.auth.authmethod", "-v", "CHAP")...); err != nil {
			return connection{}, status.Error(codes.Internal, err.Error())
		}
		if err := run(ctx, "iscsiadm", append(base, "-o", "update", "-n", "node.session.auth.username", "-v", username)...); err != nil {
			return connection{}, status.Error(codes.Internal, err.Error())
		}
		if err := run(ctx, "iscsiadm", append(base, "-o", "update", "-n", "node.session.auth.password", "-v", credential)...); err != nil {
			return connection{}, status.Error(codes.Internal, err.Error())
		}
	}
	if loginErr := run(ctx, "iscsiadm", append(base, "--login")...); loginErr != nil {
		// A retried publish can find the session already established. Treat
		// that as success and continue with device discovery.
		if !iscsiSessionPresent(ctx, target) {
			return connection{}, status.Errorf(codes.Internal, "iSCSI login failed: %v", loginErr)
		}
	}
	device, err := waitForISCSIDevice(ctx, portal, target, lun)
	if err != nil {
		return connection{}, status.Error(codes.Internal, err.Error())
	}
	return connection{protocol: "iscsi", target: target, portal: portal, lun: lun, device: device}, nil
}

func iscsiSessionPresent(ctx context.Context, target string) bool {
	output, err := commandOutput(ctx, "iscsiadm", "-m", "session")
	return err == nil && strings.Contains(output, target)
}

func (node *Node) connectNVMe(ctx context.Context, request *csipb.NodePublishVolumeRequest) (connection, error) {
	values := request.GetPublishContext()
	target := values["target"]
	portal := values["portal"]
	initiator := values["initiator"]
	credential := values["credential"]
	nsid := values["nsid"]
	if initiator == "" {
		initiator = node.Initiator
	}
	if nsid == "" {
		nsid = "1"
	}
	if target == "" || portal == "" {
		return connection{}, status.Error(codes.InvalidArgument, "publish_context target and portal are required for NVMe")
	}
	if initiator == "" {
		return connection{}, status.Error(codes.InvalidArgument, "an NVMe host NQN is required")
	}
	if credential == "" {
		var credentialErr error
		credential, credentialErr = node.credential(ctx, values)
		if credentialErr != nil {
			return connection{}, credentialErr
		}
	}
	host, port, err := net.SplitHostPort(portal)
	if err != nil {
		return connection{}, status.Errorf(codes.InvalidArgument, "invalid NVMe portal: %v", err)
	}
	if err := run(ctx, "nvme", "check-tls-key", "--insert",
		"--hostnqn", initiator, "--subsysnqn", target,
		"--keydata", credential, "--identity=1"); err != nil {
		return connection{}, status.Errorf(codes.Internal, "install NVMe TLS key: %v", err)
	}
	connectErr := run(ctx, "nvme", "connect", "-t", "tcp", "-a", host, "-s", port, "-n", target, "--hostnqn", initiator, "--tls")
	device, waitErr := waitForNVMeDevice(ctx, portal, target, nsid)
	if waitErr != nil {
		if connectErr != nil {
			return connection{}, status.Errorf(codes.Internal, "NVMe connect failed: %v", connectErr)
		}
		return connection{}, status.Error(codes.Internal, waitErr.Error())
	}
	return connection{protocol: "nvme-tcp", target: target, portal: portal, lun: nsid, device: device}, nil
}

func (node *Node) credential(ctx context.Context, values map[string]string) (string, error) {
	if credential := values["credential"]; credential != "" {
		return credential, nil
	}
	volumeID := values["volume_id"]
	if volumeID == "" {
		return "", status.Error(codes.InvalidArgument, "publish_context volume_id is required to fetch the credential")
	}
	endpoint := values["endpoint"]
	if endpoint == "" {
		endpoint = node.agentEndpoint
	}
	if endpoint == "" || node.agentTLS == nil {
		return "", status.Error(codes.FailedPrecondition, "node agent TLS client is not configured")
	}
	client := node.agent
	if client == nil || endpoint != node.agentEndpoint {
		dialed, err := dialHost(endpoint, node.agentTLS)
		if err != nil {
			return "", status.Errorf(codes.Unavailable, "dial agent %s: %v", endpoint, err)
		}
		defer func() { _ = dialed.close() }()
		client = dialed
	}
	response, err := client.volume.GetCredential(ctx, &koujav1.GetCredentialRequest{
		VolumeId: volumeID, Owner: values["owner"],
	})
	if err != nil {
		return "", status.Errorf(codes.FailedPrecondition, "fetch volume credential: %v", err)
	}
	if response.GetCredential() == "" {
		return "", status.Error(codes.FailedPrecondition, "agent returned an empty credential")
	}
	return response.GetCredential(), nil
}

func waitForNVMeDevice(ctx context.Context, portal, target, nsid string) (string, error) {
	candidates := []string{
		"/dev/disk/by-path/nvme-tcp-" + portal + "-nvme-" + target + "-ns-" + nsid,
		"/dev/disk/by-path/nvme-tcp-" + portal + "-nvme-" + target,
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		for _, candidate := range candidates {
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
		if device := nvmeSysfsDevice(target, nsid); device != "" {
			return device, nil
		}
		if device := nvmeSubsysDevice(ctx, target, nsid); device != "" {
			return device, nil
		}
		if device := nvmeListItem(ctx, target); device != "" {
			return device, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("NVMe device did not appear for %s on %s", target, portal)
}

// nvmeSysfsDevice uses the controller's authoritative sysfs attributes rather
// than parsing nvme-cli output. The JSON list output changed across nvme-cli
// releases and does not always include the subsystem NQN.
func nvmeSysfsDevice(target, nsid string) string {
	entries, err := os.ReadDir("/sys/class/nvme")
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "nvme") {
			continue
		}
		subsysnqn, err := os.ReadFile("/sys/class/nvme/" + name + "/subsysnqn") //nolint:gosec // kernel-generated controller name
		if err != nil || strings.TrimSpace(string(subsysnqn)) != target {
			continue
		}
		device := "/dev/" + name + "n" + nsid
		if _, err := os.Stat(device); err == nil {
			return device
		}
	}
	return ""
}

func nvmeSubsysDevice(ctx context.Context, target, nsid string) string {
	output, err := commandOutput(ctx, "nvme", "list-subsys")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if !strings.Contains(line, target) {
			continue
		}
		matches := nvmeController.FindStringSubmatch(line)
		if len(matches) != 2 {
			continue
		}
		device := "/dev/" + matches[1] + "n" + nsid
		if _, err := os.Stat(device); err == nil {
			return device
		}
	}
	return ""
}

var nvmeController = regexp.MustCompile(`\bnvme\d+\b`)

// nvmeListOutput is used as a fallback on nvme-cli versions that expose the
// namespace path in JSON rather than in list-subsys text.
type nvmeListOutput struct {
	Devices []struct {
		DevicePath   string `json:"DevicePath"`
		SubsystemNQN string `json:"SubsystemNQN"`
	} `json:"Devices"`
}

func nvmeListItem(ctx context.Context, target string) string {
	output, err := commandOutput(ctx, "nvme", "list", "-o", "json")
	if err != nil {
		return ""
	}
	var list nvmeListOutput
	if json.Unmarshal([]byte(output), &list) != nil {
		return ""
	}
	for _, device := range list.Devices {
		if device.SubsystemNQN == target {
			return device.DevicePath
		}
	}
	return ""
}

var iscsiInitiatorPath = "/etc/iscsi/initiatorname.iscsi"

func writeInitiator(value string) error {
	path := iscsiInitiatorPath
	if err := os.MkdirAll("/etc/iscsi", 0o750); err != nil {
		return fmt.Errorf("create /etc/iscsi: %w", err)
	}
	return os.WriteFile(path, []byte("InitiatorName="+value+"\n"), 0o600) //nolint:gosec // target path is fixed
}

func ensureFilesystem(ctx context.Context, device, fsType string) error {
	output, err := commandOutput(ctx, "blkid", "-o", "value", "-s", "TYPE", device)
	if err == nil && strings.TrimSpace(output) != "" {
		return nil
	}
	switch fsType {
	case "ext4", "xfs":
		return run(ctx, "mkfs."+fsType, "-F", device)
	default:
		return fmt.Errorf("unsupported filesystem %q", fsType)
	}
}

var attachedDisk = regexp.MustCompile(`Attached scsi disk\s+(\S+)`)

// ensureDeviceNode makes new block devices visible inside kind's /dev tmpfs.
// On a normal host the node already exists and this is a no-op.
func ensureDeviceNode(name string) error {
	path := "/dev/" + name
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	value, err := os.ReadFile("/sys/block/" + name + "/dev") //nolint:gosec // kernel-generated device name
	if err != nil {
		return err
	}
	var major, minor uint32
	if _, err := fmt.Sscanf(strings.TrimSpace(string(value)), "%d:%d", &major, &minor); err != nil {
		return fmt.Errorf("parse device number for %s: %w", name, err)
	}
	//nolint:gosec // Linux dev_t is represented in an int
	if err := unix.Mknod(path, unix.S_IFBLK|0o600, int(unix.Mkdev(major, minor))); err != nil {
		return fmt.Errorf("create device node %s: %w", path, err)
	}
	return nil
}

func waitForISCSIDevice(ctx context.Context, portal, target, lun string) (string, error) {
	byPath := "/dev/disk/by-path/ip-" + portal + "-iscsi-" + target + "-lun-" + lun
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(byPath); err == nil {
			return byPath, nil
		}
		output, _ := commandOutput(ctx, "iscsiadm", "-m", "session", "-P", "3")
		if matches := attachedDisk.FindStringSubmatch(output); len(matches) == 2 {
			device := "/dev/" + matches[1]
			if err := ensureDeviceNode(matches[1]); err == nil {
				return device, nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return "", fmt.Errorf("iSCSI device did not appear for %s on %s", target, portal)
}

func isMounted(ctx context.Context, target string) (bool, error) {
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	command := exec.CommandContext(ctx, "mountpoint", "-q", target) //nolint:gosec // fixed command and validated path
	err := command.Run()
	if err == nil {
		return true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return false, nil
	}
	return false, err
}

func run(ctx context.Context, command string, args ...string) error {
	cmd := exec.CommandContext(ctx, command, args...) //nolint:gosec // fixed command names and explicit argv
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w: %s", command, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func commandOutput(ctx context.Context, command string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, command, args...) //nolint:gosec // fixed command names and explicit argv
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s: %w: %s", command, err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}
