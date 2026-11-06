package state

import (
	"database/sql"
	"fmt"
)

const operationColumns = `id, owner, volume_id, resource_uuid, action, request_id,
	request_hash, intent_json, state, attempt, max_attempts, deadline_at,
	last_error_code, last_error_retryable, created_at, updated_at`

func scanOperation(row rowScanner) (Operation, error) {
	var operation Operation
	var requestID, lastErrorCode sql.NullString
	var retryable int
	var deadlineAt, createdAt, updatedAt string
	if err := row.Scan(
		&operation.ID,
		&operation.Owner,
		&operation.VolumeID,
		&operation.ResourceUUID,
		&operation.Action,
		&requestID,
		&operation.RequestHash,
		&operation.IntentJSON,
		&operation.State,
		&operation.Attempt,
		&operation.MaxAttempts,
		&deadlineAt,
		&lastErrorCode,
		&retryable,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Operation{}, notFound(err)
	}
	operation.RequestID = stringOrEmpty(requestID)
	operation.LastErrorCode = stringOrEmpty(lastErrorCode)
	operation.LastErrorRetryable = intBool(int64(retryable))
	var err error
	if operation.DeadlineAt, err = parseTime(deadlineAt); err != nil {
		return Operation{}, err
	}
	if operation.CreatedAt, err = parseTime(createdAt); err != nil {
		return Operation{}, err
	}
	if operation.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return Operation{}, err
	}
	return operation, nil
}

func (tx *sqlTx) GetOperation(owner, volumeID, action, requestHash string) (Operation, error) {
	if err := validateOwnerVolume(owner, volumeID); err != nil {
		return Operation{}, err
	}
	if action == "" || requestHash == "" {
		return Operation{}, fmt.Errorf("%w: action and request_hash are required", ErrInvalid)
	}
	row := tx.tx.QueryRowContext(tx.ctx,
		"SELECT "+operationColumns+` FROM operations
		 WHERE owner = ? AND volume_id = ? AND action = ? AND request_hash = ?`,
		owner, volumeID, action, requestHash)
	return scanOperation(row)
}

func (tx *sqlTx) GetOperationByID(id string) (Operation, error) {
	if id == "" {
		return Operation{}, fmt.Errorf("%w: operation id is required", ErrInvalid)
	}
	row := tx.tx.QueryRowContext(tx.ctx,
		"SELECT "+operationColumns+" FROM operations WHERE id = ?", id)
	return scanOperation(row)
}

func (tx *sqlTx) ListOperations(owner, volumeID string) ([]Operation, error) {
	query := "SELECT " + operationColumns + " FROM operations"
	args := make([]any, 0, 2)
	switch {
	case owner != "" && volumeID != "":
		query += " WHERE owner = ? AND volume_id = ?"
		args = append(args, owner, volumeID)
	case owner != "":
		query += " WHERE owner = ?"
		args = append(args, owner)
	case volumeID != "":
		query += " WHERE volume_id = ?"
		args = append(args, volumeID)
	}
	query += " ORDER BY created_at DESC, id"
	return tx.queryOperations(query, args...)
}

func (tx *sqlTx) ListRunningOperations() ([]Operation, error) {
	return tx.queryOperations("SELECT " + operationColumns +
		" FROM operations WHERE state = 'running' ORDER BY created_at, id")
}

func (tx *sqlTx) queryOperations(query string, args ...any) ([]Operation, error) {
	rows, err := tx.tx.QueryContext(tx.ctx, query, args...)
	if err != nil {
		return nil, mapError(err)
	}
	defer func() { _ = rows.Close() }()

	operations := make([]Operation, 0)
	for rows.Next() {
		operation, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		operations = append(operations, operation)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err)
	}
	return operations, nil
}

func (tx *sqlTx) PutOperation(operation Operation) error {
	if err := validateOperation(operation); err != nil {
		return err
	}
	_, err := tx.tx.ExecContext(tx.ctx, `
		INSERT INTO operations (
			id, owner, volume_id, resource_uuid, action, request_id, request_hash,
			intent_json, state, attempt, max_attempts, deadline_at, last_error_code,
			last_error_retryable, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			resource_uuid = excluded.resource_uuid,
			request_id = excluded.request_id,
			request_hash = excluded.request_hash,
			intent_json = excluded.intent_json,
			state = excluded.state,
			attempt = excluded.attempt,
			max_attempts = excluded.max_attempts,
			deadline_at = excluded.deadline_at,
			last_error_code = excluded.last_error_code,
			last_error_retryable = excluded.last_error_retryable,
			updated_at = excluded.updated_at`,
		operation.ID,
		operation.Owner,
		operation.VolumeID,
		operation.ResourceUUID,
		operation.Action,
		nullableString(operation.RequestID),
		operation.RequestHash,
		operation.IntentJSON,
		operation.State,
		operation.Attempt,
		operation.MaxAttempts,
		timeText(operation.DeadlineAt),
		nullableString(operation.LastErrorCode),
		boolInt(operation.LastErrorRetryable),
		timeText(operation.CreatedAt),
		timeText(operation.UpdatedAt),
	)
	return mapError(err)
}

