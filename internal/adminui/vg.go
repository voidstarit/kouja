package adminui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/voidstarit/kouja/internal/agent"
	"github.com/voidstarit/kouja/internal/lvm"
)

func (server *Server) handleRegisterVG(response http.ResponseWriter, request *http.Request) {
	body, err := readLimitedBody(response, request, 1<<20)
	if err != nil {
		writeMappedError(response, err)
		return
	}
	var payload struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(body, &payload); err != nil {
		writeMappedError(response, fmt.Errorf("%w: %v", errBadRequest, err))
		return
	}
	if payload.Name == "" {
		writeMappedError(response, fmt.Errorf("%w: name is required", errBadRequest))
		return
	}
	server.vgClassGate.Lock()
	defer server.vgClassGate.Unlock()
	capacity, err := server.options.VGs.Register(request.Context(), payload.Name, "", false)
	if err != nil {
		writeMappedError(response, err)
		return
	}
	view := server.buildVGStatusFromCapacity(payload.Name, capacity, server.configuredVG(payload.Name), "")
	warnings := []WarningView{}
	if auditErr := server.writeAudit(request.Context(), "vg_register", "", "", "", "succeeded", "", request.RemoteAddr); auditErr != nil {
		warnings = append(warnings, WarningView{Code: "audit_write_failed", Message: auditErr.Error()})
	}
	writeJSON(response, http.StatusCreated, RegisterVGResponse{VG: view, Warnings: warnings})
}

func (server *Server) handleDeregisterVG(response http.ResponseWriter, request *http.Request) {
	name, err := pathName(request.URL.Path, "/api/vgs/")
	if err != nil {
		writeMappedError(response, err)
		return
	}
	server.vgClassGate.Lock()
	defer server.vgClassGate.Unlock()
	if err := server.options.VGs.Deregister(request.Context(), name); err != nil {
		writeMappedError(response, err)
		return
	}
	if auditErr := server.writeAudit(request.Context(), "vg_deregister", "", "", "", "succeeded", "", request.RemoteAddr); auditErr != nil {
		server.options.Logger.Warn("admin UI audit write failed", "action", "vg_deregister", "error", auditErr)
	}
	response.WriteHeader(http.StatusNoContent)
}

func (server *Server) handleSetClass(response http.ResponseWriter, request *http.Request) {
	class, err := pathName(request.URL.Path, "/api/classes/")
	if err != nil {
		writeMappedError(response, err)
		return
	}
	body, err := readLimitedBody(response, request, 1<<20)
	if err != nil {
		writeMappedError(response, err)
		return
	}
	var payload struct {
		VG string `json:"vg"`
	}
	if err := decodeJSON(body, &payload); err != nil {
		writeMappedError(response, fmt.Errorf("%w: %v", errBadRequest, err))
		return
	}
	if payload.VG == "" {
		writeMappedError(response, fmt.Errorf("%w: vg is required", errBadRequest))
		return
	}
	server.vgClassGate.Lock()
	defer server.vgClassGate.Unlock()
	vgs, err := server.options.VGs.List(request.Context())
	if err != nil {
		writeMappedError(response, err)
		return
	}
	found := false
	for _, vg := range vgs {
		if vg.Name == payload.VG {
			found = true
			break
		}
	}
	if !found {
		writeMappedError(response, fmt.Errorf("%w: VG %q is not registered", agent.ErrVGUnavailable, payload.VG))
		return
	}
	warnings, err := server.options.Classes.Set(request.Context(), class, payload.VG)
	if err != nil {
		writeMappedError(response, err)
		return
	}
	configState, err := server.configState()
	if err != nil {
		writeMappedError(response, fmt.Errorf("%w: %v", errInternal, err))
		return
	}
	result := SetClassResponse{
		Classes: server.options.Classes.Snapshot(), ConfigChanged: configState.ConfigChanged,
		RestartRequired: configState.RestartRequired, Warnings: warningViews(warnings),
	}
	if auditErr := server.writeAudit(request.Context(), "class_set", "", "", "", "succeeded", "", request.RemoteAddr); auditErr != nil {
		result.Warnings = append(result.Warnings, WarningView{Code: "audit_write_failed", Message: auditErr.Error()})
	}
	writeJSON(response, http.StatusOK, result)
}

func (server *Server) handleRemoveClass(response http.ResponseWriter, request *http.Request) {
	class, err := pathName(request.URL.Path, "/api/classes/")
	if err != nil {
		writeMappedError(response, err)
		return
	}
	server.vgClassGate.Lock()
	defer server.vgClassGate.Unlock()
	if _, err := server.options.Classes.Remove(request.Context(), class); err != nil {
		writeMappedError(response, err)
		return
	}
	if auditErr := server.writeAudit(request.Context(), "class_remove", "", "", "", "succeeded", "", request.RemoteAddr); auditErr != nil {
		server.options.Logger.Warn("admin UI audit write failed", "action", "class_remove", "error", auditErr)
	}
	response.WriteHeader(http.StatusNoContent)
}

func (server *Server) buildVGStatusFromCapacity(name string, capacity lvm.Capacity, configured bool, errMessage string) VGStatus {
	view := VGStatus{
		Name: name, UUID: capacity.VGUUID, State: capacity.VGState,
		StorageClass: server.options.Classes.ClassName(name), Configured: configured,
		TotalBytes: capacity.TotalBytes, FreeBytes: capacity.FreeBytes,
		AllocatableBytes: capacity.AllocatableBytes, ExtentSizeBytes: capacity.ExtentSizeBytes,
		MaxVolumeBytes: capacity.MaxVolumeBytes, ObservedAt: formatTime(capacity.ObservedAt),
		Error: errMessage,
	}
	if view.State == "" {
		view.State = "active"
	}
	return view
}

func (server *Server) configuredVG(name string) bool {
	for _, configured := range server.effectiveVGs() {
		if configured == name {
			return true
		}
	}
	return false
}

func pathName(fullPath, prefix string) (string, error) {
	if !strings.HasPrefix(fullPath, prefix) {
		return "", fmt.Errorf("%w: invalid path", errBadRequest)
	}
	raw := strings.TrimPrefix(fullPath, prefix)
	if raw == "" || strings.Contains(raw, "/") {
		return "", fmt.Errorf("%w: name is required", errBadRequest)
	}
	name, err := url.PathUnescape(raw)
	if err != nil || name == "" {
		return "", fmt.Errorf("%w: invalid name", errBadRequest)
	}
	return name, nil
}

func decodeJSON(body []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func warningViews(warnings []string) []WarningView {
	result := make([]WarningView, 0, len(warnings))
	for _, warning := range warnings {
		message := warning
		switch warning {
		case "durability_uncertain":
			message = "the file was replaced but the directory sync failed"
		case "audit_write_failed":
			message = "the mutation succeeded but the audit row could not be written"
		}
		result = append(result, WarningView{Code: warning, Message: message})
	}
	return result
}
