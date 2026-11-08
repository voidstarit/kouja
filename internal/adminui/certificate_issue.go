package adminui

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
)

var clientCertificateNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

var certificateRoles = map[string]struct{}{
	"admin":  {},
	"caller": {},
}

func (server *Server) handleCreateCertificate(response http.ResponseWriter, request *http.Request) {
	body, err := readLimitedBody(response, request, 16<<10)
	if err != nil {
		writeMappedError(response, err)
		return
	}
	var payload struct {
		Role string `json:"role"`
		Name string `json:"name"`
	}
	if err := decodeJSON(body, &payload); err != nil {
		writeMappedError(response, fmt.Errorf("%w: %v", errBadRequest, err))
		return
	}
	certificate, privateKey, err := server.issueClientCertificate(payload.Role, payload.Name)
	if err != nil {
		writeMappedError(response, err)
		return
	}
	if auditErr := server.writeAudit(request.Context(), "certificate_issue", "", "",
		strings.TrimSpace(payload.Role)+"/"+strings.TrimSpace(payload.Name), "succeeded", "", request.RemoteAddr); auditErr != nil {
		server.options.Logger.Warn("admin UI audit write failed", "action", "certificate_issue", "error", auditErr)
	}
	server.options.Logger.Info("client certificate issued", "role", strings.TrimSpace(payload.Role), "name", strings.TrimSpace(payload.Name), "remote", request.RemoteAddr)
	writeJSON(response, http.StatusCreated, CreateCertificateResponse{
		Certificate: certificate, PrivateKey: privateKey,
	})
}

