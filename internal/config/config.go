package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/voidstarit/kouja/internal/naming"
	"gopkg.in/yaml.v3"
)

var ErrInvalid = errors.New("config: invalid")

type Config struct {
	HostIDFile           string                     `yaml:"host_id_file"`
	DataDir              string                     `yaml:"data_dir"`
	SecretKeyFile        string                     `yaml:"secret_key_file"`
	EmergencyLog         string                     `yaml:"emergency_log"`
	Listen               ListenConfig               `yaml:"listen"`
	AdminUI              AdminUIConfig              `yaml:"admin_ui"`
	Storage              StorageConfig              `yaml:"storage"`
	Targets              TargetsConfig              `yaml:"targets"`
	Operations           OperationsConfig           `yaml:"operations"`
	Admission            AdmissionConfig            `yaml:"admission"`
	State                StateConfig                `yaml:"state"`
	CertificateAuthority CertificateAuthorityConfig `yaml:"certificate_authority"`
}

// AdminUIConfig configures the optional loopback-only local administration UI.
type AdminUIConfig struct {
	Enabled bool   `yaml:"enabled"`
	Address string `yaml:"address"`
}

// CertificateAuthorityConfig configures agent-issued client certificates.
type CertificateAuthorityConfig struct {
	Enabled               bool   `yaml:"enabled"`
	KeyFile               string `yaml:"key_file"`
	CertificateDir        string `yaml:"certificate_dir"`
	RevocationLogFile     string `yaml:"revocation_log_file"`
	DefaultTTLDays        int    `yaml:"default_ttl_days"`
	MaxTTLDays            int    `yaml:"max_ttl_days"`
	MaxActiveCertificates int    `yaml:"max_active_certificates"`
}

type ListenConfig struct {
	Address string    `yaml:"address"`
	TLS     TLSConfig `yaml:"tls"`
}

type TLSConfig struct {
	CertFile     string `yaml:"cert_file"`
	KeyFile      string `yaml:"key_file"`
	ClientCAFile string `yaml:"client_ca_file"`
	MinVersion   string `yaml:"min_version"`
}

type StorageConfig struct {
	HeadroomPercent    int64             `yaml:"headroom_percent"`
	HeadroomMinBytes   int64             `yaml:"headroom_min_bytes"`
	WipeTimeoutSeconds int               `yaml:"wipe_timeout_seconds"`
	VGs                []string          `yaml:"vgs"`
	Classes            map[string]string `yaml:"classes"`
}

// EffectiveVGs returns the explicit VGs plus every VG referenced by a storage
// class, deduplicated and in a deterministic order.
func (cfg *Config) EffectiveVGs() []string {
	seen := make(map[string]struct{}, len(cfg.Storage.VGs)+len(cfg.Storage.Classes))
	result := make([]string, 0, len(cfg.Storage.VGs)+len(cfg.Storage.Classes))
	for _, vg := range cfg.Storage.VGs {
		if _, ok := seen[vg]; ok {
			continue
		}
		seen[vg] = struct{}{}
		result = append(result, vg)
	}
	classes := make([]string, 0, len(cfg.Storage.Classes))
	for class := range cfg.Storage.Classes {
		classes = append(classes, class)
	}
	sort.Strings(classes)
	for _, class := range classes {
		vg := cfg.Storage.Classes[class]
		if _, ok := seen[vg]; ok {
			continue
		}
		seen[vg] = struct{}{}
		result = append(result, vg)
	}
	return result
}

type TargetsConfig struct {
	ISCSI   ISCSITargetConfig   `yaml:"iscsi"`
	NVMeTCP NVMeTCPTargetConfig `yaml:"nvme_tcp"`
}

type ISCSITargetConfig struct {
	Enabled   bool     `yaml:"enabled"`
	IQNPrefix string   `yaml:"iqn_prefix"`
	Portals   []Portal `yaml:"portals"`
}

