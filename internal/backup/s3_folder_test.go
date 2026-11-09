package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestS3PerBackupFolder(t *testing.T) {
	store := openBackupStore(t)
	dir := t.TempDir()
	hostID := filepath.Join(dir, "host-id")
	if err := os.WriteFile(hostID, []byte("host\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	uploader := &recordingUploader{}
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	manager, err := New(store, Options{
		TargetDir: dir, HostIDFile: hostID,
		RetentionShort: 2, RetentionDaily: 2, RetentionWeekly: 2,
		S3: &S3Options{
			Bucket: "bucket", Prefix: "prefix/", ServerSideEncryption: "AES256",
			PerBackupFolder: true,
		},
		S3Uploader: uploader,
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	expected := "prefix/2026Nov091200/kouja-20261109T120000.000000000Z.tar.gz"
	if result.S3Key != expected || uploader.key != expected {
		t.Fatalf("key = %q / %q, want %q", result.S3Key, uploader.key, expected)
	}
}
