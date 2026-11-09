package state

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

const certificateColumns = `serial_number, role, name, issued_by, issuer_serial,
	request_id, certificate_pem, client_ca_pem, cert_file, key_file,
	not_before, not_after, created_at, revoked_at, revoked_reason`

func scanCertificate(row rowScanner) (Certificate, error) {
	var certificate Certificate
	var requestID, revokedAt, revokedReason sql.NullString
	var notBefore, notAfter, createdAt string
	if err := row.Scan(
		&certificate.SerialNumber,
		&certificate.Role,
		&certificate.Name,
		&certificate.IssuedBy,
		&certificate.IssuerSerial,
		&requestID,
		&certificate.CertificatePEM,
		&certificate.ClientCAPEM,
		&certificate.CertFile,
		&certificate.KeyFile,
		&notBefore,
		&notAfter,
		&createdAt,
		&revokedAt,
		&revokedReason,
	); err != nil {
		return Certificate{}, notFound(err)
	}
	var err error
	if certificate.RequestID, err = nullableStringPointer(requestID); err != nil {
		return Certificate{}, err
	}
	if certificate.NotBefore, err = parseTime(notBefore); err != nil {
		return Certificate{}, err
	}
	if certificate.NotAfter, err = parseTime(notAfter); err != nil {
		return Certificate{}, err
	}
	if certificate.CreatedAt, err = parseTime(createdAt); err != nil {
		return Certificate{}, err
	}
	if certificate.RevokedAt, err = parseNullableTime(revokedAt); err != nil {
		return Certificate{}, err
	}
	if revokedReason.Valid {
		certificate.RevokedReason = revokedReason.String
	}
	certificate.Revoked = certificate.RevokedAt != nil
	return certificate, nil
}

func nullableStringPointer(value sql.NullString) (*string, error) {
	if !value.Valid {
		return nil, nil
	}
	copied := value.String
	return &copied, nil
}

func (tx *sqlTx) PutCertificate(certificate Certificate) error {
	if certificate.SerialNumber == "" || certificate.Role == "" || certificate.Name == "" ||
		certificate.IssuedBy == "" || certificate.CertificatePEM == "" ||
		certificate.ClientCAPEM == "" || certificate.NotBefore.IsZero() ||
		certificate.NotAfter.IsZero() || certificate.CreatedAt.IsZero() {
		return fmt.Errorf("%w: certificate serial, role, name, issuer, PEMs, and times are required", ErrInvalid)
	}
	_, err := tx.tx.ExecContext(tx.ctx, `
		INSERT INTO certificates (`+certificateColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		certificate.SerialNumber,
		certificate.Role,
		certificate.Name,
		certificate.IssuedBy,
		certificate.IssuerSerial,
		nullableStringPointerValue(certificate.RequestID),
		certificate.CertificatePEM,
		certificate.ClientCAPEM,
		certificate.CertFile,
		certificate.KeyFile,
		timeText(certificate.NotBefore),
		timeText(certificate.NotAfter),
		timeText(certificate.CreatedAt),
		nullableTime(certificate.RevokedAt),
		nullableString(certificate.RevokedReason),
	)
	return mapError(err)
}

func nullableStringPointerValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func (tx *sqlTx) GetCertificate(serial string) (Certificate, error) {
	if serial == "" {
		return Certificate{}, fmt.Errorf("%w: serial number is required", ErrInvalid)
	}
	row := tx.tx.QueryRowContext(tx.ctx,
		"SELECT "+certificateColumns+" FROM certificates WHERE serial_number = ? AND not_after > ?",
		serial, timeText(nowUTC()))
	return scanCertificate(row)
}

func (tx *sqlTx) ListCertificates(pageSize int32, pageToken string) ([]Certificate, string, error) {
	limit := normalizeCertificatePageSize(pageSize)
	var rows *sql.Rows
	var err error
	args := []any{timeText(nowUTC()), limit}
	query := "SELECT " + certificateColumns + ` FROM certificates
		WHERE not_after > ?`
	if pageToken != "" {
		createdAt, serial, err := decodeCertificateToken(pageToken)
		if err != nil {
			return nil, "", err
		}
		query += ` AND (created_at < ? OR (created_at = ? AND serial_number > ?))`
		args = []any{timeText(nowUTC()), createdAt, createdAt, serial, limit}
	}
	query += " ORDER BY created_at DESC, serial_number ASC LIMIT ?"
	args[len(args)-1] = limit + 1
	rows, err = tx.tx.QueryContext(tx.ctx, query, args...)
	if err != nil {
		return nil, "", mapError(err)
	}
	defer func() { _ = rows.Close() }()

	certificates := make([]Certificate, 0, limit)
	for rows.Next() {
		certificate, err := scanCertificate(rows)
		if err != nil {
			return nil, "", err
		}
		certificates = append(certificates, certificate)
	}
	if err := rows.Err(); err != nil {
		return nil, "", mapError(err)
	}
	next := ""
	if len(certificates) > limit {
		certificates = certificates[:limit]
		last := certificates[len(certificates)-1]
		next = encodeCertificateToken(last.CreatedAt, last.SerialNumber)
	}
	return certificates, next, nil
}

