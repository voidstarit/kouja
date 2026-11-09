package restore

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/voidstarit/kouja/internal/backup"
	"github.com/voidstarit/kouja/internal/config"
)

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunSourceAndManifestErrors(t *testing.T) {
	ctx := context.Background()
	if err := Run(ctx, Options{}); err == nil {
		t.Fatal("missing source did not fail")
	}
	if err := Run(ctx, Options{Archive: "a", Directory: "b"}); err == nil {
		t.Fatal("multiple sources did not fail")
	}
	if err := Run(ctx, Options{Archive: filepath.Join(t.TempDir(), "missing")}); err == nil {
		t.Fatal("missing archive did not fail")
	}
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain")
	writeTestFile(t, plain, []byte("not-an-archive"))
	if err := Run(ctx, Options{Archive: plain}); err == nil {
		t.Fatal("invalid archive did not fail")
	}
	key := bytes.Repeat([]byte{0x61}, 32)
	keyPath := filepath.Join(dir, "key")
	writeTestFile(t, keyPath, key)
	encrypted := filepath.Join(dir, "archive.enc")
	if err := backup.EncryptFile(plain, encrypted, key); err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, Options{Archive: encrypted}); err == nil {
		t.Fatal("encrypted archive without key did not fail")
	}
	shortKey := filepath.Join(dir, "short-key")
	writeTestFile(t, shortKey, []byte("short"))
	if err := Run(ctx, Options{Archive: encrypted, KeyFile: shortKey}); err == nil {
		t.Fatal("encrypted archive with invalid key did not fail")
	}
	if err := Run(ctx, Options{Directory: filepath.Join(dir, "missing")}); err == nil {
		t.Fatal("missing directory did not fail")
	}
	if err := Run(ctx, Options{Directory: dir}); err == nil {
		t.Fatal("directory without manifest did not fail")
	}
	encManifestDir := filepath.Join(dir, "enc-manifest")
	writeTestFile(t, filepath.Join(encManifestDir, "restore.json.enc"), []byte("not-encrypted"))
	if err := Run(ctx, Options{Directory: encManifestDir}); err == nil {
		t.Fatal("encrypted manifest without key did not fail")
	}
	if err := Run(ctx, Options{Directory: encManifestDir, KeyFile: keyPath}); err == nil {
		t.Fatal("invalid encrypted manifest did not fail")
	}
	plainManifestDir := filepath.Join(dir, "plain-manifest")
	writeTestFile(t, filepath.Join(plainManifestDir, "restore.json"), []byte(`{"encrypted":true}`))
	if err := Run(ctx, Options{Directory: plainManifestDir}); err == nil {
		t.Fatal("encrypted manifest without key did not fail")
	}
	if err := Run(ctx, Options{Directory: plainManifestDir, KeyFile: shortKey}); err == nil {
		t.Fatal("encrypted manifest with invalid key did not fail")
	}
	unsafeManifestDir := filepath.Join(dir, "unsafe")
	writeTestFile(t, filepath.Join(unsafeManifestDir, "restore.json"), []byte(`{"files":{"../bad":{"remote":"x"}}}`))
	if err := Run(ctx, Options{Directory: unsafeManifestDir}); err == nil {
		t.Fatal("unsafe manifest logical path did not fail")
	}
	unsafeRemoteDir := filepath.Join(dir, "unsafe-remote")
	writeTestFile(t, filepath.Join(unsafeRemoteDir, "restore.json"), []byte(`{"files":{"x":{"remote":"../bad"}}}`))
	if err := Run(ctx, Options{Directory: unsafeRemoteDir}); err == nil {
		t.Fatal("unsafe manifest remote path did not fail")
	}
}

