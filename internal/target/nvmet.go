package target

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"
)

type NVMe struct {
	fs     FileSystem
	portID string
}

var _ Exporter = (*NVMe)(nil)

func NewNVMe(fs FileSystem) *NVMe {
	if fs == nil {
		fs = NewConfigFS("")
	}
	return &NVMe{fs: fs, portID: "1"}
}

func (adapter *NVMe) Export(ctx context.Context, spec ExportSpec, _ Credential) (ExportResult, error) {
	if err := adapter.validate(spec); err != nil {
		return ExportResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ExportResult{}, err
	}
	subsystem := adapter.subsystemPath(spec)
	if err := adapter.fs.Mkdir(subsystem); err != nil {
		return ExportResult{}, err
	}
	if err := adapter.fs.Write(subsystem+"/attr_allow_any_host", "0"); err != nil {
		return ExportResult{}, err
	}
	serial := spec.ResourceUUID
	if len(serial) > 20 {
		serial = serial[:20]
	}
	if err := adapter.fs.Write(subsystem+"/attr_serial", serial); err != nil {
		return ExportResult{}, err
	}
	if err := adapter.fs.Write(subsystem+"/attr_model", "kouja"); err != nil {
		return ExportResult{}, err
	}
	hostPath := adapter.hostPath(spec)
	if err := adapter.fs.Mkdir(hostPath); err != nil {
		return ExportResult{}, err
	}
	if err := adapter.fs.Mkdir(subsystem + "/allowed_hosts"); err != nil {
		return ExportResult{}, err
	}
	if err := adapter.fs.Symlink(hostPath, subsystem+"/allowed_hosts/"+spec.Initiator); err != nil {
		return ExportResult{}, err
	}
	namespace := subsystem + "/namespaces/1"
	if err := adapter.fs.Mkdir(namespace); err != nil {
		return ExportResult{}, err
	}
	if err := adapter.fs.Write(namespace+"/device_path", "/dev/"+spec.VGName+"/"+spec.LVName); err != nil {
		return ExportResult{}, err
	}
	if err := adapter.fs.Write(namespace+"/enable", "1"); err != nil {
		return ExportResult{}, err
	}
	if err := adapter.ensurePort(spec); err != nil {
		return ExportResult{}, err
	}
	port := adapter.portPath()
	if err := adapter.fs.Mkdir(port + "/subsystems"); err != nil {
		return ExportResult{}, err
	}
	if err := adapter.fs.Symlink(subsystem, port+"/subsystems/"+spec.TargetName); err != nil {
		return ExportResult{}, err
	}
	return ExportResult{TargetName: spec.TargetName, Portal: spec.Portal, LUN: 0, NSID: 1}, nil
}

func (adapter *NVMe) Unexport(ctx context.Context, spec ExportSpec, force bool) error {
	if err := adapter.validate(spec); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	observation, err := adapter.Observe(ctx, spec)
	if err != nil {
		return err
	}
	if !observation.TargetPresent {
		return nil
	}
	if observation.SessionState == SessionUnknown && !force {
		return ErrSessionUnknown
	}
	return adapter.remove(spec)
}

func (adapter *NVMe) Delete(ctx context.Context, spec ExportSpec, force bool) error {
	return adapter.Unexport(ctx, spec, force)
}

func (adapter *NVMe) Resize(ctx context.Context, spec ExportSpec) error {
	if err := adapter.validate(spec); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	namespace := adapter.subsystemPath(spec) + "/namespaces/1"
	if !adapter.fs.Exists(namespace) {
		return nil
	}
	if err := adapter.fs.Write(namespace+"/enable", "0"); err != nil {
		return err
	}
	if err := adapter.fs.Write(namespace+"/device_path", "/dev/"+spec.VGName+"/"+spec.LVName); err != nil {
		return err
	}
	return adapter.fs.Write(namespace+"/enable", "1")
}

func (adapter *NVMe) Observe(ctx context.Context, spec ExportSpec) (ExportObservation, error) {
	if err := adapter.validate(spec); err != nil {
		return ExportObservation{}, err
	}
	if err := ctx.Err(); err != nil {
		return ExportObservation{}, err
	}
	subsystem := adapter.subsystemPath(spec)
	observation := ExportObservation{
		TargetPresent: adapter.fs.Exists(subsystem),
		SessionState:  SessionUnknown,
		ObservedAt:    time.Now().UTC(),
	}
	observation.NamespacePresent = adapter.fs.Exists(subsystem + "/namespaces/1/enable")
	observation.ACLPresent = adapter.fs.Exists(subsystem + "/allowed_hosts/" + spec.Initiator)
	if observation.ACLPresent {
		observation.ObservedInitiator = spec.Initiator
	}
	return observation, nil
}

func (adapter *NVMe) ListSessions(context.Context) ([]Session, error) {
	return nil, nil
}

func (adapter *NVMe) Restore(ctx context.Context, spec ExportSpec, credential Credential) error {
	_, err := adapter.Export(ctx, spec, credential)
	return err
}

func (adapter *NVMe) ensurePort(spec ExportSpec) error {
	port := adapter.portPath()
	if adapter.fs.Exists(port) {
		return nil
	}
	host, portNumber, err := splitPortal(spec.Portal)
	if err != nil {
		return err
	}
	if err := adapter.fs.Mkdir(port); err != nil {
		return err
	}
	for _, attribute := range []struct {
		path  string
		value string
	}{
		{port + "/addr_trtype", "tcp"},
		{port + "/addr_tsas", "tls1.3"},
		{port + "/addr_treq", "required"},
		{port + "/addr_adrfam", "ipv4"},
		{port + "/addr_traddr", host},
		{port + "/addr_trsvcid", portNumber},
	} {
		if err := adapter.fs.Write(attribute.path, attribute.value); err != nil {
			return err
		}
	}
	return nil
}

func (adapter *NVMe) remove(spec ExportSpec) error {
	subsystem := adapter.subsystemPath(spec)
	paths := []string{
		adapter.portPath() + "/subsystems/" + spec.TargetName,
		subsystem + "/allowed_hosts/" + spec.Initiator,
		subsystem + "/namespaces/1",
		subsystem,
	}
	for _, path := range paths {
		if err := adapter.fs.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

func (adapter *NVMe) validate(spec ExportSpec) error {
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
	if _, _, err := splitPortal(spec.Portal); err != nil {
		return err
	}
	return nil
}

func (adapter *NVMe) subsystemPath(spec ExportSpec) string {
	return "nvmet/subsystems/" + spec.TargetName
}

func (adapter *NVMe) hostPath(spec ExportSpec) string {
	return "nvmet/hosts/" + spec.Initiator
}

func (adapter *NVMe) portPath() string {
	return "nvmet/ports/" + adapter.portID
}

func splitPortal(portal string) (string, string, error) {
	host, portText, err := net.SplitHostPort(portal)
	if err != nil {
		return "", "", fmt.Errorf("%w: invalid portal %q: %v", ErrInvalid, portal, err)
	}
	if net.ParseIP(host) == nil || net.ParseIP(host).To4() == nil {
		return "", "", fmt.Errorf("%w: portal address must be IPv4: %q", ErrInvalid, host)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return "", "", fmt.Errorf("%w: invalid portal port %q", ErrInvalid, portText)
	}
	return host, strconv.Itoa(port), nil
}
