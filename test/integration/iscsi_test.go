//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/voidstarit/kouja/internal/target"
)

func TestISCSIAdapterLifecycle(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("integration test requires root")
	}
	for _, command := range []string{"lvs", "vgs", "pvcreate", "vgcreate", "vgremove", "pvremove", "losetup", "iscsiadm", "systemctl"} {
		requireCommand(t, command)
	}

	image := filepath.Join(t.TempDir(), "iscsi.img")
	run(t, "truncate", "-s", "1G", image)
	loop := run(t, "losetup", "--find", "--show", image)
	vg := fmt.Sprintf("kouja-iscsi-%d", os.Getpid())
	lv := fmt.Sprintf("kouja-iscsi-%d", time.Now().UnixNano())
	targetName := fmt.Sprintf("iqn.2026-11.io.kouja:it-%d", time.Now().UnixNano())
	initiator := fmt.Sprintf("iqn.2026-11.io.kouja:initiator-%d", os.Getpid())
	portal := "127.0.0.1:3266"

	origInitiator, _ := os.ReadFile("/etc/iscsi/initiatorname.iscsi")
	t.Cleanup(func() {
		if len(origInitiator) != 0 {
			_ = os.WriteFile("/etc/iscsi/initiatorname.iscsi", origInitiator, 0o644)
			_ = exec.Command("systemctl", "restart", "iscsid").Run()
		}
		_ = exec.Command("iscsiadm", "-m", "node", "-T", targetName, "-p", portal, "--logout").Run()
		_ = exec.Command("iscsiadm", "-m", "node", "-T", targetName, "-p", portal, "-o", "delete").Run()
		_ = exec.Command("targetcli", "/iscsi", "delete", targetName).Run()
		_ = exec.Command("vgremove", "-f", vg).Run()
		_ = exec.Command("pvremove", "-f", loop).Run()
		_ = exec.Command("losetup", "-d", loop).Run()
		_ = os.Remove(image)
	})

	run(t, "pvcreate", "-f", loop)
	run(t, "vgcreate", vg, loop)
	run(t, "lvcreate", "-y", "-L", "512M", "-n", lv, vg)

	if err := os.WriteFile("/etc/iscsi/initiatorname.iscsi", []byte("InitiatorName="+initiator+"\n"), 0o644); err != nil {
		t.Fatalf("write initiator name: %v", err)
	}
	run(t, "systemctl", "restart", "iscsid")

	adapter := target.NewISCSI(target.NewConfigFS("/sys/kernel/config"))
	spec := target.ExportSpec{
		Owner:        "integration",
		VolumeID:     "volume-1",
		ResourceUUID: fmt.Sprintf("it-%d", time.Now().UnixNano()),
		VGName:       vg,
		LVName:       lv,
		Protocol:     "iscsi",
		Initiator:    initiator,
		TargetName:   targetName,
		Portal:       portal,
	}
	credential := target.Credential{Username: "kouja-it-user", Secret: "kouja-it-secret"}

	result, err := adapter.Export(context.Background(), spec, credential)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if result.TargetName != targetName || result.LUN != 0 {
		t.Fatalf("Export result: %#v", result)
	}

	run(t, "iscsiadm", "-m", "node", "-o", "new", "-T", targetName, "-p", portal)
	run(t, "iscsiadm", "-m", "node", "-T", targetName, "-p", portal, "--op", "update", "-n", "node.session.auth.authmethod", "-v", "CHAP")
	run(t, "iscsiadm", "-m", "node", "-T", targetName, "-p", portal, "--op", "update", "-n", "node.session.auth.username", "-v", credential.Username)
	run(t, "iscsiadm", "-m", "node", "-T", targetName, "-p", portal, "--op", "update", "-n", "node.session.auth.password", "-v", credential.Secret)
	run(t, "iscsiadm", "-m", "node", "-T", targetName, "-p", portal, "--login")

	deadline := time.Now().Add(10 * time.Second)
	var observation target.ExportObservation
	for time.Now().Before(deadline) {
		observation, err = adapter.Observe(context.Background(), spec)
		if err != nil {
			t.Fatalf("Observe: %v", err)
		}
		if observation.SessionState == target.SessionActive {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if observation.SessionState != target.SessionActive {
		t.Fatalf("session did not become active: %#v", observation)
	}
	if err := adapter.Unexport(context.Background(), spec, false); !errors.Is(err, target.ErrSessionActive) {
		t.Fatalf("Unexport with active session = %v, want ErrSessionActive", err)
	}

	run(t, "iscsiadm", "-m", "node", "-T", targetName, "-p", portal, "--logout")
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		observation, err = adapter.Observe(context.Background(), spec)
		if err == nil && observation.SessionState == target.SessionNone {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if observation.SessionState != target.SessionNone {
		t.Fatalf("session did not become none: %#v", observation)
	}
	if err := adapter.Unexport(context.Background(), spec, false); err != nil {
		t.Fatalf("Unexport: %v", err)
	}
	if err := adapter.Delete(context.Background(), spec, false); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join("/sys/kernel/config/target/iscsi", targetName)); err == nil {
		t.Fatal("target remains after Delete")
	}
	if _, err := os.Stat(filepath.Join("/sys/kernel/config/target/core", "iblock_0", lv)); err == nil {
		t.Fatal("backstore remains after Delete")
	}
}