func TestRestoreManifestAndHelperErrors(t *testing.T) {
	if got, err := manifestKey("restore.json", ""); err != nil || got != nil {
		t.Fatalf("plain manifest key = %v, %v", got, err)
	}
	key := bytes.Repeat([]byte{0x62}, 32)
	keyPath := filepath.Join(t.TempDir(), "key")
	writeTestFile(t, keyPath, key)
	if _, err := manifestKey("restore.json.enc", ""); err == nil {
		t.Fatal("encrypted manifest without key did not fail")
	}
	if _, err := manifestKey("restore.json.enc", filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("encrypted manifest with missing key did not fail")
	}
	if _, err := keyForManifest([]byte("bad"), ""); err == nil {
		t.Fatal("bad manifest JSON did not fail")
	}
	if _, err := keyForManifest([]byte(`{"encrypted":true}`), ""); err == nil {
		t.Fatal("encrypted manifest without key did not fail")
	}
	if _, err := keyForManifest([]byte(`{"encrypted":true}`), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("encrypted manifest with missing key did not fail")
	}
	if _, err := readManifestFile(filepath.Join(t.TempDir(), "missing.json.enc"), t.TempDir(), key); err == nil {
		t.Fatal("missing encrypted manifest did not fail")
	}
	if _, err := findLocalManifest(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing manifest directory did not fail")
	}
	if _, err := findLocalManifest(t.TempDir()); err == nil {
		t.Fatal("empty manifest directory did not fail")
	}
	if _, err := findRemoteManifest(nil); err == nil {
		t.Fatal("missing remote manifest did not fail")
	}
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	selected, err := findRemoteManifest([]backup.RemoteObject{
		{Key: "prefix/a-restore.json", LastModified: now},
		{Key: "prefix/b-restore.json", LastModified: now},
	})
	if err != nil || selected.Key != "prefix/b-restore.json" {
		t.Fatalf("remote manifest = %#v, %v", selected, err)
	}
	if _, _, err := parseS3URI("https://example.com/prefix"); err == nil {
		t.Fatal("invalid S3 URI did not fail")
	}
	if _, _, err := parseS3URI("s3://"); err == nil {
		t.Fatal("empty S3 bucket did not fail")
	}
	for _, value := range []string{"", ".", "..", "/absolute", "../bad"} {
		if _, err := safeRelative(value); err == nil {
			t.Fatalf("safeRelative(%q) did not fail", value)
		}
	}
	if err := verifyDigest(filepath.Join(t.TempDir(), "missing"), "abc"); err == nil {
		t.Fatal("missing digest file did not fail")
	}
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "file"), []byte("x"))
	if err := verifyDigest(filepath.Join(dir, "file"), "deadbeef"); err == nil {
		t.Fatal("digest mismatch did not fail")
	}
	if err := verifyDigest(dir, "deadbeef"); err == nil {
		t.Fatal("directory digest did not fail")
	}
}

func TestMaterializeManifestErrors(t *testing.T) {
	key := bytes.Repeat([]byte{0x63}, 32)
	extracted := t.TempDir()
	if err := materializeManifest([]byte("bad"), extracted, nil, nil); err == nil {
		t.Fatal("bad manifest did not fail")
	}
	if err := materializeManifest([]byte(`{"encrypted":true}`), extracted, nil, nil); err == nil {
		t.Fatal("encrypted manifest without key did not fail")
	}
	writeTestFile(t, filepath.Join(extracted, "blocked"), []byte("x"))
	source := func(remote string) (string, func(), error) {
		return filepath.Join(extracted, "source"), func() {}, nil
	}
	writeTestFile(t, filepath.Join(extracted, "source"), []byte("payload"))
	cases := []string{
		`{"files":{"../bad":{"remote":"source"}}}`,
		`{"files":{"blocked/child":{"remote":"../source"}}}`,
		`{"files":{"blocked/child":{"remote":"source"}}}`,
		`{"files":{"out":{"remote":"source.enc"}}}`,
		`{"files":{"out":{"remote":"source","sha256":"deadbeef"}}}`,
	}
	for _, data := range cases {
		if err := materializeManifest([]byte(data), extracted, nil, source); err == nil {
			t.Fatalf("manifest %s did not fail", data)
		}
	}
	if err := materializeManifest([]byte(`{"files":{"out":{"remote":"source"}}}`), filepath.Join(extracted, "blocked"), key, source); err == nil {
		t.Fatal("manifest copy into blocked path did not fail")
	}
	sourceErr := func(string) (string, func(), error) { return "", nil, errors.New("source") }
	if err := materializeManifest([]byte(`{"files":{"out":{"remote":"source"}}}`), extracted, nil, sourceErr); err == nil {
		t.Fatal("source error did not fail")
	}
	if err := materializeManifest([]byte(`{"files":{"dec":{"remote":"source.enc"}}}`), extracted, key, source); err == nil {
		t.Fatal("invalid encrypted source did not fail")
	}
	// A file whose source cannot be opened hits the plain-copy branch.
	missingSource := func(string) (string, func(), error) { return filepath.Join(extracted, "missing"), func() {}, nil }
	if err := materializeManifest([]byte(`{"files":{"out":{"remote":"missing"}}}`), extracted, nil, missingSource); err == nil {
		t.Fatal("missing plain source did not fail")
	}
}

