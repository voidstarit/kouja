package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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

type Options struct {
	TargetDir       string
	HostIDFile      string
	SecretKeyFile   string
	RetentionShort  int
	RetentionDaily  int
	RetentionWeekly int
	Now             func() time.Time
}

type Manager struct {
	store *state.Store
	opts  Options
}

type Result struct {
	DatabasePath string
	KeyPath      string
	HostIDPath   string
	ManifestPath string
	CreatedAt    time.Time
}

type manifest struct {
	CreatedAt     time.Time `json:"created_at"`
	Database      string    `json:"database"`
	SecretKey     string    `json:"secret_key,omitempty"`
	HostID        string    `json:"host_id"`
	HostUUID      string    `json:"host_uuid"`
	HasKey        bool      `json:"has_key"`
	HasHostID     bool      `json:"has_host_id"`
	SchemaVersion int       `json:"schema_version"`
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
	manifestPath := filepath.Join(manager.opts.TargetDir, base+".restore.json")
	for _, path := range []string{databasePath, keyPath, hostIDPath, manifestPath} {
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
	document := manifest{
		CreatedAt:     now,
		Database:      filepath.Base(databasePath),
		SecretKey:     filepath.Base(keyPath),
		HostID:        filepath.Base(hostIDPath),
		HostUUID:      hostUUID,
		HasKey:        hasKey,
		HasHostID:     hasHostID,
		SchemaVersion: state.SchemaVersion(),
	}
	if !hasKey {
		document.SecretKey = ""
	}
	if err := writeJSONAtomic(manifestPath, document); err != nil {
		return fail(err)
	}
	created = append(created, manifestPath)
	if err := syncDir(manager.opts.TargetDir); err != nil {
		return fail(err)
	}
	if err := manager.Rotate(now); err != nil {
		return Result{}, err
	}
	resultKey := ""
	if hasKey {
		resultKey = keyPath
	}
	return Result{
		DatabasePath: databasePath,
		KeyPath:      resultKey,
		HostIDPath:   hostIDPath,
		ManifestPath: manifestPath,
		CreatedAt:    now,
	}, nil
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
		for _, suffix := range []string{".db", ".secret.key", ".host-id", ".restore.json"} {
			_ = os.Remove(filepath.Join(manager.opts.TargetDir, candidate.base+suffix))
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
