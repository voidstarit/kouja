package pki

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var certificateFilePattern = regexp.MustCompile(`^(admin|caller|controller|node)-(.+)-([0-9a-f]{1,40})\.(crt|key)$`)

func (s *Service) ListFiles(_ context.Context) ([]FileInfo, error) {
	if s == nil || s.certDir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(s.certDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("pki: list certificate directory: %w", err)
	}
	files := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		matches := certificateFilePattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		kind := "certificate"
		if matches[4] == "key" {
			kind = "private_key"
		}
		files = append(files, FileInfo{
			Name: entry.Name(), Role: matches[1], Kind: kind, SerialNumber: matches[3],
			SizeBytes: info.Size(), ModifiedAt: info.ModTime(), Managed: true,
		})
	}
	return files, nil
}

func (s *Service) ResolveFile(name string) (string, error) {
	if s == nil || s.certDir == "" {
		return "", ErrNotFound
	}
	if filepath.Base(name) != name || strings.Contains(name, string(filepath.Separator)) {
		return "", ErrNotFound
	}
	if !certificateFilePattern.MatchString(name) {
		return "", ErrNotFound
	}
	path := filepath.Join(s.certDir, name)
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", ErrNotFound
	}
	return path, nil
}
