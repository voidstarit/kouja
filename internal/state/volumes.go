package state

import (
	"database/sql"
	"fmt"
)

const volumeColumns = `owner, volume_id, resource_uuid, lv_name, vg_name,
	requested_size_bytes, actual_size_bytes, state, drift_reasons,
	last_operation_id, error_code, legacy_identity, created_at, updated_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanVolume(row rowScanner) (Volume, error) {
	var volume Volume
	var actual sql.NullInt64
	var driftReasons, lastOperationID, errorCode, legacyIdentity sql.NullString
	var createdAt, updatedAt string
	if err := row.Scan(
		&volume.Owner,
		&volume.VolumeID,
		&volume.ResourceUUID,
		&volume.LVName,
		&volume.VGName,
		&volume.RequestedSizeBytes,
		&actual,
		&volume.State,
		&driftReasons,
		&lastOperationID,
		&errorCode,
		&legacyIdentity,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Volume{}, notFound(err)
	}
	if actual.Valid {
		volume.ActualSizeBytes = actual.Int64
	}
	volume.LastOperationID = stringOrEmpty(lastOperationID)
	volume.ErrorCode = stringOrEmpty(errorCode)
	var err error
	if volume.DriftReasons, err = decodeStrings(driftReasons); err != nil {
		return Volume{}, err
	}
	if volume.LegacyIdentity, err = decodeMap(legacyIdentity); err != nil {
		return Volume{}, err
	}
	if volume.CreatedAt, err = parseTime(createdAt); err != nil {
		return Volume{}, err
	}
	volume.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return Volume{}, err
	}
	return volume, nil
}

func (tx *sqlTx) GetVolume(owner, volumeID string) (Volume, error) {
	if err := validateOwnerVolume(owner, volumeID); err != nil {
		return Volume{}, err
	}
	row := tx.tx.QueryRowContext(tx.ctx,
		"SELECT "+volumeColumns+" FROM volumes WHERE owner = ? AND volume_id = ?",
		owner, volumeID)
	return scanVolume(row)
}

func (tx *sqlTx) GetVolumeByResourceUUID(resourceUUID string) (Volume, error) {
	if resourceUUID == "" {
		return Volume{}, fmt.Errorf("%w: resource_uuid is required", ErrInvalid)
	}
	row := tx.tx.QueryRowContext(tx.ctx,
		"SELECT "+volumeColumns+" FROM volumes WHERE resource_uuid = ?", resourceUUID)
	return scanVolume(row)
}

func (tx *sqlTx) ListVolumes(owner string, page Page) ([]Volume, error) {
	limit, cursor := normalizePage(page)
	query := "SELECT " + volumeColumns + " FROM volumes"
	args := make([]any, 0, 3)
	if owner != "" {
		query += " WHERE owner = ?"
		args = append(args, owner)
		if cursor != "" {
			query += " AND volume_id > ?"
			args = append(args, cursor)
		}
	} else if cursor != "" {
		query += " WHERE volume_id > ?"
		args = append(args, cursor)
	}
	query += " ORDER BY volume_id LIMIT ?"
	args = append(args, limit)

	rows, err := tx.tx.QueryContext(tx.ctx, query, args...)
	if err != nil {
		return nil, mapError(err)
	}
	defer func() { _ = rows.Close() }()

	volumes := make([]Volume, 0, limit)
	for rows.Next() {
		volume, err := scanVolume(rows)
		if err != nil {
			return nil, err
		}
		volumes = append(volumes, volume)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err)
	}
	return volumes, nil
}

func (tx *sqlTx) PutVolume(volume Volume) error {
	if err := validateVolume(volume); err != nil {
		return err
	}
	driftReasons, err := encodeStrings(volume.DriftReasons)
	if err != nil {
		return err
	}
	legacyIdentity, err := encodeMap(volume.LegacyIdentity)
	if err != nil {
		return err
	}
	_, err = tx.tx.ExecContext(tx.ctx, `
		INSERT INTO volumes (
			owner, volume_id, resource_uuid, lv_name, vg_name,
			requested_size_bytes, actual_size_bytes, state, drift_reasons,
			last_operation_id, error_code, legacy_identity, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(owner, volume_id) DO UPDATE SET
			resource_uuid = excluded.resource_uuid,
			lv_name = excluded.lv_name,
			vg_name = excluded.vg_name,
			requested_size_bytes = excluded.requested_size_bytes,
			actual_size_bytes = excluded.actual_size_bytes,
			state = excluded.state,
			drift_reasons = excluded.drift_reasons,
			last_operation_id = excluded.last_operation_id,
			error_code = excluded.error_code,
			legacy_identity = excluded.legacy_identity,
			updated_at = excluded.updated_at`,
		volume.Owner,
		volume.VolumeID,
		volume.ResourceUUID,
		volume.LVName,
		volume.VGName,
		volume.RequestedSizeBytes,
		volume.ActualSizeBytes,
		volume.State,
		driftReasons,
		nullableString(volume.LastOperationID),
		nullableString(volume.ErrorCode),
		legacyIdentity,
		timeText(volume.CreatedAt),
		timeText(volume.UpdatedAt),
	)
	return mapError(err)
}

func (tx *sqlTx) DeleteVolume(owner, volumeID string) error {
	if err := validateOwnerVolume(owner, volumeID); err != nil {
		return err
	}
	result, err := tx.tx.ExecContext(tx.ctx,
		"DELETE FROM volumes WHERE owner = ? AND volume_id = ?", owner, volumeID)
	if err != nil {
		return mapError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return mapError(err)
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

const tombstoneColumns = `owner, volume_id, resource_uuid, lv_name, vg_name,
	size_bytes, deleted_at, last_operation_id, reason`

func scanTombstone(row rowScanner) (Tombstone, error) {
	var tombstone Tombstone
	var lastOperationID, reason sql.NullString
	var deletedAt string
	if err := row.Scan(
		&tombstone.Owner,
		&tombstone.VolumeID,
		&tombstone.ResourceUUID,
		&tombstone.LVName,
		&tombstone.VGName,
		&tombstone.SizeBytes,
		&deletedAt,
		&lastOperationID,
		&reason,
	); err != nil {
		return Tombstone{}, notFound(err)
	}
	tombstone.LastOperationID = stringOrEmpty(lastOperationID)
	tombstone.Reason = stringOrEmpty(reason)
	var err error
	if tombstone.DeletedAt, err = parseTime(deletedAt); err != nil {
		return Tombstone{}, err
	}
	return tombstone, nil
}

func (tx *sqlTx) PutTombstone(tombstone Tombstone) error {
	if err := validateTombstone(tombstone); err != nil {
		return err
	}
	_, err := tx.tx.ExecContext(tx.ctx, `
		INSERT INTO volume_tombstones (
			owner, volume_id, resource_uuid, lv_name, vg_name,
			size_bytes, deleted_at, last_operation_id, reason
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(owner, volume_id, resource_uuid) DO UPDATE SET
			lv_name = excluded.lv_name,
			vg_name = excluded.vg_name,
			size_bytes = excluded.size_bytes,
			deleted_at = excluded.deleted_at,
			last_operation_id = excluded.last_operation_id,
			reason = excluded.reason`,
		tombstone.Owner,
		tombstone.VolumeID,
		tombstone.ResourceUUID,
		tombstone.LVName,
		tombstone.VGName,
		tombstone.SizeBytes,
		timeText(tombstone.DeletedAt),
		nullableString(tombstone.LastOperationID),
		nullableString(tombstone.Reason),
	)
	return mapError(err)
}

func (tx *sqlTx) GetTombstone(owner, volumeID string) (Tombstone, error) {
	if err := validateOwnerVolume(owner, volumeID); err != nil {
		return Tombstone{}, err
	}
	row := tx.tx.QueryRowContext(tx.ctx,
		"SELECT "+tombstoneColumns+" FROM volume_tombstones WHERE owner = ? AND volume_id = ? ORDER BY deleted_at DESC LIMIT 1",
		owner, volumeID)
	return scanTombstone(row)
}

func (tx *sqlTx) ListTombstones(owner string) ([]Tombstone, error) {
	query := "SELECT " + tombstoneColumns + " FROM volume_tombstones"
	args := make([]any, 0, 1)
	if owner != "" {
		query += " WHERE owner = ?"
		args = append(args, owner)
	}
	query += " ORDER BY deleted_at DESC"
	rows, err := tx.tx.QueryContext(tx.ctx, query, args...)
	if err != nil {
		return nil, mapError(err)
	}
	defer func() { _ = rows.Close() }()

	tombstones := make([]Tombstone, 0)
	for rows.Next() {
		tombstone, err := scanTombstone(rows)
		if err != nil {
			return nil, err
		}
		tombstones = append(tombstones, tombstone)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err)
	}
	return tombstones, nil
}

func (tx *sqlTx) GetObservation(owner, volumeID string) (Observation, error) {
	if err := validateOwnerVolume(owner, volumeID); err != nil {
		return Observation{}, err
	}
	row := tx.tx.QueryRowContext(tx.ctx, `
		SELECT owner, volume_id, lv_present, observed_size_bytes, device_path,
			observed_at, source
		FROM volume_observations WHERE owner = ? AND volume_id = ?`, owner, volumeID)
	var observation Observation
	var present int
	var size sql.NullInt64
	var devicePath sql.NullString
	var observedAt string
	if err := row.Scan(&observation.Owner, &observation.VolumeID, &present, &size,
		&devicePath, &observedAt, &observation.Source); err != nil {
		return Observation{}, notFound(err)
	}
	observation.LVPresent = intBool(int64(present))
	if size.Valid {
		observation.ObservedSizeBytes = size.Int64
	}
	observation.DevicePath = stringOrEmpty(devicePath)
	var err error
	if observation.ObservedAt, err = parseTime(observedAt); err != nil {
		return Observation{}, err
	}
	return observation, nil
}

func (tx *sqlTx) PutObservation(observation Observation) error {
	if err := validateOwnerVolume(observation.Owner, observation.VolumeID); err != nil {
		return err
	}
	if observation.Source == "" {
		return fmt.Errorf("%w: source is required", ErrInvalid)
	}
	_, err := tx.tx.ExecContext(tx.ctx, `
		INSERT INTO volume_observations (
			owner, volume_id, lv_present, observed_size_bytes, device_path,
			observed_at, source
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(owner, volume_id) DO UPDATE SET
			lv_present = excluded.lv_present,
			observed_size_bytes = excluded.observed_size_bytes,
			device_path = excluded.device_path,
			observed_at = excluded.observed_at,
			source = excluded.source`,
		observation.Owner,
		observation.VolumeID,
		boolInt(observation.LVPresent),
		observation.ObservedSizeBytes,
		nullableString(observation.DevicePath),
		timeText(observation.ObservedAt),
		observation.Source,
	)
	return mapError(err)
}

func (tx *sqlTx) GetExportObservation(owner, volumeID string) (ExportObservation, error) {
	if err := validateOwnerVolume(owner, volumeID); err != nil {
		return ExportObservation{}, err
	}
	row := tx.tx.QueryRowContext(tx.ctx, `
		SELECT owner, volume_id, target_present, namespace_present, acl_present,
			active_sessions, session_state, observed_initiator, observed_at, source
		FROM export_observations WHERE owner = ? AND volume_id = ?`, owner, volumeID)
	var observation ExportObservation
	var targetPresent, namespacePresent, aclPresent int
	var observedInitiator sql.NullString
	var observedAt string
	if err := row.Scan(&observation.Owner, &observation.VolumeID, &targetPresent,
		&namespacePresent, &aclPresent, &observation.ActiveSessions,
		&observation.SessionState, &observedInitiator, &observedAt,
		&observation.Source); err != nil {
		return ExportObservation{}, notFound(err)
	}
	observation.TargetPresent = intBool(int64(targetPresent))
	observation.NamespacePresent = intBool(int64(namespacePresent))
	observation.ACLPresent = intBool(int64(aclPresent))
	observation.ObservedInitiator = stringOrEmpty(observedInitiator)
	var err error
	if observation.ObservedAt, err = parseTime(observedAt); err != nil {
		return ExportObservation{}, err
	}
	return observation, nil
}

func (tx *sqlTx) PutExportObservation(observation ExportObservation) error {
	if err := validateOwnerVolume(observation.Owner, observation.VolumeID); err != nil {
		return err
	}
	if observation.SessionState == "" || observation.Source == "" {
		return fmt.Errorf("%w: session_state and source are required", ErrInvalid)
	}
	_, err := tx.tx.ExecContext(tx.ctx, `
		INSERT INTO export_observations (
			owner, volume_id, target_present, namespace_present, acl_present,
			active_sessions, session_state, observed_initiator, observed_at, source
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(owner, volume_id) DO UPDATE SET
			target_present = excluded.target_present,
			namespace_present = excluded.namespace_present,
			acl_present = excluded.acl_present,
			active_sessions = excluded.active_sessions,
			session_state = excluded.session_state,
			observed_initiator = excluded.observed_initiator,
			observed_at = excluded.observed_at,
			source = excluded.source`,
		observation.Owner,
		observation.VolumeID,
		boolInt(observation.TargetPresent),
		boolInt(observation.NamespacePresent),
		boolInt(observation.ACLPresent),
		observation.ActiveSessions,
		observation.SessionState,
		nullableString(observation.ObservedInitiator),
		timeText(observation.ObservedAt),
		observation.Source,
	)
	return mapError(err)
}

func normalizePage(page Page) (int, string) {
	limit := page.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	return limit, page.Cursor
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
