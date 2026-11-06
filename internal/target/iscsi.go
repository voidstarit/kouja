package target

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ISCSI implements the target.Exporter interface directly against LIO
// configfs. One target and TPG are used per volume.
type ISCSI struct {
	fs FileSystem
}

var _ Exporter = (*ISCSI)(nil)

func NewISCSI(fs FileSystem) *ISCSI {
	if fs == nil {
		fs = NewConfigFS("")
	}
	return &ISCSI{fs: fs}
}

func (adapter *ISCSI) Export(ctx context.Context, spec ExportSpec, credential Credential) (ExportResult, error) {
	if err := adapter.validate(spec, credential); err != nil {
		return ExportResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ExportResult{}, err
	}

	backstore := adapter.backstorePath(spec)
	if !adapter.fs.Exists(backstore) {
		if err := adapter.fs.Mkdir(adapter.hbaPath(spec)); err != nil {
			return ExportResult{}, err
		}
		if err := adapter.fs.Mkdir(backstore); err != nil {
			return ExportResult{}, err
		}
		if err := adapter.fs.Write(backstore+"/control", "udev_path="+adapter.devicePath(spec)+"\n"); err != nil {
			return ExportResult{}, err
		}
		if err := adapter.fs.Write(backstore+"/enable", "1"); err != nil {
			return ExportResult{}, err
		}
	} else if err := adapter.verifyBackstore(spec); err != nil {
		return ExportResult{}, err
	}

	targetPath := adapter.targetPath(spec)
	tpgPath := targetPath + "/tpgt_1"
	if !adapter.fs.Exists(tpgPath) {
		if err := adapter.fs.Mkdir(tpgPath); err != nil {
			return ExportResult{}, err
		}
	}
	if err := adapter.fs.Mkdir(tpgPath + "/attrib"); err != nil {
		return ExportResult{}, err
	}
	if !adapter.fs.Exists(tpgPath + "/attrib/authentication") {
		if err := adapter.fs.Write(tpgPath+"/attrib/authentication", "0"); err != nil {
			return ExportResult{}, err
		}
	}

	lunGroup := tpgPath + "/lun/lun_0"
	if err := adapter.fs.Mkdir(lunGroup); err != nil {
		return ExportResult{}, err
	}
	if err := adapter.fs.Symlink(backstore, lunGroup+"/"+spec.ResourceUUID); err != nil {
		return ExportResult{}, err
	}

	aclPath := tpgPath + "/acls/" + spec.Initiator
	if err := adapter.fs.Mkdir(aclPath); err != nil {
		return ExportResult{}, err
	}
	if err := adapter.fs.Mkdir(aclPath + "/auth"); err != nil {
		return ExportResult{}, err
	}
	aclLunGroup := aclPath + "/lun_0"
	if err := adapter.fs.Mkdir(aclLunGroup); err != nil {
		return ExportResult{}, err
	}
	if err := adapter.fs.Symlink(lunGroup, aclLunGroup+"/"+spec.ResourceUUID); err != nil {
		return ExportResult{}, err
	}
	if err := adapter.fs.Write(aclLunGroup+"/write_protect", "0"); err != nil {
		return ExportResult{}, err
	}
	if err := adapter.fs.Write(aclPath+"/auth/userid", credential.Username); err != nil {
		return ExportResult{}, err
	}
	if err := adapter.fs.Write(aclPath+"/auth/password", credential.Secret); err != nil {
		return ExportResult{}, err
	}
	if err := adapter.fs.Write(tpgPath+"/attrib/authentication", "1"); err != nil {
		return ExportResult{}, err
	}

	portalName, err := portalDirectory(spec.Portal)
	if err != nil {
		return ExportResult{}, err
	}
	if err := adapter.fs.Mkdir(tpgPath + "/np/" + portalName); err != nil {
		return ExportResult{}, err
	}
	if err := adapter.fs.Write(tpgPath+"/enable", "1"); err != nil {
		return ExportResult{}, err
	}
	return ExportResult{
		TargetName: spec.TargetName,
		Portal:     spec.Portal,
		LUN:        0,
		NSID:       0,
		Credential: credential,
	}, nil
}

