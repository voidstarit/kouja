package fsutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileAtomicWritesAndReplaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := WriteFileAtomic(path, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	if err := WriteFileAtomic(path, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path) //nolint:gosec // temporary test path
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second\n" {
		t.Fatalf("data = %q", data)
	}
}

func TestWriteFileAtomicRejectsEmptyPathAndRenamesDirectory(t *testing.T) {
	if err := WriteFileAtomic("", []byte("x"), 0o600); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty path error = %v", err)
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "child"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(target, []byte("x"), 0o600); err == nil {
		t.Fatal("rename over a non-empty directory succeeded")
	}
}

func TestWriteFileAtomicCreateTempFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil { //nolint:gosec // deliberate permission test
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }() //nolint:gosec // restore test directory
	if err := WriteFileAtomic(filepath.Join(dir, "file"), []byte("x"), 0o600); err == nil {
		t.Fatal("write into read-only directory succeeded")
	}
}

func TestWriteFileAtomicMissingParent(t *testing.T) {
	if err := WriteFileAtomic(filepath.Join(t.TempDir(), "missing", "file"), []byte("x"), 0o600); err == nil {
		t.Fatal("write into missing parent succeeded")
	}
}
