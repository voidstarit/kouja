package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type recordingUploader struct {
	path string
	key  string
}

func (u *recordingUploader) Upload(_ context.Context, path, key string) error {
	u.path, u.key = path, key
	return nil
}

func TestS3ArchiveContainsRecoveryInputs(t *testing.T) {
	store := openBackupStore(t)
	dir := t.TempDir()
	hostID := filepath.Join(dir, "host-id")
	secret := filepath.Join(dir, "secret.key")
	configPath := filepath.Join(dir, "config.yaml")
	logPath := filepath.Join(dir, "emergency.log")
	caDir := filepath.Join(dir, "clients")
	if err := os.MkdirAll(caDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{
		hostID: "host\n", secret: "secret\n", configPath: "config\n", logPath: "log\n",
	} {
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(caDir, "admin.crt"), []byte("cert"), 0o600); err != nil {
		t.Fatal(err)
	}
	uploader := &recordingUploader{}
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	manager, err := New(store, Options{
		TargetDir: dir, HostIDFile: hostID, SecretKeyFile: secret,
		RetentionShort: 2, RetentionDaily: 2, RetentionWeekly: 2,
		ArchiveFiles: map[string]string{"config.yaml": configPath, "emergency.log": logPath},
		ArchiveDirs:  map[string]string{"certificates": caDir},
		S3:           &S3Options{Bucket: "bucket", Prefix: "prefix/", ServerSideEncryption: "AES256"},
		S3Uploader:   uploader,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.ArchivePath == "" || result.S3Key != "prefix/kouja-20261109T120000.000000000Z.tar.gz" {
		t.Fatalf("result = %#v", result)
	}
	if uploader.path != result.ArchivePath || uploader.key != result.S3Key {
		t.Fatalf("upload = %q %q", uploader.path, uploader.key)
	}
	file, err := os.Open(result.ArchivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = gzipReader.Close() }()
	reader := tar.NewReader(gzipReader)
	names := map[string]bool{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names[header.Name] = true
	}
	for _, name := range []string{"state.db", "config.yaml", "emergency.log", "certificates/admin.crt", "restore.json"} {
		if !names[name] {
			t.Fatalf("archive missing %s: %v", name, names)
		}
	}
}
