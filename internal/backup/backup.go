package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/voidstarit/kouja/internal/state"
)

var (
	ErrInvalid  = errors.New("backup: invalid configuration")
	ErrTarget   = errors.New("backup: target is not writable")
	ErrConflict = errors.New("backup: destination already exists")
)

const timestampLayout = "20060102T150405.000000000Z"

type JournalSnapshotter interface {
	SnapshotJournal(ctx context.Context, destination string) error
}

type Options struct {
	TargetDir          string
	HostIDFile         string
	SecretKeyFile      string
	RetentionShort     int
	RetentionDaily     int
	RetentionWeekly    int
	JournalSnapshotter JournalSnapshotter
	RevocationLogName  string
	ArchiveFiles       map[string]string
	ArchiveDirs        map[string]string
	EncryptionKeyFile  string
	S3                 *S3Options
	S3Uploader         ObjectUploader
	Now                func() time.Time
}

type Manager struct {
	store *state.Store
	opts  Options
}

type Result struct {
	DatabasePath string
	KeyPath      string
	HostIDPath   string
	JournalPath  string
	ManifestPath string
	ArchivePath  string
	S3Key        string
	S3Prefix     string
	CreatedAt    time.Time
}

type manifest struct {
	CreatedAt     time.Time             `json:"created_at"`
	Database      string                `json:"database"`
	SecretKey     string                `json:"secret_key,omitempty"`
	HostID        string                `json:"host_id"`
	HostUUID      string                `json:"host_uuid"`
	HasKey        bool                  `json:"has_key"`
	HasHostID     bool                  `json:"has_host_id"`
	RevocationLog string                `json:"revocation_log,omitempty"`
	Archive       string                `json:"archive,omitempty"`
	S3Bucket      string                `json:"s3_bucket,omitempty"`
	S3Prefix      string                `json:"s3_prefix,omitempty"`
	S3Key         string                `json:"s3_key,omitempty"`
	Encrypted     bool                  `json:"encrypted,omitempty"`
	Files         map[string]fileRecord `json:"files,omitempty"`
	SchemaVersion int                   `json:"schema_version"`
}

