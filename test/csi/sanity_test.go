//go:build csi_sanity

package csi_sanity

import (
	"os"
	"os/exec"
	"testing"
)

// TestSanity runs the upstream csi-sanity binary when CSI_ENDPOINT is set.
// It is intentionally separate from the unit suite because csi-sanity needs a
// live CSI socket and, for node tests, a staging directory.
func bddTestSanity(t testing.TB) {
	endpoint := os.Getenv("CSI_ENDPOINT")
	if endpoint == "" {
		t.Skip("set CSI_ENDPOINT to a live CSI socket")
	}
	args := []string{"-csi.endpoint", endpoint}
	if staging := os.Getenv("CSI_STAGING_DIR"); staging != "" {
		args = append(args, "-csi.stagingdir", staging)
	}
	if target := os.Getenv("CSI_TARGET_DIR"); target != "" {
		args = append(args, "-csi.mountdir", target)
	}
	binary := os.Getenv("CSI_SANITY_BIN")
	if binary == "" {
		binary = "csi-sanity"
	}
	command := exec.Command(binary, args...) //nolint:gosec // operator-provided test binary
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("csi-sanity: %v\n%s", err, output)
	}
	t.Log(string(output))
}
