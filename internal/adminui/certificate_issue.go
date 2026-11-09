package adminui

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/voidstarit/kouja/internal/pki"
	"github.com/voidstarit/kouja/internal/warnings"
)

func (server *Server) handleCreateCertificate(response http.ResponseWriter, request *http.Request) {
	body, err := readLimitedBody(response, request, 16<<10)
	if err != nil {
		writeMappedError(response, err)
		return
	}
	var payload struct {
		Role    string `json:"role"`
		Name    string `json:"name"`
		TTLDays int32  `json:"ttl_days"`
	}
	if err := decodeJSON(body, &payload); err != nil {
		writeMappedError(response, fmt.Errorf("%w: %v", errBadRequest, err))
		return
	}
	if server.options.Issuer == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "unavailable", "certificate issuer is not configured")
		return
	}
	issued, err := server.options.Issuer.Issue(request.Context(), pki.Caller{
		Owner: "local-admin", Source: "admin-ui", RemoteAddr: request.RemoteAddr,
	}, pki.IssueRequest{Role: payload.Role, Name: payload.Name, TTLDays: payload.TTLDays})
	if err != nil {
		writePKIError(response, err)
		return
	}
	server.options.Logger.Info("client certificate issued", "role", issued.Role, "name", issued.Name, "serial", issued.SerialNumber, "remote", request.RemoteAddr)
	writeJSON(response, http.StatusCreated, CreateCertificateResponse{
		Certificate:  issuedFile(issued.Role, issued.Name, issued.SerialNumber, "certificate", issued.NotAfter),
		PrivateKey:   issuedFile(issued.Role, issued.Name, issued.SerialNumber, "private_key", issued.NotAfter),
		SerialNumber: issued.SerialNumber, Role: issued.Role, Name: issued.Name,
		NotAfter: formatTime(issued.NotAfter), Warnings: warningsView(issued.Warnings),
	})
}

func issuedFile(role, name, serial, kind string, notAfter time.Time) CertificateFile {
	suffix := ".crt"
	if kind == "private_key" {
		suffix = ".key"
	}
	fileName := role + "-" + name + "-" + serial + suffix
	return CertificateFile{
		Name: fileName, CertificateName: name, SerialNumber: serial, Kind: kind, Role: role,
		NotAfter: formatTime(notAfter), DownloadURL: "/api/certificates/" + url.PathEscape(fileName),
	}
}

func warningsView(values []warnings.Warning) []WarningView {
	result := make([]WarningView, 0, len(values))
	for _, value := range values {
		result = append(result, WarningView{Code: value.Code, Message: value.Message})
	}
	return result
}

func writePKIError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, pki.ErrDisabled):
		writeAPIError(response, http.StatusServiceUnavailable, "issuance_disabled", err.Error())
	case errors.Is(err, pki.ErrCAUnavailable):
		writeAPIError(response, http.StatusServiceUnavailable, "ca_unavailable", err.Error())
	case errors.Is(err, pki.ErrInvalidRole), errors.Is(err, pki.ErrInvalidName), errors.Is(err, pki.ErrInvalidTTL), errors.Is(err, pki.ErrInvalidSerial), errors.Is(err, pki.ErrExternalInvalid):
		writeAPIError(response, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, pki.ErrQuotaExceeded):
		writeAPIError(response, http.StatusTooManyRequests, "quota_exceeded", err.Error())
	case errors.Is(err, pki.ErrNotFound):
		writeAPIError(response, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, pki.ErrJournalUnhealthy):
		writeAPIError(response, http.StatusServiceUnavailable, "journal_unhealthy", err.Error())
	default:
		writeAPIError(response, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

var _ = strings.TrimSpace
