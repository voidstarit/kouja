package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/voidstarit/kouja/internal/config"
)

// The target stacks are probed through configfs because the kernel support may
// be built in rather than loaded as modules. Built-in modules do not
// necessarily appear under /sys/module, while the configfs fabric directories
// exist in both cases. Tests override the roots.
var (
	lioConfigFSRoot   = "/sys/kernel/config/target"
	nvmetConfigFSRoot = "/sys/kernel/config/nvmet"
)

// CheckDependencies verifies the host-side prerequisites before the agent
// starts serving. It intentionally fails closed: an incomplete target stack
// must not expose a partially working API.
func CheckDependencies(ctx context.Context, cfg *config.Config) error {
	for _, binary := range []string{"lvs", "vgs", "pvs", "lvcreate", "lvremove", "lvchange", "udevadm", "findmnt", "blockdev"} {
		if _, err := exec.LookPath(binary); err != nil {
			return fmt.Errorf("required LVM/host tool %q is missing", binary)
		}
	}
	if cfg.Targets.ISCSI.Enabled {
		// Requiring /sys/module/target_core_mod and friends rejects kernels
		// that build LIO in (CONFIG_TARGET_CORE=y, CONFIG_ISCSI_TARGET=y)
		// because built-in modules need not have a /sys/module entry. The
		// configfs directories are only created once target core, the
		// backstore layer, and the iSCSI fabric have registered, so they are
		// the authoritative availability signal for either build.
		if err := requirePaths(lioConfigFSRoot, []requiredPath{
			{relative: "", description: "LIO configfs"},
			{relative: "core", description: "LIO backstore core"},
			{relative: "iscsi", description: "iSCSI target fabric"},
		}); err != nil {
			return err
		}
	}
	if cfg.Targets.NVMeTCP.Enabled {
		// Apply the same built-in-versus-modular rule to nvmet.
		if err := requirePaths(nvmetConfigFSRoot, []requiredPath{
			{relative: "", description: "nvmet configfs"},
			{relative: "ports", description: "nvmet port fabric"},
			{relative: "subsystems", description: "nvmet subsystem fabric"},
		}); err != nil {
			return err
		}
		if err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", cfg.Targets.NVMeTCP.TLS.TLSHDService).Run(); err != nil { //nolint:gosec // configured service name, explicit argv
			return fmt.Errorf("TLS handshake service %q is not active", cfg.Targets.NVMeTCP.TLS.TLSHDService)
		}
	}
	return nil
}

type requiredPath struct {
	relative    string
	description string
}

func requirePaths(root string, paths []requiredPath) error {
	for _, path := range paths {
		if err := requirePath(filepath.Join(root, path.relative), path.description); err != nil {
			return err
		}
	}
	return nil
}

func requirePath(path, description string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("%s is unavailable at %s: %w", description, path, err)
	}
	return nil
}
