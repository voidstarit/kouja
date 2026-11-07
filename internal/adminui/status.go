package adminui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/voidstarit/kouja/internal/config"
	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/state"
)

func (server *Server) buildStatus(ctx context.Context) (*StatusResponse, error) {
	var vgs []state.VG
	var volumes []state.VolumeStatus
	var counts map[string]int
	var operations []state.Operation
	var operationCount int
	err := server.options.Store.Transact(ctx, func(tx state.Tx) error {
		values, err := tx.ListVGs()
		if err != nil {
			return err
		}
		vgs = values
		volumes, err = tx.ListVolumeStatuses(256)
		if err != nil {
			return err
		}
		counts, err = tx.VolumeStateCounts()
		if err != nil {
			return err
		}
		operations, err = tx.ListRecentOperations(64)
		if err != nil {
			return err
		}
		operationCount, err = tx.OperationCount()
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errStoreUnavailable, err)
	}
	configState, err := server.configState()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errConfigMarshal, err)
	}

	configured := make(map[string]bool)
	for _, name := range server.effectiveVGs() {
		configured[name] = true
	}
	vgViews, capacityErrors := server.probeVGs(ctx, vgs, configured)
	known := make(map[string]bool, len(vgViews))
	for _, vg := range vgViews {
		known[vg.Name] = true
	}
	configuredNames := make([]string, 0, len(configured))
	for name := range configured {
		configuredNames = append(configuredNames, name)
	}
	sort.Strings(configuredNames)
	for _, name := range configuredNames {
		if known[name] {
			continue
		}
		vgViews = append(vgViews, VGStatus{
			Name: name, State: "not_registered", Configured: true,
			StorageClass: server.options.Classes.ClassName(name),
			Error:        "configured VG is not registered",
		})
	}
	sort.Slice(vgViews, func(left, right int) bool { return vgViews[left].Name < vgViews[right].Name })

	now := server.options.Now()
	interval := time.Duration(server.options.Config.State.ReconcileIntervalSeconds) * time.Second
	volumeViews := make([]VolumeStatus, 0, len(volumes))
	for _, volume := range volumes {
		volumeViews = append(volumeViews, server.volumeView(volume, now, interval))
	}
	totalVolumes := 0
	for _, count := range counts {
		totalVolumes += count
	}
	operationViews := make([]OperationView, 0, len(operations))
	for _, operation := range operations {
		operationViews = append(operationViews, operationView(operation))
	}
	metrics := server.buildMetrics(ctx, volumes)

	status := &StatusResponse{
		Agent: AgentStatus{
			Version:         server.options.AgentVersion,
			APIVersion:      server.options.APIVersion,
			HostUUID:        server.options.HostUUID,
			Ready:           server.options.Ready(),
			StartedAt:       formatTime(server.options.StartedAt),
			UptimeSeconds:   int64(now.Sub(server.options.StartedAt).Seconds()),
			ConfigPath:      server.options.ConfigPath,
			ConfigChanged:   configState.ConfigChanged,
			RestartRequired: configState.RestartRequired,
			RestartReason:   configState.RestartReason,
			LastGoodError:   configState.LastGoodError,
			ListenAddress:   server.options.Config.Listen.Address,
			AdminAddress:    server.options.Config.AdminUI.Address,
			Protocols:       protocols(server.options.Config),
		},
		ISCSI:      iscsiView(server.options.Config),
		Metrics:    metrics,
		VGs:        vgViews,
		Volumes:    volumeViews,
		Operations: operationViews,
		Errors:     capacityErrors,
		Counts: Counts{
			Volumes:             totalVolumes,
			ByState:             counts,
			TruncatedVolumes:    totalVolumes > len(volumeViews),
			Operations:          operationCount,
			TruncatedOperations: operationCount > len(operationViews),
		},
	}
	if !configState.DiskValid {
		code := "config_" + configState.DiskErrorCode
		switch configState.DiskErrorCode {
		case "parse_error":
			code = "config_invalid"
		case "unsafe_path":
			code = "config_unsafe"
		case "read_error":
			code = "config_unreadable"
		}
		status.Errors = append(status.Errors, ErrorView{Code: code, Message: configState.DiskError})
	}
	if status.Errors == nil {
		status.Errors = []ErrorView{}
	}
	if status.Volumes == nil {
		status.Volumes = []VolumeStatus{}
	}
	if status.Operations == nil {
		status.Operations = []OperationView{}
	}
	return status, nil
}