func (adapter *ISCSI) Unexport(ctx context.Context, spec ExportSpec, force bool) error {
	if err := adapter.validateSpec(spec); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	observation, err := adapter.Observe(ctx, spec)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		return err
	}
	if !observation.TargetPresent {
		return nil
	}
	switch observation.SessionState {
	case SessionActive:
		if !force {
			return ErrSessionActive
		}
	case SessionUnknown:
		if !force {
			return ErrSessionUnknown
		}
	}
	_ = adapter.fs.Write(adapter.targetPath(spec)+"/tpgt_1/enable", "0")
	return adapter.removeTarget(spec)
}

func (adapter *ISCSI) Resize(ctx context.Context, spec ExportSpec) error {
	if err := adapter.validateSpec(spec); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// LIO's iblock backstore re-reads the block-device capacity when the
	// initiator rescans the session; no configfs mutation is required. The
	// node plugin performs the session rescan and filesystem growth.
	return nil
}

func (adapter *ISCSI) Delete(ctx context.Context, spec ExportSpec, force bool) error {
	if err := adapter.validateSpec(spec); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := adapter.Unexport(ctx, spec, force); err != nil {
		return err
	}
	backstore := adapter.backstorePath(spec)
	if adapter.fs.Exists(backstore) {
		_ = adapter.fs.Write(backstore+"/enable", "0")
		if err := adapter.fs.Remove(backstore); err != nil {
			return err
		}
	}
	return nil
}

func (adapter *ISCSI) Observe(ctx context.Context, spec ExportSpec) (ExportObservation, error) {
	if err := adapter.validateSpec(spec); err != nil {
		return ExportObservation{}, err
	}
	if err := ctx.Err(); err != nil {
		return ExportObservation{}, err
	}
	observation := ExportObservation{
		ObservedAt:   time.Now().UTC(),
		SessionState: SessionNone,
	}
	targetPath := adapter.targetPath(spec)
	observation.TargetPresent = adapter.fs.Exists(targetPath)
	if !observation.TargetPresent {
		return observation, nil
	}
	tpgPath := targetPath + "/tpgt_1"
	aclPath := tpgPath + "/acls/" + spec.Initiator
	observation.ACLPresent = adapter.fs.Exists(aclPath)
	if observation.ACLPresent {
		observation.ObservedInitiator = spec.Initiator
	}

	sessionData, err := adapter.fs.ReadFile(targetPath + "/fabric_statistics/iscsi_instance/sessions")
	if err != nil {
		observation.SessionState = SessionUnknown
		return observation, nil
	}
	sessions, err := strconv.Atoi(strings.TrimSpace(sessionData))
	if err != nil {
		observation.SessionState = SessionUnknown
		return observation, nil
	}
	if sessions < 0 || sessions > math.MaxInt32 {
		observation.SessionState = SessionUnknown
		return observation, nil
	}
	observation.ActiveSessions = int32(sessions) //nolint:gosec // range checked immediately above
	if sessions > 0 {
		observation.SessionState = SessionActive
	}
	return observation, nil
}

func (adapter *ISCSI) ListSessions(ctx context.Context) ([]Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := adapter.fs.ReadDir("target/iscsi")
	if err != nil {
		return nil, err
	}
	sessions := make([]Session, 0)
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "discovery_auth" || entry.Name() == "version" {
			continue
		}
		targetName := entry.Name()
		state := SessionUnknown
		data, err := adapter.fs.ReadFile(filepath.ToSlash(filepath.Join("target/iscsi", targetName, "fabric_statistics/iscsi_instance/sessions")))
		if err == nil {
			count, parseErr := strconv.Atoi(strings.TrimSpace(data))
			switch {
			case parseErr != nil:
				state = SessionUnknown
			case count > 0:
				state = SessionActive
			default:
				state = SessionNone
			}
		}
		sessions = append(sessions, Session{Target: targetName, State: state})
	}
	return sessions, nil
}