type NVMeTCPTargetConfig struct {
	Enabled   bool          `yaml:"enabled"`
	NQNPrefix string        `yaml:"nqn_prefix"`
	Portals   []Portal      `yaml:"portals"`
	TLS       NVMeTLSConfig `yaml:"tls"`
}

type NVMeTLSConfig struct {
	Enabled      bool   `yaml:"enabled"`
	Keyring      string `yaml:"keyring"`
	TLSHDService string `yaml:"tlshd_service"`
}

type Portal struct {
	Address string `yaml:"address"`
	Port    int    `yaml:"port"`
}

type OperationsConfig struct {
	MaxAttempts      int            `yaml:"max_attempts"`
	TotalTimeoutSecs int            `yaml:"total_timeout_seconds"`
	StepTimeoutSecs  map[string]int `yaml:"step_timeout_seconds"`
}

type AdmissionConfig struct {
	MaxConcurrentOperations int `yaml:"max_concurrent_operations"`
	PerCallerOperations     int `yaml:"per_caller_operations"`
	CapacityRequestsPerSec  int `yaml:"capacity_requests_per_second"`
}

type StateConfig struct {
	BackupDir                  string `yaml:"backup_dir"`
	BackupTarget               string `yaml:"backup_target"`
	BackupIntervalSeconds      int    `yaml:"backup_interval_seconds"`
	BackupRetentionShort       int    `yaml:"backup_retention_short"`
	BackupRetentionDaily       int    `yaml:"backup_retention_daily"`
	BackupRetentionWeekly      int    `yaml:"backup_retention_weekly"`
	IntegrityCheckIntervalSecs int    `yaml:"integrity_check_interval_seconds"`
	ReconcileIntervalSeconds   int    `yaml:"reconcile_interval_seconds"`
	AuditRetentionDays         int    `yaml:"audit_retention_days"`
}

func Default() Config {
	//nolint:gosec // fixed installation paths; the key path is not a credential value
	return Config{
		HostIDFile:    "/var/lib/kouja/host-id",
		DataDir:       "/var/lib/kouja",
		SecretKeyFile: "/var/lib/kouja/secret.key",
		EmergencyLog:  "/var/lib/kouja/emergency.log",
		Listen: ListenConfig{
			Address: "0.0.0.0:9443",
			TLS: TLSConfig{
				CertFile:     "/etc/kouja/tls/server.crt",
				KeyFile:      "/etc/kouja/tls/server.key",
				ClientCAFile: "/etc/kouja/tls/client-ca.crt",
				MinVersion:   "TLS1.3",
			},
		},
		AdminUI: AdminUIConfig{
			Enabled: false,
			Address: "127.0.0.1:9444",
		},
		Storage: StorageConfig{
			HeadroomPercent:    5,
			HeadroomMinBytes:   1 << 30,
			WipeTimeoutSeconds: 1800,
		},
		Targets: TargetsConfig{
			ISCSI: ISCSITargetConfig{
				Enabled:   true,
				IQNPrefix: "iqn.2026-11.io.kouja",
			},
			NVMeTCP: NVMeTCPTargetConfig{
				Enabled:   false,
				NQNPrefix: "nqn.2026-11.io.kouja",
				TLS: NVMeTLSConfig{
					Enabled:      true,
					Keyring:      ".nvme",
					TLSHDService: "tlshd",
				},
			},
		},
		Operations: OperationsConfig{
			MaxAttempts:      3,
			TotalTimeoutSecs: 300,
			StepTimeoutSecs: map[string]int{
				"create": 120,
				"export": 60,
				"delete": 120,
				"resize": 120,
			},
		},
		Admission: AdmissionConfig{
			MaxConcurrentOperations: 64,
			PerCallerOperations:     10,
			CapacityRequestsPerSec:  10,
		},
		State: StateConfig{
			BackupDir:                  "/var/lib/kouja/backups",
			BackupIntervalSeconds:      900,
			BackupRetentionShort:       96,
			BackupRetentionDaily:       7,
			BackupRetentionWeekly:      4,
			IntegrityCheckIntervalSecs: 86400,
			ReconcileIntervalSeconds:   60,
			AuditRetentionDays:         90,
		},
		CertificateAuthority: CertificateAuthorityConfig{
			Enabled:               false,
			RevocationLogFile:     "/var/lib/kouja/revoked-serials.log",
			DefaultTTLDays:        30,
			MaxTTLDays:            365,
			MaxActiveCertificates: 1000,
		},
	}
}

