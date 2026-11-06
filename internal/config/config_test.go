package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const validConfig = `
host_id_file: /var/lib/kouja/host-id
data_dir: /var/lib/kouja
secret_key_file: /var/lib/kouja/secret.key
emergency_log: /var/lib/kouja/emergency.log
listen:
  address: 127.0.0.1:9443
  tls:
    cert_file: /etc/kouja/tls/server.crt
    key_file: /etc/kouja/tls/server.key
    client_ca_file: /etc/kouja/tls/client-ca.crt
storage:
  vgs: [vg1]
targets:
  iscsi:
    portals:
      - address: 127.0.0.1
        port: 3260
`

func bddTestParseAppliesDefaultsAndValidates(t testing.TB) {
	cfg, err := Parse([]byte(validConfig))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Listen.TLS.MinVersion != "TLS1.3" || cfg.Storage.HeadroomPercent != 5 ||
		cfg.Storage.HeadroomMinBytes != 1<<30 || cfg.Targets.ISCSI.IQNPrefix != "iqn.2026-11.io.kouja" ||
		cfg.Targets.ISCSI.Enabled != true || cfg.Operations.MaxAttempts != 3 ||
		cfg.Operations.StepTimeoutSecs["create"] != 120 || cfg.Admission.MaxConcurrentOperations != 64 ||
		cfg.State.BackupRetentionShort != 96 || cfg.State.AuditRetentionDays != 90 {
		t.Fatalf("defaults not applied: %#v", cfg)
	}
	if len(cfg.Storage.VGs) != 1 || cfg.Storage.VGs[0] != "vg1" {
		t.Fatalf("storage VGs: %#v", cfg.Storage.VGs)
	}
}

func bddTestLoadFromFile(t testing.TB) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(validConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DataDir != "/var/lib/kouja" {
		t.Fatalf("DataDir = %q", cfg.DataDir)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("Load missing file succeeded")
	}
	if _, err := Load(""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Load empty path = %v, want ErrInvalid", err)
	}
}

func bddTestUnknownFieldRejected(t testing.TB) {
	_, err := Parse([]byte(validConfig + "\nunknown_field: true\n"))
	if err == nil {
		t.Fatal("unknown field was accepted")
	}
}

func bddTestValidationFailures(t testing.TB) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "missing TLS",
			yaml: `
host_id_file: /var/lib/kouja/host-id
data_dir: /var/lib/kouja
secret_key_file: /var/lib/kouja/secret.key
emergency_log: /var/lib/kouja/emergency.log
listen:
  address: 127.0.0.1:9443
  tls: {cert_file: "", key_file: "", client_ca_file: ""}
targets:
  iscsi:
    portals: [{address: 127.0.0.1, port: 3260}]
`,
		},
		{
			name: "bad portal",
			yaml: `
host_id_file: /var/lib/kouja/host-id
data_dir: /var/lib/kouja
secret_key_file: /var/lib/kouja/secret.key
emergency_log: /var/lib/kouja/emergency.log
listen:
  address: 127.0.0.1:9443
  tls: {cert_file: /a, key_file: /b, client_ca_file: /c}
storage: {vgs: [vg1]}
targets:
  iscsi:
    portals: [{address: 2001:db8::1, port: 3260}]
`,
		},
		{
			name: "bad headroom",
			yaml: `
host_id_file: /var/lib/kouja/host-id
data_dir: /var/lib/kouja
secret_key_file: /var/lib/kouja/secret.key
emergency_log: /var/lib/kouja/emergency.log
listen:
  address: 127.0.0.1:9443
  tls: {cert_file: /a, key_file: /b, client_ca_file: /c}
storage: {headroom_percent: 101, vgs: [vg1]}
targets:
  iscsi:
    portals: [{address: 127.0.0.1, port: 3260}]
`,
		},
		{
			name: "nvme without TLS",
			yaml: `
host_id_file: /var/lib/kouja/host-id
data_dir: /var/lib/kouja
secret_key_file: /var/lib/kouja/secret.key
emergency_log: /var/lib/kouja/emergency.log
listen:
  address: 127.0.0.1:9443
  tls: {cert_file: /a, key_file: /b, client_ca_file: /c}
storage: {vgs: [vg1]}
targets:
  iscsi:
    portals: [{address: 127.0.0.1, port: 3260}]
  nvme_tcp:
    enabled: true
    portals: [{address: 127.0.0.1, port: 4420}]
    tls: {enabled: false}
`,
		},
	}
	for _, test := range tests {
		bddSubtest(t, test.name, func(t testing.TB) {
			if _, err := Parse([]byte(test.yaml)); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
		})
	}
}

func bddTestConfigValidationBranches(t testing.TB) {
	cfg := Default()
	cfg.Listen.TLS.MinVersion = "TLS1.2"
	if err := cfg.Validate(); err == nil {
		t.Fatal("TLS 1.2 accepted")
	}
	cfg = Default()
	cfg.Storage.HeadroomPercent = 101
	if err := cfg.Validate(); err == nil {
		t.Fatal("invalid headroom accepted")
	}
	cfg = Default()
	cfg.Storage.WipeTimeoutSeconds = 0
	if err := cfg.Validate(); err == nil {
		t.Fatal("zero wipe timeout accepted")
	}
	cfg = Default()
	cfg.Targets.ISCSI.Enabled = true
	cfg.Targets.ISCSI.Portals = nil
	if err := cfg.Validate(); err == nil {
		t.Fatal("missing iSCSI portals accepted")
	}
	cfg = Default()
	cfg.Targets.NVMeTCP.Enabled = true
	cfg.Targets.NVMeTCP.Portals = []Portal{{Address: "127.0.0.1", Port: 4420}}
	cfg.Targets.NVMeTCP.TLS.Enabled = false
	if err := cfg.Validate(); err == nil {
		t.Fatal("NVMe without TLS accepted")
	}
	cfg = Default()
	cfg.Targets.NVMeTCP.Enabled = true
	cfg.Targets.NVMeTCP.Portals = []Portal{{Address: "127.0.0.1", Port: 4420}}
	cfg.Targets.NVMeTCP.TLS.Keyring = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("NVMe without keyring accepted")
	}
	if err := validateHostPort("x", "bad"); err == nil {
		t.Fatal("bad host port accepted")
	}
	if err := validateHostPort("x", "127.0.0.1:99999"); err == nil {
		t.Fatal("bad port accepted")
	}
	if err := validateTargetPortals("x", "", nil); err == nil {
		t.Fatal("missing prefix accepted")
	}
	if err := validateTargetPortals("x", "prefix", nil); err == nil {
		t.Fatal("missing portals accepted")
	}
	if err := validateTargetPortals("x", "prefix", []Portal{{Address: "example", Port: 1}}); err == nil {
		t.Fatal("invalid portal address accepted")
	}
	if err := requireAbsolute("x", "relative"); err == nil {
		t.Fatal("relative path accepted")
	}
	cfg = Default()
	cfg.Operations.StepTimeoutSecs = nil
	cfg.applyDefaults()
	if cfg.Operations.StepTimeoutSecs["create"] == 0 {
		t.Fatal("defaults were not applied")
	}
}
