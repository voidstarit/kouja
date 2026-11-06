//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voidstarit/kouja/internal/credential"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
)

func bddTestNVMeTLSAdapterLifecycle(t testing.TB) {
	if os.Geteuid() != 0 {
		t.Skip("integration test requires root")
	}
	for _, command := range []string{"lvs", "vgs", "pvcreate", "vgcreate", "vgremove", "pvremove", "losetup", "nvme", "systemctl", "tlshd", "modprobe"} {
		requireCommand(t, command)
	}
	run(t, "modprobe", "nvmet")
	run(t, "modprobe", "nvmet_tcp")
	if err := exec.Command("systemctl", "restart", "tlshd").Run(); err != nil {
		t.Skip("tlshd is not active")
	}
	hostNQN, err := os.ReadFile("/etc/nvme/hostnqn")
	if err != nil {
		t.Skip("host NQN is not configured")
	}
	image := filepath.Join(t.TempDir(), "nvme.img")
	run(t, "truncate", "-s", "1G", image)
	loop := run(t, "losetup", "--find", "--show", image)
	vg := fmt.Sprintf("kouja-nvme-%d", os.Getpid())
	lv := fmt.Sprintf("kouja-nvme-lv-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_ = exec.Command("nvme", "disconnect", "-n", "nqn.2026-11.io.kouja:it-nvme").Run()
		_ = exec.Command("vgremove", "-f", vg).Run()
		_ = exec.Command("pvremove", "-f", loop).Run()
		_ = exec.Command("losetup", "-d", loop).Run()
		_ = os.Remove(image)
	})
	run(t, "pvcreate", "-f", loop)
	run(t, "vgcreate", vg, loop)
	run(t, "lvcreate", "-y", "-L", "256M", "-n", lv, vg)

	store, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	credentials, err := credential.Open(store, filepath.Join(t.TempDir(), "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := credentials.Generate(context.Background(), "integration", "nvme-volume", "resource", credential.ProtocolNVMeTCP)
	if err != nil {
		t.Fatal(err)
	}
	spec := target.ExportSpec{
		Owner: "integration", VolumeID: "nvme-volume", ResourceUUID: "resource",
		VGName: vg, LVName: lv, Protocol: "nvme-tcp", Initiator: strings.TrimSpace(string(hostNQN)),
		TargetName: "nqn.2026-11.io.kouja:it-nvme", Portal: "127.0.0.1:4422",
	}
	adapter := target.NewNVMe(target.NewConfigFS("/sys/kernel/config"))
	if _, err := adapter.Export(context.Background(), spec, target.Credential{}); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if err := credentials.Install(context.Background(), spec, stored); err != nil {
		t.Fatalf("Install key: %v", err)
	}
	run(t, "nvme", "connect", "-t", "tcp", "-a", "127.0.0.1", "-s", "4422",
		"-n", spec.TargetName, "--hostnqn", spec.Initiator, "--tls")
	defer func() {
		_ = adapter.Unexport(context.Background(), spec, true)
	}()
	device := ""
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		matches, _ := filepath.Glob("/dev/nvme*n1")
		if len(matches) > 0 {
			device = matches[0]
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if device == "" {
		t.Fatal("NVMe/TCP namespace did not appear")
	}
	run(t, "dd", "if=/dev/zero", "of="+device, "bs=1M", "count=8", "conv=fsync", "status=none")
	run(t, "dd", "if="+device, "of=/dev/null", "bs=1M", "count=8", "status=none")
	run(t, "nvme", "disconnect", "-n", spec.TargetName)
	if err := adapter.Unexport(context.Background(), spec, true); err != nil {
		t.Fatalf("Unexport: %v", err)
	}
	if _, err := os.Stat(filepath.Join("/sys/kernel/config/target/nvmet/subsystems", spec.TargetName)); err == nil {
		t.Fatal("NVMe subsystem remains after Unexport")
	}
}
