package agent

import (
	"fmt"
	"strings"
)

// storageClassForVG returns the configured class for an LVM volume group. A
// VG without an explicit class is its own class for compatibility with
// deployments that select storage by VG name.
func storageClassForVG(classes map[string]string, vg string) string {
	for class, mapped := range classes {
		if mapped == vg {
			return class
		}
	}
	return vg
}

// resolveStorageClass maps a requested storage class to its LVM volume group.
// Callers may use either a configured class, a raw VG name, or both as long as
// they agree.
func resolveStorageClass(classes map[string]string, class, vg string) (string, string, error) {
	class = strings.TrimSpace(class)
	vg = strings.TrimSpace(vg)
	if class == "" && vg == "" {
		return "", "", fmt.Errorf("storage_class or vg is required")
	}
	if class == "" {
		if !vgNamePattern.MatchString(vg) {
			return "", "", fmt.Errorf("invalid vg %q", vg)
		}
		return storageClassForVG(classes, vg), vg, nil
	}
	if !vgNamePattern.MatchString(class) {
		return "", "", fmt.Errorf("invalid storage_class %q", class)
	}
	if mapped, ok := classes[class]; ok {
		if vg != "" && vg != mapped {
			return "", "", fmt.Errorf("storage_class %q maps to vg %q, not %q", class, mapped, vg)
		}
		return class, mapped, nil
	}
	if vg != "" && vg != class {
		return "", "", fmt.Errorf("unknown storage_class %q and vg %q do not match", class, vg)
	}
	// A class without a mapping is treated as a raw VG name. This preserves
	// the original API and CSI behavior for deployments without class config.
	return class, class, nil
}
