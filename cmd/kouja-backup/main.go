package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/voidstarit/kouja/internal/backup"
	"github.com/voidstarit/kouja/internal/config"
	"github.com/voidstarit/kouja/internal/pki"
	"github.com/voidstarit/kouja/internal/state"
	"golang.org/x/sys/unix"
)

func main() {
	if err := run(); err != nil {
		slog.Error("backup stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "/etc/kouja/config.yaml", "path to the agent configuration")
	checkConfig := flag.Bool("check-config", false, "validate the configuration and exit")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *checkConfig {
		_, _ = fmt.Fprintln(os.Stdout, "configuration valid")
		return nil
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	targetDir := cfg.State.BackupTarget
	if targetDir == "" {
		targetDir = cfg.State.BackupDir
		if targetDir == "" {
			return fmt.Errorf("backup target is not configured")
		}
		if err := os.MkdirAll(targetDir, 0o700); err != nil {
			return fmt.Errorf("create backup directory: %w", err)
		}
	}
	lock, err := acquireLock(filepath.Join(cfg.DataDir, "backup.lock"))
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	source, err := state.OpenSnapshotSource(ctx, filepath.Join(cfg.DataDir, "state.db"))
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()

	var s3Options *backup.S3Options
	if cfg.State.S3Backup.Enabled {
		s3Options = &backup.S3Options{
			Bucket:               cfg.State.S3Backup.Bucket,
			Prefix:               cfg.State.S3Backup.Prefix,
			Region:               cfg.State.S3Backup.Region,
			Endpoint:             cfg.State.S3Backup.Endpoint,
			ForcePathStyle:       cfg.State.S3Backup.ForcePathStyle,
			ServerSideEncryption: cfg.State.S3Backup.ServerSideEncryption,
			KMSKeyID:             cfg.State.S3Backup.KMSKeyID,
			PerBackupFolder:      cfg.State.S3Backup.PerBackupFolder,
			RetentionDays:        cfg.State.S3Backup.RetentionDays,
		}
	}
	archiveFiles := map[string]string{
		"config.yaml":       *configPath,
		"emergency.log":     cfg.EmergencyLog,
		"tls/server.crt":    cfg.Listen.TLS.CertFile,
		"tls/server.key":    cfg.Listen.TLS.KeyFile,
		"tls/client-ca.crt": cfg.Listen.TLS.ClientCAFile,
		"ca/ca.key":         cfg.CertificateAuthority.KeyFile,
	}
	archiveDirs := map[string]string{}
	if cfg.CertificateAuthority.CertificateDir != "" {
		archiveDirs["certificates"] = cfg.CertificateAuthority.CertificateDir
	}
	manager, err := backup.New(source, backup.Options{
		TargetDir:          targetDir,
		HostIDFile:         cfg.HostIDFile,
		SecretKeyFile:      cfg.SecretKeyFile,
		RetentionShort:     cfg.State.BackupRetentionShort,
		RetentionDaily:     cfg.State.BackupRetentionDaily,
		RetentionWeekly:    cfg.State.BackupRetentionWeekly,
		JournalSnapshotter: journalFileSnapshotter{path: cfg.CertificateAuthority.RevocationLogFile},
		RevocationLogName:  "revoked-serials.log",
		ArchiveFiles:       archiveFiles,
		ArchiveDirs:        archiveDirs,
		EncryptionKeyFile:  cfg.State.S3Backup.EncryptionKeyFile,
		S3:                 s3Options,
	})
	if err != nil {
		return err
	}
	result, err := manager.Run(ctx)
	if err != nil {
		return err
	}
	logger.Info("backup complete", "manifest", result.ManifestPath, "s3_key", result.S3Key)
	return nil
}

type journalFileSnapshotter struct{ path string }

func (snapshotter journalFileSnapshotter) SnapshotJournal(ctx context.Context, destination string) error {
	return pki.SnapshotJournalFile(ctx, snapshotter.path, destination)
}

func acquireLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // path is derived from the configured data directory
	if err != nil {
		return nil, fmt.Errorf("open backup lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("another backup worker holds %s: %w", path, err)
	}
	return file, nil
}