func (server *Server) issueClientCertificate(role, name string) (CertificateFile, CertificateFile, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	name = strings.TrimSpace(name)
	if _, ok := certificateRoles[role]; !ok {
		return CertificateFile{}, CertificateFile{}, fmt.Errorf("%w: role must be admin or caller", errBadRequest)
	}
	if !clientCertificateNamePattern.MatchString(name) {
		return CertificateFile{}, CertificateFile{}, fmt.Errorf("%w: certificate name must be 1-64 characters and contain only letters, digits, dot, underscore, or hyphen", errBadRequest)
	}
	dir, err := server.ensureClientCertDir()
	if err != nil {
		return CertificateFile{}, CertificateFile{}, err
	}
	certName := role + "-" + name + ".crt"
	keyName := role + "-" + name + ".key"
	certPath := filepath.Join(dir, certName)
	keyPath := filepath.Join(dir, keyName)
	if server.isClientCAKey(certPath) || server.isClientCAKey(keyPath) {
		return CertificateFile{}, CertificateFile{}, fmt.Errorf("%w: requested certificate would overwrite the client CA private key", errConflict)
	}
	if err := ensureMissing(certPath); err != nil {
		return CertificateFile{}, CertificateFile{}, err
	}
	if err := ensureMissing(keyPath); err != nil {
		return CertificateFile{}, CertificateFile{}, err
	}

	caCertificate, caKey, err := server.loadClientCA()
	if err != nil {
		return CertificateFile{}, CertificateFile{}, err
	}
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return CertificateFile{}, CertificateFile{}, fmt.Errorf("%w: generate client key: %v", errInternal, err)
	}
	serial, err := randomSerialNumber()
	if err != nil {
		return CertificateFile{}, CertificateFile{}, fmt.Errorf("%w: generate serial number: %v", errInternal, err)
	}
	now := time.Now().UTC()
	if server.options.Now != nil {
		now = server.options.Now().UTC()
	}
	notAfter := now.Add(825 * 24 * time.Hour)
	if caCertificate.NotAfter.Before(notAfter) {
		notAfter = caCertificate.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "kouja-" + role + "-" + name},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		URIs:                  []*url.URL{{Scheme: "spiffe", Host: "kouja", Path: "/" + role + "/" + name}},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCertificate, &clientKey.PublicKey, caKey)
	if err != nil {
		return CertificateFile{}, CertificateFile{}, fmt.Errorf("%w: sign client certificate: %v", errInternal, err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(clientKey)
	if err != nil {
		return CertificateFile{}, CertificateFile{}, fmt.Errorf("%w: encode client key: %v", errInternal, err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if certificatePEM == nil || keyPEM == nil {
		return CertificateFile{}, CertificateFile{}, fmt.Errorf("%w: encode client credentials", errInternal)
	}

	if err := writeExclusive(certPath, certificatePEM, 0o644); err != nil {
		return CertificateFile{}, CertificateFile{}, err
	}
	if err := writeExclusive(keyPath, keyPEM, 0o600); err != nil {
		_ = os.Remove(certPath)
		return CertificateFile{}, CertificateFile{}, err
	}
	certificate, err := certificateFile(certPath, role, "certificate")
	if err != nil {
		_ = os.Remove(certPath)
		_ = os.Remove(keyPath)
		return CertificateFile{}, CertificateFile{}, err
	}
	privateKey, err := certificateFile(keyPath, role, "private_key")
	if err != nil {
		_ = os.Remove(certPath)
		_ = os.Remove(keyPath)
		return CertificateFile{}, CertificateFile{}, err
	}
	return certificate, privateKey, nil
}

func (server *Server) ensureClientCertDir() (string, error) {
	dir := server.clientCertDir()
	if dir == "" {
		return "", fmt.Errorf("%w: client certificate directory is not configured", errInvalidConfig)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil { //nolint:gosec // client certificate directory must be private
		return "", fmt.Errorf("%w: create client certificate directory: %v", errInvalidConfig, err)
	}
	info, err := os.Lstat(dir)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("%w: client certificate directory is not a regular directory", errUnsafePath)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%w: client certificate directory must be 0700", errUnsafePath)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return "", fmt.Errorf("%w: client certificate directory must be owned by the agent user", errUnsafePath)
	}
	return dir, nil
}

func (server *Server) clientCAKeyPath() string {
	if path := strings.TrimSpace(server.options.Config.AdminUI.ClientCAKeyFile); path != "" {
		return path
	}
	if path := strings.TrimSpace(server.options.Config.Listen.TLS.ClientCAFile); path != "" {
		return filepath.Join(filepath.Dir(path), "ca.key")
	}
	return ""
}

func (server *Server) isClientCAKey(path string) bool {
	keyPath := server.clientCAKeyPath()
	return keyPath != "" && filepath.Clean(path) == filepath.Clean(keyPath)
}

func (server *Server) loadClientCA() (*x509.Certificate, crypto.Signer, error) {
	certPath := strings.TrimSpace(server.options.Config.Listen.TLS.ClientCAFile)
	if certPath == "" {
		return nil, nil, fmt.Errorf("%w: client CA certificate is not configured", errInvalidConfig)
	}
	certInfo, err := os.Lstat(certPath)
	if err != nil || certInfo.Mode()&os.ModeSymlink != 0 || !certInfo.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%w: client CA certificate is not a regular file", errInvalidConfig)
	}
	certData, err := os.ReadFile(certPath) //nolint:gosec // operator-configured CA path
	if err != nil {
		return nil, nil, fmt.Errorf("%w: read client CA certificate", errInvalidConfig)
	}
	certBlock, _ := pem.Decode(certData)
	if certBlock == nil || certBlock.Type != "CERTIFICATE" {
		return nil, nil, fmt.Errorf("%w: client CA certificate is not PEM", errInvalidConfig)
	}
	caCertificate, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil || !caCertificate.IsCA {
		return nil, nil, fmt.Errorf("%w: client CA certificate is invalid", errInvalidConfig)
	}
	now := time.Now()
	if server.options.Now != nil {
		now = server.options.Now()
	}
	if now.Before(caCertificate.NotBefore) || now.After(caCertificate.NotAfter) {
		return nil, nil, fmt.Errorf("%w: client CA certificate is not currently valid", errInvalidConfig)
	}

	keyPath := server.clientCAKeyPath()
	if keyPath == "" {
		return nil, nil, fmt.Errorf("%w: client CA private key is not configured", errInvalidConfig)
	}
	keyInfo, err := os.Lstat(keyPath)
	if err != nil || keyInfo.Mode()&os.ModeSymlink != 0 || !keyInfo.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%w: client CA private key is not a regular file", errInvalidConfig)
	}
	if keyInfo.Mode().Perm()&0o077 != 0 {
		return nil, nil, fmt.Errorf("%w: client CA private key must be 0600 or stricter", errInvalidConfig)
	}
	keyStat, ok := keyInfo.Sys().(*syscall.Stat_t)
	if !ok || int(keyStat.Uid) != os.Geteuid() {
		return nil, nil, fmt.Errorf("%w: client CA private key must be owned by the agent user", errInvalidConfig)
	}
	keyData, err := os.ReadFile(keyPath) //nolint:gosec // operator-configured CA private key path
	if err != nil {
		return nil, nil, fmt.Errorf("%w: read client CA private key", errInvalidConfig)
	}
	keyBlock, _ := pem.Decode(keyData)
	if keyBlock == nil {
		return nil, nil, fmt.Errorf("%w: client CA private key is not PEM", errInvalidConfig)
	}
	if _, encrypted := keyBlock.Headers["Proc-Type"]; encrypted {
		return nil, nil, fmt.Errorf("%w: encrypted client CA private keys are not supported", errInvalidConfig)
	}
	caKey, err := parsePrivateKey(keyBlock)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: parse client CA private key: %v", errInvalidConfig, err)
	}
	certPublic, err := x509.MarshalPKIXPublicKey(caCertificate.PublicKey)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: encode client CA public key", errInvalidConfig)
	}
	keyPublic, err := x509.MarshalPKIXPublicKey(caKey.Public())
	if err != nil {
		return nil, nil, fmt.Errorf("%w: encode client CA private key", errInvalidConfig)
	}
	if !bytes.Equal(certPublic, keyPublic) {
		return nil, nil, fmt.Errorf("%w: client CA certificate and private key do not match", errInvalidConfig)
	}
	return caCertificate, caKey, nil
}

