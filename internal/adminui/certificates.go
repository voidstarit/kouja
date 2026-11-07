package adminui

import (
	"crypto/x509"
	"encoding/pem"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var certificateFilePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.(crt|key|pem)$`)

var sensitiveCertificateNames = map[string]struct{}{
	"ca.crt": {}, "ca.key": {}, "ca.srl": {},
	"server.crt": {}, "server.key": {},
	"client-ca.crt": {},
}

func allowedCertificateName(name string) bool {
	if !certificateFilePattern.MatchString(name) || filepath.Base(name) != name {
		return false
	}
	_, sensitive := sensitiveCertificateNames[strings.ToLower(name)]
	return !sensitive
}

func (server *Server) clientCertDir() string {
	if dir := strings.TrimSpace(server.options.Config.AdminUI.ClientCertDir); dir != "" {
		return dir
	}
	certFile := server.options.Config.Listen.TLS.CertFile
	if certFile == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(certFile), "clients")
}

func (server *Server) handleListCertificates(response http.ResponseWriter, _ *http.Request) {
	dir := server.clientCertDir()
	result := CertificatesResponse{Directory: dir, Files: []CertificateFile{}, CA: server.clientCA()}
	if dir == "" {
		result.Error = "client certificate directory is not configured"
		writeJSON(response, http.StatusOK, result)
		return
	}
	info, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			result.Error = "client certificate directory does not exist"
		} else {
			result.Error = err.Error()
		}
		writeJSON(response, http.StatusOK, result)
		return
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		result.Error = "client certificate directory is not a regular directory"
		writeJSON(response, http.StatusOK, result)
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		result.Error = err.Error()
		writeJSON(response, http.StatusOK, result)
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		if !allowedCertificateName(name) {
			continue
		}
		fileInfo, err := entry.Info()
		if err != nil || !fileInfo.Mode().IsRegular() {
			continue
		}
		kind := "certificate"
		if strings.HasSuffix(name, ".key") {
			kind = "private_key"
		}
		role := roleFromCertificateName(name)
		if kind == "certificate" {
			if parsedRole := roleFromCertificate(filepath.Join(dir, name)); parsedRole != "" {
				role = parsedRole
			}
		}
		result.Files = append(result.Files, CertificateFile{
			Name: name, Kind: kind, Role: role, SizeBytes: fileInfo.Size(),
			ModifiedAt:  formatTime(fileInfo.ModTime()),
			DownloadURL: "/api/certificates/" + url.PathEscape(name),
		})
	}
	sort.Slice(result.Files, func(left, right int) bool {
		return result.Files[left].Name < result.Files[right].Name
	})
	writeJSON(response, http.StatusOK, result)
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

func (server *Server) clientCA() *CertificateFile {
	path := server.options.Config.Listen.TLS.ClientCAFile
	if path == "" {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil
	}
	return &CertificateFile{
		Name: filepath.Base(path), Kind: "certificate", Role: "ca",
		SizeBytes: info.Size(), ModifiedAt: formatTime(info.ModTime()),
		DownloadURL: "/api/ca",
	}
}

func (server *Server) handleDownloadCA(response http.ResponseWriter, request *http.Request) {
	path := server.options.Config.Listen.TLS.ClientCAFile
	if path == "" {
		writeAPIError(response, http.StatusNotFound, "not_found", "client CA is not configured")
		return
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		writeAPIError(response, http.StatusNotFound, "not_found", "client CA is not available")
		return
	}
	data, err := os.ReadFile(path) //nolint:gosec // operator-configured client CA path
	if err != nil {
		writeAPIError(response, http.StatusNotFound, "not_found", "client CA is not available")
		return
	}
	name := filepath.Base(path)
	response.Header().Set("Content-Type", "application/x-pem-file")
	response.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(data) //nolint:gosec // serves the configured public CA certificate as a download
	server.options.Logger.Info("client CA downloaded", "remote", request.RemoteAddr)
}

func (server *Server) handleDownloadCertificate(response http.ResponseWriter, request *http.Request) {
	name, err := pathName(request.URL.Path, "/api/certificates/")
	if err != nil || !allowedCertificateName(name) {
		writeAPIError(response, http.StatusNotFound, "not_found", "certificate not found")
		return
	}
	dir := server.clientCertDir()
	if dir == "" {
		writeAPIError(response, http.StatusNotFound, "not_found", "certificate directory is not configured")
		return
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil || dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		writeAPIError(response, http.StatusNotFound, "not_found", "certificate directory is not available")
		return
	}
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path) //nolint:gosec // path is a validated basename under the configured client certificate directory
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		writeAPIError(response, http.StatusNotFound, "not_found", "certificate not found")
		return
	}
	data, err := os.ReadFile(path) //nolint:gosec // path is constrained to the configured client certificate directory
	if err != nil {
		writeAPIError(response, http.StatusNotFound, "not_found", "certificate not found")
		return
	}
	response.Header().Set("Content-Type", "application/x-pem-file")
	response.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(data) //nolint:gosec // serves a validated client certificate/key file as a download
	server.options.Logger.Info("client certificate downloaded", "name", name, "remote", request.RemoteAddr)
}

func roleFromCertificateName(name string) string {
	base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, ".crt"), ".key"), ".pem")
	role := strings.SplitN(base, "-", 2)[0]
	switch role {
	case "admin", "caller", "controller", "node":
		return role
	default:
		return ""
	}
}

func roleFromCertificate(path string) string {
	data, err := os.ReadFile(path) //nolint:gosec // path is constrained to the configured client certificate directory
	if err != nil {
		return ""
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return ""
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return ""
	}
	for _, uri := range certificate.URIs {
		if uri.Scheme != "spiffe" || uri.Host != "kouja" {
			continue
		}
		parts := strings.Split(strings.Trim(uri.Path, "/"), "/")
		if len(parts) == 2 {
			return parts[0]
		}
	}
	return ""
}
