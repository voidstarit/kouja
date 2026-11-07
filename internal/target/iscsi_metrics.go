package target

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
)

// ISCSIMetricsProvider exposes the live LIO counters used by the local admin
// UI. It is deliberately separate from Exporter: metrics are read-only and
// must never make an export operation fail.
type ISCSIMetricsProvider interface {
	Metrics(context.Context) (ISCSIMetrics, error)
}

var _ ISCSIMetricsProvider = (*ISCSI)(nil)

type ISCSIMetrics struct {
	Targets         []ISCSITargetMetrics
	Connections     []ISCSIClientConnection
	ConnectionError string
}

type ISCSITargetMetrics struct {
	Target          string
	Sessions        int64
	Nodes           int64
	Portals         int64
	Login           ISCSILoginMetrics
	Logout          ISCSILogoutMetrics
	SessionErrors   ISCSISessionErrorMetrics
	Initiators      []ISCSIInitiatorMetrics
	LUNs            []ISCSILUNMetrics
	ClientAddresses []string
	PortalPorts     []int
	Error           string
}

type ISCSIClientConnection struct {
	ClientAddress string
	ClientPort    int
	LocalAddress  string
	LocalPort     int
	Target        string
	Initiator     string
	Ambiguous     bool
}

type ISCSILoginMetrics struct {
	Accepts           int64
	AuthenticateFails int64
	AuthorizeFails    int64
	NegotiateFails    int64
	Redirects         int64
	OtherFails        int64
	TargetLoginFails  int64
}

type ISCSILogoutMetrics struct {
	Normal   int64
	Abnormal int64
}

type ISCSISessionErrorMetrics struct {
	FormatErrors     int64
	ConnectionErrors int64
	DigestErrors     int64
}

type ISCSIInitiatorMetrics struct {
	Initiator               string
	RXBytes                 int64
	TXBytes                 int64
	CommandPDUs             int64
	ResponsePDUs            int64
	ConnectionTimeoutErrors int64
	ConnectionDigestErrors  int64
	ClientAddresses         []string
}

type ISCSILUNMetrics struct {
	LUN                    int32
	ResourceUUID           string
	Commands               int64
	ReadBytes              int64
	WriteBytes             int64
	Resets                 int64
	AbortsComplete         int64
	AbortsNoTask           int64
	Status                 string
	StateBit               string
	BusyCount              int64
	InCommands             int64
	HighPriorityInCommands int64
	Error                  string
}

// Metrics reads the target, initiator, and LUN statistics that LIO exposes in
// configfs. Missing optional attributes are represented as zero; a target that
// disappears between ReadDir and the attribute reads is skipped. The caller
// can treat an error as a non-fatal metrics-unavailable condition.
func (adapter *ISCSI) Metrics(ctx context.Context) (ISCSIMetrics, error) {
	result := ISCSIMetrics{Targets: []ISCSITargetMetrics{}, Connections: []ISCSIClientConnection{}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	entries, err := adapter.fs.ReadDir("target/iscsi")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return result, nil
		}
		return result, fmt.Errorf("target: read iSCSI metrics: %w", err)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !entry.IsDir() || entry.Name() == "discovery_auth" || entry.Name() == "version" {
			continue
		}
		metrics, ok := adapter.targetMetrics(entry.Name())
		if ok {
			result.Targets = append(result.Targets, metrics)
		}
	}
	sort.Slice(result.Targets, func(left, right int) bool {
		return result.Targets[left].Target < result.Targets[right].Target
	})
	result.Connections, result.ConnectionError = adapter.clientConnections(result.Targets)
	return result, nil
}

