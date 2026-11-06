package state

import (
	"database/sql"
	"fmt"
)

//nolint:gosec // SQL column list, not a credential
const credentialColumns = `id, owner, volume_id, protocol, secret_ciphertext,
	nonce, key_id, key_fingerprint, state, created_at, rotated_at`

func scanCredential(row rowScanner) (Credential, error) {
	var credential Credential
	var rotatedAt sql.NullString
	var createdAt string
	if err := row.Scan(
		&credential.ID,
		&credential.Owner,
		&credential.VolumeID,
		&credential.Protocol,
		&credential.SecretCiphertext,
		&credential.Nonce,
		&credential.KeyID,
		&credential.KeyFingerprint,
		&credential.State,
		&createdAt,
		&rotatedAt,
	); err != nil {
		return Credential{}, notFound(err)
	}
	var err error
	if credential.CreatedAt, err = parseTime(createdAt); err != nil {
		return Credential{}, err
	}
	if credential.RotatedAt, err = parseNullableTime(rotatedAt); err != nil {
		return Credential{}, err
	}
	return credential, nil
}

func (tx *sqlTx) PutCredential(credential Credential) error {
	if err := validateCredential(credential); err != nil {
		return err
	}
	_, err := tx.tx.ExecContext(tx.ctx, `
		INSERT INTO credentials (
			id, owner, volume_id, protocol, secret_ciphertext, nonce,
			key_id, key_fingerprint, state, created_at, rotated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			protocol = excluded.protocol,
			secret_ciphertext = excluded.secret_ciphertext,
			nonce = excluded.nonce,
			key_id = excluded.key_id,
			key_fingerprint = excluded.key_fingerprint,
			state = excluded.state,
			rotated_at = excluded.rotated_at`,
		credential.ID,
		credential.Owner,
		credential.VolumeID,
		credential.Protocol,
		credential.SecretCiphertext,
		credential.Nonce,
		credential.KeyID,
		credential.KeyFingerprint,
		credential.State,
		timeText(credential.CreatedAt),
		nullableTime(credential.RotatedAt),
	)
	return mapError(err)
}

func (tx *sqlTx) GetActiveCredential(owner, volumeID string) (Credential, error) {
	if err := validateOwnerVolume(owner, volumeID); err != nil {
		return Credential{}, err
	}
	row := tx.tx.QueryRowContext(tx.ctx,
		"SELECT "+credentialColumns+` FROM credentials
		 WHERE owner = ? AND volume_id = ? AND state = 'active'
		 ORDER BY created_at DESC LIMIT 1`,
		owner, volumeID)
	return scanCredential(row)
}

func (tx *sqlTx) DeleteCredential(owner, volumeID string) error {
	if err := validateOwnerVolume(owner, volumeID); err != nil {
		return err
	}
	result, err := tx.tx.ExecContext(tx.ctx,
		"DELETE FROM credentials WHERE owner = ? AND volume_id = ?", owner, volumeID)
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

func (tx *sqlTx) PutAudit(audit Audit) error {
	if audit.Caller == "" || audit.OperationID == "" || audit.Action == "" || audit.Result == "" {
		return fmt.Errorf("%w: caller, operation_id, action, and result are required", ErrInvalid)
	}
	if audit.Timestamp.IsZero() {
		audit.Timestamp = nowUTC()
	}
	if audit.ID > 0 {
		_, err := tx.tx.ExecContext(tx.ctx, `
			INSERT INTO audit (
				id, timestamp, caller, operation_id, request_id, volume_id,
				action, protocol, initiator, pre_state, post_state, result,
				error_code, source_ip
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			audit.ID,
			timeText(audit.Timestamp),
			audit.Caller,
			audit.OperationID,
			nullableString(audit.RequestID),
			nullableString(audit.VolumeID),
			audit.Action,
			nullableString(audit.Protocol),
			nullableString(audit.Initiator),
			nullableString(audit.PreState),
			nullableString(audit.PostState),
			audit.Result,
			nullableString(audit.ErrorCode),
			nullableString(audit.SourceIP),
		)
		return mapError(err)
	}
	_, err := tx.tx.ExecContext(tx.ctx, `
		INSERT INTO audit (
			timestamp, caller, operation_id, request_id, volume_id, action,
			protocol, initiator, pre_state, post_state, result, error_code, source_ip
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		timeText(audit.Timestamp),
		audit.Caller,
		audit.OperationID,
		nullableString(audit.RequestID),
		nullableString(audit.VolumeID),
		audit.Action,
		nullableString(audit.Protocol),
		nullableString(audit.Initiator),
		nullableString(audit.PreState),
		nullableString(audit.PostState),
		audit.Result,
		nullableString(audit.ErrorCode),
		nullableString(audit.SourceIP),
	)
	return mapError(err)
}
