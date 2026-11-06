package state

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const dbTimeLayout = time.RFC3339Nano

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "constraint failed") ||
		strings.Contains(message, "unique constraint") ||
		strings.Contains(message, "foreign key") ||
		strings.Contains(message, "database is locked") ||
		strings.Contains(message, "database is busy") {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return err
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return mapError(err)
}

func nowUTC() time.Time {
	return time.Now().UTC()
}

func timeText(value time.Time) string {
	return value.UTC().Format(dbTimeLayout)
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(dbTimeLayout, value)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, value)
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: invalid timestamp %q: %v", ErrInvalid, value, err)
	}
	return parsed.UTC(), nil
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return timeText(*value)
}

func parseNullableTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func intBool(value int64) bool {
	return value != 0
}

func stringOrEmpty(value sql.NullString) string {
	if value.Valid {
		return value.String
	}
	return ""
}

func nullableInt32(value sql.NullInt64) int32 {
	if !value.Valid || value.Int64 < 0 || value.Int64 > math.MaxInt32 {
		return 0
	}
	return int32(value.Int64)
}

func encodeStrings(values []string) (any, error) {
	if values == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("%w: encode string list: %v", ErrInvalid, err)
	}
	return string(encoded), nil
}

func decodeStrings(value sql.NullString) ([]string, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	var decoded []string
	if err := json.Unmarshal([]byte(value.String), &decoded); err != nil {
		return nil, fmt.Errorf("%w: decode string list: %v", ErrInvalid, err)
	}
	return decoded, nil
}

func encodeMap(values map[string]string) (any, error) {
	if values == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("%w: encode string map: %v", ErrInvalid, err)
	}
	return string(encoded), nil
}

func decodeMap(value sql.NullString) (map[string]string, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	var decoded map[string]string
	if err := json.Unmarshal([]byte(value.String), &decoded); err != nil {
		return nil, fmt.Errorf("%w: decode string map: %v", ErrInvalid, err)
	}
	return decoded, nil
}

func validateOwnerVolume(owner, volumeID string) error {
	if owner == "" || volumeID == "" {
		return fmt.Errorf("%w: owner and volume_id are required", ErrInvalid)
	}
	return nil
}

func validateVolume(volume Volume) error {
	if err := validateOwnerVolume(volume.Owner, volume.VolumeID); err != nil {
		return err
	}
	if volume.ResourceUUID == "" || volume.LVName == "" || volume.VGName == "" || volume.State == "" {
		return fmt.Errorf("%w: resource_uuid, lv_name, vg_name, and state are required", ErrInvalid)
	}
	if volume.RequestedSizeBytes < 0 {
		return fmt.Errorf("%w: requested_size_bytes must not be negative", ErrInvalid)
	}
	if volume.ActualSizeBytes < 0 {
		return fmt.Errorf("%w: actual_size_bytes must not be negative", ErrInvalid)
	}
	return nil
}

func validateExport(export Export) error {
	if err := validateOwnerVolume(export.Owner, export.VolumeID); err != nil {
		return err
	}
	if export.Protocol == "" || export.Initiator == "" || export.TargetName == "" || export.Portal == "" || export.State == "" {
		return fmt.Errorf("%w: protocol, initiator, target_name, portal, and state are required", ErrInvalid)
	}
	return nil
}

func validateTombstone(tombstone Tombstone) error {
	if err := validateOwnerVolume(tombstone.Owner, tombstone.VolumeID); err != nil {
		return err
	}
	if tombstone.ResourceUUID == "" || tombstone.LVName == "" || tombstone.VGName == "" {
		return fmt.Errorf("%w: resource_uuid, lv_name, and vg_name are required", ErrInvalid)
	}
	if tombstone.SizeBytes < 0 {
		return fmt.Errorf("%w: size_bytes must not be negative", ErrInvalid)
	}
	return nil
}

func validateOperation(operation Operation) error {
	if err := validateOwnerVolume(operation.Owner, operation.VolumeID); err != nil {
		return err
	}
	if operation.ID == "" || operation.ResourceUUID == "" || operation.Action == "" ||
		operation.RequestHash == "" || operation.IntentJSON == "" || operation.State == "" {
		return fmt.Errorf("%w: operation id, resource_uuid, action, request_hash, intent_json, and state are required", ErrInvalid)
	}
	if operation.Attempt < 0 || operation.MaxAttempts < 0 {
		return fmt.Errorf("%w: operation attempts must not be negative", ErrInvalid)
	}
	return nil
}

func validateCredential(credential Credential) error {
	if err := validateOwnerVolume(credential.Owner, credential.VolumeID); err != nil {
		return err
	}
	if credential.ID == "" || credential.Protocol == "" || credential.KeyID == "" ||
		credential.KeyFingerprint == "" || credential.State == "" {
		return fmt.Errorf("%w: credential id, protocol, key_id, key_fingerprint, and state are required", ErrInvalid)
	}
	if len(credential.SecretCiphertext) == 0 || len(credential.Nonce) == 0 {
		return fmt.Errorf("%w: credential ciphertext and nonce are required", ErrInvalid)
	}
	return nil
}