func (adapter *ISCSI) targetMetrics(name string) (ISCSITargetMetrics, bool) {
	targetPath := path.Join("target/iscsi", name)
	tpgtPath := path.Join(targetPath, "tpgt_1")
	if !adapter.fs.Exists(tpgtPath) {
		return ISCSITargetMetrics{}, false
	}
	metrics := ISCSITargetMetrics{
		Target: name, Initiators: []ISCSIInitiatorMetrics{}, LUNs: []ISCSILUNMetrics{},
		ClientAddresses: []string{}, PortalPorts: []int{},
	}
	metrics.PortalPorts = adapter.portalPorts(tpgtPath)
	adapter.requiredMetric(path.Join(targetPath, "fabric_statistics/iscsi_instance/sessions"), &metrics.Sessions, &metrics.Error)
	adapter.requiredMetric(path.Join(targetPath, "fabric_statistics/iscsi_instance/nodes"), &metrics.Nodes, &metrics.Error)
	adapter.requiredMetric(path.Join(targetPath, "fabric_statistics/iscsi_instance/portals"), &metrics.Portals, &metrics.Error)

	metrics.Login = ISCSILoginMetrics{
		Accepts:           adapter.optionalMetric(path.Join(targetPath, "fabric_statistics/iscsi_login_stats/accepts")),
		AuthenticateFails: adapter.optionalMetric(path.Join(targetPath, "fabric_statistics/iscsi_login_stats/authenticate_fails")),
		AuthorizeFails:    adapter.optionalMetric(path.Join(targetPath, "fabric_statistics/iscsi_login_stats/authorize_fails")),
		NegotiateFails:    adapter.optionalMetric(path.Join(targetPath, "fabric_statistics/iscsi_login_stats/negotiate_fails")),
		Redirects:         adapter.optionalMetric(path.Join(targetPath, "fabric_statistics/iscsi_login_stats/redirects")),
		OtherFails:        adapter.optionalMetric(path.Join(targetPath, "fabric_statistics/iscsi_login_stats/other_fails")),
		TargetLoginFails:  adapter.optionalMetric(path.Join(targetPath, "fabric_statistics/iscsi_tgt_attr/login_fails")),
	}
	metrics.Logout = ISCSILogoutMetrics{
		Normal:   adapter.optionalMetric(path.Join(targetPath, "fabric_statistics/iscsi_logout_stats/normal_logouts")),
		Abnormal: adapter.optionalMetric(path.Join(targetPath, "fabric_statistics/iscsi_logout_stats/abnormal_logouts")),
	}
	metrics.SessionErrors = ISCSISessionErrorMetrics{
		FormatErrors:     adapter.optionalMetric(path.Join(targetPath, "fabric_statistics/iscsi_sess_err/format_errors")),
		ConnectionErrors: adapter.optionalMetric(path.Join(targetPath, "fabric_statistics/iscsi_sess_err/cxn_errors")),
		DigestErrors:     adapter.optionalMetric(path.Join(targetPath, "fabric_statistics/iscsi_sess_err/digest_errors")),
	}

	if entries, err := adapter.fs.ReadDir(path.Join(tpgtPath, "acls")); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			appendMetricError(&metrics.Error, err)
		}
	} else {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			aclPath := path.Join(tpgtPath, "acls", entry.Name())
			initiator := adapter.initiatorMetrics(entry.Name(), aclPath)
			initiator.ClientAddresses = adapter.clientAddresses(aclPath)
			metrics.Initiators = append(metrics.Initiators, initiator)
			metrics.ClientAddresses = appendUniqueStrings(metrics.ClientAddresses, initiator.ClientAddresses...)
		}
		sort.Strings(metrics.ClientAddresses)
		sort.Slice(metrics.Initiators, func(left, right int) bool {
			return metrics.Initiators[left].Initiator < metrics.Initiators[right].Initiator
		})
	}

	if entries, err := adapter.fs.ReadDir(path.Join(tpgtPath, "lun")); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			appendMetricError(&metrics.Error, err)
		}
	} else {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			index, err := parseLUNIndex(entry.Name())
			if err != nil {
				continue
			}
			metrics.LUNs = append(metrics.LUNs,
				adapter.lunMetrics(index, path.Join(tpgtPath, "lun", entry.Name()))...)
		}
		sort.Slice(metrics.LUNs, func(left, right int) bool {
			if metrics.LUNs[left].LUN == metrics.LUNs[right].LUN {
				return metrics.LUNs[left].ResourceUUID < metrics.LUNs[right].ResourceUUID
			}
			return metrics.LUNs[left].LUN < metrics.LUNs[right].LUN
		})
	}
	return metrics, true
}