func TestCopyAndExtractErrorPaths(t *testing.T) {
	dir := t.TempDir()
	if err := copyFile(filepath.Join(dir, "missing"), filepath.Join(dir, "out"), 0o600); err == nil {
		t.Fatal("missing copy source did not fail")
	}
	blocker := filepath.Join(dir, "blocker")
	writeTestFile(t, blocker, []byte("x"))
	if err := copyFile(blocker, filepath.Join(blocker, "out"), 0o600); err == nil {
		t.Fatal("blocked copy target did not fail")
	}
	if err := copyFile(blocker, dir, 0o600); err == nil {
		t.Fatal("copy over directory did not fail")
	}
	if err := copyFile(dir, filepath.Join(dir, "out"), 0o600); err == nil {
		t.Fatal("copy directory did not fail")
	}
	if err := copyRestoredFile(filepath.Join(dir, "missing"), filepath.Join(dir, "out"), 0o600); err == nil {
		t.Fatal("missing restored source did not fail")
	}
	if err := copyRestoredFile(blocker, filepath.Join(blocker, "out"), 0o600); err == nil {
		t.Fatal("blocked restored target did not fail")
	}
	if err := copyRestoredFile(dir, filepath.Join(dir, "out"), 0o600); err == nil {
		t.Fatal("directory restored source did not fail")
	}
	if err := copyRestoredFile(blocker, dir, 0o600); err == nil {
		t.Fatal("restored target directory did not fail")
	}
	if _, err := isEncrypted(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing encrypted archive did not fail")
	}
	badArchive := filepath.Join(dir, "bad.tar.gz")
	writeTestFile(t, badArchive, []byte("not-gzip"))
	if err := extract(badArchive, filepath.Join(dir, "extract")); err == nil {
		t.Fatal("invalid archive did not fail")
	}
	traversal := filepath.Join(dir, "traversal.tar.gz")
	writeTar(t, traversal, map[string]string{"../evil": "x"})
	if err := extract(traversal, filepath.Join(dir, "extract")); err == nil {
		t.Fatal("traversal archive did not fail")
	}
	blockedDestination := filepath.Join(dir, "blocked-destination")
	writeTestFile(t, blockedDestination, []byte("x"))
	if err := extract(traversal, blockedDestination); err == nil {
		t.Fatal("blocked destination did not fail")
	}
	targetDir := filepath.Join(dir, "target")
	writeTestFile(t, targetDir, []byte("x"))
	dirEntryArchive := filepath.Join(dir, "dir-entry.tar.gz")
	writeTar(t, dirEntryArchive, map[string]string{"dir/": ""})
	if err := extract(dirEntryArchive, filepath.Join(dir, "extract-dir")); err != nil {
		t.Fatal(err)
	}
	if err := extract(dirEntryArchive, targetDir); err == nil {
		t.Fatal("archive destination file did not fail")
	}
	openFail := filepath.Join(dir, "open-fail.tar.gz")
	writeTar(t, openFail, map[string]string{"state.db": "x"})
	openFailDest := filepath.Join(dir, "open-fail")
	if err := os.MkdirAll(filepath.Join(openFailDest, "state.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := extract(openFail, openFailDest); err == nil {
		t.Fatal("archive entry directory collision did not fail")
	}
}

func TestRestoreExtractedAndApplyErrors(t *testing.T) {
	dir := t.TempDir()
	if err := restoreExtracted(dir, Options{Stdout: io.Discard}); err == nil {
		t.Fatal("missing config did not fail")
	}
	writeTestFile(t, filepath.Join(dir, "config.yaml"), []byte("not: [valid"))
	if err := restoreExtracted(dir, Options{Stdout: io.Discard}); err == nil {
		t.Fatal("invalid config did not fail")
	}
	base := t.TempDir()
	configText := restoreConfig(t, base)
	extracted := t.TempDir()
	writeTestFile(t, filepath.Join(extracted, "config.yaml"), []byte(configText))
	writeTestFile(t, filepath.Join(extracted, "state.db"), []byte("db"))
	configTargetDir := filepath.Join(base, "config-dir")
	if err := os.MkdirAll(configTargetDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := restoreExtracted(extracted, Options{ConfigPath: configTargetDir, Apply: true, Stdout: io.Discard}); err == nil {
		t.Fatal("restore onto directory did not fail")
	}
}

func TestRunDefaultOptions(t *testing.T) {
	base := t.TempDir()
	archive := createBackupArchive(t, map[string]string{"config.yaml": restoreConfig(t, base), "state.db": "db"})
	if err := Run(context.Background(), Options{Archive: archive, Apply: false}); err != nil {
		t.Fatal(err)
	}
}

func writeTar(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	file, err := os.Create(path) //nolint:gosec // test archive
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	writer := tar.NewWriter(gzipWriter)
	for name, data := range entries {
		header := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg}
		if strings.HasSuffix(name, "/") {
			header.Typeflag = tar.TypeDir
			header.Size = 0
		}
		if err := writer.WriteHeader(header); err != nil {
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
}

func TestRestoreRemainingBranches(t *testing.T) {
	dir := t.TempDir()
	if _, err := isEncrypted(dir); err == nil {
		t.Fatal("directory encryption check did not fail")
	}
	if err := extract(filepath.Join(dir, "missing"), filepath.Join(dir, "out")); err == nil {
		t.Fatal("missing archive extraction did not fail")
	}
	junk := filepath.Join(dir, "junk.tar.gz")
	file, err := os.Create(junk) //nolint:gosec // test archive
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	_, _ = gzipWriter.Write([]byte("not a tar archive"))
	_ = gzipWriter.Close()
	_ = file.Close()
	if err := extract(junk, filepath.Join(dir, "out")); err == nil {
		t.Fatal("invalid tar extraction did not fail")
	}
	blocker := filepath.Join(dir, "blocker")
	writeTestFile(t, blocker, []byte("x"))
	validArchive := createBackupArchive(t, map[string]string{"config.yaml": "x"})
	if err := extract(validArchive, filepath.Join(blocker, "out")); err == nil {
		t.Fatal("blocked extraction destination did not fail")
	}
	key := bytes.Repeat([]byte{0x64}, 32)
	keyPath := filepath.Join(dir, "key")
	writeTestFile(t, keyPath, key)
	corrupt := filepath.Join(dir, "corrupt.enc")
	writeTestFile(t, corrupt, append([]byte("KOUJA1"), []byte("junk")...))
	if err := materializeArchive(corrupt, filepath.Join(dir, "archive-out"), dir, keyPath); err == nil {
		t.Fatal("corrupt encrypted archive did not fail")
	}
	if _, err := buildPlan(dir, &config.Config{}, filepath.Join(dir, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(dir, "source")
	writeTestFile(t, source, []byte("source"))
	readOnly := filepath.Join(dir, "read-only")
	if err := os.Mkdir(readOnly, 0o500); err != nil { //nolint:gosec // deliberate mode test
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(readOnly, 0o700) }() //nolint:gosec // restore test directory
	if err := copyRestoredFile(source, filepath.Join(readOnly, "out"), 0o600); err == nil {
		t.Fatal("read-only restored destination did not fail")
	}
	nonEmpty := filepath.Join(dir, "non-empty")
	if err := os.MkdirAll(filepath.Join(nonEmpty, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := copyRestoredFile(source, nonEmpty, 0o600); err == nil {
		t.Fatal("restored target over directory did not fail")
	}
	if err := Run(context.Background(), Options{S3URI: "https://example.com/prefix"}); err == nil {
		t.Fatal("invalid S3 URI run did not fail")
	}
	if err := materializeS3(context.Background(), Options{S3URI: "https://example.com/prefix"}, dir, dir); err == nil {
		t.Fatal("invalid S3 materialization did not fail")
	}
}
