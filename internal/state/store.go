package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var (
	ErrNotFound    = errors.New("state: not found")
	ErrConflict    = errors.New("state: conflict")
	ErrInvalid     = errors.New("state: invalid argument")
	ErrSchemaNewer = errors.New("state: database schema is newer than the binary")
)

const schemaVersion = 2

// Store owns the SQLite database. A Store is safe for concurrent use.
type Store struct {
	path string
	db   *sql.DB
}

// Open opens the database, verifies its integrity, and applies pending
// migrations. The database is locked down to the owner where the filesystem
// permits it because it contains encrypted credentials and ownership metadata.
func Open(ctx context.Context, path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: empty database path", ErrInvalid)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	memory := path == ":memory:"
	if !memory {
		dir := filepath.Dir(path)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, fmt.Errorf("state: create database directory: %w", err)
			}
		}
	}

	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("state: open database: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	db.SetConnMaxLifetime(0)

	s := &Store{path: path, db: db}
	fail := func(err error) (*Store, error) {
		_ = db.Close()
		return nil, err
	}

	if err := db.PingContext(ctx); err != nil {
		return fail(fmt.Errorf("state: connect database: %w", err))
	}
	if err := s.checkIntegrity(ctx); err != nil {
		return fail(err)
	}
	if err := s.migrate(ctx); err != nil {
		return fail(err)
	}
	if !memory {
		_ = os.Chmod(path, 0o600)
		for _, suffix := range []string{"-wal", "-shm"} {
			_ = os.Chmod(path+suffix, 0o600)
		}
	}
	return s, nil
}

func sqliteDSN(path string) string {
	base := "file:" + filepath.ToSlash(path)
	params := url.Values{}
	if path == ":memory:" {
		base = "file::memory:"
		params.Set("cache", "shared")
	}
	params.Add("_pragma", "busy_timeout(5000)")
	params.Add("_pragma", "journal_mode(WAL)")
	params.Add("_pragma", "synchronous(FULL)")
	params.Add("_pragma", "foreign_keys(ON)")
	return base + "?" + params.Encode()
}

func (s *Store) checkIntegrity(ctx context.Context) error {
	var result string
	if err := s.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("state: integrity check: %w", err)
	}
	if !strings.EqualFold(result, "ok") {
		return fmt.Errorf("state: integrity check failed: %s", result)
	}
	return nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("state: create migrations table: %w", err)
	}

	var current int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&current); err != nil {
		return fmt.Errorf("state: read schema version: %w", err)
	}
	if current > schemaVersion {
		return fmt.Errorf("%w: database version %d, binary version %d", ErrSchemaNewer, current, schemaVersion)
	}

	if current < 2 && !strings.EqualFold(s.path, ":memory:") {
		backup := filepath.Join(filepath.Dir(s.path),
			fmt.Sprintf("state.db.pre-v2-%d.bak", time.Now().UTC().UnixNano()))
		if err := s.Backup(ctx, backup); err != nil {
			return fmt.Errorf("state: create pre-v2 backup: %w", err)
		}
	}

	for version := current + 1; version <= schemaVersion; version++ {
		statements, ok := migrations[version]
		if !ok {
			return fmt.Errorf("state: missing migration %d", version)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("state: begin migration %d: %w", version, err)
		}
		ok = false
		defer func() {
			if !ok {
				_ = tx.Rollback()
			}
		}()
		for _, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("state: apply migration %d: %w", version, err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)",
			version, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return fmt.Errorf("state: record migration %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("state: commit migration %d: %w", version, err)
		}
		ok = true
	}
	return nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// SchemaVersion is the schema version understood by this binary.
func SchemaVersion() int {
	return schemaVersion
}

// Backup writes a consistent copy of the database using VACUUM INTO. The
// destination must not already exist. The copy is synced and mode 0600.
func (s *Store) Backup(ctx context.Context, destination string) error {
	return vacuumInto(ctx, s.db, destination)
}

// Snapshot is the read-only snapshot contract used by the backup worker.
func (s *Store) Snapshot(ctx context.Context, destination string) error {
	return s.Backup(ctx, destination)
}

// SnapshotSource reads the live database without running migrations or any
// other writes. It is intended for an external backup worker that must not
// modify the database owned by the agent.
type SnapshotSource struct {
	db *sql.DB
}

// OpenSnapshotSource opens path read-only with a busy timeout so a backup
// worker can snapshot a database that the agent is actively using.
func OpenSnapshotSource(ctx context.Context, path string) (*SnapshotSource, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%w: empty database path", ErrInvalid)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	params := url.Values{}
	params.Set("mode", "ro")
	params.Add("_pragma", "busy_timeout(15000)")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?"+params.Encode())
	if err != nil {
		return nil, fmt.Errorf("state: open snapshot source: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("state: connect snapshot source: %w", err)
	}
	return &SnapshotSource{db: db}, nil
}

// Snapshot writes a consistent copy while leaving the source untouched.
func (s *SnapshotSource) Snapshot(ctx context.Context, destination string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("%w: snapshot source is not open", ErrInvalid)
	}
	return vacuumInto(ctx, s.db, destination)
}

