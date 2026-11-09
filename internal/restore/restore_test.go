package restore

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voidstarit/kouja/internal/backup"
	"github.com/voidstarit/kouja/internal/state"
)

func createBackupArchive(t *testing.T, files map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "backup.tar.gz")
	file, err := os.Create(path) //nolint:gosec // test archive
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	writer := tar.NewWriter(gzipWriter)
	for name, data := range files {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func restoreConfig(t *testing.T, base string) string {
	t.Helper()
	data := filepath.Join(base, "data")
	return fmt.Sprintf(`host_id_file: %s
data_dir: %s
secret_key_file: %s
emergency_log: %s
listen:
  address: 127.0.0.1:9443
  tls:
    cert_file: %s
    key_file: %s
    client_ca_file: %s
certificate_authority:
  enabled: false
  key_file: %s
  certificate_dir: %s
  revocation_log_file: %s
storage:
  vgs: [vg1]
targets:
  iscsi:
    portals:
      - address: 127.0.0.1
        port: 3260
`, filepath.Join(base, "host-id"), data, filepath.Join(base, "secret.key"),
		filepath.Join(base, "emergency.log"), filepath.Join(base, "tls", "server.crt"),
		filepath.Join(base, "tls", "server.key"), filepath.Join(base, "tls", "client-ca.crt"),
		filepath.Join(base, "tls", "ca.key"), filepath.Join(base, "tls", "clients"),
		filepath.Join(base, "revoked.log"))
}

func TestRestoreDryRunAndApply(t *testing.T) {
	base := t.TempDir()
	configText := restoreConfig(t, base)
	archive := createBackupArchive(t, map[string]string{
		"config.yaml": configText, "state.db": "db", "secret.key": "secret",
		"host-id": "host", "revoked-serials.log": "journal",
		"tls/server.crt": "cert", "tls/server.key": "key", "tls/client-ca.crt": "ca",
		"ca/ca.key": "cakey", "certificates/admin.crt": "client-cert",
		"certificates/admin.key": "client-key", "restore.json": "{}",
	})
	var output bytes.Buffer
	if err := Run(context.Background(), Options{Archive: archive, Stdout: &output}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "state.db") || !strings.Contains(output.String(), "dry run") {
		t.Fatalf("dry run output = %q", output.String())
	}
	configPath := filepath.Join(base, "restored", "config.yaml")
	if err := Run(context.Background(), Options{Archive: archive, ConfigPath: configPath, Apply: true, Stdout: &output}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(base, "data", "state.db"), filepath.Join(base, "secret.key"),
		filepath.Join(base, "host-id"), filepath.Join(base, "revoked.log"),
		filepath.Join(base, "tls", "server.crt"), filepath.Join(base, "tls", "server.key"),
		filepath.Join(base, "tls", "ca.key"), filepath.Join(base, "tls", "clients", "admin.crt"),
		configPath,
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing restored file %s: %v", path, err)
		}
	}
}

func TestRestoreEncryptedArchive(t *testing.T) {
	base := t.TempDir()
	archive := createBackupArchive(t, map[string]string{"config.yaml": restoreConfig(t, base), "state.db": "db", "secret.key": "secret", "host-id": "host"})
	key := bytes.Repeat([]byte{0x11}, 32)
	keyPath := filepath.Join(t.TempDir(), "backup.key")
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	encrypted := archive + ".enc"
	if err := backup.EncryptFile(archive, encrypted, key); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := Run(context.Background(), Options{Archive: encrypted, KeyFile: keyPath, Stdout: &output}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "state.db") {
		t.Fatalf("output = %q", output.String())
	}
}

type captureUploader struct {
	data map[string][]byte
}

func (u *captureUploader) Upload(_ context.Context, path, key string) error {
	data, err := os.ReadFile(path) //nolint:gosec // test upload
	if err != nil {
		return err
	}
	if u.data == nil {
		u.data = make(map[string][]byte)
	}
	u.data[key] = data
	return nil
}

func (u *captureUploader) DeleteBefore(context.Context, time.Time) error { return nil }