func Load(path string) (*Config, error) {
	cfg, _, err := LoadWithData(path)
	return cfg, err
}

// LoadWithData loads and parses the configuration and returns the exact bytes
// that were read. The raw bytes are used for the admin UI recovery file.
func LoadWithData(path string) (*Config, []byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil, fmt.Errorf("%w: empty configuration path", ErrInvalid)
	}
	data, err := os.ReadFile(path) //nolint:gosec // path is the operator-supplied configuration file
	if err != nil {
		return nil, nil, fmt.Errorf("config: read %q: %w", path, err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, nil, err
	}
	return cfg, data, nil
}

func Parse(data []byte) (*Config, error) {
	cfg := Default()
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config: parse YAML: %w", err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (cfg *Config) applyDefaults() {
	defaults := Default()
	if cfg.AdminUI.Address == "" {
		cfg.AdminUI.Address = defaults.AdminUI.Address
	}
	if cfg.CertificateAuthority.KeyFile == "" && cfg.Listen.TLS.ClientCAFile != "" {
		cfg.CertificateAuthority.KeyFile = filepath.Join(filepath.Dir(cfg.Listen.TLS.ClientCAFile), "ca.key")
	}
	if cfg.CertificateAuthority.CertificateDir == "" && cfg.Listen.TLS.CertFile != "" {
		cfg.CertificateAuthority.CertificateDir = filepath.Join(filepath.Dir(cfg.Listen.TLS.CertFile), "clients")
	}
	if cfg.CertificateAuthority.RevocationLogFile == "" {
		cfg.CertificateAuthority.RevocationLogFile = defaults.CertificateAuthority.RevocationLogFile
	}
	for _, action := range []string{"create", "export", "delete", "resize"} {
		if cfg.Operations.StepTimeoutSecs == nil {
			cfg.Operations.StepTimeoutSecs = make(map[string]int)
		}
		if cfg.Operations.StepTimeoutSecs[action] == 0 {
			cfg.Operations.StepTimeoutSecs[action] = defaults.Operations.StepTimeoutSecs[action]
		}
	}
}

