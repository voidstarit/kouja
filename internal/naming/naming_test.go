package naming

import (
	"errors"
	"testing"
)

func TestValidators(t *testing.T) {
	validators := NewValidators()
	for _, name := range []string{"a", "admin", "bootstrap-admin", "a.b_c"} {
		if err := validators.CertificateName(name); err != nil {
			t.Fatalf("certificate %q: %v", name, err)
		}
		if err := validators.VolumeGroupName(name); err != nil {
			t.Fatalf("vg %q: %v", name, err)
		}
		if err := validators.StorageClassName(name); err != nil {
			t.Fatalf("class %q: %v", name, err)
		}
	}
	if err := validators.CertificateName("-bad"); !errors.Is(err, ErrInvalidCertificateName) {
		t.Fatalf("certificate error = %v", err)
	}
	if err := validators.VolumeGroupName("bad/name"); !errors.Is(err, ErrInvalidVolumeGroupName) {
		t.Fatalf("vg error = %v", err)
	}
	if err := validators.StorageClassName(""); !errors.Is(err, ErrInvalidStorageClassName) {
		t.Fatalf("class error = %v", err)
	}
}