func parsePrivateKey(block *pem.Block) (crypto.Signer, error) {
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		return key, err
	case "EC PRIVATE KEY":
		key, err := x509.ParseECPrivateKey(block.Bytes)
		return key, err
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		signer, ok := key.(crypto.Signer)
		if !ok {
			return nil, errors.New("private key does not implement crypto.Signer")
		}
		return signer, nil
	default:
		return nil, fmt.Errorf("unsupported private key PEM block %q", block.Type)
	}
}

func randomSerialNumber() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	for {
		serial, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return nil, err
		}
		if serial.Sign() > 0 {
			return serial, nil
		}
	}
}

func ensureMissing(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%w: certificate file %s already exists", errConflict, filepath.Base(path))
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect certificate path: %v", errInternal, err)
	}
	return nil
}

func writeExclusive(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode) //nolint:gosec // path is a validated basename under the client certificate directory
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%w: certificate file %s already exists", errConflict, filepath.Base(path))
		}
		return fmt.Errorf("%w: create certificate file: %v", errInternal, err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return fmt.Errorf("%w: write certificate file: %v", errInternal, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("%w: close certificate file: %v", errInternal, err)
	}
	return nil
}

func certificateFile(path, role, kind string) (CertificateFile, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return CertificateFile{}, fmt.Errorf("%w: certificate file is not available", errInternal)
	}
	return CertificateFile{
		Name: filepath.Base(path), Kind: kind, Role: role,
		SizeBytes: info.Size(), ModifiedAt: formatTime(info.ModTime()),
		DownloadURL: "/api/certificates/" + url.PathEscape(filepath.Base(path)),
	}, nil
}