func (cfg *Config) Validate() error {
	for kind, path := range map[string]string{
		"host_id_file":    cfg.HostIDFile,
		"data_dir":        cfg.DataDir,
		"secret_key_file": cfg.SecretKeyFile,
		"emergency_log":   cfg.EmergencyLog,
		"tls cert_file":   cfg.Listen.TLS.CertFile,
		"tls key_file":    cfg.Listen.TLS.KeyFile,
		"tls client_ca":   cfg.Listen.TLS.ClientCAFile,
	} {
		if err := requireAbsolute(kind, path); err != nil {
			return err
		}
	}
	if cfg.Listen.TLS.MinVersion != "TLS1.3" {
		return fmt.Errorf("%w: listen.tls.min_version must be TLS1.3", ErrInvalid)
	}
	if err := validateHostPort("listen.address", cfg.Listen.Address); err != nil {
		return err
	}
	if err := validateAdminUIAddress(cfg.AdminUI.Address); err != nil {
		return err
	}
	if cfg.Storage.HeadroomPercent < 0 || cfg.Storage.HeadroomPercent > 100 {
		return fmt.Errorf("%w: storage.headroom_percent must be between 0 and 100", ErrInvalid)
	}
	if cfg.Storage.HeadroomMinBytes < 0 {
		return fmt.Errorf("%w: storage.headroom_min_bytes must not be negative", ErrInvalid)
	}
	if cfg.Storage.WipeTimeoutSeconds <= 0 {
		return fmt.Errorf("%w: storage.wipe_timeout_seconds must be positive", ErrInvalid)
	}
	validators := naming.NewValidators()
	for _, vg := range cfg.Storage.VGs {
		if err := validators.VolumeGroupName(vg); err != nil {
			return fmt.Errorf("%w: invalid storage.vgs entry %q", ErrInvalid, vg)
		}
	}
	classVGs := make(map[string]string, len(cfg.Storage.Classes))
	for class, vg := range cfg.Storage.Classes {
		if err := validators.StorageClassName(class); err != nil {
			return fmt.Errorf("%w: invalid storage.classes name %q", ErrInvalid, class)
		}
		if err := validators.VolumeGroupName(vg); err != nil {
			return fmt.Errorf("%w: invalid storage.classes.%s VG %q", ErrInvalid, class, vg)
		}
		if other, ok := classVGs[vg]; ok {
			return fmt.Errorf("%w: storage classes %q and %q reference the same VG %q", ErrInvalid, other, class, vg)
		}
		classVGs[vg] = class
	}
	if cfg.Targets.ISCSI.Enabled {
		if err := validateTargetPortals("targets.iscsi", cfg.Targets.ISCSI.IQNPrefix, cfg.Targets.ISCSI.Portals); err != nil {
			return err
		}
	}
	if cfg.Targets.NVMeTCP.Enabled {
		if err := validateTargetPortals("targets.nvme_tcp", cfg.Targets.NVMeTCP.NQNPrefix, cfg.Targets.NVMeTCP.Portals); err != nil {
			return err
		}
		if !cfg.Targets.NVMeTCP.TLS.Enabled {
			return fmt.Errorf("%w: targets.nvme_tcp.tls.enabled must be true in v1", ErrInvalid)
		}
		if cfg.Targets.NVMeTCP.TLS.Keyring == "" || cfg.Targets.NVMeTCP.TLS.TLSHDService == "" {
			return fmt.Errorf("%w: targets.nvme_tcp.tls keyring and tlshd_service are required", ErrInvalid)
		}
	}
	if cfg.Operations.MaxAttempts <= 0 {
		return fmt.Errorf("%w: operations.max_attempts must be positive", ErrInvalid)
	}
	if cfg.Operations.TotalTimeoutSecs <= 0 {
		return fmt.Errorf("%w: operations.total_timeout_seconds must be positive", ErrInvalid)
	}
	for action, seconds := range cfg.Operations.StepTimeoutSecs {
		if seconds <= 0 {
			return fmt.Errorf("%w: operations.step_timeout_seconds.%s must be positive", ErrInvalid, action)
		}
	}
	if cfg.Admission.MaxConcurrentOperations <= 0 || cfg.Admission.PerCallerOperations <= 0 || cfg.Admission.CapacityRequestsPerSec <= 0 {
		return fmt.Errorf("%w: admission values must be positive", ErrInvalid)
	}
	if err := requireAbsolute("state.backup_dir", cfg.State.BackupDir); err != nil {
		return err
	}
	if cfg.State.BackupTarget != "" {
		if err := requireAbsolute("state.backup_target", cfg.State.BackupTarget); err != nil {
			return err
		}
	}
	if cfg.State.BackupIntervalSeconds <= 0 || cfg.State.BackupRetentionShort <= 0 ||
		cfg.State.BackupRetentionDaily <= 0 || cfg.State.BackupRetentionWeekly <= 0 ||
		cfg.State.IntegrityCheckIntervalSecs <= 0 || cfg.State.ReconcileIntervalSeconds <= 0 ||
		cfg.State.AuditRetentionDays <= 0 {
		return fmt.Errorf("%w: state interval and retention values must be positive", ErrInvalid)
	}
	if err := requireAbsolute("certificate_authority.key_file", cfg.CertificateAuthority.KeyFile); err != nil {
		return err
	}
	if err := requireAbsolute("certificate_authority.certificate_dir", cfg.CertificateAuthority.CertificateDir); err != nil {
		return err
	}
	if err := requireAbsolute("certificate_authority.revocation_log_file", cfg.CertificateAuthority.RevocationLogFile); err != nil {
		return err
	}
	if cfg.CertificateAuthority.DefaultTTLDays < 1 || cfg.CertificateAuthority.DefaultTTLDays > 365 {
		return fmt.Errorf("%w: certificate_authority.default_ttl_days must be between 1 and 365", ErrInvalid)
	}
	if cfg.CertificateAuthority.MaxTTLDays < 1 || cfg.CertificateAuthority.MaxTTLDays > 365 {
		return fmt.Errorf("%w: certificate_authority.max_ttl_days must be between 1 and 365", ErrInvalid)
	}
	if cfg.CertificateAuthority.MaxTTLDays < cfg.CertificateAuthority.DefaultTTLDays {
		return fmt.Errorf("%w: certificate_authority.max_ttl_days must be >= default_ttl_days", ErrInvalid)
	}
	if cfg.CertificateAuthority.MaxActiveCertificates <= 0 {
		return fmt.Errorf("%w: certificate_authority.max_active_certificates must be positive", ErrInvalid)
	}
	for _, other := range []string{
		cfg.Listen.TLS.CertFile,
		cfg.Listen.TLS.KeyFile,
		cfg.Listen.TLS.ClientCAFile,
		cfg.CertificateAuthority.KeyFile,
	} {
		if other == "" {
			continue
		}
		if filepath.Clean(cfg.CertificateAuthority.CertificateDir) == filepath.Clean(filepath.Dir(other)) {
			return fmt.Errorf("%w: certificate_authority.certificate_dir must not share a directory with %s", ErrInvalid, other)
		}
	}
	if sameOrWithin(cfg.CertificateAuthority.RevocationLogFile, cfg.CertificateAuthority.CertificateDir) {
		return fmt.Errorf("%w: certificate_authority.revocation_log_file must not be inside certificate_dir", ErrInvalid)
	}
	for _, other := range []string{
		cfg.Listen.TLS.CertFile,
		cfg.Listen.TLS.KeyFile,
		cfg.Listen.TLS.ClientCAFile,
		cfg.CertificateAuthority.KeyFile,
	} {
		if other != "" && filepath.Clean(cfg.CertificateAuthority.RevocationLogFile) == filepath.Clean(other) {
			return fmt.Errorf("%w: certificate_authority.revocation_log_file must not equal %s", ErrInvalid, other)
		}
	}
	return nil
}

