package config

// StorageClassForVG returns the configured storage-class name for a volume
// group. A VG without an explicit class is its own class for compatibility
// with deployments that select storage by VG name.
func StorageClassForVG(classes map[string]string, vg string) string {
	for class, mapped := range classes {
		if mapped == vg {
			return class
		}
	}
	return vg
}
