package adminui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"reflect"
	"sort"
	"time"

	"github.com/voidstarit/kouja/internal/agent"
	"github.com/voidstarit/kouja/internal/config"
	"github.com/voidstarit/kouja/internal/fsutil"
	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

type configFile struct {
	Raw           []byte
	Parsed        *config.Config
	ETag          string
	DiskError     string
	DiskErrorCode string
	Valid         bool
}

func readConfig(path string) (configFile, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return configFile{ETag: "*", DiskErrorCode: "missing", DiskError: "configuration file does not exist"}, nil
	}
	if err != nil {
		return configFile{DiskErrorCode: "read_error", DiskError: err.Error()}, nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return configFile{DiskErrorCode: "unsafe_path", DiskError: "configuration path is not a regular file"}, nil
	}
	raw, err := os.ReadFile(path) //nolint:gosec // operator-supplied configuration path
	if err != nil {
		return configFile{DiskErrorCode: "read_error", DiskError: err.Error()}, nil
	}
	etag := rawETag(raw)
	parsed, err := config.Parse(raw)
	if err != nil {
		return configFile{Raw: raw, ETag: etag, DiskErrorCode: "parse_error", DiskError: err.Error()}, nil
	}
	return configFile{Raw: raw, Parsed: parsed, ETag: etag, Valid: true}, nil
}

func rawETag(data []byte) string {
	sum := sha256.Sum256(data)
	return `"sha256:` + hex.EncodeToString(sum[:]) + `"`
}

func normalizeConfig(input *config.Config, classes agent.ClassRegistry) *config.Config {
	if input == nil {
		return nil
	}
	copyOfConfig := *input
	copyOfConfig.Storage.VGs = append([]string(nil), input.Storage.VGs...)
	sort.Strings(copyOfConfig.Storage.VGs)
	seen := make(map[string]struct{}, len(copyOfConfig.Storage.VGs))
	normalized := copyOfConfig.Storage.VGs[:0]
	for _, vg := range copyOfConfig.Storage.VGs {
		if _, ok := seen[vg]; ok {
			continue
		}
		seen[vg] = struct{}{}
		normalized = append(normalized, vg)
	}
	copyOfConfig.Storage.VGs = normalized
	if classes != nil {
		copyOfConfig.Storage.Classes = classes.Snapshot()
	} else if input.Storage.Classes != nil {
		copyOfConfig.Storage.Classes = make(map[string]string, len(input.Storage.Classes))
		for class, vg := range input.Storage.Classes {
			copyOfConfig.Storage.Classes[class] = vg
		}
	}
	return &copyOfConfig
}

func configEquivalent(running, candidate *config.Config, classes agent.ClassRegistry) bool {
	if running == nil || candidate == nil {
		return false
	}
	left, err := yaml.Marshal(normalizeConfig(running, classes))
	if err != nil {
		return false
	}
	right, err := yaml.Marshal(normalizeConfig(candidate, classes))
	if err != nil {
		return false
	}
	return string(left) == string(right)
}

func bindingSocketChanged(running, candidate *config.Config) (bool, string) {
	if running == nil || candidate == nil {
		return false, ""
	}
	if running.Listen.Address != candidate.Listen.Address {
		return true, "listen_address_changed"
	}
	if running.AdminUI.Enabled != candidate.AdminUI.Enabled {
		return true, "admin_enabled_changed"
	}
	if (running.AdminUI.Enabled || candidate.AdminUI.Enabled) &&
		running.AdminUI.Address != candidate.AdminUI.Address {
		return true, "admin_address_changed"
	}
	if running.Targets.ISCSI.Enabled != candidate.Targets.ISCSI.Enabled {
		return true, "iscsi_enabled_changed"
	}
	if (running.Targets.ISCSI.Enabled || candidate.Targets.ISCSI.Enabled) &&
		!samePortalSet(running.Targets.ISCSI.Portals, candidate.Targets.ISCSI.Portals) {
		return true, "iscsi_portals_changed"
	}
	if running.Targets.NVMeTCP.Enabled != candidate.Targets.NVMeTCP.Enabled {
		return true, "nvme_tcp_enabled_changed"
	}
	if (running.Targets.NVMeTCP.Enabled || candidate.Targets.NVMeTCP.Enabled) &&
		!samePortalSet(running.Targets.NVMeTCP.Portals, candidate.Targets.NVMeTCP.Portals) {
		return true, "nvme_tcp_portals_changed"
	}
	return false, ""
}

func samePortalSet(left, right []config.Portal) bool {
	if len(left) != len(right) {
		return false
	}
	leftSet := make(map[string]int, len(left))
	for _, portal := range left {
		leftSet[fmt.Sprintf("%s:%d", portal.Address, portal.Port)]++
	}
	for _, portal := range right {
		key := fmt.Sprintf("%s:%d", portal.Address, portal.Port)
		leftSet[key]--
	}
	for _, count := range leftSet {
		if count != 0 {
			return false
		}
	}
	return true
}