func (adapter *ISCSI) initiatorMetrics(initiator, aclPath string) ISCSIInitiatorMetrics {
	stats := path.Join(aclPath, "fabric_statistics/iscsi_sess_stats")
	return ISCSIInitiatorMetrics{
		Initiator:               initiator,
		RXBytes:                 adapter.optionalMetric(path.Join(stats, "rxdata_octs")),
		TXBytes:                 adapter.optionalMetric(path.Join(stats, "txdata_octs")),
		CommandPDUs:             adapter.optionalMetric(path.Join(stats, "cmd_pdus")),
		ResponsePDUs:            adapter.optionalMetric(path.Join(stats, "rsp_pdus")),
		ConnectionTimeoutErrors: adapter.optionalMetric(path.Join(stats, "conn_timeout_errors")),
		ConnectionDigestErrors:  adapter.optionalMetric(path.Join(stats, "conn_digest_errors")),
	}
}

func (adapter *ISCSI) lunMetrics(index int32, lunPath string) []ISCSILUNMetrics {
	entries, err := adapter.fs.ReadDir(lunPath)
	if err != nil {
		return nil
	}
	result := make([]ISCSILUNMetrics, 0, len(entries))
	for _, entry := range entries {
		if !isLUNResource(entry) {
			continue
		}
		metrics := ISCSILUNMetrics{
			LUN: index, ResourceUUID: entry.Name(),
			Status: "unknown", StateBit: "unknown",
		}
		statsPath := path.Join(lunPath, entry.Name(), "statistics")
		adapter.requiredMetric(path.Join(statsPath, "scsi_lu/num_cmds"), &metrics.Commands, &metrics.Error)
		readMB := adapter.optionalMetric(path.Join(statsPath, "scsi_lu/read_mbytes"))
		writeMB := adapter.optionalMetric(path.Join(statsPath, "scsi_lu/write_mbytes"))
		metrics.ReadBytes = mbytes(readMB)
		metrics.WriteBytes = mbytes(writeMB)
		metrics.Resets = adapter.optionalMetric(path.Join(statsPath, "scsi_lu/resets"))
		metrics.AbortsComplete = adapter.optionalMetric(path.Join(statsPath, "scsi_tgt_dev/aborts_complete"))
		metrics.AbortsNoTask = adapter.optionalMetric(path.Join(statsPath, "scsi_tgt_dev/aborts_no_task"))
		metrics.Resets += adapter.optionalMetric(path.Join(statsPath, "scsi_tgt_dev/resets"))
		metrics.Status = adapter.optionalString(path.Join(statsPath, "scsi_lu/status"), "unknown")
		metrics.StateBit = adapter.optionalString(path.Join(statsPath, "scsi_lu/state_bit"), "unknown")

		portStats := path.Join(lunPath, "statistics/scsi_tgt_port")
		metrics.BusyCount = adapter.optionalMetric(path.Join(portStats, "busy_count"))
		metrics.InCommands = adapter.optionalMetric(path.Join(portStats, "in_cmds"))
		metrics.HighPriorityInCommands = adapter.optionalMetric(path.Join(portStats, "hs_in_cmds"))
		result = append(result, metrics)
	}
	return result
}

func (adapter *ISCSI) requiredMetric(rel string, target *int64, metricError *string) {
	value, err := adapter.statInt(rel)
	if err != nil {
		appendMetricError(metricError, err)
		return
	}
	*target = value
}

func (adapter *ISCSI) optionalMetric(rel string) int64 {
	value, err := adapter.statInt(rel)
	if err != nil {
		return 0
	}
	return value
}

