package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/voidstarit/kouja/internal/agent"
	"github.com/voidstarit/kouja/internal/backup"
	"github.com/voidstarit/kouja/internal/config"
	"github.com/voidstarit/kouja/internal/credential"
	"github.com/voidstarit/kouja/internal/hostid"
	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/observability"
	"github.com/voidstarit/kouja/internal/operations"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
	"golang.org/x/sys/unix"
)

const (
	agentVersion = "dev"
	apiVersion   = "v1"
)

func main() {
	if err := run(); err != nil {
		slog.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "/etc/kouja/config.yaml", "path to the agent configuration")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	if err := os.MkdirAll(cfg.State.BackupDir, 0o700); err != nil {
		return fmt.Errorf("create local backup directory: %w", err)
	}
	if err := agent.CheckDependencies(context.Background(), cfg); err != nil {
		return err
	}
	lock, err := acquireLock(filepath.Join(cfg.DataDir, "lock"))
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()

	store, err := state.Open(context.Background(), filepath.Join(cfg.DataDir, "state.db"))
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	hostManager := hostid.New(cfg.HostIDFile, store)
	hostUUID, err := hostManager.Ensure(context.Background())
	if err != nil {
		return err
	}
	if _, err := hostManager.RecordBootID(context.Background()); err != nil {
		return err
	}

	credentials, err := credential.Open(store, cfg.SecretKeyFile)
	if err != nil {
		return err
	}
	lvmBackend := lvm.New(
		lvm.WithHeadroom(cfg.Storage.HeadroomPercent, cfg.Storage.HeadroomMinBytes),
		lvm.WithCriticalPaths([]string{"/", cfg.DataDir, cfg.State.BackupDir}),
	)
	iscsi := target.NewISCSI(target.NewConfigFS(""))
	nvme := target.NewNVMe(target.NewConfigFS(""))
	if err := agent.InitializeVGs(context.Background(), store, lvmBackend, cfg.EffectiveVGs()); err != nil {
		return err
	}

	stepTimeouts := make(map[operations.Action]time.Duration)
	for action, seconds := range cfg.Operations.StepTimeoutSecs {
		stepTimeouts[operations.Action(action)] = time.Duration(seconds) * time.Second
	}
	metrics := observability.New()
	manager := operations.New(store, lvmBackend, iscsi, nvme, credentials, operations.Options{
		MaxAttempts:  cfg.Operations.MaxAttempts,
		TotalTimeout: time.Duration(cfg.Operations.TotalTimeoutSecs) * time.Second,
		StepTimeouts: stepTimeouts,
		Metrics:      metrics,
	})

	var ready atomic.Bool
	admission := agent.NewAdmission(
		cfg.Admission.MaxConcurrentOperations,
		cfg.Admission.PerCallerOperations,
		cfg.Admission.CapacityRequestsPerSec,
	)
	volumeService := agent.NewVolumeService(store, lvmBackend, manager, credentials, agent.ServiceOptions{
		HostUUID:       hostUUID,
		AgentVersion:   agentVersion,
		APIVersion:     apiVersion,
		ISCSIEnabled:   cfg.Targets.ISCSI.Enabled,
		ISCSIPrefix:    cfg.Targets.ISCSI.IQNPrefix,
		ISCSIPortals:   cfg.Targets.ISCSI.Portals,
		NVMeEnabled:    cfg.Targets.NVMeTCP.Enabled,
		NVMePrefix:     cfg.Targets.NVMeTCP.NQNPrefix,
		NVMePortals:    cfg.Targets.NVMeTCP.Portals,
		Ready:          ready.Load,
		Admission:      admission,
		StorageClasses: cfg.Storage.Classes,
	})
	adminService := agent.NewAdminService(store, lvmBackend,
		agent.WithTargetExporters(map[string]target.Exporter{"iscsi": iscsi, "nvme-tcp": nvme}),
		agent.WithCredentialManager(credentials),
		agent.WithDriftRetrier(manager),
		agent.WithStorageClasses(cfg.Storage.Classes),
	)
	server, err := agent.NewServer(cfg, volumeService, adminService,
		agent.WithReadiness(ready.Load), agent.WithMetrics(metrics))
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	backupDone := make(chan struct{})
	go runBackups(ctx, store, cfg, logger, backupDone)
	reconciler := agent.NewReconciler(store, lvmBackend, map[string]target.Exporter{
		"iscsi": iscsi, "nvme-tcp": nvme,
	}, time.Duration(cfg.State.ReconcileIntervalSeconds)*time.Second)
	reconcileDone := make(chan struct{})
	go func() {
		defer close(reconcileDone)
		reconciler.Run(ctx)
	}()
	ready.Store(true)
	logger.Info("agent listening", "address", cfg.Listen.Address, "host_uuid", hostUUID)
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve() }()

	select {
	case <-ctx.Done():
		server.GracefulStop()
		<-reconcileDone
		<-backupDone
		return nil
	case err := <-serveErr:
		ready.Store(false)
		server.Stop()
		<-reconcileDone
		<-backupDone
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	}
}

func acquireLock(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // path is the configured data-directory lock
	if err != nil {
		return nil, fmt.Errorf("open agent lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("another agent instance holds %s: %w", path, err)
	}
	return file, nil
}

func runBackups(ctx context.Context, store *state.Store, cfg *config.Config, logger *slog.Logger, done chan<- struct{}) {
	defer close(done)
	if cfg.State.BackupTarget == "" {
		logger.Warn("backup target is not configured; backups are disabled")
		return
	}
	manager, err := backup.New(store, backup.Options{
		TargetDir:       cfg.State.BackupTarget,
		HostIDFile:      cfg.HostIDFile,
		SecretKeyFile:   cfg.SecretKeyFile,
		RetentionShort:  cfg.State.BackupRetentionShort,
		RetentionDaily:  cfg.State.BackupRetentionDaily,
		RetentionWeekly: cfg.State.BackupRetentionWeekly,
	})
	if err != nil {
		logger.Error("backup configuration failed", "error", err)
		return
	}
	run := func() {
		if _, err := manager.Run(ctx); err != nil {
			logger.Error("backup failed", "error", err)
		}
	}
	run()
	ticker := time.NewTicker(time.Duration(cfg.State.BackupIntervalSeconds) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
