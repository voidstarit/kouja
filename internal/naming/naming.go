// Package naming holds the shared name validators used by configuration,
// the agent, the admin UI, and the Terraform provider.
package naming

import (
	"errors"
	"fmt"
	"regexp"
)

var (
	ErrInvalidCertificateName  = errors.New("naming: invalid certificate name")
	ErrInvalidVolumeGroupName  = errors.New("naming: invalid volume group name")
	ErrInvalidStorageClassName = errors.New("naming: invalid storage class name")
)

var (
	certificateNamePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	volumeGroupNamePattern  = regexp.MustCompile(`^[A-Za-z0-9+_.-]{1,127}$`)
	storageClassNamePattern = regexp.MustCompile(`^[A-Za-z0-9+_.-]{1,127}$`)
)

// Validators exposes per-kind methods so callers cannot accidentally use the
// VG/class regex for a certificate name or the reverse.
type Validators struct{}

// NewValidators returns a stateless validator set.
func NewValidators() *Validators { return &Validators{} }

func (v *Validators) CertificateName(name string) error {
	if !certificateNamePattern.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrInvalidCertificateName, name)
	}
	return nil
}

func (v *Validators) VolumeGroupName(name string) error {
	if !volumeGroupNamePattern.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrInvalidVolumeGroupName, name)
	}
	return nil
}

func (v *Validators) StorageClassName(name string) error {
	if !storageClassNamePattern.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrInvalidStorageClassName, name)
	}
	return nil
}
