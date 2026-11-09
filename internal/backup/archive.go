package backup

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

func createArchive(destination string, files map[string]string, dirs map[string]string) error {
	if destination == "" {
		return fmt.Errorf("%w: archive destination is required", ErrInvalid)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("backup: create archive directory: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(destination), ".kouja-archive-*")
	if err != nil {
		return fmt.Errorf("backup: create archive: %w", err)
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	gzipWriter := gzip.NewWriter(temp)
	tarWriter := tar.NewWriter(gzipWriter)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := addArchiveFile(tarWriter, name, files[name]); err != nil {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			_ = temp.Close()
			return err
		}
	}
	dirNames := make([]string, 0, len(dirs))
	for name := range dirs {
		dirNames = append(dirNames, name)
	}
	sort.Strings(dirNames)
	for _, name := range dirNames {
		if err := addArchiveDir(tarWriter, name, dirs[name]); err != nil {
			_ = tarWriter.Close()
			_ = gzipWriter.Close()
			_ = temp.Close()
			return err
		}
	}
	if err := tarWriter.Close(); err != nil {
		_ = gzipWriter.Close()
		_ = temp.Close()
		return err
	}
	if err := gzipWriter.Close(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, destination); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(destination)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func addArchiveFile(writer *tar.Writer, archiveName, source string) error {
	if source == "" {
		return nil
	}
	archiveName = path.Clean(archiveName)
	if archiveName == "." || archiveName == ".." || strings.HasPrefix(archiveName, "../") {
		return fmt.Errorf("%w: unsafe archive name %q", ErrInvalid, archiveName)
	}
	file, err := os.Open(source) //nolint:gosec // operator-configured backup input
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("backup: open archive input %q: %w", source, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("backup: archive input %q is not a regular file", source)
	}
	header := &tar.Header{
		Name: path.Clean(archiveName), Mode: 0o600, Size: info.Size(),
		ModTime: info.ModTime(), Typeflag: tar.TypeReg,
	}
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	if _, err := io.Copy(writer, file); err != nil {
		return err
	}
	return nil
}

func addArchiveDir(writer *tar.Writer, archiveName, source string) error {
	if source == "" {
		return nil
	}
	archiveName = path.Clean(archiveName)
	if archiveName == "." || archiveName == ".." || strings.HasPrefix(archiveName, "../") {
		return fmt.Errorf("%w: unsafe archive name %q", ErrInvalid, archiveName)
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("backup: open archive directory %q: %w", source, err)
	}
	defer func() { _ = root.Close() }()
	return filepath.Walk(source, func(filePath string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(source, filePath)
		if err != nil {
			return err
		}
		file, err := root.Open(relative)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		header := &tar.Header{
			Name: path.Join(path.Clean(archiveName), filepath.ToSlash(relative)),
			Mode: 0o600, Size: info.Size(), ModTime: info.ModTime(), Typeflag: tar.TypeReg,
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if _, err := io.Copy(writer, file); err != nil {
			return err
		}
		return nil
	})
}
