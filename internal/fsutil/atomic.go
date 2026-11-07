package fsutil

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrDurabilityUncertain means the file was renamed into place successfully,
// but syncing the containing directory failed. The new contents are visible;
// callers may treat this as success with a warning.
var ErrDurabilityUncertain = errors.New("fsutil: file renamed but directory sync failed")

// WriteFileAtomic writes data to a temporary file in the destination directory,
// syncs it, renames it over path, and syncs the directory. The temporary file
// is removed on failure. The caller is responsible for validating path and for
// any file locking.
func WriteFileAtomic(path string, data []byte, mode fs.FileMode) error {
	if path == "" {
		return fmt.Errorf("fsutil: empty destination path")
	}
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("fsutil: create temporary file: %w", err)
	}
	tempName := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempName)
	}
	fail := func(operation string, err error) error {
		cleanup()
		return fmt.Errorf("fsutil: %s: %w", operation, err)
	}
	if err := temp.Chmod(mode); err != nil {
		return fail("chmod temporary file", err)
	}
	if _, err := temp.Write(data); err != nil {
		return fail("write temporary file", err)
	}
	if err := temp.Sync(); err != nil {
		return fail("sync temporary file", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempName)
		return fmt.Errorf("fsutil: close temporary file: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		_ = os.Remove(tempName)
		return fmt.Errorf("fsutil: rename temporary file: %w", err)
	}
	directory, err := os.Open(dir) //nolint:gosec // destination directory is validated by the caller
	if err != nil {
		return fmt.Errorf("%w: open directory: %v", ErrDurabilityUncertain, err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return fmt.Errorf("%w: %v", ErrDurabilityUncertain, syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("%w: close directory: %v", ErrDurabilityUncertain, closeErr)
	}
	return nil
}
