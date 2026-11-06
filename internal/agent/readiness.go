package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/voidstarit/kouja/internal/config"
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
		if err := requirePath("/sys/kernel/config/target", "LIO configfs"); err != nil {
			return err
		}
		for _, module := range []string{"target_core_mod", "target_core_iblock", "iscsi_target_mod"} {
			if err := requirePath("/sys/module/"+module, "kernel module "+module); err != nil {
				return err
			}
		}
	}
	if cfg.Targets.NVMeTCP.Enabled {
		if err := requirePath("/sys/kernel/config/nvmet", "nvmet configfs"); err != nil {
			return err
		}
		for _, module := range []string{"nvmet", "nvmet_tcp"} {
			if err := requirePath("/sys/module/"+module, "kernel module "+module); err != nil {
				return err
			}
		}
		if err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", cfg.Targets.NVMeTCP.TLS.TLSHDService).Run(); err != nil { //nolint:gosec // configured service name, explicit argv
			return fmt.Errorf("TLS handshake service %q is not active", cfg.Targets.NVMeTCP.TLS.TLSHDService)
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