func (adapter *ISCSI) optionalString(rel, fallback string) string {
	value, err := adapter.fs.ReadFile(rel)
	if err != nil {
		return fallback
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func (adapter *ISCSI) statInt(rel string) (int64, error) {
	value, err := adapter.fs.ReadFile(rel)
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("target: parse configfs stat %q: %w", rel, err)
	}
	return parsed, nil
}

func appendMetricError(target *string, err error) {
	if *target != "" {
		*target += "; "
	}
	*target += err.Error()
}

func parseLUNIndex(name string) (int32, error) {
	if !strings.HasPrefix(name, "lun_") {
		return 0, fmt.Errorf("target: invalid LUN directory %q", name)
	}
	value, err := strconv.ParseInt(strings.TrimPrefix(name, "lun_"), 10, 32)
	if err != nil {
		return 0, fmt.Errorf("target: invalid LUN directory %q: %w", name, err)
	}
	return int32(value), nil
}

func isLUNResource(entry os.DirEntry) bool {
	name := entry.Name()
	if entry.IsDir() || entry.Type().IsRegular() || strings.HasPrefix(name, ".") {
		return false
	}
	return true
}

func mbytes(value int64) int64 {
	if value <= 0 {
		return 0
	}
	return value * 1024 * 1024
}

func (adapter *ISCSI) portalPorts(tpgtPath string) []int {
	entries, err := adapter.fs.ReadDir(path.Join(tpgtPath, "np"))
	if err != nil {
		return []int{}
	}
	ports := make([]int, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		_, portText, err := net.SplitHostPort(entry.Name())
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		ports = appendUniqueInts(ports, port)
	}
	sort.Ints(ports)
	return ports
}

func (adapter *ISCSI) clientAddresses(aclPath string) []string {
	data, err := adapter.fs.ReadFile(path.Join(aclPath, "info"))
	if err != nil {
		return []string{}
	}
	addresses := []string{}
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "Address" {
			continue
		}
		address := strings.TrimSuffix(fields[1], ",")
		if net.ParseIP(address) != nil {
			addresses = appendUniqueStrings(addresses, address)
		}
	}
	sort.Strings(addresses)
	return addresses
}

type tcpConnection struct {
	localAddress  string
	localPort     int
	clientAddress string
	clientPort    int
}

func (adapter *ISCSI) clientConnections(targets []ISCSITargetMetrics) ([]ISCSIClientConnection, string) {
	ports := make(map[int]struct{})
	for _, target := range targets {
		for _, port := range target.PortalPorts {
			ports[port] = struct{}{}
		}
	}
	if len(ports) == 0 {
		return []ISCSIClientConnection{}, ""
	}
	connections, err := adapter.readProcNetConnections(ports)
	if err != nil {
		return []ISCSIClientConnection{}, err.Error()
	}
	result := make([]ISCSIClientConnection, 0, len(connections))
	for _, connection := range connections {
		view := ISCSIClientConnection{
			ClientAddress: connection.clientAddress,
			ClientPort:    connection.clientPort,
			LocalAddress:  connection.localAddress,
			LocalPort:     connection.localPort,
		}
		type match struct {
			target    string
			initiator string
		}
		matches := []match{}
		for _, target := range targets {
			if !containsInt(target.PortalPorts, connection.localPort) {
				continue
			}
			for _, initiator := range target.Initiators {
				if containsString(initiator.ClientAddresses, connection.clientAddress) {
					matches = append(matches, match{target: target.Target, initiator: initiator.Initiator})
				}
			}
		}
		switch len(matches) {
		case 1:
			view.Target = matches[0].target
			view.Initiator = matches[0].initiator
		case 0:
			// ACL info can be unavailable briefly while a session is being
			// established. If exactly one target owns this portal port, it is
			// still unambiguous.
			targetsOnPort := []ISCSITargetMetrics{}
			for _, target := range targets {
				if containsInt(target.PortalPorts, connection.localPort) {
					targetsOnPort = append(targetsOnPort, target)
				}
			}
			if len(targetsOnPort) == 1 {
				view.Target = targetsOnPort[0].Target
				if len(targetsOnPort[0].Initiators) == 1 {
					view.Initiator = targetsOnPort[0].Initiators[0].Initiator
				}
			}
		default:
			view.Ambiguous = true
		}
		result = append(result, view)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].ClientAddress == result[right].ClientAddress {
			if result[left].ClientPort == result[right].ClientPort {
				return result[left].LocalPort < result[right].LocalPort
			}
			return result[left].ClientPort < result[right].ClientPort
		}
		return result[left].ClientAddress < result[right].ClientAddress
	})
	return result, ""
}

