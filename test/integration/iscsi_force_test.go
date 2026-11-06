//go:build integration

package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/voidstarit/kouja/internal/target"
)

func TestISCSIAdapterForceUnexportActiveSession(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("integration test requires root")
	}
	for _, command := range []string{"pvcreate", "vgcreate", "vgremove", "pvremove", "losetup", "iscsiadm", "systemctl"} {
		requireCommand(t, command)
	}

	image := filepath.Join(t.TempDir(), "iscsi-force.img")
	run(t, "truncate", "-s", "1G", image)
	loop := run(t, "losetup", "--find", "--show", image)
	vg := "kouja-force-" + time.Now().Format("150405")
	lv := "kouja-force-lv"
	targetName := "iqn.2026-11.io.kouja:force-" + time.Now().Format("150405")
	initiator := "iqn.2026-11.io.kouja:force-initiator"
	portal := "127.0.0.1:3267"

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
	run(t, "lvcreate", "-y", "-L", "256M", "-n", lv, vg)
	if err := os.WriteFile("/etc/iscsi/initiatorname.iscsi", []byte("InitiatorName="+initiator+"\n"), 0o644); err != nil {
		t.Fatalf("write initiator name: %v", err)
	}
	run(t, "systemctl", "restart", "iscsid")

	adapter := target.NewISCSI(target.NewConfigFS("/sys/kernel/config"))
	spec := target.ExportSpec{
		Owner:        "integration",
		VolumeID:     "force-volume",
		ResourceUUID: "force-resource",
		VGName:       vg,
		LVName:       lv,
		Protocol:     "iscsi",
		Initiator:    initiator,
		TargetName:   targetName,
		Portal:       portal,
	}
	credential := target.Credential{Username: "force-user", Secret: "force-secret"}
	if _, err := adapter.Export(context.Background(), spec, credential); err != nil {
		t.Fatalf("Export: %v", err)
	}
	run(t, "iscsiadm", "-m", "node", "-o", "new", "-T", targetName, "-p", portal)
	run(t, "iscsiadm", "-m", "node", "-T", targetName, "-p", portal, "--op", "update", "-n", "node.session.auth.authmethod", "-v", "CHAP")
	run(t, "iscsiadm", "-m", "node", "-T", targetName, "-p", portal, "--op", "update", "-n", "node.session.auth.username", "-v", credential.Username)
	run(t, "iscsiadm", "-m", "node", "-T", targetName, "-p", portal, "--op", "update", "-n", "node.session.auth.password", "-v", credential.Secret)
	run(t, "iscsiadm", "-m", "node", "-T", targetName, "-p", portal, "--login")

	if err := adapter.Unexport(context.Background(), spec, true); err != nil {
		t.Fatalf("force Unexport: %v", err)
	}
	if _, err := os.Stat(filepath.Join("/sys/kernel/config/target/iscsi", targetName)); err == nil {
		t.Fatal("target remains after forced Unexport")
	}
	if err := adapter.Delete(context.Background(), spec, true); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}