func (server *Server) listVGStatuses(ctx context.Context) ([]VGStatus, error) {
	var vgs []state.VG
	err := server.options.Store.Transact(ctx, func(tx state.Tx) error {
		values, err := tx.ListVGs()
		vgs = values
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errStoreUnavailable, err)
	}
	configured := make(map[string]bool)
	for _, name := range server.effectiveVGs() {
		configured[name] = true
	}
	views, _ := server.probeVGs(ctx, vgs, configured)
	known := make(map[string]bool, len(views))
	for _, vg := range views {
		known[vg.Name] = true
	}
	for name := range configured {
		if known[name] {
			continue
		}
		views = append(views, VGStatus{
			Name: name, State: "not_registered", Configured: true,
			StorageClass: server.options.Classes.ClassName(name),
			Error:        "configured VG is not registered",
		})
	}
	sort.Slice(views, func(left, right int) bool { return views[left].Name < views[right].Name })
	return views, nil
}

func (server *Server) effectiveVGs() []string {
	if server.options.Config == nil {
		return nil
	}
	copyOfConfig := *server.options.Config
	copyOfConfig.Storage.Classes = server.options.Classes.Snapshot()
	return copyOfConfig.EffectiveVGs()
}

func (server *Server) probeVGs(ctx context.Context, vgs []state.VG, configured map[string]bool) ([]VGStatus, []ErrorView) {
	type result struct {
		index    int
		capacity lvm.Capacity
		err      error
	}
	results := make(chan result, len(vgs))
	semaphore := make(chan struct{}, 4)
	var group sync.WaitGroup
	for index, vg := range vgs {
		group.Add(1)
		go func(index int, vg state.VG) {
			defer group.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				results <- result{index: index, err: ctx.Err()}
				return
			}
			defer func() { <-semaphore }()
			probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			capacity, err := server.options.Capacity.Capacity(probeCtx, vg.Name)
			results <- result{index: index, capacity: capacity, err: err}
		}(index, vg)
	}
	go func() {
		group.Wait()
		close(results)
	}()

	capacities := make([]result, len(vgs))
	for result := range results {
		capacities[result.index] = result
	}
	views := make([]VGStatus, 0, len(vgs))
	errorViews := make([]ErrorView, 0)
	for index, vg := range vgs {
		view := VGStatus{
			Name: vg.Name, UUID: vg.UUID, State: vg.State,
			Configured:   configured[vg.Name],
			StorageClass: server.options.Classes.ClassName(vg.Name),
		}
		probe := capacities[index]
		if probe.err != nil {
			if errors.Is(probe.err, context.DeadlineExceeded) || errors.Is(probe.err, context.Canceled) {
				view.State = "unknown"
				view.Error = "capacity probe timed out"
				errorViews = append(errorViews, ErrorView{Code: "capacity_unknown", Message: fmt.Sprintf("vg %s: %v", vg.Name, probe.err)})
			} else {
				view.State = "unavailable"
				view.Error = probe.err.Error()
				errorViews = append(errorViews, ErrorView{Code: "capacity_unavailable", Message: fmt.Sprintf("vg %s: %v", vg.Name, probe.err)})
			}
			views = append(views, view)
			continue
		}
		if probe.capacity.VGState != "" {
			view.State = probe.capacity.VGState
		}
		if probe.capacity.VGUUID != "" {
			view.UUID = probe.capacity.VGUUID
		}
		if vg.UUID != "" && view.UUID != vg.UUID {
			view.State = "unavailable"
			view.Error = "observed VG UUID differs from the registered VG"
			errorViews = append(errorViews, ErrorView{Code: "capacity_unavailable", Message: fmt.Sprintf("vg %s: UUID mismatch", vg.Name)})
		}
		view.TotalBytes = probe.capacity.TotalBytes
		view.FreeBytes = probe.capacity.FreeBytes
		view.AllocatableBytes = probe.capacity.AllocatableBytes
		view.ExtentSizeBytes = probe.capacity.ExtentSizeBytes
		view.MaxVolumeBytes = probe.capacity.MaxVolumeBytes
		view.ObservedAt = formatTime(probe.capacity.ObservedAt)
		views = append(views, view)
	}
	if errorViews == nil {
		errorViews = []ErrorView{}
	}
	return views, errorViews
}

