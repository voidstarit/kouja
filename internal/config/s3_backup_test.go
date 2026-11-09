package config

import (
	"errors"
	"strings"
	"testing"
)

func TestS3BackupDefaultsAndValidation(t *testing.T) {
	cfg, err := Parse([]byte(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.State.S3Backup.Enabled || cfg.State.S3Backup.Prefix != "kouja/" ||
		cfg.State.S3Backup.ServerSideEncryption != "AES256" || !cfg.State.S3Backup.PerBackupFolder {
		t.Fatalf("defaults = %#v", cfg.State.S3Backup)
	}
	cfg, err = Parse([]byte(validConfig + `
state:
  s3_backup:
    enabled: true
    bucket: kouja-backups
    prefix: prod/
    region: eu-central-1
    server_side_encryption: aws:kms
    kms_key_id: alias/kouja
    per_backup_folder: false
`))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.State.S3Backup.Enabled || cfg.State.S3Backup.Bucket != "kouja-backups" ||
		cfg.State.S3Backup.KMSKeyID != "alias/kouja" || cfg.State.S3Backup.PerBackupFolder {
		t.Fatalf("s3 config = %#v", cfg.State.S3Backup)
	}
	for _, test := range []struct{ name, extra, want string }{
		{"missing bucket", "\nstate:\n  s3_backup:\n    enabled: true\n", "bucket"},
		{"bad encryption", "\nstate:\n  s3_backup:\n    enabled: true\n    bucket: b\n    server_side_encryption: none\n", "server_side_encryption"},
		{"missing kms key", "\nstate:\n  s3_backup:\n    enabled: true\n    bucket: b\n    server_side_encryption: aws:kms\n", "kms_key_id"},
		{"bad endpoint", "\nstate:\n  s3_backup:\n    enabled: true\n    bucket: b\n    endpoint: not-a-url\n", "endpoint"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(validConfig + test.extra))
			if err == nil || !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want ErrInvalid containing %q", err, test.want)
			}
		})
	}
}