func (server *Server) configState() (ConfigResponse, error) {
	file, err := readConfig(server.options.ConfigPath)
	if err != nil {
		return ConfigResponse{}, err
	}
	response := ConfigResponse{
		Path:          server.options.ConfigPath,
		BackupPath:    server.options.ConfigPath + ".bak",
		LastGoodPath:  server.options.ConfigPath + ".last-good",
		DiskYAML:      string(file.Raw),
		DiskETag:      file.ETag,
		DiskValid:     file.Valid,
		DiskError:     file.DiskError,
		DiskErrorCode: file.DiskErrorCode,
		LastGoodError: server.lastGoodErrorValue(),
		Warnings:      []WarningView{},
		TLS:           server.tlsInfo(),
	}
	running := normalizeConfig(server.options.Config, server.options.Classes)
	runningYAML, err := yaml.Marshal(running)
	if err != nil {
		return ConfigResponse{}, err
	}
	response.RunningYAML = string(runningYAML)
	if file.Valid && file.Parsed != nil {
		response.ConfigChanged = !configEquivalent(server.options.Config, file.Parsed, server.options.Classes)
		response.RestartRequired, response.RestartReason = bindingSocketChanged(server.options.Config, file.Parsed)
	} else {
		response.ConfigChanged = true
	}
	return response, nil
}

func checkPreconditions(file configFile, ifMatch, ifNoneMatch string) error {
	existing := file.DiskErrorCode != "missing"
	if existing {
		if ifMatch == "" {
			if ifNoneMatch != "" {
				return fmt.Errorf("%w: If-None-Match does not match an existing file", errConflict)
			}
			return fmt.Errorf("%w: If-Match is required", errPreconditionRequired)
		}
		if ifMatch != file.ETag {
			return fmt.Errorf("%w: configuration changed", errConflict)
		}
		return nil
	}
	if ifNoneMatch != "*" {
		if ifNoneMatch == "" {
			if ifMatch != "" {
				return fmt.Errorf("%w: If-Match does not match a missing file", errConflict)
			}
			return fmt.Errorf("%w: If-None-Match: * is required", errPreconditionRequired)
		}
		return fmt.Errorf("%w: invalid If-None-Match", errBadRequest)
	}
	return nil
}

func (server *Server) saveConfig(ctx context.Context, body []byte, ifMatch, ifNoneMatch, remote string) (SaveConfigResponse, error) {
	current, err := readConfig(server.options.ConfigPath)
	if err != nil {
		return SaveConfigResponse{}, err
	}
	if current.DiskErrorCode == "unsafe_path" {
		return SaveConfigResponse{}, errUnsafePath
	}
	if current.DiskErrorCode == "read_error" {
		return SaveConfigResponse{}, fmt.Errorf("%w: %s", errInternal, current.DiskError)
	}
	if err := checkPreconditions(current, ifMatch, ifNoneMatch); err != nil {
		return SaveConfigResponse{}, err
	}
	candidate, err := config.Parse(body)
	if err != nil {
		return SaveConfigResponse{}, fmt.Errorf("%w: %v", errInvalidConfig, err)
	}
	preflightCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	warnings := server.preflight(preflightCtx, candidate)
	cancel()

	lock, err := acquireFileLock(server.options.ConfigPath+".lock", 2*time.Second)
	if err != nil {
		return SaveConfigResponse{}, err
	}
	locked := true
	defer func() {
		if locked {
			releaseFileLock(lock)
		}
	}()
	rechecked, err := readConfig(server.options.ConfigPath)
	if err != nil {
		return SaveConfigResponse{}, err
	}
	if rechecked.ETag != current.ETag || rechecked.DiskErrorCode != current.DiskErrorCode {
		return SaveConfigResponse{}, fmt.Errorf("%w: configuration changed while saving", errConflict)
	}
	if current.DiskErrorCode != "missing" {
		if err := fsutil.WriteFileAtomic(server.options.ConfigPath+".bak", current.Raw, 0o600); err != nil {
			return SaveConfigResponse{}, fmt.Errorf("%w: write backup: %v", errInternal, err)
		}
	}
	if err := fsutil.WriteFileAtomic(server.options.ConfigPath, body, 0o600); err != nil {
		if errors.Is(err, fsutil.ErrDurabilityUncertain) {
			warnings = append(warnings, WarningView{Code: "durability_uncertain", Message: err.Error()})
		} else {
			return SaveConfigResponse{}, fmt.Errorf("%w: write configuration: %v", errInternal, err)
		}
	}
	releaseFileLock(lock)
	locked = false

	state, err := server.configState()
	if err != nil {
		return SaveConfigResponse{}, err
	}
	response := SaveConfigResponse{
		Saved: true, Path: server.options.ConfigPath,
		BackupPath: server.options.ConfigPath + ".bak",
		DiskETag:   state.DiskETag, ConfigChanged: state.ConfigChanged,
		RestartRequired: state.RestartRequired, RestartReason: state.RestartReason,
		Warnings: warnings,
	}
	auditErr := server.writeAudit(ctx, "config_save", "", "", "", "succeeded", "", remote)
	if auditErr != nil {
		response.Warnings = append(response.Warnings, WarningView{Code: "audit_write_failed", Message: auditErr.Error()})
	}
	return response, nil
}

