package adminui

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/voidstarit/kouja/internal/agent"
	"github.com/voidstarit/kouja/internal/config"
	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/operations"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
	"gopkg.in/yaml.v3"
)

type testTargetMetricsProvider struct {
	metrics target.ISCSIMetrics
	err     error
}

func (provider *testTargetMetricsProvider) Metrics(context.Context) (target.ISCSIMetrics, error) {
	return provider.metrics, provider.err
}

type testTargetDisabler struct {
	calls       int
	enableCalls int
	owner       string
	volumeID    string
	err         error
	enableErr   error
}

func (disabler *testTargetDisabler) DisableTarget(_ context.Context, owner, volumeID string) error {
	disabler.calls++
	disabler.owner = owner
	disabler.volumeID = volumeID
	return disabler.err
}

func (disabler *testTargetDisabler) EnableTarget(_ context.Context, owner, volumeID string) error {
	disabler.enableCalls++
	disabler.owner = owner
	disabler.volumeID = volumeID
	return disabler.enableErr
}

type testVGService struct {
	vgs      []state.VG
	capacity lvm.Capacity
}

func (service *testVGService) Register(_ context.Context, name, _ string, _ bool) (lvm.Capacity, error) {
	service.capacity.VGName = name
	service.vgs = append(service.vgs, state.VG{Name: name, UUID: service.capacity.VGUUID, State: "active", RegisteredAt: time.Now(), UpdatedAt: time.Now()})
	return service.capacity, nil
}
func (service *testVGService) Deregister(_ context.Context, name string) error {
	for index, vg := range service.vgs {
		if vg.Name == name {
			service.vgs = append(service.vgs[:index], service.vgs[index+1:]...)
			return nil
		}
	}
	return agent.ErrVGNotFound
}
func (service *testVGService) List(context.Context) ([]state.VG, error) {
	return append([]state.VG(nil), service.vgs...), nil
}
func (service *testVGService) Inspect(_ context.Context, name string) (lvm.Capacity, error) {
	capacity := service.capacity
	capacity.VGName = name
	return capacity, nil
}

type testCapacity struct {
	capacity lvm.Capacity
}

func (provider *testCapacity) Capacity(context.Context, string) (lvm.Capacity, error) {
	return provider.capacity, nil
}

