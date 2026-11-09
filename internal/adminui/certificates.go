package adminui

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/voidstarit/kouja/internal/pki"
)

func (server *Server) handleListCertificates(response http.ResponseWriter, request *http.Request) {
	if server.options.Registry == nil || server.options.Files == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "unavailable", "certificate registry is not configured")
		return
	}
	files, err := server.options.Files.ListFiles(request.Context())
	if err != nil {
		writePKIError(response, err)
		return
	}
	bySerial := make(map[string]pki.FileInfo, len(files))
	for _, file := range files {
		if file.Kind == "certificate" {
			bySerial[file.SerialNumber] = file
		}
	}
	result := CertificatesResponse{
		IssuanceEnabled: server.options.Config.CertificateAuthority.Enabled && server.options.CAStatus != nil && server.options.CAStatus.Ready(),
		DefaultTTLDays:  server.options.Config.CertificateAuthority.DefaultTTLDays,
		MaxTTLDays:      server.options.Config.CertificateAuthority.MaxTTLDays,
		Certificates:    []CertificateFile{},
		CA:              server.clientCA(),
	}
	if server.options.CAStatus != nil {
		if err := server.options.CAStatus.CAState(); err != nil {
			result.Error = err.Error()
		}
	}
	token := ""
	for {
		page, err := server.options.Registry.List(request.Context(), pki.ListOptions{PageSize: 100, PageToken: token})
		if err != nil {
			writePKIError(response, err)
			return
		}
		for _, summary := range page.Certificates {
			file := bySerial[summary.SerialNumber]
			row := CertificateFile{
				CertificateName: summary.Name,
				SerialNumber:    summary.SerialNumber, Role: summary.Role, Kind: "certificate",
				NotAfter: formatTime(summary.NotAfter), Revoked: summary.Revoked,
				DownloadURL: "/api/certificates/" + url.PathEscape(file.Name),
			}
			if summary.RevokedAt != nil {
				row.RevokedAt = formatTime(*summary.RevokedAt)
			}
			if file.Name != "" {
				row.Name = file.Name
				row.SizeBytes = file.SizeBytes
				row.ModifiedAt = formatTime(file.ModifiedAt)
			}
			if keyName := strings.TrimSuffix(file.Name, ".crt") + ".key"; file.Name != "" {
				row.KeyDownloadURL = "/api/certificates/" + url.PathEscape(keyName)
			}
			result.Certificates = append(result.Certificates, row)
		}
		if page.NextPageToken == "" {
			break
		}
		token = page.NextPageToken
	}
	result.Files = result.Certificates
	writeJSON(response, http.StatusOK, result)
}

func (server *Server) handleRevokeCertificate(response http.ResponseWriter, request *http.Request) {
	if server.options.Registry == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "unavailable", "certificate registry is not configured")
		return
	}
	serial := strings.TrimPrefix(request.URL.Path, "/api/certificates/")
	if serial == "" || strings.Contains(serial, "/") {
		writeAPIError(response, http.StatusBadRequest, "invalid_request", "serial number is required")
		return
	}
	record, verdicts, err := server.options.Registry.Revoke(request.Context(), pki.Caller{
		Owner: "local-admin", Source: "admin-ui", RemoteAddr: request.RemoteAddr,
	}, serial, "admin-ui")
	if err != nil {
		writePKIError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, RevokeCertificateResponse{
		Certificate: CertificateFile{
			Name: filepath.Base(record.CertFile), CertificateName: record.Name,
			SerialNumber: record.SerialNumber, Role: record.Role, Kind: "certificate",
			NotAfter: formatTime(record.NotAfter),
			Revoked:  record.Revoked, DownloadURL: "/api/certificates/" + url.PathEscape(filepath.Base(record.CertFile)),
		},
		Warnings: warningsView(verdicts),
	})
}

