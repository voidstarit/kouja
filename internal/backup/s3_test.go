package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recordingUploader struct {
	path    string
	key     string
	cutoff  time.Time
	uploads map[string]string
	data    map[string][]byte
}

func (u *recordingUploader) Upload(_ context.Context, path, key string) error {
	u.path, u.key = path, key
	if u.uploads == nil {
		u.uploads = make(map[string]string)
		u.data = make(map[string][]byte)
	}
	data, err := os.ReadFile(path) //nolint:gosec // test path
	if err != nil {
		return err
	}
	u.uploads[key] = path
	u.data[key] = data
	return nil
}

func (u *recordingUploader) DeleteBefore(_ context.Context, cutoff time.Time) error {
	u.cutoff = cutoff
	return nil
}

func TestS3UploadsPlainRecoveryInputs(t *testing.T) {
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
	manifestKey := "prefix/kouja-20261109T120000.000000000Z-restore.json"
	if result.S3Key != manifestKey || uploader.key != manifestKey {
		t.Fatalf("result = %#v, upload key = %q", result, uploader.key)
	}
	for _, name := range []string{"state.db", "config.yaml", "emergency.log", "certificates/admin.crt"} {
		key := "prefix/kouja-20261109T120000.000000000Z-" + strings.ReplaceAll(name, "/", "-")
		if _, ok := uploader.uploads[key]; !ok {
			t.Fatalf("missing uploaded object %s: %v", key, uploader.uploads)
		}
	}
	if _, ok := uploader.uploads[manifestKey]; !ok {
		t.Fatalf("missing manifest upload: %v", uploader.uploads)
	}
}