func (tx *sqlTx) PutGrant(grant Grant) error {
	if err := validateOwnerVolume(grant.Owner, grant.VolumeID); err != nil {
		return err
	}
	if grant.NodeID == "" || grant.NodeCallerID == "" {
		return fmt.Errorf("%w: node_id and node_caller_id are required", ErrInvalid)
	}
	_, err := tx.tx.ExecContext(tx.ctx, `
		INSERT INTO grants (owner, volume_id, node_id, node_caller_id, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(owner, volume_id, node_id) DO UPDATE SET
			node_caller_id = excluded.node_caller_id,
			expires_at = excluded.expires_at,
			created_at = excluded.created_at`,
		grant.Owner,
		grant.VolumeID,
		grant.NodeID,
		grant.NodeCallerID,
		timeText(grant.ExpiresAt),
		timeText(grant.CreatedAt),
	)
	return mapError(err)
}

func (tx *sqlTx) GetGrant(owner, volumeID, nodeID string) (Grant, error) {
	if err := validateOwnerVolume(owner, volumeID); err != nil {
		return Grant{}, err
	}
	if nodeID == "" {
		return Grant{}, fmt.Errorf("%w: node_id is required", ErrInvalid)
	}
	row := tx.tx.QueryRowContext(tx.ctx, `
		SELECT owner, volume_id, node_id, node_caller_id, expires_at, created_at
		FROM grants WHERE owner = ? AND volume_id = ? AND node_id = ?`,
		owner, volumeID, nodeID)
	var grant Grant
	var expiresAt, createdAt string
	if err := row.Scan(&grant.Owner, &grant.VolumeID, &grant.NodeID,
		&grant.NodeCallerID, &expiresAt, &createdAt); err != nil {
		return Grant{}, notFound(err)
	}
	var err error
	if grant.ExpiresAt, err = parseTime(expiresAt); err != nil {
		return Grant{}, err
	}
	if grant.CreatedAt, err = parseTime(createdAt); err != nil {
		return Grant{}, err
	}
	return grant, nil
}

func (tx *sqlTx) ListGrants(owner, volumeID string) ([]Grant, error) {
	query := `SELECT owner, volume_id, node_id, node_caller_id, expires_at, created_at
		FROM grants`
	args := make([]any, 0, 2)
	switch {
	case owner != "" && volumeID != "":
		query += " WHERE owner = ? AND volume_id = ?"
		args = append(args, owner, volumeID)
	case owner != "":
		query += " WHERE owner = ?"
		args = append(args, owner)
	case volumeID != "":
		query += " WHERE volume_id = ?"
		args = append(args, volumeID)
	}
	query += " ORDER BY expires_at, node_id"
	rows, err := tx.tx.QueryContext(tx.ctx, query, args...)
	if err != nil {
		return nil, mapError(err)
	}
	defer func() { _ = rows.Close() }()

	grants := make([]Grant, 0)
	for rows.Next() {
		var grant Grant
		var expiresAt, createdAt string
		if err := rows.Scan(&grant.Owner, &grant.VolumeID, &grant.NodeID,
			&grant.NodeCallerID, &expiresAt, &createdAt); err != nil {
			return nil, mapError(err)
		}
		if grant.ExpiresAt, err = parseTime(expiresAt); err != nil {
			return nil, err
		}
		if grant.CreatedAt, err = parseTime(createdAt); err != nil {
			return nil, err
		}
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err)
	}
	return grants, nil
}

func (tx *sqlTx) DeleteGrant(owner, volumeID, nodeID string) error {
	if err := validateOwnerVolume(owner, volumeID); err != nil {
		return err
	}
	if nodeID == "" {
		return fmt.Errorf("%w: node_id is required", ErrInvalid)
	}
	result, err := tx.tx.ExecContext(tx.ctx,
		"DELETE FROM grants WHERE owner = ? AND volume_id = ? AND node_id = ?",
		owner, volumeID, nodeID)
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
