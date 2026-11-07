package adminui

import (
	"context"
	"fmt"
	"time"

	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
)

const minimumMetricsInterval = time.Second

type metricCounters struct {
	readBytes  int64
	writeBytes int64
	commands   int64
}

type metricsSample struct {
	at      time.Time
	targets map[string]metricCounters
}

// buildMetrics collects target counters and derives rates from the previous
// successful sample. Metrics failures are reported in the response but do not
// fail /api/status or the data path.
func (server *Server) buildMetrics(ctx context.Context, volumes []state.VolumeStatus) MetricsView {
	view := MetricsView{
		SampledAt:   formatTime(server.options.Now()),
		Connections: []ClientConnectionView{},
		Targets:     []TargetMetricsView{},
	}
	if server.options.TargetMetrics == nil {
		view.Error = "live iSCSI metrics are not configured"
		return view
	}
	raw, err := server.options.TargetMetrics.Metrics(ctx)
	if err != nil {
		view.Error = err.Error()
		return view
	}
	view.Available = true
	view.ConnectionsError = raw.ConnectionError
	for _, connection := range raw.Connections {
		view.Connections = append(view.Connections, ClientConnectionView{
			ClientAddress: connection.ClientAddress,
			ClientPort:    connection.ClientPort,
			LocalAddress:  connection.LocalAddress,
			LocalPort:     connection.LocalPort,
			Target:        connection.Target,
			Initiator:     connection.Initiator,
			Ambiguous:     connection.Ambiguous,
		})
	}

	volumesByTarget := make(map[string]state.VolumeStatus, len(volumes))
	for _, volume := range volumes {
		if volume.Export != nil && volume.Export.TargetName != "" {
			volumesByTarget[volume.Export.TargetName] = volume
		}
	}
	current := make(map[string]metricCounters, len(raw.Targets))
	for _, rawTarget := range raw.Targets {
		targetView := targetMetricsView(rawTarget, volumesByTarget[rawTarget.Target])
		view.Targets = append(view.Targets, targetView)
		current[rawTarget.Target] = metricCounters{
			readBytes: targetView.ReadBytes, writeBytes: targetView.WriteBytes,
			commands: targetView.Commands,
		}
	}

	now := server.options.Now()
	server.metricsMu.Lock()
	previous := server.lastMetrics
	ratesAvailable := previous != nil && now.After(previous.at) &&
		now.Sub(previous.at) >= minimumMetricsInterval
	anyTargetRates := false
	if ratesAvailable {
		seconds := now.Sub(previous.at).Seconds()
		for index := range view.Targets {
			targetView := &view.Targets[index]
			before, ok := previous.targets[targetView.Target]
			if !ok {
				targetView.RatesAvailable = false
				continue
			}
			targetView.ReadBytesPerSecond, targetView.RatesAvailable = rate(before.readBytes, current[targetView.Target].readBytes, seconds)
			var writeOK bool
			targetView.WriteBytesPerSecond, writeOK = rate(before.writeBytes, current[targetView.Target].writeBytes, seconds)
			targetView.RatesAvailable = targetView.RatesAvailable && writeOK
			targetView.CommandsPerSecond, _ = rate(before.commands, current[targetView.Target].commands, seconds)
			anyTargetRates = anyTargetRates || targetView.RatesAvailable
		}
	}
	if previous == nil || ratesAvailable {
		server.lastMetrics = &metricsSample{at: now, targets: current}
	}
	server.metricsMu.Unlock()

	view.SampledAt = formatTime(now)
	view.RatesAvailable = ratesAvailable && anyTargetRates
	for _, targetView := range view.Targets {
		view.ReadBytes += targetView.ReadBytes
		view.WriteBytes += targetView.WriteBytes
		view.ReadBytesPerSecond += targetView.ReadBytesPerSecond
		view.WriteBytesPerSecond += targetView.WriteBytesPerSecond
		view.Commands += targetView.Commands
		view.CommandsPerSecond += targetView.CommandsPerSecond
		view.ErrorCount += targetView.ErrorCount
	}
	return view
}