func normalizeCertificatePageSize(pageSize int32) int {
	if pageSize <= 0 {
		return 50
	}
	if pageSize > 100 {
		return 100
	}
	return int(pageSize)
}

func encodeCertificateToken(createdAt time.Time, serial string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(timeText(createdAt) + "|" + serial))
}

func decodeCertificateToken(token string) (string, string, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", "", fmt.Errorf("%w: invalid page token", ErrInvalid)
	}
	parts := strings.SplitN(string(decoded), "|", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("%w: invalid page token", ErrInvalid)
	}
	if _, err := parseTime(parts[0]); err != nil {
		return "", "", err
	}
	return parts[0], parts[1], nil
}

func (tx *sqlTx) RevokeCertificate(serial, reason string, at time.Time) (Certificate, error) {
	if serial == "" {
		return Certificate{}, fmt.Errorf("%w: serial number is required", ErrInvalid)
	}
	if at.IsZero() {
		at = nowUTC()
	}
	result, err := tx.tx.ExecContext(tx.ctx,
		`UPDATE certificates SET revoked_at = ?, revoked_reason = ?
		 WHERE serial_number = ? AND revoked_at IS NULL`,
		timeText(at), nullableString(reason), serial)
	if err != nil {
		return Certificate{}, mapError(err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return Certificate{}, mapError(err)
	}
	certificate, err := tx.GetCertificate(serial)
	if err != nil {
		return Certificate{}, err
	}
	if affected == 0 && certificate.RevokedAt == nil {
		return Certificate{}, ErrNotFound
	}
	return certificate, nil
}

func (tx *sqlTx) DeleteCertificate(serial string) error {
	if serial == "" {
		return fmt.Errorf("%w: serial number is required", ErrInvalid)
	}
	_, err := tx.tx.ExecContext(tx.ctx,
		"DELETE FROM certificates WHERE serial_number = ?", serial)
	return mapError(err)
}

func (tx *sqlTx) ListExpiredCertificates(before time.Time) ([]Certificate, error) {
	rows, err := tx.tx.QueryContext(tx.ctx,
		"SELECT "+certificateColumns+" FROM certificates WHERE not_after <= ? ORDER BY not_after",
		timeText(before))
	if err != nil {
		return nil, mapError(err)
	}
	defer func() { _ = rows.Close() }()
	certificates := make([]Certificate, 0)
	for rows.Next() {
		certificate, err := scanCertificate(rows)
		if err != nil {
			return nil, err
		}
		certificates = append(certificates, certificate)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err)
	}
	return certificates, nil
}

func (tx *sqlTx) ListRevokedSerials() ([]string, error) {
	rows, err := tx.tx.QueryContext(tx.ctx,
		`SELECT serial_number FROM certificates
		 WHERE revoked_at IS NOT NULL AND not_after > ? ORDER BY serial_number`,
		timeText(nowUTC()))
	if err != nil {
		return nil, mapError(err)
	}
	defer func() { _ = rows.Close() }()
	serials := make([]string, 0)
	for rows.Next() {
		var serial string
		if err := rows.Scan(&serial); err != nil {
			return nil, mapError(err)
		}
		serials = append(serials, serial)
	}
	if err := rows.Err(); err != nil {
		return nil, mapError(err)
	}
	return serials, nil
}

func (tx *sqlTx) CountActiveCertificates(now time.Time) (int, error) {
	var count int
	err := tx.tx.QueryRowContext(tx.ctx,
		`SELECT COUNT(*) FROM certificates
		 WHERE revoked_at IS NULL AND not_after > ?`,
		timeText(now)).Scan(&count)
	if err != nil {
		return 0, mapError(err)
	}
	return count, nil
}
