package operations

import (
	"encoding/json"
	"fmt"
)

// CreateIntent is the normalized ensure request. ExportVolume uses the same
// action key when the API layer canonicalizes it into a create request.
type CreateIntent struct {
	ResourceUUID string `json:"resource_uuid"`
	SizeBytes    int64  `json:"size_bytes"`
	VG           string `json:"vg"`
	Export       bool   `json:"export"`
	Protocol     string `json:"protocol,omitempty"`
	Initiator    string `json:"initiator,omitempty"`
	TargetName   string `json:"target_name,omitempty"`
	Portal       string `json:"portal,omitempty"`
}

type ExportIntent struct {
	Protocol         string `json:"protocol"`
	Initiator        string `json:"initiator"`
	TargetName       string `json:"target_name"`
	Portal           string `json:"portal"`
	RotateCredential bool   `json:"rotate_credential,omitempty"`
	Force            bool   `json:"force,omitempty"`
	AttachmentID     string `json:"attachment_id,omitempty"`
}

type UnexportIntent struct {
	Force bool `json:"force"`
	// RequestID makes an explicit admin disable a distinct operation when the
	// same volume is exported and disabled again later.
	RequestID string `json:"request_id,omitempty"`
}

type ResizeIntent struct {
	SizeBytes   int64 `json:"size_bytes"`
	AllowShrink bool  `json:"allow_shrink,omitempty"`
}

type DeleteIntent struct {
	Force bool `json:"force"`
}

func parseCreateIntent(raw json.RawMessage) (CreateIntent, error) {
	var intent CreateIntent
	if err := json.Unmarshal(raw, &intent); err != nil {
		return CreateIntent{}, fmt.Errorf("%w: create intent: %v", ErrInvalidRequest, err)
	}
	if intent.ResourceUUID == "" || intent.SizeBytes <= 0 || intent.VG == "" {
		return CreateIntent{}, fmt.Errorf("%w: resource_uuid, positive size_bytes, and vg are required", ErrInvalidRequest)
	}
	if intent.Export {
		if intent.Protocol == "" || intent.Initiator == "" || intent.TargetName == "" || intent.Portal == "" {
			return CreateIntent{}, fmt.Errorf("%w: protocol, initiator, target_name, and portal are required when export is true", ErrInvalidRequest)
		}
	}
	return intent, nil
}

func parseExportIntent(raw json.RawMessage) (ExportIntent, error) {
	var intent ExportIntent
	if err := json.Unmarshal(raw, &intent); err != nil {
		return ExportIntent{}, fmt.Errorf("%w: export intent: %v", ErrInvalidRequest, err)
	}
	if intent.Protocol == "" || intent.Initiator == "" || intent.TargetName == "" || intent.Portal == "" {
		return ExportIntent{}, fmt.Errorf("%w: protocol, initiator, target_name, and portal are required", ErrInvalidRequest)
	}
	return intent, nil
}

func parseUnexportIntent(raw json.RawMessage) (UnexportIntent, error) {
	var intent UnexportIntent
	if len(raw) == 0 {
		return intent, nil
	}
	if err := json.Unmarshal(raw, &intent); err != nil {
		return UnexportIntent{}, fmt.Errorf("%w: unexport intent: %v", ErrInvalidRequest, err)
	}
	return intent, nil
}

func parseResizeIntent(raw json.RawMessage) (ResizeIntent, error) {
	var intent ResizeIntent
	if err := json.Unmarshal(raw, &intent); err != nil {
		return ResizeIntent{}, fmt.Errorf("%w: resize intent: %v", ErrInvalidRequest, err)
	}
	if intent.SizeBytes <= 0 {
		return ResizeIntent{}, fmt.Errorf("%w: positive size_bytes is required", ErrInvalidRequest)
	}
	return intent, nil
}

func parseDeleteIntent(raw json.RawMessage) (DeleteIntent, error) {
	var intent DeleteIntent
	if len(raw) == 0 {
		return intent, nil
	}
	if err := json.Unmarshal(raw, &intent); err != nil {
		return DeleteIntent{}, fmt.Errorf("%w: delete intent: %v", ErrInvalidRequest, err)
	}
	return intent, nil
}
