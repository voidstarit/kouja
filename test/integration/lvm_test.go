//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voidstarit/kouja/internal/lvm"
)

func requireCommand(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("%s is not installed", name)
	}
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	command := exec.Command(name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}

func TestCommandBackendLifecycle(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("integration test requires root")
	}
	for _, command := range []string{"lvs", "vgs", "pvcreate", "vgcreate", "vgremove", "pvremove", "losetup", "blockdev", "udevadm", "findmnt"} {
		requireCommand(t, command)
	}

	image := filepath.Join(t.TempDir(), "lvm.img")
	run(t, "truncate", "-s", "1G", image)
	loop := run(t, "losetup", "--find", "--show", image)
	vg := fmt.Sprintf("kouja-it-%d", os.Getpid())
	lv := fmt.Sprintf("kouja-test-%d", time.Now().UnixNano())

	t.Cleanup(func() {
		_ = exec.Command("vgremove", "-f", vg).Run()
		_ = exec.Command("pvremove", "-f", loop).Run()
		_ = exec.Command("losetup", "-d", loop).Run()
		_ = os.Remove(image)
	})

	run(t, "pvcreate", "-f", loop)
	run(t, "vgcreate", vg, loop)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	backend := lvm.New()
	if err := backend.Eligibility(ctx, vg); err != nil {
		t.Fatalf("Eligibility: %v", err)
	}
	capacity, err := backend.Capacity(ctx, vg)
	if err != nil {
		t.Fatalf("Capacity: %v", err)
	}
	if capacity.TotalBytes <= 0 || capacity.FreeBytes <= 0 || capacity.ExtentSizeBytes <= 0 || capacity.VGState != "active" {
		t.Fatalf("unexpected capacity: %#v", capacity)
	}

	created, err := backend.Create(ctx, vg, lv, 64<<20, map[string]string{
		"kouja.managed": "true",
		"kouja.owner":   "owner-before",
		"kouja.volume":  "resource-1",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.SizeBytes < 64<<20 || created.DevicePath == "" {
		t.Fatalf("unexpected created LV: %#v", created)
	}
	if err := backend.SetOwnerTag(ctx, vg, lv, "owner-after"); err != nil {
		t.Fatalf("SetOwnerTag: %v", err)
	}
	observed, err := backend.Observe(ctx, vg, lv)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if observed.Tags["kouja.owner"] != "owner-after" || observed.Tags["kouja.managed"] != "true" {
		t.Fatalf("unexpected tags after owner update: %#v", observed.Tags)
	}
	managed, err := backend.ListManaged(ctx)
	if err != nil {
		t.Fatalf("ListManaged: %v", err)
	}
	found := false
	for _, item := range managed {
		if item.VG == vg && item.Name == lv {
			found = true
		}
	}
	if !found {
		t.Fatalf("created LV missing from ListManaged: %#v", managed)
	}
	if err := backend.Remove(ctx, vg, lv); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := backend.Observe(ctx, vg, lv); !errors.Is(err, lvm.ErrNotFound) {
		t.Fatalf("Observe after Remove = %v, want ErrNotFound", err)
	}
}
