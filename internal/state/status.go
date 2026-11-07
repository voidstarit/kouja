package state

import "database/sql"

// VolumeStatus is the joined read model used by the local admin UI. Related
// rows are nil when the corresponding observation or export does not exist.
type VolumeStatus struct {
	Volume
	Export            *Export
	Observation       *Observation
	ExportObservation *ExportObservation
}

var volumeStates = []string{
	"creating", "available", "exporting", "exported", "unexporting",
	"resizing", "deleting", "failed", "drift",
}

const volumeStatusColumns = `v.owner, v.volume_id, v.resource_uuid, v.lv_name, v.vg_name,
	v.requested_size_bytes, v.actual_size_bytes, v.state, v.drift_reasons,
	v.last_operation_id, v.error_code, v.legacy_identity, v.created_at, v.updated_at,
	e.owner, e.volume_id, e.protocol, e.initiator, e.target_name, e.lun, e.nsid,
	e.portal, e.state, e.created_at, e.updated_at,
	o.owner, o.volume_id, o.lv_present, o.observed_size_bytes, o.device_path,
	o.observed_at, o.source,
	x.owner, x.volume_id, x.target_present, x.namespace_present, x.acl_present,
	x.active_sessions, x.session_state, x.observed_initiator, x.observed_at, x.source`

func (tx *sqlTx) ListVolumeStatuses(limit int) ([]VolumeStatus, error) {
	if limit <= 0 {
		limit = 256
	}
	if limit > 256 {
		limit = 256
	}
	rows, err := tx.tx.QueryContext(tx.ctx, `
		SELECT `+volumeStatusColumns+`
		FROM volumes v
		LEFT JOIN exports e
			ON e.owner = v.owner AND e.volume_id = v.volume_id
		LEFT JOIN volume_observations o
			ON o.owner = v.owner AND o.volume_id = v.volume_id
		LEFT JOIN export_observations x
			ON x.owner = v.owner AND x.volume_id = v.volume_id
		WHERE v.state != 'deleted'
		ORDER BY v.updated_at DESC, v.owner, v.volume_id
		LIMIT ?`, limit)
	if err != nil {
		return nil, mapError(err)
	}
	defer func() { _ = rows.Close() }()

	statuses := make([]VolumeStatus, 0, limit)
	for rows.Next() {
		status, err := scanVolumeStatus(rows)
		if err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err)
	}
	return statuses, nil
}

