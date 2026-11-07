package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAdminUIConfigAndLoadWithData(t *testing.T) {
	cfg, err := Parse([]byte(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminUI.Enabled || cfg.AdminUI.Address != "127.0.0.1:9444" ||
		cfg.AdminUI.ClientCertDir != "/etc/kouja/tls/clients" {
		t.Fatalf("admin defaults = %#v", cfg.AdminUI)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := []byte(validConfig + "\nadmin_ui:\n  enabled: true\n  address: 127.0.0.1:9555\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, data, err := LoadWithData(path)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.AdminUI.Enabled || loaded.AdminUI.Address != "127.0.0.1:9555" {
		t.Fatalf("admin config = %#v", loaded.AdminUI)
	}
	if string(data) != string(raw) {
		t.Fatal("LoadWithData did not preserve bytes")
	}
	for _, address := range []string{"0.0.0.0:9444", "127.0.0.2:9444", "localhost:9444", "127.0.0.1:0", "127.0.0.1:70000"} {
		_, err := Parse([]byte(validConfig + "\nadmin_ui:\n  enabled: false\n  address: " + address + "\n"))
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("address %q error = %v, want ErrInvalid", address, err)
		}
	}
	_, err = Parse([]byte(validConfig + "\nadmin_ui:\n  enabled: false\n  client_cert_dir: relative\n"))
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative client cert dir = %v, want ErrInvalid", err)
	}
}

func TestStorageClassUpdater(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(validConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	warnings, err := SetClass(path, "ssd", "vg1")
	if err != nil || len(warnings) != 0 {
		t.Fatalf("SetClass = %v, %v", warnings, err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.Classes["ssd"] != "vg1" {
		t.Fatalf("classes = %#v", cfg.Storage.Classes)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if _, err := SetClass(path, "other", "vg1"); !errors.Is(err, ErrClassConflict) {
		t.Fatalf("duplicate VG error = %v", err)
	}
	if _, err := SetClass(path, "ssd", "vg2"); err != nil {
		t.Fatalf("replace class: %v", err)
	}
	if _, err := RemoveClass(path, "missing"); !errors.Is(err, ErrClassNotFound) {
		t.Fatalf("missing remove = %v", err)
	}
	if _, err := RemoveClass(path, "ssd"); err != nil {
		t.Fatalf("remove class: %v", err)
	}
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Storage.Classes["ssd"]; ok {
		t.Fatal("class was not removed")
	}
}

func TestStorageClassUpdaterRejectsUnsafePath(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(target, []byte(validConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := SetClass(link, "ssd", "vg1"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestStorageClassUpdaterErrorsAndMissingStorage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(validConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireConfigLock(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SetClass(path, "ssd", "vg1"); !errors.Is(err, ErrLocked) {
		t.Fatalf("locked SetClass = %v", err)
	}
	releaseConfigLock(lock)
	if _, err := SetClass(filepath.Join(dir, "missing.yaml"), "ssd", "vg1"); err == nil {
		t.Fatal("missing file SetClass succeeded")
	}
	if _, err := SetClass(path, "bad class", "vg1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid class = %v", err)
	}
	noStorage := []byte(`
host_id_file: /var/lib/kouja/host-id
data_dir: /var/lib/kouja
secret_key_file: /var/lib/kouja/secret.key
emergency_log: /var/lib/kouja/emergency.log
listen:
  address: 127.0.0.1:9443
  tls: {cert_file: /a, key_file: /b, client_ca_file: /c}
targets:
  iscsi:
    portals: [{address: 127.0.0.1, port: 3260}]
`)
	noStoragePath := filepath.Join(dir, "no-storage.yaml")
	if err := os.WriteFile(noStoragePath, noStorage, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SetClass(noStoragePath, "ssd", "vg1"); err != nil {
		t.Fatalf("create storage section: %v", err)
	}
	if _, err := RemoveClass(noStoragePath, "missing"); !errors.Is(err, ErrClassNotFound) {
		t.Fatalf("missing class = %v", err)
	}
}

func TestLoadWithDataErrors(t *testing.T) {
	if _, _, err := LoadWithData(""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty = %v", err)
	}
	if _, _, err := LoadWithData(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing load succeeded")
	}
}

func TestExampleConfigParses(t *testing.T) {
	path := filepath.Join("..", "..", "deploy", "config.example.yaml")
	raw, err := os.ReadFile(path) //nolint:gosec // repository example file
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(raw); err != nil {
		t.Fatalf("example config: %v", err)
	}
}
