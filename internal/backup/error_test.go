package backup

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestLoadBackupKeyErrorAndEncodingPaths(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadBackupKey(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing key file did not fail")
	}
	if err := os.WriteFile(filepath.Join(dir, "short"), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBackupKey(filepath.Join(dir, "short")); err == nil {
		t.Fatal("short key did not fail")
	}
	raw := bytes.Repeat([]byte{0x31}, 32)
	rawStd := base64.RawStdEncoding.EncodeToString(raw)
	rawStdPath := filepath.Join(dir, "rawstd")
	if err := os.WriteFile(rawStdPath, []byte(rawStd), 0o600); err != nil {
		t.Fatal(err)
	}
	if key, err := LoadBackupKey(rawStdPath); err != nil || !bytes.Equal(key, raw) {
		t.Fatalf("raw std key = %v, %v", key, err)
	}
	hexPath := filepath.Join(dir, "hex")
	if err := os.WriteFile(hexPath, []byte(hex.EncodeToString(raw)), 0o600); err != nil {
		t.Fatal(err)
	}
	if key, err := LoadBackupKey(hexPath); err != nil || !bytes.Equal(key, raw) {
		t.Fatalf("hex key = %v, %v", key, err)
	}
	crlfPath := filepath.Join(dir, "crlf")
	if err := os.WriteFile(crlfPath, append(append([]byte{}, raw...), '\r', '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if key, err := LoadBackupKey(crlfPath); err != nil || !bytes.Equal(key, raw) {
		t.Fatalf("crlf key = %v, %v", key, err)
	}
}

func TestEncryptionErrorPaths(t *testing.T) {
	dir := t.TempDir()
	key := bytes.Repeat([]byte{0x44}, 32)
	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EncryptFile(plain, filepath.Join(dir, "out"), []byte{1}); err == nil {
		t.Fatal("short encryption key did not fail")
	}
	if err := EncryptFile(filepath.Join(dir, "missing"), filepath.Join(dir, "out"), key); err == nil {
		t.Fatal("missing encryption input did not fail")
	}
	if err := EncryptFile(plain, filepath.Join(dir, "missing", "out"), key); err == nil {
		t.Fatal("missing encryption destination directory did not fail")
	}
	if err := EncryptFile(dir, filepath.Join(dir, "out"), key); err == nil {
		t.Fatal("directory encryption input did not fail")
	}
	if err := DecryptFile(filepath.Join(dir, "out"), filepath.Join(dir, "dec"), []byte{1}); err == nil {
		t.Fatal("short decryption key did not fail")
	}
	if err := DecryptFile(filepath.Join(dir, "missing"), filepath.Join(dir, "dec"), key); err == nil {
		t.Fatal("missing decryption input did not fail")
	}
	if err := DecryptFile(plain, filepath.Join(dir, "missing", "dec"), key); err == nil {
		t.Fatal("missing decryption destination directory did not fail")
	}
	if err := DecryptFile(plain, filepath.Join(dir, "dec"), key); err == nil {
		t.Fatal("plain archive decryption did not fail")
	}
	encrypted := filepath.Join(dir, "encrypted")
	if err := EncryptFile(plain, encrypted, key); err != nil {
		t.Fatal(err)
	}
	if err := DecryptFile(encrypted, filepath.Join(dir, "dec"), bytes.Repeat([]byte{0x55}, 32)); err == nil {
		t.Fatal("wrong key did not fail")
	}
	truncatedNonce := filepath.Join(dir, "nonce")
	if err := os.WriteFile(truncatedNonce, append(append([]byte{}, backupMagic...), 1, 2, 3), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := DecryptFile(truncatedNonce, filepath.Join(dir, "dec"), key); err == nil {
		t.Fatal("truncated nonce did not fail")
	}
	missingLength := filepath.Join(dir, "length")
	if err := os.WriteFile(missingLength, append(append([]byte{}, backupMagic...), make([]byte, 12)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := DecryptFile(missingLength, filepath.Join(dir, "dec"), key); err == nil {
		t.Fatal("missing chunk length did not fail")
	}
	shortSealed := filepath.Join(dir, "sealed")
	data := append([]byte{}, backupMagic...)
	data = append(data, make([]byte, 12)...)
	length := make([]byte, 4)
	binary.BigEndian.PutUint32(length, 8)
	data = append(data, length...)
	data = append(data, 1, 2, 3)
	if err := os.WriteFile(shortSealed, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := DecryptFile(shortSealed, filepath.Join(dir, "dec"), key); err == nil {
		t.Fatal("short sealed chunk did not fail")
	}
	if _, err := readUint32(bytes.NewReader([]byte{1, 2})); err == nil {
		t.Fatal("short uint32 did not fail")
	}
}

type errorUploader struct {
	failUploadOn string
	uploadErr    error
	deleteErr    error
}

func (u *errorUploader) Upload(_ context.Context, _ string, key string) error {
	if u.uploadErr != nil && (u.failUploadOn == "" || strings.Contains(key, u.failUploadOn)) {
		return u.uploadErr
	}
	return nil
}

func (u *errorUploader) DeleteBefore(context.Context, time.Time) error { return u.deleteErr }

type errorJournal struct{ err error }

func (j errorJournal) SnapshotJournal(context.Context, string) error { return j.err }

func backupManagerForError(t *testing.T, target string, uploader ObjectUploader, now time.Time) *Manager {
	t.Helper()
	store := openBackupStore(t)
	hostID := filepath.Join(target, "host-id")
	if err := os.WriteFile(hostID, []byte(uuid.NewString()), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(store, Options{
		TargetDir: target, HostIDFile: hostID,
		RetentionShort: 2, RetentionDaily: 2, RetentionWeekly: 2,
		S3:         &S3Options{Bucket: "bucket", Prefix: "prefix/", RetentionDays: 7},
		S3Uploader: uploader,
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func TestManagerS3AndPayloadErrorPaths(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	if _, err := New(openBackupStore(t), Options{TargetDir: dir, HostIDFile: filepath.Join(dir, "host-id"), S3: &S3Options{}}); err == nil {
		t.Fatal("empty S3 bucket did not fail")
	}
	manager := backupManagerForError(t, dir, &errorUploader{}, now)
	if _, err := manager.payloadFiles("", "", "", "", false, false, false); err != nil {
		t.Fatal(err)
	}
	manager.opts.ArchiveFiles = map[string]string{"../bad": filepath.Join(dir, "host-id")}
	if _, err := manager.payloadFiles("", "", "", "", false, false, false); err == nil {
		t.Fatal("unsafe archive name did not fail")
	}
	manager.opts.ArchiveFiles = map[string]string{"config.yaml": filepath.Join(dir, "host-id", "child")}
	if _, err := manager.payloadFiles("", "", "", "", false, false, false); err == nil {
		t.Fatal("archive stat error did not fail")
	}
	manager.opts.ArchiveFiles = nil
	manager.opts.ArchiveDirs = map[string]string{"../bad": dir}
	if _, err := manager.payloadFiles("", "", "", "", false, false, false); err == nil {
		t.Fatal("unsafe archive directory did not fail")
	}
	manager.opts.ArchiveDirs = map[string]string{"certs": filepath.Join(dir, "missing")}
	if files, err := manager.payloadFiles("", "", "", "", false, false, false); err != nil || len(files) != 1 {
		t.Fatalf("missing archive directory = %v, %v", files, err)
	}
	manager.opts.ArchiveDirs = map[string]string{"certs": filepath.Join(dir, "host-id", "child")}
	if _, err := manager.payloadFiles("", "", "", "", false, false, false); err == nil {
		t.Fatal("archive directory stat error did not fail")
	}
	manager.opts.ArchiveDirs = map[string]string{"certs": dir}
	if _, err := manager.payloadFiles("", "", "", "", false, false, false); err != nil {
		t.Fatalf("archive walk: %v", err)
	}
	manager.opts.ArchiveDirs = nil
	if err := manager.uploadEncrypted(context.Background(), filepath.Join(dir, "missing"), "k", make([]byte, 32)); err == nil {
		t.Fatal("missing upload source did not fail")
	}
	if err := manager.uploadEncrypted(context.Background(), filepath.Join(dir, "host-id"), "k", []byte{1}); err == nil {
		t.Fatal("short upload key did not fail")
	}
	manager.opts.S3Uploader = &errorUploader{uploadErr: errors.New("upload")}
	if err := manager.uploadEncrypted(context.Background(), filepath.Join(dir, "host-id"), "k", make([]byte, 32)); err == nil {
		t.Fatal("failing uploader did not fail")
	}
	if _, _, err := fileDigest(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing digest source did not fail")
	}
	if _, _, err := fileDigest(dir); err == nil {
		t.Fatal("directory digest source did not fail")
	}
	if err := manager.Rotate(now); err != nil {
		t.Fatal(err)
	}
	if err := (&Manager{opts: Options{TargetDir: filepath.Join(dir, "missing")}}).Rotate(now); err == nil {
		t.Fatal("missing rotate target did not fail")
	}
	if err := manager.validateTarget(); err != nil {
		t.Fatal(err)
	}
}

func TestRunFailurePaths(t *testing.T) {
	now := time.Date(2026, 11, 9, 13, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	hostID := filepath.Join(dir, "host-id")
	if err := os.WriteFile(hostID, []byte(uuid.NewString()), 0o600); err != nil {
		t.Fatal(err)
	}
	store := openBackupStore(t)
	manager, err := New(store, Options{
		TargetDir: dir, HostIDFile: hostID, RetentionShort: 2, RetentionDaily: 2, RetentionWeekly: 2,
		JournalSnapshotter: errorJournal{err: errors.New("journal")},
		Now:                func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Run(context.Background()); err == nil {
		t.Fatal("journal snapshot failure did not fail")
	}
	manager, err = New(store, Options{
		TargetDir: dir, HostIDFile: hostID, RetentionShort: 2, RetentionDaily: 2, RetentionWeekly: 2,
		S3:         &S3Options{Bucket: "bucket", Prefix: "prefix/", RetentionDays: 7},
		S3Uploader: &errorUploader{}, EncryptionKeyFile: filepath.Join(dir, "missing-key"),
		Now: func() time.Time { return now.Add(time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Run(context.Background()); err == nil {
		t.Fatal("missing encryption key did not fail")
	}
	manager, err = New(store, Options{
		TargetDir: dir, HostIDFile: hostID, RetentionShort: 2, RetentionDaily: 2, RetentionWeekly: 2,
		ArchiveFiles: map[string]string{"bad": dir},
		S3:           &S3Options{Bucket: "bucket", Prefix: "prefix/", RetentionDays: 7},
		S3Uploader:   &errorUploader{},
		Now:          func() time.Time { return now.Add(2 * time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Run(context.Background()); err == nil {
		t.Fatal("directory digest input did not fail")
	}
	manager, err = New(store, Options{
		TargetDir: dir, HostIDFile: hostID, RetentionShort: 2, RetentionDaily: 2, RetentionWeekly: 2,
		S3:         &S3Options{Bucket: "bucket", Prefix: "prefix/", RetentionDays: 7},
		S3Uploader: &errorUploader{uploadErr: errors.New("upload")},
		Now:        func() time.Time { return now.Add(3 * time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Run(context.Background()); err == nil {
		t.Fatal("upload failure did not fail")
	}
	manager, err = New(store, Options{
		TargetDir: dir, HostIDFile: hostID, RetentionShort: 2, RetentionDaily: 2, RetentionWeekly: 2,
		S3:         &S3Options{Bucket: "bucket", Prefix: "prefix/", RetentionDays: 7},
		S3Uploader: &errorUploader{uploadErr: errors.New("manifest"), failUploadOn: "restore.json"},
		Now:        func() time.Time { return now.Add(4 * time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Run(context.Background()); err == nil {
		t.Fatal("manifest upload failure did not fail")
	}
	manager, err = New(store, Options{
		TargetDir: dir, HostIDFile: hostID, RetentionShort: 2, RetentionDaily: 2, RetentionWeekly: 2,
		S3:         &S3Options{Bucket: "bucket", Prefix: "prefix/", RetentionDays: 7},
		S3Uploader: &errorUploader{deleteErr: errors.New("delete")},
		Now:        func() time.Time { return now.Add(5 * time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Run(context.Background()); err == nil {
		t.Fatal("retention failure did not fail")
	}
	unsafe, err := New(store, Options{
		TargetDir: dir, HostIDFile: hostID, RetentionShort: 2, RetentionDaily: 2, RetentionWeekly: 2,
		ArchiveFiles: map[string]string{"../bad": hostID},
		S3:           &S3Options{Bucket: "bucket", Prefix: "prefix/", RetentionDays: 7},
		S3Uploader:   &errorUploader{},
		Now:          func() time.Time { return now.Add(6 * time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unsafe.Run(context.Background()); err == nil {
		t.Fatal("unsafe archive name did not fail")
	}
}

func TestCopyAndJSONErrorPaths(t *testing.T) {
	dir := t.TempDir()
	if copied, err := copyAtomic(dir, filepath.Join(dir, "missing", "dest"), false); err == nil || copied {
		t.Fatalf("copy to missing parent = %v, %v", copied, err)
	}
	if copied, err := copyAtomic(dir, filepath.Join(dir, "dest"), false); err == nil || copied {
		t.Fatalf("copy directory = %v, %v", copied, err)
	}
	targetDir := filepath.Join(dir, "target-dir")
	if err := os.Mkdir(targetDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "child"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source")
	if err := os.WriteFile(source, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if copied, err := copyAtomic(source, targetDir, false); err == nil || copied {
		t.Fatalf("copy over directory = %v, %v", copied, err)
	}
	if err := writeJSONAtomic(filepath.Join(dir, "manifest"), make(chan int)); err == nil {
		t.Fatal("unsupported JSON value did not fail")
	}
	if err := writeJSONAtomic(targetDir, map[string]string{"x": "y"}); err == nil {
		t.Fatal("manifest over directory did not fail")
	}
	if err := validateTargetForTest(dir); err != nil {
		t.Fatal(err)
	}
}

func validateTargetForTest(dir string) error {
	manager := &Manager{opts: Options{TargetDir: dir}}
	return manager.validateTarget()
}

func TestNewS3PrefixAndStoreFailure(t *testing.T) {
	store := openBackupStore(t)
	dir := t.TempDir()
	hostID := filepath.Join(dir, "host-id")
	if err := os.WriteFile(hostID, []byte(uuid.NewString()), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := New(store, Options{
		TargetDir: dir, HostIDFile: hostID,
		S3:         &S3Options{Bucket: "bucket", Prefix: "prefix", RetentionDays: 1},
		S3Uploader: &errorUploader{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if manager.opts.S3.Prefix != "prefix/" {
		t.Fatalf("prefix = %q", manager.opts.S3.Prefix)
	}
	manager, err = New(store, Options{
		TargetDir: dir, HostIDFile: hostID,
		S3:         &S3Options{Bucket: "bucket", RetentionDays: 1},
		S3Uploader: &errorUploader{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if manager.opts.S3.Prefix != "kouja/" {
		t.Fatalf("default prefix = %q", manager.opts.S3.Prefix)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Run(context.Background()); err == nil {
		t.Fatal("backup with closed store did not fail")
	}
}

func TestPayloadSkipsAndWalkFailure(t *testing.T) {
	manager := &Manager{opts: Options{ArchiveFiles: map[string]string{"empty": ""}}}
	if files, err := manager.payloadFiles("db", "", "", "", false, false, false); err != nil || len(files) != 1 {
		t.Fatalf("empty archive file = %#v, %v", files, err)
	}
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.Mkdir(blocked, 0o000); err != nil { //nolint:gosec // deliberate permission test
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(blocked, 0o700) }() //nolint:gosec // restore test directory
	manager = &Manager{opts: Options{ArchiveDirs: map[string]string{"certs": dir}}}
	if _, err := manager.payloadFiles("db", "", "", "", false, false, false); err == nil {
		t.Fatal("unreadable archive directory did not fail")
	}
}

func TestRotateInvalidAndJournalCleanup(t *testing.T) {
	dir := t.TempDir()
	manager, err := New(openBackupStore(t), Options{
		TargetDir: dir, HostIDFile: filepath.Join(dir, "host-id"),
		RetentionShort: 1, RetentionDaily: 1, RetentionWeekly: 1,
		JournalSnapshotter: errorJournal{}, RevocationLogName: "revoked.log",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	old := "kouja-" + now.Add(-48*time.Hour).Format(timestampLayout)
	newer := "kouja-" + now.Add(-time.Hour).Format(timestampLayout)
	for _, name := range []string{old + ".db", old + "-revoked.log", newer + ".db", "kouja-invalid.db"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Rotate(now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, old+"-revoked.log")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old journal still exists: %v", err)
	}
}

func TestRunJournalRemovalAndRotateFailure(t *testing.T) {
	now := time.Date(2026, 11, 9, 14, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	hostID := filepath.Join(dir, "host-id")
	if err := os.WriteFile(hostID, []byte(uuid.NewString()), 0o600); err != nil {
		t.Fatal(err)
	}
	store := openBackupStore(t)
	manager, err := New(store, Options{
		TargetDir: dir, HostIDFile: hostID, RetentionShort: 2, RetentionDaily: 2, RetentionWeekly: 2,
		JournalSnapshotter: removeTargetJournal{target: dir},
		Now:                func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Run(context.Background()); err == nil {
		t.Fatal("journal removing target did not fail")
	}
	dir = t.TempDir()
	hostID = filepath.Join(dir, "host-id")
	if err := os.WriteFile(hostID, []byte(uuid.NewString()), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err = New(store, Options{
		TargetDir: dir, HostIDFile: hostID, RetentionShort: 2, RetentionDaily: 2, RetentionWeekly: 2,
		S3:         &S3Options{Bucket: "bucket", Prefix: "prefix/", RetentionDays: 7},
		S3Uploader: &removingUploader{target: dir},
		Now:        func() time.Time { return now.Add(time.Minute) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Run(context.Background()); err == nil {
		t.Fatal("uploader removing target did not fail")
	}
}

type removeTargetJournal struct{ target string }

func (j removeTargetJournal) SnapshotJournal(_ context.Context, destination string) error {
	if err := os.WriteFile(destination, []byte("journal"), 0o600); err != nil {
		return err
	}
	return os.RemoveAll(j.target)
}

type removingUploader struct{ target string }

func (u *removingUploader) Upload(context.Context, string, string) error {
	return os.RemoveAll(u.target)
}

func (u *removingUploader) DeleteBefore(context.Context, time.Time) error { return nil }