func newTestServer(t *testing.T) (*Server, *state.Store, string, *testVGService) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.AdminUI.Enabled = true
	cfg.AdminUI.Address = "127.0.0.1:9444"
	cfg.Listen.Address = "127.0.0.1:9443"
	cfg.Targets.ISCSI.Portals = []config.Portal{{Address: "127.0.0.1", Port: 3260}}
	cfg.Storage.VGs = []string{"vg1"}
	cfg.Storage.Classes = map[string]string{}
	raw, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	parsed, err := config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(context.Background(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		now := time.Now().UTC()
		if err := tx.PutVG(state.VG{Name: "vg1", UUID: "vg-uuid", State: "active", RegisteredAt: now, UpdatedAt: now}); err != nil {
			return err
		}
		return tx.PutVolume(state.Volume{
			Owner: "owner", VolumeID: "vol1", ResourceUUID: "resource-1",
			LVName: "lv-vol1", VGName: "vg1", RequestedSizeBytes: 1024,
			ActualSizeBytes: 1024, State: "exported", CreatedAt: now, UpdatedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	classes := agent.NewClassMap(parsed.Storage.Classes, configPath)
	vgs := &testVGService{
		vgs: []state.VG{{Name: "vg1", UUID: "vg-uuid", State: "active", RegisteredAt: time.Now(), UpdatedAt: time.Now()}},
		capacity: lvm.Capacity{
			VGName: "vg1", VGUUID: "vg-uuid", VGState: "active",
			TotalBytes: 10 << 30, FreeBytes: 8 << 30, AllocatableBytes: 7 << 30,
			ExtentSizeBytes: 4 << 20, MaxVolumeBytes: 1 << 40,
			ObservedAt: time.Now().UTC(),
		},
	}
	server, err := New(Options{
		ConfigPath: configPath, Config: parsed, ConfigData: raw,
		Store: store, Capacity: &testCapacity{capacity: vgs.capacity}, VGs: vgs, Classes: classes,
		Ready: func() bool { return true }, HostUUID: "host-uuid",
		AgentVersion: "test", APIVersion: "v1", StartedAt: time.Now(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:    time.Now, Listener: listener,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.MarkRunning(); err != nil {
		t.Fatal(err)
	}
	return server, store, configPath, vgs
}

func doRequest(t *testing.T, server *Server, method, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Host = server.Address().String()
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	return recorder
}

func TestAdminUIStatusConfigAndMutations(t *testing.T) {
	server, _, configPath, _ := newTestServer(t)

	response := doRequest(t, server, http.MethodGet, "/api/status", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status code = %d body=%s", response.Code, response.Body.String())
	}
	var status StatusResponse
	if err := jsonUnmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Agent.Ready || len(status.VGs) != 1 || len(status.Volumes) != 1 {
		t.Fatalf("unexpected status: %#v", status)
	}
	if !status.ISCSI.Enabled || status.ISCSI.IQNPrefix == "" || len(status.ISCSI.Portals) != 1 {
		t.Fatalf("unexpected iSCSI info: %#v", status.ISCSI)
	}
	if status.Volumes[0].Serving {
		t.Fatal("volume without observations should not be serving")
	}

	configResponse := doRequest(t, server, http.MethodGet, "/api/config", nil, nil)
	if configResponse.Code != http.StatusOK {
		t.Fatalf("config code = %d body=%s", configResponse.Code, configResponse.Body.String())
	}
	var current ConfigResponse
	if err := jsonUnmarshal(configResponse.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if !current.DiskValid || current.DiskETag == "" || current.RunningYAML == "" {
		t.Fatalf("unexpected config: %#v", current)
	}

	parsed, err := config.Parse([]byte(current.DiskYAML))
	if err != nil {
		t.Fatal(err)
	}
	parsed.Storage.HeadroomPercent++
	candidate, err := yaml.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	save := doRequest(t, server, http.MethodPut, "/api/config", candidate, map[string]string{
		"Content-Type": "application/yaml", "If-Match": current.DiskETag,
	})
	if save.Code != http.StatusOK {
		t.Fatalf("save code = %d body=%s", save.Code, save.Body.String())
	}
	var saved SaveConfigResponse
	if err := jsonUnmarshal(save.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.Saved || !saved.ConfigChanged || saved.RestartRequired {
		t.Fatalf("unexpected save response: %#v", saved)
	}

	register := doRequest(t, server, http.MethodPost, "/api/vgs", []byte(`{"name":"vg2"}`), map[string]string{"Content-Type": "application/json"})
	if register.Code != http.StatusCreated {
		t.Fatalf("register code = %d body=%s", register.Code, register.Body.String())
	}
	classes := doRequest(t, server, http.MethodPut, "/api/classes/ssd", []byte(`{"vg":"vg1"}`), map[string]string{"Content-Type": "application/json"})
	if classes.Code != http.StatusOK {
		t.Fatalf("class code = %d body=%s", classes.Code, classes.Body.String())
	}
	updated, err := os.ReadFile(configPath) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(updated, []byte("ssd: vg1")) {
		t.Fatalf("class mapping not persisted: %s", updated)
	}
}

func jsonUnmarshal(data []byte, target any) error {
	return json.Unmarshal(data, target)
}

func TestConfigHelpers(t *testing.T) {
	dir := t.TempDir()
	missing, _ := readConfig(filepath.Join(dir, "missing.yaml"))
	if missing.DiskErrorCode != "missing" || missing.ETag != "*" {
		t.Fatalf("missing = %#v", missing)
	}
	invalidPath := filepath.Join(dir, "invalid.yaml")
	if err := os.WriteFile(invalidPath, []byte("not: [valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	invalid, _ := readConfig(invalidPath)
	if invalid.DiskErrorCode != "parse_error" || invalid.ETag == "" || len(invalid.Raw) == 0 {
		t.Fatalf("invalid = %#v", invalid)
	}
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(invalidPath, link); err != nil {
		t.Fatal(err)
	}
	if unsafe, _ := readConfig(link); unsafe.DiskErrorCode != "unsafe_path" {
		t.Fatalf("symlink = %#v", unsafe)
	}
	if unsafe, _ := readConfig(dir); unsafe.DiskErrorCode != "unsafe_path" {
		t.Fatalf("directory = %#v", unsafe)
	}

	running := config.Default()
	running.Storage.VGs = []string{"vg1"}
	running.Targets.ISCSI.Portals = []config.Portal{{Address: "127.0.0.1", Port: 3260}, {Address: "127.0.0.2", Port: 3260}}
	candidate := running
	candidate.Targets.ISCSI.Portals = []config.Portal{{Address: "127.0.0.2", Port: 3260}, {Address: "127.0.0.1", Port: 3260}}
	if changed, reason := bindingSocketChanged(&running, &candidate); changed || reason != "" {
		t.Fatalf("reorder should be set-equal: %v %q", changed, reason)
	}
	candidate.Targets.ISCSI.Portals = []config.Portal{{Address: "127.0.0.1", Port: 3261}}
	if changed, reason := bindingSocketChanged(&running, &candidate); !changed || reason != "iscsi_portals_changed" {
		t.Fatalf("portal change = %v %q", changed, reason)
	}
	candidate = running
	candidate.Listen.Address = "127.0.0.1:9555"
	if changed, reason := bindingSocketChanged(&running, &candidate); !changed || reason != "listen_address_changed" {
		t.Fatalf("listen change = %v %q", changed, reason)
	}
	candidate = running
	candidate.AdminUI.Address = "127.0.0.1:9556"
	if changed, _ := bindingSocketChanged(&running, &candidate); changed {
		t.Fatal("disabled admin address change should not require restart")
	}
	candidate = running
	candidate.AdminUI.Enabled = true
	if changed, reason := bindingSocketChanged(&running, &candidate); !changed || reason != "admin_enabled_changed" {
		t.Fatalf("admin enabled = %v %q", changed, reason)
	}
	running.AdminUI.Enabled = true
	candidate = running
	candidate.AdminUI.Address = "127.0.0.1:9557"
	if changed, reason := bindingSocketChanged(&running, &candidate); !changed || reason != "admin_address_changed" {
		t.Fatalf("admin address = %v %q", changed, reason)
	}
	running.AdminUI.Enabled = false
	candidate = running
	candidate.Targets.NVMeTCP.Enabled = true
	if changed, reason := bindingSocketChanged(&running, &candidate); !changed || reason != "nvme_tcp_enabled_changed" {
		t.Fatalf("nvme enabled = %v %q", changed, reason)
	}
	if configEquivalent(nil, &running, nil) || configEquivalent(&running, nil, nil) {
		t.Fatal("nil config compared equal")
	}

	classes := agent.NewClassMap(map[string]string{"ssd": "vg1"}, "")
	if !configEquivalent(&running, &running, classes) {
		t.Fatal("identical configs differ")
	}
	other := running
	other.Storage.HeadroomPercent++
	if configEquivalent(&running, &other, classes) {
		t.Fatal("changed configs compare equal")
	}
	other = running
	other.Storage.VGs = []string{"vg1", "vg1"}
	if !configEquivalent(&running, &other, classes) {
		t.Fatal("duplicate VG changed equivalence")
	}

	if err := checkPreconditions(configFile{ETag: `"x"`}, `"x"`, ""); err != nil {
		t.Fatal(err)
	}
	if err := checkPreconditions(configFile{ETag: `"x"`}, `"y"`, ""); !errors.Is(err, errConflict) {
		t.Fatalf("mismatch = %v", err)
	}
	if err := checkPreconditions(configFile{DiskErrorCode: "missing", ETag: "*"}, "", "*"); err != nil {
		t.Fatal(err)
	}
	if err := checkPreconditions(configFile{DiskErrorCode: "missing", ETag: "*"}, "", ""); !errors.Is(err, errPreconditionRequired) {
		t.Fatalf("missing precondition = %v", err)
	}
}

func TestAdminUIStatusObservationsAndCapacityErrors(t *testing.T) {
	server, store, _, _ := newTestServer(t)
	now := time.Now().UTC()
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		if err := tx.PutExport(state.Export{
			Owner: "owner", VolumeID: "vol1", Protocol: "iscsi", Initiator: "iqn.i",
			TargetName: "iqn.t", Portal: "127.0.0.1:3260", State: "exported",
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			return err
		}
		if err := tx.PutObservation(state.Observation{
			Owner: "owner", VolumeID: "vol1", LVPresent: true, ObservedSizeBytes: 1024,
			DevicePath: "/dev/vg1/lv-vol1", ObservedAt: now, Source: "lvm",
		}); err != nil {
			return err
		}
		if err := tx.PutExportObservation(state.ExportObservation{
			Owner: "owner", VolumeID: "vol1", TargetPresent: true, NamespacePresent: true,
			ACLPresent: true, ActiveSessions: 2, SessionState: "active",
			ObservedInitiator: "iqn.i", ObservedAt: now, Source: "lio",
		}); err != nil {
			return err
		}
		return tx.PutOperation(state.Operation{
			ID: "op1", Owner: "owner", VolumeID: "vol1", ResourceUUID: "resource-1",
			Action: "create", RequestHash: "hash", IntentJSON: "{}", State: "failed",
			Attempt: 3, MaxAttempts: 3, DeadlineAt: now, CreatedAt: now, UpdatedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}
	response := doRequest(t, server, http.MethodGet, "/api/status", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d %s", response.Code, response.Body.String())
	}
	var status StatusResponse
	if err := jsonUnmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Volumes[0].Serving || status.Volumes[0].ExportObservation == nil ||
		status.Volumes[0].ExportObservation.ActiveSessions == nil ||
		*status.Volumes[0].ExportObservation.ActiveSessions != 2 {
		t.Fatalf("volume view = %#v", status.Volumes[0])
	}
	if len(status.Operations) != 1 || status.Counts.Operations != 1 {
		t.Fatalf("operations = %#v count=%#v", status.Operations, status.Counts)
	}

	server.options.Capacity = failingCapacity{}
	response = doRequest(t, server, http.MethodGet, "/api/status", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status after capacity failure = %d", response.Code)
	}
	if err := jsonUnmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Errors) == 0 || status.VGs[0].State != "unavailable" {
		t.Fatalf("capacity failure view = %#v", status)
	}

	old := now.Add(-10 * time.Hour)
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		return tx.PutObservation(state.Observation{
			Owner: "owner", VolumeID: "vol1", LVPresent: true, ObservedSizeBytes: 1024,
			DevicePath: "/dev/vg1/lv-vol1", ObservedAt: old, Source: "lvm",
		})
	}); err != nil {
		t.Fatal(err)
	}
	response = doRequest(t, server, http.MethodGet, "/api/status", nil, nil)
	if err := jsonUnmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Volumes[0].Observation.Stale || status.Volumes[0].Serving {
		t.Fatalf("stale observation = %#v", status.Volumes[0])
	}
}

type failingCapacity struct{}

func (failingCapacity) Capacity(context.Context, string) (lvm.Capacity, error) {
	return lvm.Capacity{}, errors.New("capacity failed")
}

func TestAdminUIStatusMetricsAndRates(t *testing.T) {
	server, store, _, _ := newTestServer(t)
	now := time.Now().UTC()
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		return tx.PutExport(state.Export{
			Owner: "owner", VolumeID: "vol1", Protocol: "iscsi", Initiator: "iqn.i",
			TargetName: "iqn.t", Portal: "127.0.0.1:3260", State: "exported",
			CreatedAt: now, UpdatedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}
	provider := &testTargetMetricsProvider{metrics: target.ISCSIMetrics{
		Targets: []target.ISCSITargetMetrics{{
			Target: "iqn.t", Sessions: 1,
			ClientAddresses: []string{"192.0.2.10"},
			Login:           target.ISCSILoginMetrics{TargetLoginFails: 1},
			Initiators: []target.ISCSIInitiatorMetrics{{
				Initiator: "iqn.i", RXBytes: 2 << 20, TXBytes: 1 << 20, CommandPDUs: 10,
				ClientAddresses: []string{"192.0.2.10"},
			}},
			LUNs: []target.ISCSILUNMetrics{{
				LUN: 0, ResourceUUID: "resource-1", Commands: 10, Status: "available",
			}},
		}},
		Connections: []target.ISCSIClientConnection{{
			ClientAddress: "192.0.2.10", ClientPort: 40000,
			LocalAddress: "192.0.2.1", LocalPort: 3260,
			Target: "iqn.t", Initiator: "iqn.i",
		}},
	}}
	server.options.TargetMetrics = provider
	server.options.Now = func() time.Time { return now }

	response := doRequest(t, server, http.MethodGet, "/api/status", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d %s", response.Code, response.Body.String())
	}
	var status StatusResponse
	if err := jsonUnmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Metrics.Available || status.Metrics.RatesAvailable || len(status.Metrics.Targets) != 1 {
		t.Fatalf("metrics = %#v", status.Metrics)
	}
	first := status.Metrics.Targets[0]
	if first.VolumeOwner != "owner" || first.VolumeID != "vol1" || first.ReadBytes != 1<<20 ||
		first.WriteBytes != 2<<20 || first.Commands != 10 || first.RatesAvailable ||
		first.ErrorCount != 1 || first.Errors.LoginFailures != 1 ||
		len(first.ClientAddresses) != 1 || first.ClientAddresses[0] != "192.0.2.10" {
		t.Fatalf("target metrics = %#v", first)
	}
	if len(status.Metrics.Connections) != 1 ||
		status.Metrics.Connections[0].ClientAddress != "192.0.2.10" ||
		status.Metrics.Connections[0].ClientPort != 40000 ||
		status.Metrics.Connections[0].Target != "iqn.t" ||
		status.Metrics.Connections[0].Initiator != "iqn.i" {
		t.Fatalf("connections = %#v", status.Metrics.Connections)
	}

	now = now.Add(2 * time.Second)
	provider.metrics.Targets[0].Initiators[0].RXBytes = 5 << 20
	provider.metrics.Targets[0].Initiators[0].TXBytes = 3 << 20
	provider.metrics.Targets[0].LUNs[0].Commands = 20
	response = doRequest(t, server, http.MethodGet, "/api/status", nil, nil)
	if err := jsonUnmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Metrics.RatesAvailable || !status.Metrics.Targets[0].RatesAvailable {
		t.Fatalf("rates unavailable: %#v", status.Metrics)
	}
	if status.Metrics.Targets[0].ReadBytesPerSecond != float64(2<<20)/2 ||
		status.Metrics.Targets[0].WriteBytesPerSecond != float64(3<<20)/2 ||
		status.Metrics.Targets[0].CommandsPerSecond != 5 ||
		status.Metrics.ReadBytesPerSecond != float64(2<<20)/2 ||
		status.Metrics.WriteBytesPerSecond != float64(3<<20)/2 {
		t.Fatalf("rates = %#v", status.Metrics)
	}

	provider.err = errors.New("configfs unavailable")
	response = doRequest(t, server, http.MethodGet, "/api/status", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("metrics error changed status = %d", response.Code)
	}
	if err := jsonUnmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Metrics.Available || status.Metrics.Error == "" {
		t.Fatalf("metrics error = %#v", status.Metrics)
	}
}

func TestAdminUIDisableISCSITarget(t *testing.T) {
	server, store, _, _ := newTestServer(t)
	now := time.Now().UTC()
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		return tx.PutExport(state.Export{
			Owner: "owner", VolumeID: "vol1", Protocol: "iscsi", Initiator: "iqn.i",
			TargetName: "iqn.t", Portal: "127.0.0.1:3260", State: "exported",
			CreatedAt: now, UpdatedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}
	server.options.DisableTarget = nil
	server.options.EnableTarget = nil
	response := doRequest(t, server, http.MethodPost, "/api/targets/disable",
		[]byte(`{"owner":"owner","volume_id":"vol1"}`),
		map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured disable = %d", response.Code)
	}
	response = doRequest(t, server, http.MethodPost, "/api/targets/enable",
		[]byte(`{"owner":"owner","volume_id":"vol1"}`),
		map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured enable = %d", response.Code)
	}
	disabler := &testTargetDisabler{}
	server.options.DisableTarget = disabler
	server.options.EnableTarget = disabler
	response = doRequest(t, server, http.MethodPost, "/api/targets/disable",
		[]byte(`{"owner":"owner","volume_id":"vol1"}`),
		map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusNoContent {
		t.Fatalf("disable = %d %s", response.Code, response.Body.String())
	}
	if disabler.calls != 1 || disabler.owner != "owner" || disabler.volumeID != "vol1" {
		t.Fatalf("disable call = %#v", disabler)
	}
	response = doRequest(t, server, http.MethodPost, "/api/targets/enable",
		[]byte(`{"owner":"owner","volume_id":"vol1"}`),
		map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusNoContent {
		t.Fatalf("enable = %d %s", response.Code, response.Body.String())
	}
	if disabler.enableCalls != 1 || disabler.owner != "owner" || disabler.volumeID != "vol1" {
		t.Fatalf("enable call = %#v", disabler)
	}

	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		return tx.PutExport(state.Export{
			Owner: "owner", VolumeID: "vol1", Protocol: "nvme-tcp", Initiator: "iqn.i",
			TargetName: "nqn.t", Portal: "127.0.0.1:4420", State: "exported",
			CreatedAt: now, UpdatedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}
	response = doRequest(t, server, http.MethodPost, "/api/targets/disable",
		[]byte(`{"owner":"owner","volume_id":"vol1"}`),
		map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("non-iSCSI target = %d", response.Code)
	}
	response = doRequest(t, server, http.MethodPost, "/api/targets/disable",
		[]byte(`{"owner":"owner","volume_id":"missing"}`),
		map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing target = %d", response.Code)
	}
	response = doRequest(t, server, http.MethodPost, "/api/targets/disable",
		[]byte(`{"owner":"owner"}`),
		map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid request = %d", response.Code)
	}
}

func TestTargetActionErrorMappings(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{state.ErrNotFound, http.StatusNotFound, "not_found"},
		{operations.ErrVolumeInDrift, http.StatusConflict, "volume_in_drift"},
		{operations.ErrSessionActive, http.StatusConflict, "session_active"},
		{operations.ErrSessionUnknown, http.StatusConflict, "session_unknown"},
		{operations.ErrOperationInProgress, http.StatusConflict, "operation_in_progress"},
		{operations.ErrVolumeAlreadyExport, http.StatusConflict, "already_exported"},
		{operations.ErrInvalidRequest, http.StatusUnprocessableEntity, "invalid_target"},
		{target.ErrConflict, http.StatusConflict, "conflict"},
		{context.Canceled, http.StatusGatewayTimeout, "timeout"},
		{errors.New("other"), http.StatusInternalServerError, "internal_error"},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		writeTargetActionError(recorder, test.err)
		var body APIError
		if err := jsonUnmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != test.status || body.Code != test.code {
			t.Fatalf("%v: status=%d code=%q, want %d %q", test.err, recorder.Code, body.Code, test.status, test.code)
		}
	}
}

func TestMetricsViewFallbackAndCounts(t *testing.T) {
	view := targetMetricsView(target.ISCSITargetMetrics{
		Target: "iqn.t",
		LUNs: []target.ISCSILUNMetrics{{
			LUN: 0, Commands: 4, ReadBytes: 2, WriteBytes: 3,
			Status: "available", Error: "stat unavailable",
		}},
	}, state.VolumeStatus{})
	if view.VolumeID != "" || view.ReadBytes != 2 || view.WriteBytes != 3 ||
		view.Commands != 4 || !strings.Contains(view.Error, "LUN 0") {
		t.Fatalf("fallback view = %#v", view)
	}
	errors := MetricsErrorsView{AuthenticationFailures: 2, Resets: 1}
	if metricsErrorCount(errors) != 3 {
		t.Fatalf("fallback error count = %d", metricsErrorCount(errors))
	}
	if value, ok := rate(10, 9, 1); ok || value != 0 {
		t.Fatalf("counter reset rate = %v %v", value, ok)
	}
	if value, ok := rate(10, 20, 2); !ok || value != 5 {
		t.Fatalf("rate = %v %v", value, ok)
	}

	server, _, _, _ := newTestServer(t)
	noProvider := server.buildMetrics(context.Background(), nil)
	if noProvider.Available || noProvider.Error == "" {
		t.Fatalf("no provider metrics = %#v", noProvider)
	}
}

func TestAdminUIHTTPGuardsAndAssets(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	server.assets = newAssetFS(fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<html>ok</html>")},
		"app.js":     &fstest.MapFile{Data: []byte("console.log('ok')")},
	})
	response := doRequest(t, server, http.MethodGet, "/", nil, nil)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("ok")) {
		t.Fatalf("index = %d %s", response.Code, response.Body.String())
	}
	response = doRequest(t, server, http.MethodGet, "/assets/app.js", nil, nil)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") == "" {
		t.Fatalf("asset = %d %#v", response.Code, response.Header())
	}
	response = doRequest(t, server, http.MethodGet, "/favicon.ico", nil, nil)
	if response.Code != http.StatusNoContent {
		t.Fatalf("favicon = %d", response.Code)
	}
	response = doRequest(t, server, http.MethodGet, "/assets/missing.js", nil, nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing asset = %d", response.Code)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	request.Host = "example.com"
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("bad host = %d", recorder.Code)
	}
	response = doRequest(t, server, http.MethodPost, "/api/vgs", []byte(`{"name":"vg1"}`), map[string]string{
		"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site",
	})
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-site = %d", response.Code)
	}
	response = doRequest(t, server, http.MethodPost, "/api/vgs", []byte(`{}`), map[string]string{"Content-Type": "text/plain"})
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("content type = %d", response.Code)
	}
	response = doRequest(t, server, http.MethodOptions, "/api/status", nil, nil)
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("options = %d", response.Code)
	}
	server.assets = newAssetFS(fstest.MapFS{})
	response = doRequest(t, server, http.MethodGet, "/", nil, nil)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("fallback = %d", response.Code)
	}
}

func TestAdminUIRestartAndInvalidSave(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	configResponse := doRequest(t, server, http.MethodGet, "/api/config", nil, nil)
	var current ConfigResponse
	if err := jsonUnmarshal(configResponse.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	parsed, err := config.Parse([]byte(current.DiskYAML))
	if err != nil {
		t.Fatal(err)
	}
	parsed.Listen.Address = "127.0.0.1:9555"
	candidate, err := yaml.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	save := doRequest(t, server, http.MethodPut, "/api/config", candidate, map[string]string{
		"Content-Type": "application/yaml", "If-Match": current.DiskETag,
	})
	if save.Code != http.StatusOK {
		t.Fatalf("restart save = %d %s", save.Code, save.Body.String())
	}
	var saved SaveConfigResponse
	if err := jsonUnmarshal(save.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if !saved.RestartRequired || saved.RestartReason != "listen_address_changed" {
		t.Fatalf("restart response = %#v", saved)
	}
	stale := doRequest(t, server, http.MethodPut, "/api/config", candidate, map[string]string{
		"Content-Type": "application/yaml", "If-Match": current.DiskETag,
	})
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale save = %d", stale.Code)
	}
	invalid := doRequest(t, server, http.MethodPut, "/api/config", []byte("not: [valid"), map[string]string{
		"Content-Type": "application/yaml", "If-Match": saved.DiskETag,
	})
	if invalid.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid save = %d %s", invalid.Code, invalid.Body.String())
	}
}

func TestAdminUIListsDeregisterAndRemove(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	response := doRequest(t, server, http.MethodGet, "/api/vgs", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("vgs = %d", response.Code)
	}
	var list VGListResponse
	if err := jsonUnmarshal(response.Body.Bytes(), &list); err != nil || len(list.VGs) != 1 {
		t.Fatalf("vgs body = %s", response.Body.String())
	}
	response = doRequest(t, server, http.MethodGet, "/api/classes", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("classes = %d", response.Code)
	}
	response = doRequest(t, server, http.MethodPost, "/api/vgs", []byte(`{"name":"vg2"}`), map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusCreated {
		t.Fatalf("register = %d", response.Code)
	}
	response = doRequest(t, server, http.MethodDelete, "/api/vgs/vg2", nil, map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusNoContent {
		t.Fatalf("deregister = %d %s", response.Code, response.Body.String())
	}
	response = doRequest(t, server, http.MethodPut, "/api/classes/ssd", []byte(`{"vg":"vg1"}`), map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusOK {
		t.Fatalf("set class = %d", response.Code)
	}
	response = doRequest(t, server, http.MethodDelete, "/api/classes/ssd", nil, map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusNoContent {
		t.Fatalf("remove class = %d %s", response.Code, response.Body.String())
	}
	response = doRequest(t, server, http.MethodPut, "/api/classes/ssd", []byte(`{"vg":"missing"}`), map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing class VG = %d", response.Code)
	}
	response = doRequest(t, server, http.MethodDelete, "/api/vgs/vg1", nil, map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusNoContent {
		t.Fatalf("deregister vg1 = %d %s", response.Code, response.Body.String())
	}
}

func TestAdminUIMissingConfigAndStatusConfigError(t *testing.T) {
	server, _, configPath, _ := newTestServer(t)
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	response := doRequest(t, server, http.MethodGet, "/api/config", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("config = %d", response.Code)
	}
	var current ConfigResponse
	if err := jsonUnmarshal(response.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if current.DiskValid || current.DiskErrorCode != "missing" || current.DiskETag != "*" {
		t.Fatalf("missing config = %#v", current)
	}
	response = doRequest(t, server, http.MethodGet, "/api/status", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var status StatusResponse
	if err := jsonUnmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Errors) == 0 || status.Errors[0].Code != "config_missing" {
		t.Fatalf("config error = %#v", status.Errors)
	}
	cfg := config.Default()
	cfg.AdminUI.Enabled = true
	cfg.Targets.ISCSI.Portals = []config.Portal{{Address: "127.0.0.1", Port: 3260}}
	raw, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	response = doRequest(t, server, http.MethodPut, "/api/config", raw, map[string]string{
		"Content-Type": "application/yaml", "If-None-Match": "*",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("create = %d %s", response.Code, response.Body.String())
	}
}

func TestAdminUINewValidationAndMappedErrors(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("empty New succeeded")
	}
	cfg := config.Default()
	cfg.AdminUI.Enabled = false
	if _, err := New(Options{ConfigPath: "x", Config: &cfg, ConfigData: []byte("x"), Store: nil}); err == nil {
		t.Fatal("disabled/incomplete New succeeded")
	}
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{errBadRequest, http.StatusBadRequest, "bad_request"},
		{errInvalidConfig, http.StatusUnprocessableEntity, "invalid_config"},
		{errUnsafePath, http.StatusUnprocessableEntity, "invalid_config"},
		{errConflict, http.StatusConflict, "conflict"},
		{errPreconditionRequired, http.StatusPreconditionRequired, "precondition_required"},
		{errLocked, http.StatusConflict, "conflict"},
		{errStoreUnavailable, http.StatusServiceUnavailable, "unavailable"},
		{agent.ErrVGInUse, http.StatusConflict, "vg_in_use"},
		{agent.ErrVGConflict, http.StatusConflict, "vg_conflict"},
		{agent.ErrVGClassMapped, http.StatusConflict, "class_conflict"},
		{agent.ErrVGUnavailable, http.StatusUnprocessableEntity, "vg_unavailable"},
		{agent.ErrVGUUIDMismatch, http.StatusUnprocessableEntity, "vg_unavailable"},
		{agent.ErrVGNotFound, http.StatusNotFound, "not_found"},
		{config.ErrInvalid, http.StatusUnprocessableEntity, "invalid_config"},
		{config.ErrClassNotFound, http.StatusNotFound, "not_found"},
		{config.ErrClassConflict, http.StatusConflict, "class_conflict"},
		{config.ErrLocked, http.StatusConflict, "conflict"},
		{config.ErrUnsafePath, http.StatusUnprocessableEntity, "invalid_config"},
		{errors.New("other"), http.StatusInternalServerError, "internal_error"},
	}
	for _, testCase := range cases {
		recorder := httptest.NewRecorder()
		writeMappedError(recorder, testCase.err)
		if recorder.Code != testCase.status || !bytes.Contains(recorder.Body.Bytes(), []byte(testCase.code)) {
			t.Fatalf("mapped %v = %d %s", testCase.err, recorder.Code, recorder.Body.String())
		}
	}
}

func TestAdminUIServeAndShutdown(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	done := make(chan error, 1)
	go func() { done <- server.Serve() }()
	time.Sleep(10 * time.Millisecond)
	response, err := http.Get("http://" + server.Address().String() + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("served status = %d", response.StatusCode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAdminUIMutationValidationAndClassConflicts(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	response := doRequest(t, server, http.MethodPut, "/api/classes/ssd", []byte(`{"vg":"vg1"}`), map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusOK {
		t.Fatalf("set class = %d", response.Code)
	}
	response = doRequest(t, server, http.MethodPut, "/api/classes/other", []byte(`{"vg":"vg1"}`), map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusConflict {
		t.Fatalf("class conflict = %d %s", response.Code, response.Body.String())
	}
	response = doRequest(t, server, http.MethodDelete, "/api/classes/missing", nil, map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing class = %d", response.Code)
	}
	response = doRequest(t, server, http.MethodDelete, "/api/vgs/missing", nil, map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing VG = %d", response.Code)
	}
	response = doRequest(t, server, http.MethodPost, "/api/vgs", []byte(`not-json`), map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid JSON = %d", response.Code)
	}
	response = doRequest(t, server, http.MethodPost, "/api/vgs", []byte(`{"name":""}`), map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("empty name = %d", response.Code)
	}
	response = doRequest(t, server, http.MethodPost, "/api/vgs", []byte(`{"name":"vg1"}`), map[string]string{
		"Content-Type": "application/json", "Origin": "null",
	})
	if response.Code != http.StatusForbidden {
		t.Fatalf("null origin = %d", response.Code)
	}
	response = doRequest(t, server, http.MethodPost, "/api/vgs", bytes.Repeat([]byte("x"), 1<<20+1), map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body = %d", response.Code)
	}
}

func TestAdminUIInvalidConfigStatus(t *testing.T) {
	server, _, configPath, _ := newTestServer(t)
	if err := os.WriteFile(configPath, []byte("not: [valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	response := doRequest(t, server, http.MethodGet, "/api/config", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("config = %d", response.Code)
	}
	var current ConfigResponse
	if err := jsonUnmarshal(response.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if current.DiskValid || current.DiskErrorCode != "parse_error" {
		t.Fatalf("invalid config = %#v", current)
	}
	response = doRequest(t, server, http.MethodGet, "/api/status", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var status StatusResponse
	if err := jsonUnmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Errors) == 0 || status.Errors[0].Code != "config_invalid" {
		t.Fatalf("config errors = %#v", status.Errors)
	}
}

func TestAdminUIEmptyListsAndFallbackAssets(t *testing.T) {
	server, store, _, _ := newTestServer(t)
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		return tx.DeleteVolume("owner", "vol1")
	}); err != nil {
		t.Fatal(err)
	}
	response := doRequest(t, server, http.MethodGet, "/api/status", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("empty status = %d", response.Code)
	}
	var status StatusResponse
	if err := jsonUnmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if len(status.Volumes) != 0 || len(status.Operations) != 0 {
		t.Fatalf("empty status = %#v", status)
	}
	server.assets = &assetFS{}
	response = doRequest(t, server, http.MethodGet, "/", nil, nil)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("empty assets = %d", response.Code)
	}
	response = doRequest(t, server, http.MethodGet, "/assets/x.js", nil, nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("empty asset = %d", response.Code)
	}
}

func TestAdminUIRejectsNonLoopbackListener(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	options := server.options
	listener, err := net.Listen("tcp", "0.0.0.0:0") //nolint:gosec // listener rejection test
	if err != nil {
		t.Fatal(err)
	}
	options.Listener = listener
	if _, err := New(options); err == nil {
		t.Fatal("non-loopback listener was accepted")
	}
}

func TestDefaultAssetsWhenBuilt(t *testing.T) {
	assets := defaultAssets()
	if _, err := assets.read("index.html"); err != nil {
		t.Skip("browser assets are not built")
	}
	server, _, _, _ := newTestServer(t)
	server.assets = assets
	response := doRequest(t, server, http.MethodGet, "/", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("built index = %d %s", response.Code, response.Body.String())
	}
	index, _ := assets.read("index.html")
	marker := strings.Index(string(index), "/assets/")
	if marker < 0 {
		t.Fatal("index has no asset reference")
	}
	rest := string(index)[marker+len("/assets/"):]
	end := strings.IndexAny(rest, "\"' ")
	if end < 0 {
		end = len(rest)
	}
	asset := rest[:end]
	response = doRequest(t, server, http.MethodGet, "/assets/"+asset, nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("built asset %q = %d", asset, response.Code)
	}
}

func TestWarningViewsAndPathNames(t *testing.T) {
	views := warningViews([]string{"durability_uncertain", "audit_write_failed", "other"})
	if len(views) != 3 || views[0].Code != "durability_uncertain" || views[1].Code != "audit_write_failed" {
		t.Fatalf("warning views = %#v", views)
	}
	if _, err := pathName("/api/vgs/", "/api/vgs/"); err == nil {
		t.Fatal("empty path name accepted")
	}
	if _, err := pathName("/api/vgs/a/b", "/api/vgs/"); err == nil {
		t.Fatal("nested path name accepted")
	}
}

func TestAdminUICertificateListAndDownload(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	dir := t.TempDir()
	server.options.Config.AdminUI.ClientCertDir = dir
	if err := os.WriteFile(filepath.Join(dir, "admin-admin.crt"), []byte("CERTIFICATE"), 0o644); err != nil { //nolint:gosec // test fixture certificate
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "admin-admin.key"), []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ignore.txt"), []byte("ignore"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "server.key"), []byte("SERVER KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("CA"), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	caPath := filepath.Join(dir, "client-ca.crt")
	if err := os.WriteFile(caPath, []byte("CLIENT CA"), 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	server.options.Config.Listen.TLS.ClientCAFile = caPath
	if err := os.Symlink(filepath.Join(dir, "admin-admin.crt"), filepath.Join(dir, "link.crt")); err != nil {
		t.Fatal(err)
	}
	response := doRequest(t, server, http.MethodGet, "/api/certificates", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list certificates = %d", response.Code)
	}
	var listing CertificatesResponse
	if err := jsonUnmarshal(response.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if len(listing.Files) != 2 {
		t.Fatalf("certificate files = %#v", listing.Files)
	}
	if listing.Directory != dir || listing.Files[0].Role != "admin" {
		t.Fatalf("certificate listing = %#v", listing)
	}
	if listing.CA == nil || listing.CA.DownloadURL != "/api/ca" {
		t.Fatalf("CA listing = %#v", listing.CA)
	}
	response = doRequest(t, server, http.MethodGet, "/api/certificates/admin-admin.crt", nil, nil)
	if response.Code != http.StatusOK || response.Body.String() != "CERTIFICATE" {
		t.Fatalf("download certificate = %d %q", response.Code, response.Body.String())
	}
	if response.Header().Get("Content-Disposition") == "" {
		t.Fatal("missing content disposition")
	}
	response = doRequest(t, server, http.MethodGet, "/api/ca", nil, nil)
	if response.Code != http.StatusOK || response.Body.String() != "CLIENT CA" {
		t.Fatalf("download CA = %d %q", response.Code, response.Body.String())
	}
	for _, path := range []string{"/api/certificates/../admin-admin.crt", "/api/certificates/link.crt", "/api/certificates/ignore.txt", "/api/certificates/server.key", "/api/certificates/ca.crt"} {
		response = doRequest(t, server, http.MethodGet, path, nil, nil)
		if response.Code != http.StatusNotFound {
			t.Fatalf("unsafe path %q = %d", path, response.Code)
		}
	}
}

func TestAdminUICertificateErrorsAndISCSIDisabled(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	server.options.Config.AdminUI.ClientCertDir = ""
	server.options.Config.Listen.TLS.CertFile = filepath.Join(t.TempDir(), "server.crt")
	response := doRequest(t, server, http.MethodGet, "/api/certificates", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("fallback cert dir = %d", response.Code)
	}
	var listing CertificatesResponse
	if err := jsonUnmarshal(response.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if listing.Directory != filepath.Join(filepath.Dir(server.options.Config.Listen.TLS.CertFile), "clients") || listing.Error == "" {
		t.Fatalf("fallback listing = %#v", listing)
	}
	response = doRequest(t, server, http.MethodGet, "/api/certificates/missing.crt", nil, nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing download = %d", response.Code)
	}
	fileDir := t.TempDir()
	filePath := filepath.Join(fileDir, "not-a-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	server.options.Config.AdminUI.ClientCertDir = filePath
	response = doRequest(t, server, http.MethodGet, "/api/certificates", nil, nil)
	if err := jsonUnmarshal(response.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if listing.Error == "" {
		t.Fatal("non-directory client cert path was accepted")
	}
	server.options.Config.Targets.ISCSI.Enabled = false
	response = doRequest(t, server, http.MethodGet, "/api/status", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var status StatusResponse
	if err := jsonUnmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.ISCSI.Enabled || len(status.ISCSI.Portals) != 1 {
		t.Fatalf("disabled iSCSI view = %#v", status.ISCSI)
	}
	if roleFromCertificateName("unknown-file.crt") != "" {
		t.Fatal("unknown role accepted")
	}
}

func TestAdminUITLSServerNames(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	certPath := filepath.Join(t.TempDir(), "server.crt")
	writeTestServerCertificate(t, certPath)
	server.options.Config.Listen.TLS.CertFile = certPath
	response := doRequest(t, server, http.MethodGet, "/api/config", nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("config = %d", response.Code)
	}
	var current ConfigResponse
	if err := jsonUnmarshal(response.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if current.TLS.Error != "" || current.TLS.Subject != "kouja-agent" {
		t.Fatalf("TLS info = %#v", current.TLS)
	}
	if len(current.TLS.ServerNames) != 1 || current.TLS.ServerNames[0] != "storage.example" ||
		len(current.TLS.IPAddresses) != 1 || current.TLS.IPAddresses[0] != "127.0.0.1" {
		t.Fatalf("TLS names = %#v", current.TLS)
	}
}

func writeTestServerCertificate(t *testing.T, path string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "kouja-agent"},
		DNSNames:     []string{"storage.example"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAdminUITLSInfoErrors(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	server.options.Config.Listen.TLS.CertFile = filepath.Join(t.TempDir(), "missing.crt")
	server.options.Config.Listen.TLS.ClientCAFile = filepath.Join(t.TempDir(), "missing-ca.crt")
	response := doRequest(t, server, http.MethodGet, "/api/config", nil, nil)
	var current ConfigResponse
	if err := jsonUnmarshal(response.Body.Bytes(), &current); err != nil {
		t.Fatal(err)
	}
	if current.TLS.Error == "" {
		t.Fatal("missing server certificate did not report a TLS error")
	}
	response = doRequest(t, server, http.MethodGet, "/api/ca", nil, nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing CA download = %d", response.Code)
	}
	response = doRequest(t, server, http.MethodGet, "/api/certificates", nil, nil)
	var listing CertificatesResponse
	if err := jsonUnmarshal(response.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if listing.CA != nil {
		t.Fatalf("missing CA listed: %#v", listing.CA)
	}
}

func writeTestClientCA(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-client-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "client-ca.crt")
	keyPath := filepath.Join(dir, "ca.key")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil { //nolint:gosec // test fixture
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func TestAdminUICreateCertificate(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // test directory must be private
		t.Fatal(err)
	}
	caPath, caKeyPath := writeTestClientCA(t)
	server.options.Config.AdminUI.ClientCertDir = dir
	server.options.Config.Listen.TLS.ClientCAFile = caPath
	server.options.Config.AdminUI.ClientCAKeyFile = caKeyPath

	response := doRequest(t, server, http.MethodPost, "/api/certificates",
		[]byte(`{"role":"caller","name":"rookery-prod"}`),
		map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusCreated {
		t.Fatalf("create certificate = %d %s", response.Code, response.Body.String())
	}
	var created CreateCertificateResponse
	if err := jsonUnmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Certificate.Name != "caller-rookery-prod.crt" || created.PrivateKey.Name != "caller-rookery-prod.key" {
		t.Fatalf("created = %#v", created)
	}
	certPEM, err := os.ReadFile(filepath.Join(dir, created.Certificate.Name)) //nolint:gosec // test fixture
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("generated certificate is not PEM")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if len(certificate.URIs) != 1 || certificate.URIs[0].String() != "spiffe://kouja/caller/rookery-prod" {
		t.Fatalf("URI SANs = %#v", certificate.URIs)
	}
	if len(certificate.ExtKeyUsage) != 1 || certificate.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Fatalf("extended key usage = %#v", certificate.ExtKeyUsage)
	}
	caPEM, err := os.ReadFile(caPath) //nolint:gosec // test fixture
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("could not load test CA")
	}
	if _, err := certificate.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("generated certificate did not verify: %v", err)
	}
	keyInfo, err := os.Stat(filepath.Join(dir, created.PrivateKey.Name))
	if err != nil {
		t.Fatal(err)
	}
	if keyInfo.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode = %o", keyInfo.Mode().Perm())
	}
	response = doRequest(t, server, http.MethodPost, "/api/certificates",
		[]byte(`{"role":"caller","name":"rookery-prod"}`),
		map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusConflict {
		t.Fatalf("duplicate certificate = %d %s", response.Code, response.Body.String())
	}
	response = doRequest(t, server, http.MethodPost, "/api/certificates",
		[]byte(`{"role":"unknown","name":"x"}`),
		map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid role = %d %s", response.Code, response.Body.String())
	}
	response = doRequest(t, server, http.MethodPost, "/api/certificates",
		[]byte(`{"role":"admin","name":"../bad"}`),
		map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid name = %d %s", response.Code, response.Body.String())
	}
}

func TestAdminUICreateCertificateRequiresCAKey(t *testing.T) {
	server, _, _, _ := newTestServer(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // test directory must be private
		t.Fatal(err)
	}
	caPath, _ := writeTestClientCA(t)
	server.options.Config.AdminUI.ClientCertDir = dir
	server.options.Config.Listen.TLS.ClientCAFile = caPath
	server.options.Config.AdminUI.ClientCAKeyFile = filepath.Join(t.TempDir(), "missing.key")
	response := doRequest(t, server, http.MethodPost, "/api/certificates",
		[]byte(`{"role":"admin","name":"operator"}`),
		map[string]string{"Content-Type": "application/json"})
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing CA key = %d %s", response.Code, response.Body.String())
	}
}
