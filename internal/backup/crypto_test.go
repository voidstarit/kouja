package backup

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupEncryptionRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0x42}, 32)
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain.tar.gz")
	encrypted := filepath.Join(dir, "plain.tar.gz.enc")
	decrypted := filepath.Join(dir, "decrypted.tar.gz")
	if err := os.WriteFile(plain, []byte("recovery bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EncryptFile(plain, encrypted, key); err != nil {
		t.Fatal(err)
	}
	if err := DecryptFile(encrypted, decrypted, key); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(decrypted); err != nil || string(data) != "recovery bundle" { //nolint:gosec // test file
		t.Fatalf("decrypted = %q, %v", data, err)
	}
	if err := DecryptFile(encrypted, decrypted, bytes.Repeat([]byte{0x43}, 32)); err == nil {
		t.Fatal("wrong key decrypted the archive")
	}
}

func TestLoadBackupKeyFormats(t *testing.T) {
	dir := t.TempDir()
	raw := bytes.Repeat([]byte{0x01}, 32)
	rawPath := filepath.Join(dir, "raw.key")
	if err := os.WriteFile(rawPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if key, err := LoadBackupKey(rawPath); err != nil || !bytes.Equal(key, raw) {
		t.Fatalf("raw key = %v, %v", key, err)
	}
	newlinePath := filepath.Join(dir, "raw-newline.key")
	if err := os.WriteFile(newlinePath, append(append([]byte{}, raw...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if key, err := LoadBackupKey(newlinePath); err != nil || !bytes.Equal(key, raw) {
		t.Fatalf("raw newline key = %v, %v", key, err)
	}
	textPath := filepath.Join(dir, "key.txt")
	encoded := base64.StdEncoding.EncodeToString(raw)
	if err := os.WriteFile(textPath, []byte(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
	if key, err := LoadBackupKey(textPath); err != nil || !bytes.Equal(key, raw) {
		t.Fatalf("base64 key = %v, %v", key, err)
	}
}