func (server *Server) volumeView(volume state.VolumeStatus, now time.Time, interval time.Duration) VolumeStatus {
	view := VolumeStatus{
		Owner: volume.Owner, VolumeID: volume.VolumeID, ResourceUUID: volume.ResourceUUID,
		LVName: volume.LVName, VGName: volume.VGName,
		StorageClass:       server.options.Classes.ClassName(volume.VGName),
		RequestedSizeBytes: volume.RequestedSizeBytes, ActualSizeBytes: volume.ActualSizeBytes,
		State: volume.State, UpdatedAt: formatTime(volume.UpdatedAt),
		DriftReasons:  append([]string(nil), volume.DriftReasons...),
		LastErrorCode: volume.ErrorCode,
	}
	if view.DriftReasons == nil {
		view.DriftReasons = []string{}
	}
	if volume.Export != nil {
		view.Export = &ExportView{
			Protocol: volume.Export.Protocol, Initiator: volume.Export.Initiator,
			TargetName: volume.Export.TargetName, LUN: volume.Export.LUN, NSID: volume.Export.NSID,
			Portal: volume.Export.Portal, State: volume.Export.State,
		}
	}
	observationFresh := false
	if volume.Observation != nil {
		stale := observationStale(now, interval, volume.Observation.ObservedAt)
		observationFresh = !stale
		view.Observation = &ObservationView{
			LVPresent: volume.Observation.LVPresent, ObservedSizeBytes: volume.Observation.ObservedSizeBytes,
			DevicePath: volume.Observation.DevicePath, ObservedAt: formatTime(volume.Observation.ObservedAt),
			Source: volume.Observation.Source, Stale: stale,
		}
	}
	exportObservationFresh := false
	if volume.ExportObservation != nil {
		stale := observationStale(now, interval, volume.ExportObservation.ObservedAt)
		exportObservationFresh = !stale
		exportView := &ExportObservationView{
			TargetPresent:    volume.ExportObservation.TargetPresent,
			NamespacePresent: volume.ExportObservation.NamespacePresent,
			ACLPresent:       volume.ExportObservation.ACLPresent,
			SessionState:     volume.ExportObservation.SessionState,
			ObservedAt:       formatTime(volume.ExportObservation.ObservedAt),
			Stale:            stale,
			InitiatorSource:  "none",
		}
		if volume.ExportObservation.SessionState != "" && volume.ExportObservation.SessionState != "unknown" {
			active := volume.ExportObservation.ActiveSessions
			exportView.ActiveSessions = &active
		}
		if volume.ExportObservation.ObservedInitiator != "" {
			initiator := volume.ExportObservation.ObservedInitiator
			exportView.ObservedInitiator = &initiator
			exportView.InitiatorSource = "acl"
		}
		view.ExportObservation = exportView
	}
	view.Serving = volume.State == "exported" &&
		view.Export != nil && view.Export.State == "exported" &&
		view.Observation != nil && view.Observation.LVPresent && observationFresh &&
		view.ExportObservation != nil && view.ExportObservation.TargetPresent &&
		view.ExportObservation.ACLPresent && exportObservationFresh &&
		(view.Export.Protocol != "nvme-tcp" || view.ExportObservation.NamespacePresent)
	return view
}

func observationStale(now time.Time, interval time.Duration, observedAt time.Time) bool {
	if observedAt.IsZero() || interval <= 0 {
		return true
	}
	return now.Sub(observedAt) > 2*interval
}

func operationView(operation state.Operation) OperationView {
	return OperationView{
		ID: operation.ID, Owner: operation.Owner, VolumeID: operation.VolumeID,
		ResourceUUID: operation.ResourceUUID, Action: operation.Action, State: operation.State,
		Attempt: operation.Attempt, MaxAttempts: operation.MaxAttempts,
		DeadlineAt: formatTime(operation.DeadlineAt), LastErrorCode: operation.LastErrorCode,
		LastErrorRetryable: operation.LastErrorRetryable,
		CreatedAt:          formatTime(operation.CreatedAt), UpdatedAt: formatTime(operation.UpdatedAt),
	}
}

func iscsiView(cfg *config.Config) ISCSIInfo {
	info := ISCSIInfo{
		Enabled: cfg.Targets.ISCSI.Enabled, IQNPrefix: cfg.Targets.ISCSI.IQNPrefix,
		Portals: make([]string, 0, len(cfg.Targets.ISCSI.Portals)),
	}
	for _, portal := range cfg.Targets.ISCSI.Portals {
		info.Portals = append(info.Portals, fmt.Sprintf("%s:%d", portal.Address, portal.Port))
	}
	return info
}

func protocols(cfg *config.Config) []string {
	result := make([]string, 0, 2)
	if cfg.Targets.ISCSI.Enabled {
		result = append(result, "iscsi")
	}
	if cfg.Targets.NVMeTCP.Enabled {
		result = append(result, "nvme-tcp")
	}
	return result
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