func (adapter *ISCSI) Restore(ctx context.Context, spec ExportSpec, credential Credential) error {
	_, err := adapter.Export(ctx, spec, credential)
	return err
}

func (adapter *ISCSI) removeTarget(spec ExportSpec) error {
	targetPath := adapter.targetPath(spec)
	tpgPath := targetPath + "/tpgt_1"
	portalName, err := portalDirectory(spec.Portal)
	if err != nil {
		return err
	}
	paths := []string{
		tpgPath + "/np/" + portalName,
		tpgPath + "/acls/" + spec.Initiator + "/lun_0/" + spec.ResourceUUID,
		tpgPath + "/acls/" + spec.Initiator + "/lun_0",
		tpgPath + "/acls/" + spec.Initiator,
		tpgPath + "/lun/lun_0/" + spec.ResourceUUID,
		tpgPath + "/lun/lun_0",
		tpgPath,
		targetPath,
	}
	for _, rel := range paths {
		if err := adapter.fs.Remove(rel); err != nil {
			return err
		}
	}
	return nil
}

func (adapter *ISCSI) validate(spec ExportSpec, credential Credential) error {
	if err := adapter.validateSpec(spec); err != nil {
		return err
	}
	if credential.Username == "" || credential.Secret == "" {
		return fmt.Errorf("%w: CHAP username and secret are required", ErrInvalid)
	}
	return nil
}

func (adapter *ISCSI) validateSpec(spec ExportSpec) error {
	for kind, value := range map[string]string{
		"resource_uuid": spec.ResourceUUID,
		"vg_name":       spec.VGName,
		"lv_name":       spec.LVName,
		"target_name":   spec.TargetName,
		"initiator":     spec.Initiator,
	} {
		if err := validateComponent(kind, value); err != nil {
			return err
		}
	}
	if _, err := portalDirectory(spec.Portal); err != nil {
		return err
	}
	if spec.LUN != 0 {
		return fmt.Errorf("%w: only LUN 0 is supported", ErrInvalid)
	}
	return nil
}

func validateComponent(kind, value string) error {
	if value == "" || value == "." || value == ".." || strings.ContainsAny(value, `/\`) || strings.ContainsRune(value, 0) {
		return fmt.Errorf("%w: invalid %s %q", ErrInvalid, kind, value)
	}
	return nil
}

func (adapter *ISCSI) hbaPath(spec ExportSpec) string {
	// LIO reserves the HBA group name format; targetcli uses iblock_0 and
	// shares it across backstores. One HBA per resource is not accepted by the
	// kernel for UUID-shaped names.
	return "target/core/iblock_0"
}

func (adapter *ISCSI) backstorePath(spec ExportSpec) string {
	return adapter.hbaPath(spec) + "/" + spec.LVName
}

func (adapter *ISCSI) devicePath(spec ExportSpec) string {
	return "/dev/" + spec.VGName + "/" + spec.LVName
}

func (adapter *ISCSI) targetPath(spec ExportSpec) string {
	return "target/iscsi/" + spec.TargetName
}

func (adapter *ISCSI) verifyBackstore(spec ExportSpec) error {
	path := adapter.backstorePath(spec) + "/udev_path"
	actual, err := adapter.fs.ReadFile(path)
	if err != nil {
		return nil
	}
	actual = strings.TrimSpace(actual)
	if actual != "" && filepath.Clean(actual) != filepath.Clean(adapter.devicePath(spec)) {
		return fmt.Errorf("%w: backstore path is %q, want %q", ErrConflict, actual, adapter.devicePath(spec))
	}
	return nil
}

func portalDirectory(portal string) (string, error) {
	host, portText, err := net.SplitHostPort(portal)
	if err != nil {
		return "", fmt.Errorf("%w: invalid portal %q: %v", ErrInvalid, portal, err)
	}
	if net.ParseIP(host) == nil || net.ParseIP(host).To4() == nil {
		return "", fmt.Errorf("%w: portal address must be IPv4: %q", ErrInvalid, host)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("%w: invalid portal port %q", ErrInvalid, portText)
	}
	return host + ":" + strconv.Itoa(port), nil
}
