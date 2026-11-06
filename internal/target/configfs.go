package target

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrNotFound       = errors.New("target: not found")
	ErrInvalid        = errors.New("target: invalid argument")
	ErrConflict       = errors.New("target: conflict")
	ErrSessionActive  = errors.New("target: session is active")
	ErrSessionUnknown = errors.New("target: session state is unknown")
)

// FileSystem is the configfs surface used by the target adapters. The
// production implementation is ConfigFS; tests may substitute a fake tree.
type FileSystem interface {
	Mkdir(rel string) error
	Write(rel, value string) error
	ReadFile(rel string) (string, error)
	Symlink(targetRel, linkRel string) error
	Exists(rel string) bool
	ReadDir(rel string) ([]os.DirEntry, error)
	Remove(rel string) error
}

// ConfigFS provides the safe directory, attribute, and symlink operations used
// by the target adapters. Root is /sys/kernel/config in production and a
// temporary directory in unit tests.
type ConfigFS struct {
	root string
}

func NewConfigFS(root string) *ConfigFS {
	if root == "" {
		root = "/sys/kernel/config"
	}
	return &ConfigFS{root: filepath.Clean(root)}
}

func (fs *ConfigFS) Root() string {
	return fs.root
}

func (fs *ConfigFS) resolve(rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w: configfs path %q must be relative", ErrInvalid, rel)
	}
	clean := filepath.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: configfs path %q escapes the root", ErrInvalid, rel)
	}
	return filepath.Join(fs.root, clean), nil
}

func (fs *ConfigFS) Path(rel string) (string, error) {
	return fs.resolve(rel)
}

func (fs *ConfigFS) Mkdir(rel string) error {
	path, err := fs.resolve(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(path, 0o750); err != nil {
		return fmt.Errorf("target: create configfs directory %q: %w", rel, err)
	}
	return nil
}

func (fs *ConfigFS) Write(rel, value string) error {
	path, err := fs.resolve(rel)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		return fmt.Errorf("target: write configfs attribute %q: %w", rel, err)
	}
	return nil
}

func (fs *ConfigFS) ReadFile(rel string) (string, error) {
	path, err := fs.resolve(rel)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path) //nolint:gosec // path is validated relative to the configfs root
	if err != nil {
		return "", fmt.Errorf("target: read configfs attribute %q: %w", rel, err)
	}
	return string(data), nil
}

func (fs *ConfigFS) Symlink(targetRel, linkRel string) error {
	targetPath, err := fs.resolve(targetRel)
	if err != nil {
		return err
	}
	linkPath, err := fs.resolve(linkRel)
	if err != nil {
		return err
	}
	if existing, err := os.Readlink(linkPath); err == nil {
		if existing == targetPath || filepath.Clean(filepath.Join(filepath.Dir(linkPath), existing)) == filepath.Clean(targetPath) {
			return nil
		}
		return fmt.Errorf("%w: configfs link %q points to %q, want %q", ErrConflict, linkRel, existing, targetPath)
	}
	if err := os.Symlink(targetPath, linkPath); err != nil {
		return fmt.Errorf("target: create configfs link %q: %w", linkRel, err)
	}
	return nil
}

func (fs *ConfigFS) Exists(rel string) bool {
	path, err := fs.resolve(rel)
	if err != nil {
		return false
	}
	_, err = os.Lstat(path)
	return err == nil
}

func (fs *ConfigFS) ReadDir(rel string) ([]os.DirEntry, error) {
	path, err := fs.resolve(rel)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("target: list configfs directory %q: %w", rel, err)
	}
	return entries, nil
}

// Remove removes a file or an empty directory. A missing path is not an error
// so teardown can be retried safely.
func (fs *ConfigFS) Remove(rel string) error {
	path, err := fs.resolve(rel)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("target: remove configfs object %q: %w", rel, err)
	}
	return nil
}