func (adapter *ISCSI) readProcNetConnections(ports map[int]struct{}) ([]tcpConnection, error) {
	directory := adapter.procNetDir
	if directory == "" {
		directory = "/proc/net"
	}
	result := []tcpConnection{}
	var lastErr error
	for _, name := range []string{"tcp", "tcp6"} {
		data, err := os.ReadFile(path.Join(directory, name)) //nolint:gosec // fixed tcp/tcp6 names under the configured proc directory
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				lastErr = err
			}
			continue
		}
		connections, err := parseProcNetTCP(data, ports)
		if err != nil {
			lastErr = err
			continue
		}
		result = append(result, connections...)
	}
	if lastErr != nil {
		return result, lastErr
	}
	return result, nil
}

func parseProcNetTCP(data []byte, ports map[int]struct{}) ([]tcpConnection, error) {
	result := []tcpConnection{}
	for index, line := range strings.Split(string(data), "\n") {
		if index == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[3] != "01" {
			continue
		}
		localAddress, localPort, err := parseProcEndpoint(fields[1])
		if err != nil {
			return nil, err
		}
		clientAddress, clientPort, err := parseProcEndpoint(fields[2])
		if err != nil {
			return nil, err
		}
		if _, wanted := ports[localPort]; !wanted || clientPort == 0 {
			continue
		}
		result = append(result, tcpConnection{
			localAddress: localAddress, localPort: localPort,
			clientAddress: clientAddress, clientPort: clientPort,
		})
	}
	return result, nil
}

func parseProcEndpoint(value string) (string, int, error) {
	colon := strings.LastIndexByte(value, ':')
	if colon <= 0 || colon == len(value)-1 {
		return "", 0, fmt.Errorf("target: invalid proc TCP endpoint %q", value)
	}
	address, err := parseProcAddress(value[:colon])
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.ParseUint(value[colon+1:], 16, 16)
	if err != nil {
		return "", 0, fmt.Errorf("target: invalid proc TCP port %q: %w", value[colon+1:], err)
	}
	return address, int(port), nil
}

func parseProcAddress(value string) (string, error) {
	switch len(value) {
	case 8:
		raw, err := strconv.ParseUint(value, 16, 32)
		if err != nil {
			return "", fmt.Errorf("target: invalid proc IPv4 address %q: %w", value, err)
		}
		octets := []byte{byte(raw), byte(raw >> 8), byte(raw >> 16), byte(raw >> 24)} //nolint:gosec // parsed with bitSize 32
		return net.IPv4(octets[0], octets[1], octets[2], octets[3]).String(), nil
	case 32:
		raw := make([]byte, 0, net.IPv6len)
		for offset := 0; offset < len(value); offset += 8 {
			word, err := strconv.ParseUint(value[offset:offset+8], 16, 32)
			if err != nil {
				return "", fmt.Errorf("target: invalid proc IPv6 address %q: %w", value, err)
			}
			raw = append(raw, byte(word), byte(word>>8), byte(word>>16), byte(word>>24)) //nolint:gosec // parsed with bitSize 32
		}
		ip := net.IP(raw)
		if ipv4 := ip.To4(); ipv4 != nil {
			return ipv4.String(), nil
		}
		return ip.String(), nil
	default:
		return "", fmt.Errorf("target: invalid proc TCP address %q", value)
	}
}

func appendUniqueStrings(values []string, additions ...string) []string {
	for _, addition := range additions {
		found := false
		for _, value := range values {
			if value == addition {
				found = true
				break
			}
		}
		if !found {
			values = append(values, addition)
		}
	}
	return values
}

func appendUniqueInts(values []int, additions ...int) []int {
	for _, addition := range additions {
		found := false
		for _, value := range values {
			if value == addition {
				found = true
				break
			}
		}
		if !found {
			values = append(values, addition)
		}
	}
	return values
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsInt(values []int, wanted int) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