func sameOrWithin(path, dir string) bool {
	path = filepath.Clean(path)
	dir = filepath.Clean(dir)
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

func requireAbsolute(kind, path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("%w: %s must be an absolute path", ErrInvalid, kind)
	}
	return nil
}

func validateHostPort(kind, address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil || host == "" || portText == "" {
		return fmt.Errorf("%w: %s must be host:port", ErrInvalid, kind)
	}
	port, err := net.LookupPort("tcp", portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("%w: %s has invalid port", ErrInvalid, kind)
	}
	return nil
}

func validateAdminUIAddress(address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil || (host != "127.0.0.1" && host != "::1") {
		return fmt.Errorf("%w: admin_ui.address must be a loopback IP literal:port", ErrInvalid)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("%w: admin_ui.address has invalid port", ErrInvalid)
	}
	return nil
}

func validateTargetPortals(kind, prefix string, portals []Portal) error {
	if prefix == "" {
		return fmt.Errorf("%w: %s prefix is required", ErrInvalid, kind)
	}
	if len(portals) == 0 {
		return fmt.Errorf("%w: %s.portals must contain at least one portal", ErrInvalid, kind)
	}
	for _, portal := range portals {
		if net.ParseIP(portal.Address) == nil || net.ParseIP(portal.Address).To4() == nil {
			return fmt.Errorf("%w: %s portal address must be IPv4: %q", ErrInvalid, kind, portal.Address)
		}
		if portal.Port < 1 || portal.Port > 65535 {
			return fmt.Errorf("%w: %s portal port is invalid: %d", ErrInvalid, kind, portal.Port)
		}
	}
	return nil
}
