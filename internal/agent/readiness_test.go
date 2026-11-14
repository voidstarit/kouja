package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/voidstarit/kouja/internal/config"
)

func installFakeHostTools(t testing.TB) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"lvs", "vgs", "pvs", "lvcreate", "lvremove", "lvchange", "udevadm", "findmnt", "blockdev", "systemctl"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // executable test fixture
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

func overrideConfigFSRoots(t testing.TB, root string) {
	t.Helper()
	previousLIO, previousNVMe := lioConfigFSRoot, nvmetConfigFSRoot
	lioConfigFSRoot = filepath.Join(root, "target")
	nvmetConfigFSRoot = filepath.Join(root, "nvmet")
	t.Cleanup(func() {
		lioConfigFSRoot, nvmetConfigFSRoot = previousLIO, previousNVMe
	})
}

// A kernel with CONFIG_TARGET_CORE=y and CONFIG_NVME_TARGET=y does not
// necessarily expose target_core_mod or nvmet under /sys/module. The configfs
// fabric directories are the authoritative signal for both build styles.
func TestCheckDependenciesAcceptsBuiltInTargetSupport(t *testing.T) {
	installFakeHostTools(t)
	root := t.TempDir()
	for _, dir := range []string{"target/core", "target/iscsi", "nvmet/ports", "nvmet/subsystems"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	overrideConfigFSRoots(t, root)

	cfg := config.Default()
	cfg.Targets.ISCSI.Enabled = true
	cfg.Targets.NVMeTCP.Enabled = true
	if err := CheckDependencies(context.Background(), &cfg); err != nil {
		t.Fatalf("CheckDependencies rejected built-in target support: %v", err)
	}
}

func TestCheckDependenciesRejectsMissingConfigFSFabric(t *testing.T) {
	cases := []struct {
		name        string
		iscsi       bool
		nvme        bool
		directories []string
		want        string
	}{
		{
			name:  "missing LIO configfs",
			iscsi: true,
			want:  "LIO configfs",
		},
		{
			name:        "missing iSCSI fabric",
			iscsi:       true,
			directories: []string{"target/core"},
			want:        "iSCSI target fabric",
		},
		{
			name:        "missing nvmet fabric",
			nvme:        true,
			directories: []string{"nvmet/ports"},
			want:        "nvmet subsystem fabric",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			installFakeHostTools(t)
			root := t.TempDir()
			for _, dir := range testCase.directories {
				if err := os.MkdirAll(filepath.Join(root, dir), 0o750); err != nil {
					t.Fatal(err)
				}
			}
			overrideConfigFSRoots(t, root)

			cfg := config.Default()
			cfg.Targets.ISCSI.Enabled = testCase.iscsi
			cfg.Targets.NVMeTCP.Enabled = testCase.nvme
			err := CheckDependencies(context.Background(), &cfg)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("CheckDependencies error = %v, want %q", err, testCase.want)
			}
		})
	}
}
