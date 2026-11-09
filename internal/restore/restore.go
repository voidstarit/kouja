// Package restore extracts a kouja backup bundle and places its contents at
// the paths described by the archived configuration.
package restore

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/voidstarit/kouja/internal/backup"
	"github.com/voidstarit/kouja/internal/config"
)

type Options struct {
	Archive        string
	Directory      string
	S3URI          string
	KeyFile        string
	ConfigPath     string
	Apply          bool
	Stdout         io.Writer
	Region         string
	Endpoint       string
	ForcePathStyle bool
}

type FilePlan struct {
	ArchivePath string
	TargetPath  string
	Mode        os.FileMode
}

type backupManifest struct {
	Encrypted bool                   `json:"encrypted"`
	Files     map[string]backupEntry `json:"files"`
}

type backupEntry struct {
	Remote string `json:"remote"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func Run(ctx context.Context, options Options) error {
	sources := 0
	for _, value := range []string{options.Archive, options.Directory, options.S3URI} {
		if value != "" {
			sources++
		}
	}
	if sources != 1 {
		return fmt.Errorf("restore: select exactly one of --archive, --dir, or --s3")
	}
	if options.ConfigPath == "" {
		options.ConfigPath = "/etc/kouja/config.yaml"
	}
	if options.Stdout == nil {
		options.Stdout = io.Discard
	}
	temp, err := os.MkdirTemp("", "kouja-restore-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(temp) }()
	extracted := filepath.Join(temp, "extracted")
	if err := os.MkdirAll(extracted, 0o700); err != nil {
		return err
	}
	switch {
	case options.Archive != "":
		if err := materializeArchive(options.Archive, extracted, temp, options.KeyFile); err != nil {
			return err
		}
	case options.Directory != "":
		if err := materializeDirectory(options.Directory, extracted, temp, options.KeyFile); err != nil {
			return err
		}
	case options.S3URI != "":
		if err := materializeS3(ctx, options, extracted, temp); err != nil {
			return err
		}
	}
	return restoreExtracted(extracted, options)
}

func materializeArchive(archive, extracted, temp, keyFile string) error {
	if encrypted, err := isEncrypted(archive); err != nil {
		return err
	} else if encrypted {
		if keyFile == "" {
			return fmt.Errorf("restore: encrypted archive requires --key-file")
		}
		key, err := backup.LoadBackupKey(keyFile)
		if err != nil {
			return err
		}
		plain := filepath.Join(temp, "archive.tar.gz")
		if err := backup.DecryptFile(archive, plain, key); err != nil {
			return err
		}
		archive = plain
	}
	return extract(archive, extracted)
}

func materializeDirectory(directory, extracted, temp, keyFile string) error {
	manifestPath, err := findLocalManifest(directory)
	if err != nil {
		return err
	}
	key, err := manifestKey(manifestPath, keyFile)
	if err != nil {
		return err
	}
	data, err := readManifestFile(manifestPath, temp, key)
	if err != nil {
		return err
	}
	key, err = keyForManifest(data, keyFile)
	if err != nil {
		return err
	}
	source := func(remote string) (string, func(), error) {
		cleaned, err := safeRelative(remote)
		if err != nil {
			return "", nil, err
		}
		return filepath.Join(directory, filepath.FromSlash(cleaned)), func() {}, nil
	}
	return materializeManifest(data, extracted, key, source)
}

func materializeS3(ctx context.Context, options Options, extracted, temp string) error {
	bucket, prefix, err := parseS3URI(options.S3URI)
	if err != nil {
		return err
	}
	if isManifestName(path.Base(prefix)) {
		prefix = path.Dir(prefix)
		if prefix == "." {
			prefix = ""
		}
	}
	uploader, err := backup.NewS3Uploader(ctx, backup.S3Options{
		Bucket: bucket, Prefix: prefix, Region: options.Region,
		Endpoint: options.Endpoint, ForcePathStyle: options.ForcePathStyle,
	})
	if err != nil {
		return err
	}
	objects, err := uploader.List(ctx, prefix)
	if err != nil {
		return err
	}
	manifestObject, err := findRemoteManifest(objects)
	if err != nil {
		return err
	}
	downloadedManifest := filepath.Join(temp, path.Base(manifestObject.Key))
	if err := uploader.Download(ctx, manifestObject.Key, downloadedManifest); err != nil {
		return err
	}
	key, err := manifestKey(downloadedManifest, options.KeyFile)
	if err != nil {
		return err
	}
	data, err := readManifestFile(downloadedManifest, temp, key)
	if err != nil {
		return err
	}
	key, err = keyForManifest(data, options.KeyFile)
	if err != nil {
		return err
	}
	basePrefix := ""
	if index := strings.LastIndex(manifestObject.Key, "/"); index >= 0 {
		basePrefix = manifestObject.Key[:index+1]
	}
	source := func(remote string) (string, func(), error) {
		cleaned, err := safeRelative(remote)
		if err != nil {
			return "", nil, err
		}
		file, err := os.CreateTemp(temp, ".kouja-download-*")
		if err != nil {
			return "", nil, err
		}
		name := file.Name()
		if err := file.Close(); err != nil {
			_ = os.Remove(name)
			return "", nil, err
		}
		_ = os.Remove(name)
		if err := uploader.Download(ctx, basePrefix+cleaned, name); err != nil {
			return "", nil, err
		}
		return name, func() { _ = os.Remove(name) }, nil
	}
	return materializeManifest(data, extracted, key, source)
}

func materializeManifest(data []byte, extracted string, key []byte, source func(string) (string, func(), error)) error {
	var document backupManifest
	if err := json.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("restore: decode backup manifest: %w", err)
	}
	if document.Encrypted && len(key) == 0 {
		return fmt.Errorf("restore: backup is encrypted; provide --key-file")
	}
	names := make([]string, 0, len(document.Files))
	for name := range document.Files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry := document.Files[name]
		logical, err := safeRelative(name)
		if err != nil {
			return err
		}
		remote, err := safeRelative(entry.Remote)
		if err != nil {
			return err
		}
		sourcePath, cleanup, err := source(remote)
		if err != nil {
			return err
		}
		target := filepath.Join(extracted, filepath.FromSlash(logical))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			cleanup()
			return err
		}
		if strings.HasSuffix(remote, ".enc") || document.Encrypted {
			if len(key) == 0 {
				cleanup()
				return fmt.Errorf("restore: backup is encrypted; provide --key-file")
			}
			if err := backup.DecryptFile(sourcePath, target, key); err != nil {
				cleanup()
				return err
			}
		} else if err := copyFile(sourcePath, target, 0o600); err != nil {
			cleanup()
			return err
		}
		cleanup()
		if err := verifyDigest(target, entry.SHA256); err != nil {
			return err
		}
	}
	return writeManifestCopy(extracted, data)
}

func restoreExtracted(extracted string, options Options) error {
	configData, err := os.ReadFile(filepath.Join(extracted, "config.yaml")) //nolint:gosec // extracted archive path
	if err != nil {
		return fmt.Errorf("restore: backup is missing config.yaml: %w", err)
	}
	cfg, err := config.Parse(configData)
	if err != nil {
		return fmt.Errorf("restore: parse archived config: %w", err)
	}
	plans, err := buildPlan(extracted, cfg, options.ConfigPath)
	if err != nil {
		return err
	}
	if !options.Apply {
		for _, plan := range plans {
			_, _ = fmt.Fprintf(options.Stdout, "%s -> %s\n", plan.ArchivePath, plan.TargetPath)
		}
		_, _ = fmt.Fprintln(options.Stdout, "dry run; pass --apply after stopping the agent to restore")
		return nil
	}
	for _, plan := range plans {
		if err := copyRestored(extracted, plan); err != nil {
			return err
		}
	}
	for _, plan := range plans {
		_, _ = fmt.Fprintf(options.Stdout, "restored %s\n", plan.TargetPath)
	}
	return nil
}

func manifestKey(path, keyFile string) ([]byte, error) {
	if !strings.HasSuffix(path, ".enc") {
		return nil, nil
	}
	if keyFile == "" {
		return nil, fmt.Errorf("restore: encrypted backup requires --key-file")
	}
	return backup.LoadBackupKey(keyFile)
}

func keyForManifest(data []byte, keyFile string) ([]byte, error) {
	var document backupManifest
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("restore: decode backup manifest: %w", err)
	}
	if !document.Encrypted {
		return nil, nil
	}
	if keyFile == "" {
		return nil, fmt.Errorf("restore: backup is encrypted; provide --key-file")
	}
	return backup.LoadBackupKey(keyFile)
}

func readManifestFile(path, temp string, key []byte) ([]byte, error) {
	if !strings.HasSuffix(path, ".enc") {
		return os.ReadFile(path) //nolint:gosec // operator-selected manifest
	}
	plain := filepath.Join(temp, "manifest.json")
	if err := backup.DecryptFile(path, plain, key); err != nil {
		return nil, err
	}
	return os.ReadFile(plain) //nolint:gosec // temporary decrypted manifest
}

func findLocalManifest(directory string) (string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", err
	}
	candidates := make([]string, 0)
	for _, entry := range entries {
		if entry.IsDir() || !isManifestName(entry.Name()) {
			continue
		}
		candidates = append(candidates, filepath.Join(directory, entry.Name()))
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("restore: no restore.json manifest found in %s", directory)
	}
	sort.Strings(candidates)
	return candidates[len(candidates)-1], nil
}

func findRemoteManifest(objects []backup.RemoteObject) (backup.RemoteObject, error) {
	candidates := make([]backup.RemoteObject, 0)
	for _, object := range objects {
		if isManifestName(path.Base(object.Key)) {
			candidates = append(candidates, object)
		}
	}
	if len(candidates) == 0 {
		return backup.RemoteObject{}, fmt.Errorf("restore: no restore.json manifest found under prefix")
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].LastModified.Equal(candidates[j].LastModified) {
			return candidates[i].Key > candidates[j].Key
		}
		return candidates[i].LastModified.After(candidates[j].LastModified)
	})
	return candidates[0], nil
}

func isManifestName(name string) bool {
	return name == "restore.json" || name == "restore.json.enc" ||
		strings.HasSuffix(name, "-restore.json") || strings.HasSuffix(name, "-restore.json.enc")
}

func parseS3URI(value string) (string, string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "s3" || parsed.Host == "" {
		return "", "", fmt.Errorf("restore: --s3 must be an s3://bucket/prefix URI")
	}
	return parsed.Host, strings.TrimPrefix(parsed.Path, "/"), nil
}

func writeManifestCopy(extracted string, data []byte) error {
	return os.WriteFile(filepath.Join(extracted, "restore.json"), data, 0o600) //nolint:gosec // restored manifest path
}

func verifyDigest(path, expected string) error {
	if expected == "" {
		return nil
	}
	file, err := os.Open(path) //nolint:gosec // restored file path
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("restore: checksum mismatch for %s", path)
	}
	return nil
}

func safeRelative(value string) (string, error) {
	cleaned := path.Clean(strings.ReplaceAll(value, "\\", "/"))
	if cleaned == "." || cleaned == ".." || path.IsAbs(cleaned) || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("restore: unsafe path %q", value)
	}
	return cleaned, nil
}

func copyFile(source, target string, mode os.FileMode) error {
	input, err := os.Open(source) //nolint:gosec // extracted backup path
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode) //nolint:gosec // restored backup path
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func isEncrypted(path string) (bool, error) {
	file, err := os.Open(path) //nolint:gosec // operator-selected archive
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()
	magic := make([]byte, 6)
	n, err := io.ReadFull(file, magic)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return false, err
	}
	return n == 6 && string(magic) == "KOUJA1", nil
}

func extract(archive, destination string) error {
	file, err := os.Open(archive) //nolint:gosec // operator-selected archive
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer func() { _ = gzipReader.Close() }()
	reader := tar.NewReader(gzipReader)
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(header.Name)
		if name == "." || filepath.IsAbs(name) || strings.HasPrefix(name, ".."+string(filepath.Separator)) || name == ".." {
			return fmt.Errorf("restore: unsafe archive entry %q", header.Name)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		target := filepath.Join(destination, name)
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) //nolint:gosec // extracted backup path
		if err != nil {
			return err
		}
		if _, err := io.Copy(output, reader); err != nil { //nolint:gosec // operator-selected backup archive
			_ = output.Close()
			return err
		}
		if err := output.Close(); err != nil {
			return err
		}
	}
}

func buildPlan(extracted string, cfg *config.Config, configPath string) ([]FilePlan, error) {
	files := []FilePlan{
		{ArchivePath: "state.db", TargetPath: filepath.Join(cfg.DataDir, "state.db"), Mode: 0o600},
		{ArchivePath: "secret.key", TargetPath: cfg.SecretKeyFile, Mode: 0o600},
		{ArchivePath: "host-id", TargetPath: cfg.HostIDFile, Mode: 0o600},
		{ArchivePath: "revoked-serials.log", TargetPath: cfg.CertificateAuthority.RevocationLogFile, Mode: 0o600},
		{ArchivePath: "config.yaml", TargetPath: configPath, Mode: 0o600},
		{ArchivePath: "emergency.log", TargetPath: cfg.EmergencyLog, Mode: 0o600},
		{ArchivePath: "tls/server.crt", TargetPath: cfg.Listen.TLS.CertFile, Mode: 0o644},
		{ArchivePath: "tls/server.key", TargetPath: cfg.Listen.TLS.KeyFile, Mode: 0o600},
		{ArchivePath: "tls/client-ca.crt", TargetPath: cfg.Listen.TLS.ClientCAFile, Mode: 0o644},
		{ArchivePath: "ca/ca.key", TargetPath: cfg.CertificateAuthority.KeyFile, Mode: 0o600},
		{ArchivePath: "restore.json", TargetPath: filepath.Join(cfg.DataDir, "restore.json"), Mode: 0o600},
	}
	plans := make([]FilePlan, 0, len(files))
	for _, plan := range files {
		if plan.TargetPath == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(extracted, plan.ArchivePath)); err == nil {
			plans = append(plans, plan)
		}
	}
	certDir := filepath.Join(extracted, "certificates")
	_ = filepath.Walk(certDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		relative, err := filepath.Rel(certDir, path)
		if err != nil || strings.HasPrefix(relative, "..") {
			return nil
		}
		plans = append(plans, FilePlan{
			ArchivePath: filepath.Join("certificates", relative),
			TargetPath:  filepath.Join(cfg.CertificateAuthority.CertificateDir, relative),
			Mode:        certificateMode(relative),
		})
		return nil
	})
	sort.Slice(plans, func(i, j int) bool { return plans[i].ArchivePath < plans[j].ArchivePath })
	return plans, nil
}

func certificateMode(name string) os.FileMode {
	if strings.HasSuffix(name, ".key") {
		return 0o600
	}
	return 0o644
}

func copyRestored(root string, plan FilePlan) error {
	return copyRestoredFile(filepath.Join(root, plan.ArchivePath), plan.TargetPath, plan.Mode)
}

func copyRestoredFile(source, target string, mode os.FileMode) error {
	input, err := os.Open(source) //nolint:gosec // extracted archive path
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".kouja-restore-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := io.Copy(temporary, input); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, target); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(target)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
