package backup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestS3BackupEncryptsPlainFiles(t *testing.T) {
	store := openBackupStore(t)
	dir := t.TempDir()
	hostID := filepath.Join(dir, "host-id")
	keyPath := filepath.Join(dir, "backup.key")
	key := make([]byte, 32)
	for index := range key {
		key[index] = 0x7a
	}
	if err := os.WriteFile(hostID, []byte("host\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	uploader := &recordingUploader{}
	manager, err := New(store, Options{
		TargetDir: dir, HostIDFile: hostID,
		RetentionShort: 2, RetentionDaily: 2, RetentionWeekly: 2,
		EncryptionKeyFile: keyPath,
		S3:                &S3Options{Bucket: "bucket", Prefix: "prefix/", RetentionDays: 7},
		S3Uploader:        uploader,
		Now:               func() time.Time { return time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(result.S3Key, "-restore.json.enc") || !strings.HasSuffix(uploader.key, "-restore.json.enc") {
		t.Fatalf("archive = %q upload=%q", result.S3Key, uploader.key)
	}
	for key := range uploader.uploads {
		if !strings.HasSuffix(key, ".enc") {
			t.Fatalf("plain object uploaded: %s", key)
		}
	}
	stateKey := "prefix/kouja-20261109T120000.000000000Z-state.db.enc"
	encryptedState, ok := uploader.data[stateKey]
	if !ok {
		t.Fatalf("missing encrypted state.db: %v", uploader.uploads)
	}
	encryptedStatePath := filepath.Join(t.TempDir(), "state.db.enc")
	if err := os.WriteFile(encryptedStatePath, encryptedState, 0o600); err != nil {
		t.Fatal(err)
	}
	decrypted := filepath.Join(t.TempDir(), "state.db")
	if err := DecryptFile(encryptedStatePath, decrypted, key); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(result.DatabasePath) //nolint:gosec // test backup
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(decrypted) //nolint:gosec // test backup
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("decrypted state.db differs from backup")
	}
	manifestEncrypted, ok := uploader.data[result.S3Key]
	if !ok {
		t.Fatalf("missing encrypted manifest: %v", uploader.uploads)
	}
	encryptedManifestPath := filepath.Join(t.TempDir(), "restore.json.enc")
	if err := os.WriteFile(encryptedManifestPath, manifestEncrypted, 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(t.TempDir(), "restore.json")
	if err := DecryptFile(encryptedManifestPath, manifestPath, key); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(manifestPath) //nolint:gosec // test manifest
	if err != nil {
		t.Fatal(err)
	}
	var document manifest
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if !document.Encrypted || !strings.HasSuffix(document.Files["state.db"].Remote, ".enc") {
		t.Fatalf("manifest = %#v", document)
	}
}