func (server *Server) preflight(ctx context.Context, candidate *config.Config) []WarningView {
	running := server.options.Config
	warnings := make([]WarningView, 0)
	add := func(code, message string) {
		warnings = append(warnings, WarningView{Code: code, Message: message})
	}
	if running == nil || candidate == nil {
		return warnings
	}
	if running.SecretKeyFile != candidate.SecretKeyFile {
		add("secret_key_changed", "secret_key_file changed; existing credentials may become unreadable")
	}
	if running.HostIDFile != candidate.HostIDFile || running.DataDir != candidate.DataDir ||
		running.EmergencyLog != candidate.EmergencyLog || running.State != candidate.State {
		add("identity_or_state_path_changed", "host, data, emergency, or state paths changed; restart to apply")
	}
	if running.Listen.TLS != candidate.Listen.TLS {
		add("listen_tls_changed", "listen.tls changed; restart to load new TLS material")
	}
	if running.AdminUI.Enabled != candidate.AdminUI.Enabled {
		if !candidate.AdminUI.Enabled {
			add("admin_ui_disabled", "admin UI will disappear after the operator restarts the service")
		}
	}
	if running.AdminUI.Address != candidate.AdminUI.Address && (running.AdminUI.Enabled || candidate.AdminUI.Enabled) {
		add("admin_address_changed", "admin UI moves after the operator restarts the service")
	}
	if running.AdminUI.ClientCertDir != candidate.AdminUI.ClientCertDir {
		add("admin_client_cert_dir_changed", "client certificate directory changes apply after the operator restarts the service")
	}
	if running.Listen.Address != candidate.Listen.Address {
		if err := probeAddress(candidate.Listen.Address); err != nil {
			add("listen_address_unavailable", err.Error())
		}
	}
	if candidate.AdminUI.Enabled && running.AdminUI.Address != candidate.AdminUI.Address {
		if err := probeAddress(candidate.AdminUI.Address); err != nil {
			add("admin_address_unavailable", err.Error())
		}
	}
	for _, path := range []string{candidate.Listen.TLS.CertFile, candidate.Listen.TLS.KeyFile, candidate.Listen.TLS.ClientCAFile} {
		if _, err := os.Stat(path); err != nil {
			add("tls_file_missing", fmt.Sprintf("%s: %v", path, err))
		}
	}
	if server.options.VGs != nil {
		for _, vg := range candidate.EffectiveVGs() {
			if _, err := server.options.VGs.Inspect(ctx, vg); err != nil {
				add("vg_unavailable", fmt.Sprintf("%s: %v", vg, err))
			}
		}
	}
	if !reflect.DeepEqual(running.Targets.ISCSI.Portals, candidate.Targets.ISCSI.Portals) &&
		samePortalSet(running.Targets.ISCSI.Portals, candidate.Targets.ISCSI.Portals) {
		add("portal_order_changed", "target portal order changed; future exports after restart use the new first portal")
	}
	if !reflect.DeepEqual(running.Targets.ISCSI, candidate.Targets.ISCSI) ||
		!reflect.DeepEqual(running.Targets.NVMeTCP, candidate.Targets.NVMeTCP) {
		add("targets_changed", "target settings changed; review whether a manual restart is needed")
	}
	if !reflect.DeepEqual(running.Operations, candidate.Operations) ||
		!reflect.DeepEqual(running.Admission, candidate.Admission) ||
		running.Storage.HeadroomPercent != candidate.Storage.HeadroomPercent ||
		running.Storage.HeadroomMinBytes != candidate.Storage.HeadroomMinBytes ||
		running.State.ReconcileIntervalSeconds != candidate.State.ReconcileIntervalSeconds {
		add("startup_bound_changed", "operations, admission, headroom, or interval settings changed; a manual restart may be needed")
	}
	if !reflect.DeepEqual(running.Storage.VGs, candidate.Storage.VGs) {
		add("storage_vgs_changed", "storage.vgs is part of the first-start seed; use the live VG panel")
	}
	if !reflect.DeepEqual(running.Storage.Classes, candidate.Storage.Classes) {
		add("storage_classes_changed", "raw storage.classes changes apply on restart; use the live class panel for immediate effect")
	}
	return warnings
}

func probeAddress(address string) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("cannot bind %s: %w", address, err)
	}
	return listener.Close()
}

func acquireFileLock(path string, timeout time.Duration) (*os.File, error) {
	deadline := time.Now().Add(timeout)
	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // derived from the configuration path
		if err != nil {
			return nil, err
		}
		if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return file, nil
		} else if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = file.Close()
			return nil, err
		}
		_ = file.Close()
		if time.Now().After(deadline) {
			return nil, errLocked
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func releaseFileLock(file *os.File) {
	if file == nil {
		return
	}
	_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
	_ = file.Close()
}

func (server *Server) lastGoodErrorValue() string {
	server.stateMu.Lock()
	defer server.stateMu.Unlock()
	return server.lastGoodError
}
