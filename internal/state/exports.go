package state

import "database/sql"

const exportColumns = `owner, volume_id, protocol, initiator, target_name,
	lun, nsid, portal, state, created_at, updated_at`

func scanExport(row rowScanner) (Export, error) {
	var export Export
	var lun, nsid sql.NullInt64
	var createdAt, updatedAt string
	if err := row.Scan(
		&export.Owner,
		&export.VolumeID,
		&export.Protocol,
		&export.Initiator,
		&export.TargetName,
		&lun,
		&nsid,
		&export.Portal,
		&export.State,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Export{}, notFound(err)
	}
	export.LUN = nullableInt32(lun)
	export.NSID = nullableInt32(nsid)
	var err error
	if export.CreatedAt, err = parseTime(createdAt); err != nil {
		return Export{}, err
	}
	if export.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return Export{}, err
	}
	return export, nil
}

func (tx *sqlTx) GetExport(owner, volumeID string) (Export, error) {
	if err := validateOwnerVolume(owner, volumeID); err != nil {
		return Export{}, err
	}
	row := tx.tx.QueryRowContext(tx.ctx,
		"SELECT "+exportColumns+" FROM exports WHERE owner = ? AND volume_id = ?",
		owner, volumeID)
	return scanExport(row)
}

func (tx *sqlTx) PutExport(export Export) error {
	if err := validateExport(export); err != nil {
		return err
	}
	_, err := tx.tx.ExecContext(tx.ctx, `
		INSERT INTO exports (
			owner, volume_id, protocol, initiator, target_name, lun, nsid,
			portal, state, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(owner, volume_id) DO UPDATE SET
			protocol = excluded.protocol,
			initiator = excluded.initiator,
			target_name = excluded.target_name,
			lun = excluded.lun,
			nsid = excluded.nsid,
			portal = excluded.portal,
			state = excluded.state,
			updated_at = excluded.updated_at`,
		export.Owner,
		export.VolumeID,
		export.Protocol,
		export.Initiator,
		export.TargetName,
		export.LUN,
		export.NSID,
		export.Portal,
		export.State,
		timeText(export.CreatedAt),
		timeText(export.UpdatedAt),
	)
	return mapError(err)
}

func (tx *sqlTx) DeleteExport(owner, volumeID string) error {
	if err := validateOwnerVolume(owner, volumeID); err != nil {
		return err
	}
	result, err := tx.tx.ExecContext(tx.ctx,
		"DELETE FROM exports WHERE owner = ? AND volume_id = ?", owner, volumeID)
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