// Close releases the read-only connection.
func (s *SnapshotSource) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func vacuumInto(ctx context.Context, db *sql.DB, destination string) error {
	if destination == "" {
		return fmt.Errorf("%w: empty backup destination", ErrInvalid)
	}
	if _, err := os.Stat(destination); err == nil {
		return fmt.Errorf("%w: backup destination already exists: %s", ErrConflict, destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("state: inspect backup destination: %w", err)
	}
	quoted := strings.ReplaceAll(destination, "'", "''")
	if _, err := db.ExecContext(ctx, "VACUUM INTO '"+quoted+"'"); err != nil { //nolint:gosec // SQLite VACUUM INTO cannot bind a filename; the value is quote-escaped
		return fmt.Errorf("state: vacuum into %q: %w", destination, err)
	}
	file, err := os.OpenFile(destination, os.O_RDONLY, 0) //nolint:gosec // destination is the validated backup output
	if err != nil {
		return fmt.Errorf("state: open backup for sync: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("state: sync backup: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("state: close backup: %w", err)
	}
	if err := os.Chmod(destination, 0o600); err != nil {
		return fmt.Errorf("state: set backup mode: %w", err)
	}
	if dir, err := os.Open(filepath.Dir(destination)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

// Transact runs fn inside a database transaction. fn must not retain the Tx
// after it returns.
func (s *Store) Transact(ctx context.Context, fn func(Tx) error) error {
	if fn == nil {
		return fmt.Errorf("%w: nil transaction function", ErrInvalid)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("state: begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(&sqlTx{ctx: ctx, tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return mapError(err)
	}
	committed = true
	return nil
}

type sqlTx struct {
	ctx context.Context
	tx  *sql.Tx
}

var _ Tx = (*sqlTx)(nil)

// Tx is the state mutation surface used by the operation manager.
type Tx interface {
	GetVolume(owner, volumeID string) (Volume, error)
	GetVolumeByResourceUUID(resourceUUID string) (Volume, error)
	ListVolumes(owner string, page Page) ([]Volume, error)
	ListVolumeStatuses(limit int) ([]VolumeStatus, error)
	VolumeStateCounts() (map[string]int, error)
	PutVolume(Volume) error
	DeleteVolume(owner, volumeID string) error

	PutTombstone(Tombstone) error
	GetTombstone(owner, volumeID string) (Tombstone, error)
	ListTombstones(owner string) ([]Tombstone, error)

	GetExport(owner, volumeID string) (Export, error)
	PutExport(Export) error
	DeleteExport(owner, volumeID string) error

	GetObservation(owner, volumeID string) (Observation, error)
	PutObservation(Observation) error
	GetExportObservation(owner, volumeID string) (ExportObservation, error)
	PutExportObservation(ExportObservation) error

	GetVG(name string) (VG, error)
	ListVGs() ([]VG, error)
	PutVG(VG) error
	DeleteVG(name string) error

	GetSetting(key string) (string, error)
	PutSetting(key, value string) error

	GetOperation(owner, volumeID, action, requestHash string) (Operation, error)
	GetOperationByID(id string) (Operation, error)
	ListOperations(owner, volumeID string) ([]Operation, error)
	ListRunningOperations() ([]Operation, error)
	ListRecentOperations(limit int) ([]Operation, error)
	OperationCount() (int, error)
	PutOperation(Operation) error

	PutGrant(Grant) error
	GetGrant(owner, volumeID, nodeID string) (Grant, error)
	ListGrants(owner, volumeID string) ([]Grant, error)
	DeleteGrant(owner, volumeID, nodeID string) error

	PutCredential(Credential) error
	GetActiveCredential(owner, volumeID string) (Credential, error)
	DeleteCredential(owner, volumeID string) error

	PutAudit(Audit) error
	PutAuditEvent(AuditEvent) error
	DeleteAuditBefore(before time.Time) (int64, error)

	PutCertificate(Certificate) error
	GetCertificate(serial string) (Certificate, error)
	ListCertificates(pageSize int32, pageToken string) ([]Certificate, string, error)
	RevokeCertificate(serial, reason string, at time.Time) (Certificate, error)
	DeleteCertificate(serial string) error
	ListExpiredCertificates(before time.Time) ([]Certificate, error)
	ListRevokedSerials() ([]string, error)
	CountActiveCertificates(now time.Time) (int, error)
}