func scanVolumeStatus(row rowScanner) (VolumeStatus, error) {
	var status VolumeStatus
	var volume Volume
	var actual sql.NullInt64
	var driftReasons, lastOperationID, errorCode, legacyIdentity sql.NullString
	var volumeCreated, volumeUpdated string

	var eOwner, eVolumeID, eProtocol, eInitiator, eTarget, ePortal, eState sql.NullString
	var eLUN, eNSID sql.NullInt64
	var eCreated, eUpdated sql.NullString

	var oOwner, oVolumeID sql.NullString
	var oLVPresent sql.NullInt64
	var oSize sql.NullInt64
	var oDevicePath, oObservedAt, oSource sql.NullString

	var xOwner, xVolumeID sql.NullString
	var xTarget, xNamespace, xACL, xSessions sql.NullInt64
	var xSessionState, xObservedInitiator, xObservedAt, xSource sql.NullString

	if err := row.Scan(
		&volume.Owner, &volume.VolumeID, &volume.ResourceUUID, &volume.LVName, &volume.VGName,
		&volume.RequestedSizeBytes, &actual, &volume.State, &driftReasons,
		&lastOperationID, &errorCode, &legacyIdentity, &volumeCreated, &volumeUpdated,
		&eOwner, &eVolumeID, &eProtocol, &eInitiator, &eTarget, &eLUN, &eNSID,
		&ePortal, &eState, &eCreated, &eUpdated,
		&oOwner, &oVolumeID, &oLVPresent, &oSize, &oDevicePath, &oObservedAt, &oSource,
		&xOwner, &xVolumeID, &xTarget, &xNamespace, &xACL, &xSessions,
		&xSessionState, &xObservedInitiator, &xObservedAt, &xSource,
	); err != nil {
		return VolumeStatus{}, notFound(err)
	}

	if actual.Valid {
		volume.ActualSizeBytes = actual.Int64
	}
	volume.LastOperationID = stringOrEmpty(lastOperationID)
	volume.ErrorCode = stringOrEmpty(errorCode)
	var err error
	if volume.DriftReasons, err = decodeStrings(driftReasons); err != nil {
		return VolumeStatus{}, err
	}
	if volume.LegacyIdentity, err = decodeMap(legacyIdentity); err != nil {
		return VolumeStatus{}, err
	}
	if volume.CreatedAt, err = parseTime(volumeCreated); err != nil {
		return VolumeStatus{}, err
	}
	if volume.UpdatedAt, err = parseTime(volumeUpdated); err != nil {
		return VolumeStatus{}, err
	}
	status.Volume = volume

	if eOwner.Valid {
		export := &Export{
			Owner: stringOrEmpty(eOwner), VolumeID: stringOrEmpty(eVolumeID),
			Protocol: stringOrEmpty(eProtocol), Initiator: stringOrEmpty(eInitiator),
			TargetName: stringOrEmpty(eTarget), LUN: nullableInt32(eLUN), NSID: nullableInt32(eNSID),
			Portal: stringOrEmpty(ePortal), State: stringOrEmpty(eState),
		}
		if export.CreatedAt, err = parseTime(stringOrEmpty(eCreated)); err != nil {
			return VolumeStatus{}, err
		}
		if export.UpdatedAt, err = parseTime(stringOrEmpty(eUpdated)); err != nil {
			return VolumeStatus{}, err
		}
		status.Export = export
	}

	if oOwner.Valid {
		observation := &Observation{
			Owner: stringOrEmpty(oOwner), VolumeID: stringOrEmpty(oVolumeID),
			LVPresent:  oLVPresent.Valid && oLVPresent.Int64 != 0,
			DevicePath: stringOrEmpty(oDevicePath), Source: stringOrEmpty(oSource),
		}
		if oSize.Valid {
			observation.ObservedSizeBytes = oSize.Int64
		}
		if observation.ObservedAt, err = parseTime(stringOrEmpty(oObservedAt)); err != nil {
			return VolumeStatus{}, err
		}
		status.Observation = observation
	}

	if xOwner.Valid {
		observation := &ExportObservation{
			Owner: stringOrEmpty(xOwner), VolumeID: stringOrEmpty(xVolumeID),
			TargetPresent:     xTarget.Valid && xTarget.Int64 != 0,
			NamespacePresent:  xNamespace.Valid && xNamespace.Int64 != 0,
			ACLPresent:        xACL.Valid && xACL.Int64 != 0,
			SessionState:      stringOrEmpty(xSessionState),
			ObservedInitiator: stringOrEmpty(xObservedInitiator),
			Source:            stringOrEmpty(xSource),
		}
		if xSessions.Valid {
			observation.ActiveSessions = int32(xSessions.Int64) //nolint:gosec // session counts are kernel-sized and bounded by LIO/nvmet
		}
		if observation.ObservedAt, err = parseTime(stringOrEmpty(xObservedAt)); err != nil {
			return VolumeStatus{}, err
		}
		status.ExportObservation = observation
	}
	return status, nil
}

func (tx *sqlTx) VolumeStateCounts() (map[string]int, error) {
	counts := make(map[string]int, len(volumeStates))
	for _, state := range volumeStates {
		counts[state] = 0
	}
	rows, err := tx.tx.QueryContext(tx.ctx, "SELECT state, COUNT(*) FROM volumes WHERE state != 'deleted' GROUP BY state")
	if err != nil {
		return nil, mapError(err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return nil, mapError(err)
		}
		counts[state] = count
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err)
	}
	return counts, nil
}

func (tx *sqlTx) ListRecentOperations(limit int) ([]Operation, error) {
	if limit <= 0 {
		limit = 64
	}
	if limit > 64 {
		limit = 64
	}
	return tx.queryOperations("SELECT "+operationColumns+` FROM operations
		WHERE state IN ('running', 'failed', 'needs_attention')
		ORDER BY updated_at DESC, id DESC LIMIT ?`, limit)
}

func (tx *sqlTx) OperationCount() (int, error) {
	var count int
	if err := tx.tx.QueryRowContext(tx.ctx, `SELECT COUNT(*) FROM operations
		WHERE state IN ('running', 'failed', 'needs_attention')`).Scan(&count); err != nil {
		return 0, mapError(err)
	}
	return count, nil
}
