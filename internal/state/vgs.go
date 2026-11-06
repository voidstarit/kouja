package state

import "fmt"

func (tx *sqlTx) GetVG(name string) (VG, error) {
	if name == "" {
		return VG{}, fmt.Errorf("%w: vg name is required", ErrInvalid)
	}
	row := tx.tx.QueryRowContext(tx.ctx, `
		SELECT name, uuid, state, registered_at, updated_at
		FROM vgs WHERE name = ?`, name)
	var vg VG
	var registeredAt, updatedAt string
	if err := row.Scan(&vg.Name, &vg.UUID, &vg.State, &registeredAt, &updatedAt); err != nil {
		return VG{}, notFound(err)
	}
	var err error
	if vg.RegisteredAt, err = parseTime(registeredAt); err != nil {
		return VG{}, err
	}
	if vg.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return VG{}, err
	}
	return vg, nil
}

func (tx *sqlTx) ListVGs() ([]VG, error) {
	rows, err := tx.tx.QueryContext(tx.ctx, `
		SELECT name, uuid, state, registered_at, updated_at
		FROM vgs ORDER BY name`)
	if err != nil {
		return nil, mapError(err)
	}
	defer func() { _ = rows.Close() }()

	vgs := make([]VG, 0)
	for rows.Next() {
		var vg VG
		var registeredAt, updatedAt string
		if err := rows.Scan(&vg.Name, &vg.UUID, &vg.State, &registeredAt, &updatedAt); err != nil {
			return nil, mapError(err)
		}
		if vg.RegisteredAt, err = parseTime(registeredAt); err != nil {
			return nil, err
		}
		if vg.UpdatedAt, err = parseTime(updatedAt); err != nil {
			return nil, err
		}
		vgs = append(vgs, vg)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err)
	}
	return vgs, nil
}

func (tx *sqlTx) PutVG(vg VG) error {
	if vg.Name == "" || vg.UUID == "" || vg.State == "" {
		return fmt.Errorf("%w: vg name, uuid, and state are required", ErrInvalid)
	}
	_, err := tx.tx.ExecContext(tx.ctx, `
		INSERT INTO vgs (name, uuid, state, registered_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			uuid = excluded.uuid,
			state = excluded.state,
			updated_at = excluded.updated_at`,
		vg.Name,
		vg.UUID,
		vg.State,
		timeText(vg.RegisteredAt),
		timeText(vg.UpdatedAt),
	)
	return mapError(err)
}

func (tx *sqlTx) DeleteVG(name string) error {
	if name == "" {
		return fmt.Errorf("%w: vg name is required", ErrInvalid)
	}
	result, err := tx.tx.ExecContext(tx.ctx, "DELETE FROM vgs WHERE name = ?", name)
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

func (tx *sqlTx) GetSetting(key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("%w: setting key is required", ErrInvalid)
	}
	var value string
	if err := tx.tx.QueryRowContext(tx.ctx,
		"SELECT value FROM settings WHERE key = ?", key).Scan(&value); err != nil {
		return "", notFound(err)
	}
	return value, nil
}

func (tx *sqlTx) PutSetting(key, value string) error {
	if key == "" {
		return fmt.Errorf("%w: setting key is required", ErrInvalid)
	}
	_, err := tx.tx.ExecContext(tx.ctx, `
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return mapError(err)
}
