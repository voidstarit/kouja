package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
)

type Reconciler struct {
	store     *state.Store
	lvm       lvm.Backend
	exporters map[string]target.Exporter
	interval  time.Duration
	now       func() time.Time
}

func NewReconciler(store *state.Store, lvmBackend lvm.Backend, exporters map[string]target.Exporter, interval time.Duration) *Reconciler {
	if interval <= 0 {
		interval = time.Minute
	}
	return &Reconciler{
		store: store, lvm: lvmBackend, exporters: exporters, interval: interval,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (reconciler *Reconciler) Run(ctx context.Context) {
	reconciler.reconcileAndLog(ctx)
	ticker := time.NewTicker(reconciler.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconciler.reconcileAndLog(ctx)
		}
	}
}

func (reconciler *Reconciler) reconcileAndLog(ctx context.Context) {
	if err := reconciler.Reconcile(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("reconcile failed", "error", err)
	}
}

func (reconciler *Reconciler) Reconcile(ctx context.Context) error {
	cursor := ""
	for {
		var volumes []state.Volume
		if err := reconciler.store.Transact(ctx, func(tx state.Tx) error {
			values, err := tx.ListVolumes("", state.Page{Limit: 100, Cursor: cursor})
			volumes = values
			return err
		}); err != nil {
			return err
		}
		if len(volumes) == 0 {
			return nil
		}
		for _, volume := range volumes {
			if err := reconciler.reconcileVolume(ctx, volume); err != nil {
				return err
			}
		}
		cursor = volumes[len(volumes)-1].VolumeID
		if len(volumes) < 100 {
			return nil
		}
	}
}

func (reconciler *Reconciler) reconcileVolume(ctx context.Context, volume state.Volume) error {
	now := reconciler.now()
	observed, observeErr := reconciler.lvm.Observe(ctx, volume.VGName, volume.LVName)
	present := observeErr == nil
	observation := state.Observation{
		Owner: volume.Owner, VolumeID: volume.VolumeID, LVPresent: present,
		ObservedAt: now, Source: "lvm",
	}
	if present {
		observation.ObservedSizeBytes = observed.SizeBytes
		observation.DevicePath = observed.DevicePath
	}
	if err := reconciler.store.Transact(ctx, func(tx state.Tx) error {
		return tx.PutObservation(observation)
	}); err != nil {
		return err
	}

	reasons := make([]string, 0)
	if !present && (volume.State == "available" || volume.State == "exported" || volume.State == "drift") {
		reasons = append(reasons, "lv_absent")
	}
	if present && volume.ActualSizeBytes != 0 && observed.SizeBytes != volume.ActualSizeBytes {
		reasons = append(reasons, "size_mismatch")
	}

	var export state.Export
	exportErr := reconciler.store.Transact(ctx, func(tx state.Tx) error {
		value, err := tx.GetExport(volume.Owner, volume.VolumeID)
		if err != nil {
			return err
		}
		export = value
		return nil
	})
	if exportErr != nil && !errors.Is(exportErr, state.ErrNotFound) {
		return exportErr
	}
	if exportErr == nil {
		exporter := reconciler.exporters[export.Protocol]
		if exporter != nil {
			exportObservation, observeErr := exporter.Observe(ctx, specForExport(volume, export))
			if observeErr != nil && !errors.Is(observeErr, target.ErrNotFound) {
				return observeErr
			}
			record := state.ExportObservation{
				Owner: volume.Owner, VolumeID: volume.VolumeID,
				TargetPresent:     exportObservation.TargetPresent,
				NamespacePresent:  exportObservation.NamespacePresent,
				ACLPresent:        exportObservation.ACLPresent,
				ActiveSessions:    exportObservation.ActiveSessions,
				SessionState:      string(exportObservation.SessionState),
				ObservedInitiator: exportObservation.ObservedInitiator,
				ObservedAt:        now, Source: "target",
			}
			if record.SessionState == "" {
				record.SessionState = string(target.SessionUnknown)
			}
			if err := reconciler.store.Transact(ctx, func(tx state.Tx) error {
				return tx.PutExportObservation(record)
			}); err != nil {
				return err
			}
			if export.State == "exported" {
				if !record.TargetPresent {
					reasons = append(reasons, "target_absent")
				}
				if export.Protocol == "nvme-tcp" && !record.NamespacePresent {
					reasons = append(reasons, "namespace_absent")
				}
				if !record.ACLPresent {
					reasons = append(reasons, "acl_absent")
				}
				if record.ObservedInitiator != "" && record.ObservedInitiator != export.Initiator {
					reasons = append(reasons, "initiator_mismatch")
				}
			}
		}
	}

	if len(reasons) != 0 && volume.State != "deleting" {
		for _, reason := range reasons {
			if !containsString(volume.DriftReasons, reason) {
				volume.DriftReasons = append(volume.DriftReasons, reason)
			}
		}
		volume.State = "drift"
		volume.UpdatedAt = now
		if err := reconciler.store.Transact(ctx, func(tx state.Tx) error {
			return tx.PutVolume(volume)
		}); err != nil {
			return err
		}
	}
	return nil
}

func specForExport(volume state.Volume, export state.Export) target.ExportSpec {
	return target.ExportSpec{
		Owner: volume.Owner, VolumeID: volume.VolumeID, ResourceUUID: volume.ResourceUUID,
		VGName: volume.VGName, LVName: volume.LVName, Protocol: export.Protocol,
		Initiator: export.Initiator, TargetName: export.TargetName,
		LUN: export.LUN, NSID: export.NSID, Portal: export.Portal,
	}
}
