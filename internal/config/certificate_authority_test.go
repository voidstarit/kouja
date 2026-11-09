package config

import (
	"errors"
	"strings"
	"testing"
)

func TestCertificateAuthorityDefaultsAndOverrides(t *testing.T) {
	cfg, err := Parse([]byte(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CertificateAuthority.Enabled ||
		cfg.CertificateAuthority.DefaultTTLDays != 30 ||
		cfg.CertificateAuthority.MaxTTLDays != 365 ||
		cfg.CertificateAuthority.MaxActiveCertificates != 1000 ||
		cfg.CertificateAuthority.KeyFile == "" ||
		cfg.CertificateAuthority.CertificateDir == "" ||
		cfg.CertificateAuthority.RevocationLogFile == "" {
		t.Fatalf("defaults = %#v", cfg.CertificateAuthority)
	}
	cfg, err = Parse([]byte(validConfig + `
certificate_authority:
  enabled: true
  key_file: /etc/kouja/tls/ca.key
  certificate_dir: /etc/kouja/tls/clients
  revocation_log_file: /var/lib/kouja/revoked.log
  default_ttl_days: 60
  max_ttl_days: 180
  max_active_certificates: 20
`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.CertificateAuthority.Enabled ||
		cfg.CertificateAuthority.DefaultTTLDays != 60 ||
		cfg.CertificateAuthority.MaxTTLDays != 180 ||
		cfg.CertificateAuthority.MaxActiveCertificates != 20 {
		t.Fatalf("overrides = %#v", cfg.CertificateAuthority)
	}
}

func TestCertificateAuthorityValidation(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{"default ttl zero", validConfig + "certificate_authority:\n  default_ttl_days: 0\n", "default_ttl_days"},
		{"max ttl below default", validConfig + "certificate_authority:\n  default_ttl_days: 100\n  max_ttl_days: 90\n", "max_ttl_days"},
		{"max active zero", validConfig + "certificate_authority:\n  max_active_certificates: 0\n", "max_active_certificates"},
		{"cert dir equals tls root", validConfig + "certificate_authority:\n  certificate_dir: /etc/kouja/tls\n", "certificate_dir"},
		{"revocation log inside cert dir", validConfig + "certificate_authority:\n  certificate_dir: /var/lib/kouja/certs\n  revocation_log_file: /var/lib/kouja/certs/revoked.log\n", "revocation_log_file"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.yaml))
			if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want ErrInvalid containing %q", err, test.want)
			}
		})
	}
}