func (server *Server) handleRevokeExternalCertificate(response http.ResponseWriter, request *http.Request) {
	body, err := readLimitedBody(response, request, 1<<20)
	if err != nil {
		writeMappedError(response, err)
		return
	}
	var payload struct {
		CertificatePEM string `json:"certificate_pem"`
		Reason         string `json:"reason"`
	}
	if err := decodeJSON(body, &payload); err != nil {
		writeMappedError(response, fmt.Errorf("%w: %v", errBadRequest, err))
		return
	}
	if server.options.ExternalRevoker == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "unavailable", "external revoker is not configured")
		return
	}
	record, verdicts, err := server.options.ExternalRevoker.RevokeExternal(request.Context(), pki.Caller{
		Owner: "local-admin", Source: "admin-ui", RemoteAddr: request.RemoteAddr,
	}, payload.CertificatePEM, payload.Reason)
	if err != nil {
		writePKIError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, RevokeExternalCertificateResponse{
		Certificate: CertificateFile{CertificateName: record.Name, SerialNumber: record.SerialNumber, Role: record.Role, Kind: "certificate", NotAfter: formatTime(record.NotAfter), Revoked: record.Revoked},
		Warnings:    warningsView(verdicts),
	})
}

func (server *Server) clientCA() *CertificateFile {
	path := server.options.Config.Listen.TLS.ClientCAFile
	if path == "" {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil
	}
	return &CertificateFile{Name: filepath.Base(path), Kind: "certificate", Role: "ca", SizeBytes: info.Size(), ModifiedAt: formatTime(info.ModTime()), DownloadURL: "/api/ca"}
}

func (server *Server) handleDownloadCA(response http.ResponseWriter, request *http.Request) {
	path := serveroptionsCA(server)
	if path == "" {
		writeAPIError(response, http.StatusNotFound, "not_found", "client CA is not configured")
		return
	}
	data, err := os.ReadFile(path) //nolint:gosec // operator-configured CA path
	if err != nil {
		writeAPIError(response, http.StatusNotFound, "not_found", "client CA is not available")
		return
	}
	name := filepath.Base(path)
	response.Header().Set("Content-Type", "application/x-pem-file")
	response.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(data) //nolint:gosec // serves a resolved certificate file
}

func serveroptionsCA(server *Server) string {
	if server == nil || server.options.Config == nil {
		return ""
	}
	return server.options.Config.Listen.TLS.ClientCAFile
}

func (server *Server) handleDownloadCertificate(response http.ResponseWriter, request *http.Request) {
	if server.options.Files == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "unavailable", "certificate files are not configured")
		return
	}
	name := strings.TrimPrefix(request.URL.Path, "/api/certificates/")
	path, err := server.options.Files.ResolveFile(name)
	if err != nil {
		writeAPIError(response, http.StatusNotFound, "not_found", "certificate not found")
		return
	}
	data, err := os.ReadFile(path) //nolint:gosec // resolved by the pki FileStore
	if err != nil {
		writeAPIError(response, http.StatusNotFound, "not_found", "certificate not found")
		return
	}
	response.Header().Set("Content-Type", "application/x-pem-file")
	response.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(path)}))
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(data) //nolint:gosec // serves a resolved certificate file
}

func (server *Server) tlsInfo() TLSInfo {
	info := TLSInfo{ServerNames: []string{}, IPAddresses: []string{}}
	path := server.options.Config.Listen.TLS.CertFile
	if path == "" {
		info.Error = "server certificate path is not configured"
		return info
	}
	data, err := os.ReadFile(path) //nolint:gosec // operator-configured server certificate path
	if err != nil {
		info.Error = err.Error()
		return info
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		info.Error = "server certificate is not a PEM certificate"
		return info
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		info.Error = err.Error()
		return info
	}
	info.Subject = certificate.Subject.CommonName
	info.ServerNames = append(info.ServerNames, certificate.DNSNames...)
	for _, address := range certificate.IPAddresses {
		info.IPAddresses = append(info.IPAddresses, address.String())
	}
	info.NotAfter = formatTime(certificate.NotAfter)
	if len(info.ServerNames) == 0 && len(info.IPAddresses) == 0 {
		info.Error = "server certificate has no DNS or IP subject alternative names"
	}
	return info
}