type fileRecord struct {
	Remote string `json:"remote"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func New(store *state.Store, opts Options) (*Manager, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: state store is required", ErrInvalid)
	}
	if opts.TargetDir == "" || !filepath.IsAbs(opts.TargetDir) {
		return nil, fmt.Errorf("%w: target_dir must be an absolute path", ErrInvalid)
	}
	if opts.HostIDFile == "" || !filepath.IsAbs(opts.HostIDFile) {
		return nil, fmt.Errorf("%w: host_id_file must be an absolute path", ErrInvalid)
	}
	if opts.SecretKeyFile != "" && !filepath.IsAbs(opts.SecretKeyFile) {
		return nil, fmt.Errorf("%w: secret_key_file must be absolute", ErrInvalid)
	}
	if opts.RetentionShort <= 0 {
		opts.RetentionShort = 96
	}
	if opts.RetentionDaily <= 0 {
		opts.RetentionDaily = 7
	}
	if opts.RetentionWeekly <= 0 {
		opts.RetentionWeekly = 4
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.RevocationLogName == "" {
		opts.RevocationLogName = "revoked-serials.log"
	}
	if opts.S3 != nil {
		if opts.S3.Bucket == "" {
			return nil, fmt.Errorf("%w: S3 bucket is required", ErrInvalid)
		}
		if opts.S3.Prefix == "" {
			opts.S3.Prefix = "kouja/"
		}
		if !strings.HasSuffix(opts.S3.Prefix, "/") {
			opts.S3.Prefix += "/"
		}
		if opts.S3Uploader == nil {
			uploader, err := NewS3Uploader(context.Background(), *opts.S3)
			if err != nil {
				return nil, err
			}
			opts.S3Uploader = uploader
		}
	}
	return &Manager{store: store, opts: opts}, nil
}

func (manager *Manager) Run(ctx context.Context) (Result, error) {
	if err := manager.validateTarget(); err != nil {
		return Result{}, err
	}
	now := manager.opts.Now().UTC()
	base := "kouja-" + now.Format(timestampLayout)
	databasePath := filepath.Join(manager.opts.TargetDir, base+".db")
	keyPath := filepath.Join(manager.opts.TargetDir, base+".secret.key")
	hostIDPath := filepath.Join(manager.opts.TargetDir, base+".host-id")
	journalPath := filepath.Join(manager.opts.TargetDir, base+"-"+manager.opts.RevocationLogName)
	manifestPath := filepath.Join(manager.opts.TargetDir, base+".restore.json")
	for _, path := range []string{databasePath, keyPath, hostIDPath, journalPath, manifestPath} {
		if _, err := os.Stat(path); err == nil {
			return Result{}, fmt.Errorf("%w: %s", ErrConflict, path)
		}
	}

	created := make([]string, 0, 4)
	fail := func(err error) (Result, error) {
		for _, path := range created {
			_ = os.Remove(path)
		}
		return Result{}, err
	}

	temporary, err := os.CreateTemp(manager.opts.TargetDir, ".kouja-db-*")
	if err != nil {
		return Result{}, fmt.Errorf("backup: create temporary database: %w", err)
	}
	tempPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(tempPath)
		return Result{}, fmt.Errorf("backup: close temporary database: %w", err)
	}
	if err := os.Remove(tempPath); err != nil {
		return Result{}, fmt.Errorf("backup: prepare temporary database: %w", err)
	}
	if err := manager.store.Backup(ctx, tempPath); err != nil {
		_ = os.Remove(tempPath)
		return Result{}, err
	}
	if err := os.Rename(tempPath, databasePath); err != nil {
		_ = os.Remove(tempPath)
		return Result{}, fmt.Errorf("backup: install database: %w", err)
	}
	created = append(created, databasePath)

	hasKey, err := copyAtomic(manager.opts.SecretKeyFile, keyPath, false)
	if err != nil {
		return fail(err)
	}
	if hasKey {
		created = append(created, keyPath)
	}
	hasHostID, err := copyAtomic(manager.opts.HostIDFile, hostIDPath, true)
	if err != nil {
		return fail(err)
	}
	if hasHostID {
		created = append(created, hostIDPath)
	}
	hostUUID, err := readHostID(manager.opts.HostIDFile)
	if err != nil {
		return fail(err)
	}
	hasJournal := false
	if manager.opts.JournalSnapshotter != nil {
		if err := manager.opts.JournalSnapshotter.SnapshotJournal(ctx, journalPath); err != nil {
			return fail(err)
		}
		hasJournal = true
		created = append(created, journalPath)
	}
	payload := make(map[string]string)
	if manager.opts.S3 != nil {
		payload, err = manager.payloadFiles(databasePath, keyPath, hostIDPath, journalPath, hasKey, hasHostID, hasJournal)
		if err != nil {
			return fail(err)
		}
	}
	var encryptionKey []byte
	if manager.opts.S3 != nil && manager.opts.EncryptionKeyFile != "" {
		encryptionKey, err = LoadBackupKey(manager.opts.EncryptionKeyFile)
		if err != nil {
			return fail(err)
		}
	}
	s3Prefix := ""
	manifestKey := ""
	records := make(map[string]fileRecord)
	if manager.opts.S3 != nil {
		s3Prefix = strings.TrimSuffix(manager.opts.S3.Prefix, "/") + "/"
		if manager.opts.S3.PerBackupFolder {
			s3Prefix += now.Format("2006Jan021504") + "/"
		}
		manifestKey = remoteObjectName("restore.json", base, manager.opts.S3.PerBackupFolder)
		if len(encryptionKey) > 0 {
			manifestKey += ".enc"
		}
		for name, source := range payload {
			size, checksum, err := fileDigest(source)
			if err != nil {
				return fail(err)
			}
			remote := remoteObjectName(name, base, manager.opts.S3.PerBackupFolder)
			if len(encryptionKey) > 0 {
				remote += ".enc"
			}
			records[name] = fileRecord{Remote: remote, Size: size, SHA256: checksum}
		}
	}
	document := manifest{
		CreatedAt:     now,
		Database:      filepath.Base(databasePath),
		SecretKey:     filepath.Base(keyPath),
		HostID:        filepath.Base(hostIDPath),
		HostUUID:      hostUUID,
		HasKey:        hasKey,
		HasHostID:     hasHostID,
		Archive:       "",
		SchemaVersion: state.SchemaVersion(),
		Files:         records,
		Encrypted:     len(encryptionKey) > 0,
	}
	if manager.opts.S3 != nil {
		document.S3Bucket = manager.opts.S3.Bucket
		document.S3Prefix = s3Prefix
		document.S3Key = s3Prefix + manifestKey
	}
	if !hasKey {
		document.SecretKey = ""
	}
	if hasJournal {
		document.RevocationLog = filepath.Base(journalPath)
	}
	if err := writeJSONAtomic(manifestPath, document); err != nil {
		return fail(err)
	}
	created = append(created, manifestPath)
	if err := syncDir(manager.opts.TargetDir); err != nil {
		return fail(err)
	}
	if manager.opts.S3 != nil {
		result := Result{
			DatabasePath: databasePath, KeyPath: keyPath, HostIDPath: hostIDPath,
			JournalPath: journalPath, ManifestPath: manifestPath,
			S3Key: s3Prefix + manifestKey, S3Prefix: s3Prefix, CreatedAt: now,
		}
		names := make([]string, 0, len(records))
		for name := range records {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			record := records[name]
			if err := manager.uploadEncrypted(ctx, payload[name], s3Prefix+record.Remote, encryptionKey); err != nil {
				return result, err
			}
		}
		if err := manager.uploadEncrypted(ctx, manifestPath, s3Prefix+manifestKey, encryptionKey); err != nil {
			return result, err
		}
		retentionDays := manager.opts.S3.RetentionDays
		if retentionDays <= 0 {
			retentionDays = 7
		}
		if err := manager.opts.S3Uploader.DeleteBefore(ctx, now.AddDate(0, 0, -retentionDays)); err != nil {
			return result, err
		}
	}
	if err := manager.Rotate(now); err != nil {
		return Result{}, err
	}
	resultKey := ""
	if hasKey {
		resultKey = keyPath
	}
	resultJournal := ""
	if hasJournal {
		resultJournal = journalPath
	}
	resultS3Key := ""
	if manager.opts.S3 != nil {
		resultS3Key = s3Prefix + manifestKey
	}
	return Result{
		DatabasePath: databasePath,
		KeyPath:      resultKey,
		HostIDPath:   hostIDPath,
		JournalPath:  resultJournal,
		ManifestPath: manifestPath,
		S3Key:        resultS3Key,
		S3Prefix:     s3Prefix,
		CreatedAt:    now,
	}, nil
}

func (manager *Manager) payloadFiles(databasePath, keyPath, hostIDPath, journalPath string, hasKey, hasHostID, hasJournal bool) (map[string]string, error) {
	files := map[string]string{
		"state.db": databasePath,
	}
	if hasKey {
		files["secret.key"] = keyPath
	}
	if hasHostID {
		files["host-id"] = hostIDPath
	}
	if hasJournal {
		files["revoked-serials.log"] = journalPath
	}
	for name, source := range manager.opts.ArchiveFiles {
		if source == "" {
			continue
		}
		cleaned, err := safeBackupName(name)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("backup: stat archive input %q: %w", source, err)
		}
		files[cleaned] = source
	}
	for name, source := range manager.opts.ArchiveDirs {
		if source == "" {
			continue
		}
		cleaned, err := safeBackupName(name)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("backup: stat archive directory %q: %w", source, err)
		}
		if err := filepath.Walk(source, func(filePath string, info os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if info == nil || info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return nil
			}
			relative, err := filepath.Rel(source, filePath)
			if err != nil {
				return err
			}
			archiveName, err := safeBackupName(path.Join(cleaned, filepath.ToSlash(relative)))
			if err != nil {
				return err
			}
			files[archiveName] = filePath
			return nil
		}); err != nil {
			return nil, fmt.Errorf("backup: walk archive directory %q: %w", source, err)
		}
	}
	return files, nil
}

func (manager *Manager) uploadEncrypted(ctx context.Context, source, key string, encryptionKey []byte) error {
	if len(encryptionKey) == 0 {
		return manager.opts.S3Uploader.Upload(ctx, source, key)
	}
	temporary, err := os.CreateTemp(manager.opts.TargetDir, ".kouja-s3-*")
	if err != nil {
		return err
	}
	tempPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Remove(tempPath); err != nil {
		return err
	}
	defer func() { _ = os.Remove(tempPath) }()
	if err := EncryptFile(source, tempPath, encryptionKey); err != nil {
		return err
	}
	return manager.opts.S3Uploader.Upload(ctx, tempPath, key)
}

func remoteObjectName(name, base string, perBackupFolder bool) string {
	cleaned := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	if perBackupFolder {
		return cleaned
	}
	return base + "-" + strings.ReplaceAll(cleaned, "/", "-")
}

func safeBackupName(name string) (string, error) {
	cleaned := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	if cleaned == "." || cleaned == ".." || path.IsAbs(cleaned) || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "\\") {
		return "", fmt.Errorf("%w: unsafe backup name %q", ErrInvalid, name)
	}
	return cleaned, nil
}

func fileDigest(source string) (int64, string, error) {
	file, err := os.Open(source) //nolint:gosec // operator-configured backup input
	if err != nil {
		return 0, "", fmt.Errorf("backup: open archive input %q: %w", source, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return 0, "", err
	}
	if !info.Mode().IsRegular() {
		return 0, "", fmt.Errorf("backup: archive input %q is not a regular file", source)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return 0, "", err
	}
	return info.Size(), hex.EncodeToString(hash.Sum(nil)), nil
}

func (manager *Manager) Rotate(now time.Time) error {
	entries, err := os.ReadDir(manager.opts.TargetDir)
	if err != nil {
		return fmt.Errorf("backup: list target: %w", err)
	}
	type candidate struct {
		base string
		at   time.Time
	}
	candidates := make([]candidate, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "kouja-") || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		base := strings.TrimSuffix(entry.Name(), ".db")
		at, err := time.Parse(timestampLayout, strings.TrimPrefix(base, "kouja-"))
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{base: base, at: at})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].at.After(candidates[j].at) })

	keep := make(map[string]bool)
	for index, candidate := range candidates {
		if index < manager.opts.RetentionShort {
			keep[candidate.base] = true
		}
	}
	seenDays := make(map[string]bool)
	seenWeeks := make(map[string]bool)
	for _, candidate := range candidates {
		day := candidate.at.Format("20060102")
		if !seenDays[day] && len(seenDays) < manager.opts.RetentionDaily {
			seenDays[day] = true
			keep[candidate.base] = true
		}
		year, week := candidate.at.ISOWeek()
		weekKey := fmt.Sprintf("%04d-%02d", year, week)
		if !seenWeeks[weekKey] && len(seenWeeks) < manager.opts.RetentionWeekly {
			seenWeeks[weekKey] = true
			keep[candidate.base] = true
		}
	}
	for _, candidate := range candidates {
		if keep[candidate.base] {
			continue
		}
		for _, suffix := range []string{".db", ".secret.key", ".host-id", ".restore.json", ".tar.gz", ".tar.gz.enc"} {
			_ = os.Remove(filepath.Join(manager.opts.TargetDir, candidate.base+suffix))
		}
		if manager.opts.JournalSnapshotter != nil && manager.opts.RevocationLogName != "" {
			_ = os.Remove(filepath.Join(manager.opts.TargetDir, candidate.base+"-"+manager.opts.RevocationLogName))
		}
	}
	_ = now
	return nil
}

func (manager *Manager) validateTarget() error {
	info, err := os.Stat(manager.opts.TargetDir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrTarget, manager.opts.TargetDir)
	}
	temp, err := os.CreateTemp(manager.opts.TargetDir, ".kouja-write-test-*")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTarget, err)
	}
	name := temp.Name()
	_ = temp.Close()
	_ = os.Remove(name)
	return nil
}

func copyAtomic(source, destination string, required bool) (bool, error) {
	if source == "" {
		if required {
			return false, fmt.Errorf("%w: required source path is empty", ErrInvalid)
		}
		return false, nil
	}
	input, err := os.Open(source) //nolint:gosec // source is an operator-configured backup input
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !required {
			return false, nil
		}
		return false, fmt.Errorf("backup: open %q: %w", source, err)
	}
	defer func() { _ = input.Close() }()

	dir := filepath.Dir(destination)
	temporary, err := os.CreateTemp(dir, ".kouja-copy-*")
	if err != nil {
		return false, fmt.Errorf("backup: create temporary file: %w", err)
	}
	tempPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(tempPath)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return false, fmt.Errorf("backup: set copied file mode: %w", err)
	}
	if _, err := io.Copy(temporary, input); err != nil {
		cleanup()
		return false, fmt.Errorf("backup: copy %q: %w", source, err)
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return false, fmt.Errorf("backup: sync copied file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(tempPath)
		return false, fmt.Errorf("backup: close copied file: %w", err)
	}
	if err := os.Rename(tempPath, destination); err != nil {
		_ = os.Remove(tempPath)
		return false, fmt.Errorf("backup: install copied file: %w", err)
	}
	if err := syncDir(dir); err != nil {
		return false, err
	}
	return true, nil
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("backup: encode manifest: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, ".kouja-manifest-*")
	if err != nil {
		return fmt.Errorf("backup: create temporary manifest: %w", err)
	}
	tempPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(tempPath)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("backup: set manifest mode: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("backup: write manifest: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("backup: sync manifest: %w", err)
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("backup: close manifest: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("backup: install manifest: %w", err)
	}
	return syncDir(dir)
}

func readHostID(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is the configured host-id file
	if err != nil {
		return "", fmt.Errorf("backup: read host id: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func syncDir(path string) error {
	dir, err := os.Open(path) //nolint:gosec // path is the validated backup target directory
	if err != nil {
		return fmt.Errorf("backup: open backup directory: %w", err)
	}
	defer func() { _ = dir.Close() }()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("backup: sync backup directory: %w", err)
	}
	return nil
}