func TestRestorePlainEncryptedBackupDirectory(t *testing.T) {
	base := t.TempDir()
	configText := restoreConfig(t, base)
	configPath := filepath.Join(base, "source-config.yaml")
	if err := os.WriteFile(configPath, []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	hostID := filepath.Join(base, "source-host-id")
	if err := os.WriteFile(hostID, []byte("host-uuid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	certDir := filepath.Join(base, "source-certs")
	if err := os.MkdirAll(certDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(certDir, "admin.crt"), []byte("certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{0x22}, 32)
	keyPath := filepath.Join(base, "backup.key")
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), filepath.Join(base, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	uploader := &captureUploader{}
	manager, err := backup.New(store, backup.Options{
		TargetDir: base, HostIDFile: hostID,
		RetentionShort: 2, RetentionDaily: 2, RetentionWeekly: 2,
		ArchiveFiles:      map[string]string{"config.yaml": configPath},
		ArchiveDirs:       map[string]string{"certificates": certDir},
		EncryptionKeyFile: keyPath,
		S3:                &backup.S3Options{Bucket: "bucket", Prefix: "prefix/", PerBackupFolder: true},
		S3Uploader:        uploader,
		Now:               func() time.Time { return time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(base, "uploaded")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for key, data := range uploader.data {
		relative := strings.TrimPrefix(key, "prefix/2026Nov091200/")
		target := filepath.Join(directory, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	configTarget := filepath.Join(base, "restored", "config.yaml")
	if err := Run(context.Background(), Options{
		Directory: directory, KeyFile: keyPath, ConfigPath: configTarget,
		Apply: true, Stdout: io.Discard,
	}); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{
		filepath.Join(base, "data", "state.db"), filepath.Join(base, "host-id"), configTarget,
		filepath.Join(base, "tls", "clients", "admin.crt"),
	} {
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("missing restored %s: %v", file, err)
		}
	}
}

func TestRestoreFlatPlainDirectory(t *testing.T) {
	base := t.TempDir()
	configText := restoreConfig(t, base)
	directory := filepath.Join(base, "flat")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"kouja-base-state.db":     "database",
		"kouja-base-config.yaml":  configText,
		"kouja-base-restore.json": `{"files":{"state.db":{"remote":"kouja-base-state.db"},"config.yaml":{"remote":"kouja-base-config.yaml"}}}`,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	configTarget := filepath.Join(base, "flat-restored.yaml")
	if err := Run(context.Background(), Options{Directory: directory, ConfigPath: configTarget, Apply: true, Stdout: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "data", "state.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configTarget); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreFromS3Prefix(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	base := t.TempDir()
	configText := restoreConfig(t, base)
	manifest := `{"files":{"state.db":{"remote":"state.db"},"config.yaml":{"remote":"config.yaml"}}}`
	objects := map[string]string{
		"prefix/2026Nov091200/state.db":     "database",
		"prefix/2026Nov091200/config.yaml":  configText,
		"prefix/2026Nov091200/restore.json": manifest,
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && request.URL.Query().Get("list-type") == "2" {
			response.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(response, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>bucket</Name><Prefix>prefix/2026Nov091200/</Prefix><KeyCount>3</KeyCount><MaxKeys>1000</MaxKeys><IsTruncated>false</IsTruncated><Contents><Key>prefix/2026Nov091200/state.db</Key><LastModified>2026-11-09T12:00:00.000Z</LastModified><Size>8</Size><ETag>"e"</ETag><StorageClass>STANDARD</StorageClass></Contents><Contents><Key>prefix/2026Nov091200/config.yaml</Key><LastModified>2026-11-09T12:00:00.000Z</LastModified><Size>8</Size><ETag>"e"</ETag><StorageClass>STANDARD</StorageClass></Contents><Contents><Key>prefix/2026Nov091200/restore.json</Key><LastModified>2026-11-09T12:00:00.000Z</LastModified><Size>8</Size><ETag>"e"</ETag><StorageClass>STANDARD</StorageClass></Contents></ListBucketResult>`)
			return
		}
		key := strings.TrimPrefix(request.URL.Path, "/bucket/")
		body, ok := objects[key]
		if !ok {
			response.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(response, body)
	}))
	defer server.Close()
	configTarget := filepath.Join(base, "s3-restored", "config.yaml")
	if err := Run(context.Background(), Options{
		S3URI: "s3://bucket/prefix/2026Nov091200/", Endpoint: server.URL,
		Region: "us-east-1", ForcePathStyle: true,
		ConfigPath: configTarget, Apply: true, Stdout: io.Discard,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "data", "state.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(configTarget); err != nil {
		t.Fatal(err)
	}
}