func targetMetricsView(raw target.ISCSITargetMetrics, volume state.VolumeStatus) TargetMetricsView {
	view := TargetMetricsView{
		Target: raw.Target, Sessions: raw.Sessions,
		Initiators:      make([]InitiatorMetricsView, 0, len(raw.Initiators)),
		LUNs:            make([]LUNMetricsView, 0, len(raw.LUNs)),
		ClientAddresses: append([]string{}, raw.ClientAddresses...),
		Errors:          metricsErrorsView(raw),
		Error:           raw.Error,
	}
	if volume.Export != nil {
		view.VolumeOwner = volume.Owner
		view.VolumeID = volume.VolumeID
	}
	for _, initiator := range raw.Initiators {
		view.Initiators = append(view.Initiators, InitiatorMetricsView{
			Initiator: initiator.Initiator,
			RXBytes:   initiator.RXBytes, TXBytes: initiator.TXBytes,
			CommandPDUs: initiator.CommandPDUs, ResponsePDUs: initiator.ResponsePDUs,
			ConnectionTimeoutErrors: initiator.ConnectionTimeoutErrors,
			ConnectionDigestErrors:  initiator.ConnectionDigestErrors,
		})
		view.ReadBytes += initiator.TXBytes
		view.WriteBytes += initiator.RXBytes
	}
	for _, lun := range raw.LUNs {
		view.LUNs = append(view.LUNs, LUNMetricsView{
			LUN: lun.LUN, ResourceUUID: lun.ResourceUUID,
			Commands: lun.Commands, ReadBytes: lun.ReadBytes, WriteBytes: lun.WriteBytes,
			Resets: lun.Resets, AbortsComplete: lun.AbortsComplete, AbortsNoTask: lun.AbortsNoTask,
			Status: lun.Status, StateBit: lun.StateBit, BusyCount: lun.BusyCount,
			InCommands: lun.InCommands, HighPriorityInCommands: lun.HighPriorityInCommands,
			Error: lun.Error,
		})
		view.Commands += lun.Commands
		if len(raw.Initiators) == 0 {
			view.ReadBytes += lun.ReadBytes
			view.WriteBytes += lun.WriteBytes
		}
		if lun.Error != "" {
			if view.Error != "" {
				view.Error += "; "
			}
			view.Error += fmt.Sprintf("LUN %d: %s", lun.LUN, lun.Error)
		}
	}
	if len(raw.LUNs) == 0 {
		for _, initiator := range raw.Initiators {
			view.Commands += initiator.CommandPDUs
		}
	}
	view.ErrorCount = metricsErrorCount(view.Errors)
	return view
}

func metricsErrorsView(raw target.ISCSITargetMetrics) MetricsErrorsView {
	errors := MetricsErrorsView{
		LoginFailures:          raw.Login.TargetLoginFails,
		AuthenticationFailures: raw.Login.AuthenticateFails,
		AuthorizationFailures:  raw.Login.AuthorizeFails,
		NegotiationFailures:    raw.Login.NegotiateFails,
		OtherLoginFailures:     raw.Login.OtherFails,
		AbnormalLogouts:        raw.Logout.Abnormal,
		FormatErrors:           raw.SessionErrors.FormatErrors,
		ConnectionErrors:       raw.SessionErrors.ConnectionErrors,
		DigestErrors:           raw.SessionErrors.DigestErrors,
	}
	for _, initiator := range raw.Initiators {
		errors.ConnectionTimeouts += initiator.ConnectionTimeoutErrors
		errors.ConnectionDigestErrors += initiator.ConnectionDigestErrors
	}
	for _, lun := range raw.LUNs {
		errors.AbortsComplete += lun.AbortsComplete
		errors.AbortsNoTask += lun.AbortsNoTask
		errors.Resets += lun.Resets
	}
	return errors
}

func metricsErrorCount(errors MetricsErrorsView) int64 {
	loginFailures := errors.LoginFailures
	if loginFailures == 0 {
		loginFailures = errors.AuthenticationFailures + errors.AuthorizationFailures +
			errors.NegotiationFailures + errors.OtherLoginFailures
	}
	return loginFailures + errors.AbnormalLogouts + errors.FormatErrors +
		errors.ConnectionErrors + errors.DigestErrors + errors.ConnectionTimeouts +
		errors.ConnectionDigestErrors + errors.AbortsComplete + errors.AbortsNoTask +
		errors.Resets
}

func rate(previous, current int64, seconds float64) (float64, bool) {
	if previous < 0 || current < previous || seconds <= 0 {
		return 0, false
	}
	return float64(current-previous) / seconds, true
}
