package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/voidstarit/kouja/internal/auth"
	"github.com/voidstarit/kouja/internal/credential"
	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
)

type fixedClock struct{ now time.Time }

func (clock fixedClock) Now() time.Time { return clock.now }

type fakeLVM struct {
	mu          sync.Mutex
	lvs         map[string]lvm.LV
	capacity    lvm.Capacity
	eligibility error
	createErr   error
	removeErr   error
	createCalls int
}

func newFakeLVM() *fakeLVM {
	return &fakeLVM{
		lvs: make(map[string]lvm.LV),
		capacity: lvm.Capacity{
			TotalBytes:       1 << 30,
			FreeBytes:        1 << 30,
			AllocatableBytes: 1 << 30,
			ExtentSizeBytes:  1 << 20,
			MaxVolumeBytes:   1 << 30,
			VGState:          "active",
		},
	}
}

func lvKey(vg, lv string) string { return vg + "/" + lv }

func (backend *fakeLVM) Create(_ context.Context, vg, lvName string, bytes int64, tags map[string]string) (lvm.LV, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.createCalls++
	if backend.createErr != nil {
		return lvm.LV{}, backend.createErr
	}
	key := lvKey(vg, lvName)
	if _, exists := backend.lvs[key]; exists {
		return lvm.LV{}, fmt.Errorf("%w: LV exists", lvm.ErrNotFound)
	}
	volume := lvm.LV{
		VG: vg, Name: lvName, UUID: "uuid-" + lvName, SizeBytes: bytes,
		Tags: tags, DevicePath: "/dev/" + vg + "/" + lvName, Attr: "-wi-a-----",
	}
	backend.lvs[key] = volume
	return volume, nil
}

func (backend *fakeLVM) Extend(_ context.Context, vg, lvName string, bytes int64) (lvm.LV, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	volume, ok := backend.lvs[lvKey(vg, lvName)]
	if !ok {
		return lvm.LV{}, lvm.ErrNotFound
	}
	volume.SizeBytes = bytes
	backend.lvs[lvKey(vg, lvName)] = volume
	return volume, nil
}

func (backend *fakeLVM) Shrink(ctx context.Context, vg, lvName string, bytes int64) (lvm.LV, error) {
	return backend.Extend(ctx, vg, lvName, bytes)
}

func (backend *fakeLVM) Remove(_ context.Context, vg, lvName string) error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.removeErr != nil {
		return backend.removeErr
	}
	delete(backend.lvs, lvKey(vg, lvName))
	return nil
}

func (backend *fakeLVM) Activate(context.Context, string, string) error { return nil }

func (backend *fakeLVM) Observe(_ context.Context, vg, lvName string) (lvm.LV, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	volume, ok := backend.lvs[lvKey(vg, lvName)]
	if !ok {
		return lvm.LV{}, lvm.ErrNotFound
	}
	return volume, nil
}

func (backend *fakeLVM) ListManaged(context.Context) ([]lvm.LV, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	volumes := make([]lvm.LV, 0, len(backend.lvs))
	for _, volume := range backend.lvs {
		volumes = append(volumes, volume)
	}
	return volumes, nil
}

func (backend *fakeLVM) ListCandidates(context.Context) ([]lvm.LV, error) {
	return backend.ListManaged(context.Background())
}

func (backend *fakeLVM) SetOwnerTag(context.Context, string, string, string) error { return nil }

func (backend *fakeLVM) Capacity(context.Context, string) (lvm.Capacity, error) {
	return backend.capacity, nil
}

func (backend *fakeLVM) Eligibility(context.Context, string) error {
	return backend.eligibility
}

type fakeExporter struct {
	mu          sync.Mutex
	exports     map[string]target.ExportResult
	observation target.ExportObservation
	exportErr   error
	unexportErr error
	deleteErr   error
	exportCalls int
	resizeCalls int
	resizeErr   error
}

func newFakeExporter() *fakeExporter {
	return &fakeExporter{
		exports: make(map[string]target.ExportResult),
		observation: target.ExportObservation{
			TargetPresent: true,
			ACLPresent:    true,
			SessionState:  target.SessionNone,
		},
	}
}

func (exporter *fakeExporter) Export(_ context.Context, spec target.ExportSpec, credential target.Credential) (target.ExportResult, error) {
	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	exporter.exportCalls++
	if exporter.exportErr != nil {
		return target.ExportResult{}, exporter.exportErr
	}
	result := target.ExportResult{TargetName: spec.TargetName, Portal: spec.Portal, LUN: spec.LUN, Credential: credential}
	exporter.exports[spec.Owner+"\x00"+spec.VolumeID] = result
	return result, nil
}

func (exporter *fakeExporter) Unexport(_ context.Context, spec target.ExportSpec, _ bool) error {
	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	if exporter.unexportErr != nil {
		return exporter.unexportErr
	}
	delete(exporter.exports, spec.Owner+"\x00"+spec.VolumeID)
	return nil
}

func (exporter *fakeExporter) Observe(_ context.Context, spec target.ExportSpec) (target.ExportObservation, error) {
	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	observation := exporter.observation
	_, present := exporter.exports[spec.Owner+"\x00"+spec.VolumeID]
	observation.TargetPresent = observation.TargetPresent && present
	return observation, nil
}

func (exporter *fakeExporter) Resize(context.Context, target.ExportSpec) error {
	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	exporter.resizeCalls++
	return exporter.resizeErr
}

func (exporter *fakeExporter) ListSessions(context.Context) ([]target.Session, error) {
	return nil, nil
}

func (exporter *fakeExporter) Restore(context.Context, target.ExportSpec, target.Credential) error {
	return nil
}

func (exporter *fakeExporter) Delete(_ context.Context, spec target.ExportSpec, _ bool) error {
	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	if exporter.deleteErr != nil {
		return exporter.deleteErr
	}
	delete(exporter.exports, spec.Owner+"\x00"+spec.VolumeID)
	return nil
}

type fakeCredentials struct {
	mu      sync.Mutex
	entries map[string]credential.Credential
	next    int
}

func newFakeCredentials() *fakeCredentials {
	return &fakeCredentials{entries: make(map[string]credential.Credential)}
}

func (manager *fakeCredentials) Generate(_ context.Context, owner, volumeID, _ string, protocol credential.Protocol) (credential.Credential, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	manager.next++
	record := credential.Credential{
		ID:               fmt.Sprintf("cred-%d", manager.next),
		Owner:            owner,
		VolumeID:         volumeID,
		Protocol:         protocol,
		SecretCiphertext: []byte("cipher"),
		Nonce:            []byte("nonce"),
		KeyID:            "key",
		KeyFingerprint:   "fingerprint",
		State:            "active",
		CreatedAt:        time.Now().UTC(),
	}
	manager.entries[owner+"\x00"+volumeID] = record
	return record, nil
}

func (manager *fakeCredentials) Reveal(context.Context, credential.Credential) (target.Credential, error) {
	return target.Credential{Username: "kouja-user", Secret: "kouja-secret"}, nil
}

func (manager *fakeCredentials) Get(context.Context, auth.Identity, string, string) (credential.Credential, error) {
	return credential.Credential{}, errors.New("not used")
}

func (manager *fakeCredentials) Rotate(context.Context, auth.Identity, string, string) (credential.Credential, error) {
	return credential.Credential{}, errors.New("not used")
}

func (manager *fakeCredentials) Install(context.Context, target.ExportSpec, credential.Credential) error {
	return nil
}

func (manager *fakeCredentials) Revoke(context.Context, target.ExportSpec) error { return nil }

func newTestManager(t *testing.T) (*Manager, *fakeLVM, *fakeExporter, *fakeCredentials, *state.Store) {
	t.Helper()
	store, err := state.Open(context.Background(), t.TempDir()+"/state.db")
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		return tx.PutVG(state.VG{Name: "vg1", UUID: "vg-uuid", State: "active", RegisteredAt: now, UpdatedAt: now})
	}); err != nil {
		t.Fatal(err)
	}
	lvmBackend := newFakeLVM()
	exporter := newFakeExporter()
	credentials := newFakeCredentials()
	manager := New(store, lvmBackend, exporter, nil, credentials, Options{
		MaxAttempts:  1,
		TotalTimeout: time.Minute,
		StepTimeouts: map[Action]time.Duration{
			ActionCreate: 30 * time.Second,
			ActionExport: 30 * time.Second,
			ActionDelete: 30 * time.Second,
		},
		Clock: fixedClock{now: now},
	})
	return manager, lvmBackend, exporter, credentials, store
}

func createRequest(t *testing.T, volumeID, resourceUUID string, size int64, export bool) Request {
	t.Helper()
	intent := CreateIntent{
		ResourceUUID: resourceUUID,
		SizeBytes:    size,
		VG:           "vg1",
		Export:       export,
	}
	if export {
		intent.Protocol = "iscsi"
		intent.Initiator = "iqn.initiator"
		intent.TargetName = "iqn.target"
		intent.Portal = "127.0.0.1:3260"
	}
	raw, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	return Request{Owner: "spiffe://kouja/caller/test", VolumeID: volumeID, Action: ActionCreate, Intent: raw}
}

func getVolume(t *testing.T, store *state.Store, owner, volumeID string) (state.Volume, error) {
	t.Helper()
	var volume state.Volume
	err := store.Transact(context.Background(), func(tx state.Tx) error {
		value, err := tx.GetVolume(owner, volumeID)
		volume = value
		return err
	})
	return volume, err
}

func getOperation(t *testing.T, store *state.Store, request Request) (state.Operation, error) {
	t.Helper()
	hash := mustHash(request)
	var operation state.Operation
	err := store.Transact(context.Background(), func(tx state.Tx) error {
		value, err := tx.GetOperation(request.Owner, request.VolumeID, string(request.Action), hash)
		operation = value
		return err
	})
	return operation, err
}

func TestCreateUnexported(t *testing.T) {
	manager, lvmBackend, _, _, store := newTestManager(t)
	request := createRequest(t, "vol1", "resource-1", 64<<20, false)
	result, err := manager.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Volume.State != "available" || result.Volume.ActualSizeBytes != 64<<20 || result.OperationID == "" {
		t.Fatalf("result: %#v", result)
	}
	if lvmBackend.createCalls != 1 {
		t.Fatalf("create calls = %d", lvmBackend.createCalls)
	}
	volume, err := getVolume(t, store, request.Owner, "vol1")
	if err != nil {
		t.Fatal(err)
	}
	if volume.State != "available" || volume.LastOperationID != result.OperationID {
		t.Fatalf("stored volume: %#v", volume)
	}
	operation, err := getOperation(t, store, request)
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != "succeeded" {
		t.Fatalf("operation = %#v", operation)
	}
}

func TestCreateExportAndIdempotentReplay(t *testing.T) {
	manager, lvmBackend, exporter, _, store := newTestManager(t)
	request := createRequest(t, "vol1", "resource-1", 64<<20, true)
	result, err := manager.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Volume.State != "exported" || result.Export.State != "exported" || exporter.exportCalls != 1 {
		t.Fatalf("result: %#v export calls=%d", result, exporter.exportCalls)
	}
	var storedCredential state.Credential
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		value, err := tx.GetActiveCredential(request.Owner, "vol1")
		storedCredential = value
		return err
	}); err != nil {
		t.Fatalf("stored credential: %v", err)
	}
	if storedCredential.State != "active" {
		t.Fatalf("credential state = %q", storedCredential.State)
	}
	replay, err := manager.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.OperationID != result.OperationID || lvmBackend.createCalls != 1 || exporter.exportCalls != 1 {
		t.Fatalf("replay result=%#v createCalls=%d exportCalls=%d", replay, lvmBackend.createCalls, exporter.exportCalls)
	}
}

func TestCreateDifferentParametersRejected(t *testing.T) {
	manager, _, _, _, store := newTestManager(t)
	request := createRequest(t, "vol1", "resource-1", 64<<20, false)
	if _, err := manager.Run(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	changed := createRequest(t, "vol1", "resource-2", 64<<20, false)
	if _, err := manager.Run(context.Background(), changed); !errors.Is(err, ErrVolumeAlreadyExists) {
		t.Fatalf("changed create = %v, want ErrVolumeAlreadyExists", err)
	}
	var count int
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		ops, err := tx.ListOperations(request.Owner, "vol1")
		count = len(ops)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("operation count = %d, want 1", count)
	}
}

func TestUnexportRequiresForceAndSupersedesCredential(t *testing.T) {
	manager, _, exporter, _, store := newTestManager(t)
	create := createRequest(t, "vol1", "resource-1", 64<<20, true)
	if _, err := manager.Run(context.Background(), create); err != nil {
		t.Fatal(err)
	}
	exporter.observation.SessionState = target.SessionActive
	unexportIntent, _ := json.Marshal(UnexportIntent{Force: false})
	unexport := Request{Owner: create.Owner, VolumeID: "vol1", Action: ActionUnexport, Intent: unexportIntent}
	if _, err := manager.Run(context.Background(), unexport); !errors.Is(err, ErrSessionActive) {
		t.Fatalf("unexport active = %v, want ErrSessionActive", err)
	}
	unexportIntent, _ = json.Marshal(UnexportIntent{Force: true})
	unexport.Intent = unexportIntent
	result, err := manager.Run(context.Background(), unexport)
	if err != nil {
		t.Fatalf("forced unexport: %v", err)
	}
	if result.Volume.State != "available" || result.Export.State != "unexported" {
		t.Fatalf("unexport result: %#v", result)
	}
	credentialMissing := false
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		_, err := tx.GetActiveCredential(create.Owner, "vol1")
		if errors.Is(err, state.ErrNotFound) {
			credentialMissing = true
			return nil
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !credentialMissing {
		t.Fatal("active credential remains after unexport")
	}
}

func TestDeleteForceAndSessionGuard(t *testing.T) {
	manager, lvmBackend, exporter, _, store := newTestManager(t)
	create := createRequest(t, "vol1", "resource-1", 64<<20, true)
	if _, err := manager.Run(context.Background(), create); err != nil {
		t.Fatal(err)
	}
	exporter.observation.SessionState = target.SessionActive
	intent, _ := json.Marshal(DeleteIntent{Force: false})
	request := Request{Owner: create.Owner, VolumeID: "vol1", Action: ActionDelete, Intent: intent}
	if _, err := manager.Run(context.Background(), request); !errors.Is(err, ErrSessionActive) {
		t.Fatalf("delete active = %v, want ErrSessionActive", err)
	}
	intent, _ = json.Marshal(DeleteIntent{Force: true})
	request.Intent = intent
	result, err := manager.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("forced delete: %v", err)
	}
	if result.OperationID == "" {
		t.Fatal("delete result has no operation ID")
	}
	if _, err := getVolume(t, store, create.Owner, "vol1"); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("volume after delete = %v", err)
	}
	if lvmBackend.createCalls != 1 {
		t.Fatalf("unexpected create calls: %d", lvmBackend.createCalls)
	}
	var tombstone state.Tombstone
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		value, err := tx.GetTombstone(create.Owner, "vol1")
		tombstone = value
		return err
	}); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	if tombstone.ResourceUUID != "resource-1" {
		t.Fatalf("tombstone = %#v", tombstone)
	}
}

func TestResizeExtendsVolumeAndRefreshesExport(t *testing.T) {
	manager, lvmBackend, exporter, _, store := newTestManager(t)
	create := createRequest(t, "vol1", "resource-1", 64<<20, true)
	if _, err := manager.Run(context.Background(), create); err != nil {
		t.Fatal(err)
	}
	intent, _ := json.Marshal(ResizeIntent{SizeBytes: 128 << 20})
	request := Request{Owner: create.Owner, VolumeID: "vol1", Action: ActionResize, Intent: intent}
	result, err := manager.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	if result.Volume.ActualSizeBytes != 128<<20 || result.Volume.RequestedSizeBytes != 128<<20 ||
		result.Volume.State != "exported" {
		t.Fatalf("resize result: %#v", result)
	}
	if exporter.resizeCalls != 1 {
		t.Fatalf("resize calls = %d", exporter.resizeCalls)
	}
	if lv := lvmBackend.lvs[lvKey("vg1", result.Volume.LVName)]; lv.SizeBytes != 128<<20 {
		t.Fatalf("LV size = %d", lv.SizeBytes)
	}
	stored, err := getVolume(t, store, create.Owner, "vol1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.ActualSizeBytes != 128<<20 || stored.RequestedSizeBytes != 128<<20 {
		t.Fatalf("stored volume: %#v", stored)
	}
	if _, err := manager.Run(context.Background(), Request{
		Owner: create.Owner, VolumeID: "vol1", Action: ActionResize,
		Intent: mustJSON(t, ResizeIntent{SizeBytes: 32 << 20}),
	}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("shrink = %v, want ErrInvalidRequest", err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestExecutionFailureIsJournaledAndNotReplayed(t *testing.T) {
	manager, lvmBackend, _, _, store := newTestManager(t)
	lvmBackend.createErr = errors.New("storage offline")
	request := createRequest(t, "vol1", "resource-1", 64<<20, false)
	_, err := manager.Run(context.Background(), request)
	if err == nil {
		t.Fatal("Run succeeded despite LVM failure")
	}
	var operationError *OperationError
	if !errors.As(err, &operationError) || operationError.Code != "UNAVAILABLE" {
		t.Fatalf("error = %#v", err)
	}
	operation, err := getOperation(t, store, request)
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != "failed" {
		t.Fatalf("operation = %#v", operation)
	}
	volume, err := getVolume(t, store, request.Owner, "vol1")
	if err != nil {
		t.Fatal(err)
	}
	if volume.State != "failed" {
		t.Fatalf("volume = %#v", volume)
	}
	if _, err := manager.Run(context.Background(), request); !errors.Is(err, ErrOperationFailed) {
		t.Fatalf("replay = %v, want ErrOperationFailed", err)
	}
}

func TestTombstoneRetiresStaleDeleteOperation(t *testing.T) {
	manager, _, _, _, store := newTestManager(t)
	first := createRequest(t, "vol1", "resource-1", 64<<20, false)
	if _, err := manager.Run(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	deleteIntent, _ := json.Marshal(DeleteIntent{Force: true})
	deleteRequest := Request{Owner: first.Owner, VolumeID: "vol1", Action: ActionDelete, Intent: deleteIntent}
	if _, err := manager.Run(context.Background(), deleteRequest); err != nil {
		t.Fatalf("first delete: %v", err)
	}
	second := createRequest(t, "vol1", "resource-2", 64<<20, false)
	if _, err := manager.Run(context.Background(), second); err != nil {
		t.Fatalf("new create after tombstone: %v", err)
	}
	if _, err := manager.Run(context.Background(), deleteRequest); err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if _, err := getVolume(t, store, first.Owner, "vol1"); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("volume remains after stale delete repair: %v", err)
	}
}

func TestRetryRearmsFailedOperation(t *testing.T) {
	manager, lvmBackend, _, _, store := newTestManager(t)
	lvmBackend.createErr = errors.New("storage offline")
	request := createRequest(t, "vol1", "resource-1", 64<<20, false)
	if _, err := manager.Run(context.Background(), request); err == nil {
		t.Fatal("initial create unexpectedly succeeded")
	}
	lvmBackend.createErr = nil
	result, err := manager.Retry(context.Background(), request.Owner, "vol1")
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if result.Volume.State != "available" || result.OperationID == "" {
		t.Fatalf("retry result = %#v", result)
	}
	operation, err := getOperation(t, store, request)
	if err != nil {
		t.Fatal(err)
	}
	if operation.State != "succeeded" || operation.Attempt < 2 {
		t.Fatalf("operation after retry = %#v", operation)
	}
	volume, err := getVolume(t, store, request.Owner, "vol1")
	if err != nil || volume.State != "available" {
		t.Fatalf("volume after retry = %#v, %v", volume, err)
	}
}

func TestExportDifferentInitiatorRejected(t *testing.T) {
	manager, _, _, _, _ := newTestManager(t)
	create := createRequest(t, "vol1", "resource-1", 64<<20, false)
	if _, err := manager.Run(context.Background(), create); err != nil {
		t.Fatal(err)
	}
	exportIntent := ExportIntent{Protocol: "iscsi", Initiator: "iqn.one", TargetName: "iqn.target", Portal: "127.0.0.1:3260"}
	raw, _ := json.Marshal(exportIntent)
	exportRequest := Request{Owner: create.Owner, VolumeID: "vol1", Action: ActionExport, Intent: raw}
	if _, err := manager.Run(context.Background(), exportRequest); err != nil {
		t.Fatalf("first export: %v", err)
	}
	exportIntent.Initiator = "iqn.two"
	raw, _ = json.Marshal(exportIntent)
	exportRequest.Intent = raw
	if _, err := manager.Run(context.Background(), exportRequest); !errors.Is(err, ErrVolumeAlreadyExport) {
		t.Fatalf("different initiator = %v, want ErrVolumeAlreadyExport", err)
	}
}

func exportRequest(t *testing.T, owner, volumeID string, intent ExportIntent) Request {
	t.Helper()
	raw, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	return Request{Owner: owner, VolumeID: volumeID, Action: ActionExport, Intent: raw}
}

func activeCredential(t *testing.T, store *state.Store, owner, volumeID string) state.Credential {
	t.Helper()
	var value state.Credential
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		var err error
		value, err = tx.GetActiveCredential(owner, volumeID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestExportRotationAfterUnexportUsesNewCredential(t *testing.T) {
	manager, _, exporter, _, store := newTestManager(t)
	create := createRequest(t, "vol1", "resource-1", 64<<20, false)
	if _, err := manager.Run(context.Background(), create); err != nil {
		t.Fatal(err)
	}
	intent := ExportIntent{
		Protocol: "iscsi", Initiator: "iqn.one", TargetName: "iqn.target", Portal: "127.0.0.1:3260",
		RotateCredential: true, AttachmentID: "node-a",
	}
	export := exportRequest(t, create.Owner, "vol1", intent)
	if _, err := manager.Run(context.Background(), export); err != nil {
		t.Fatal(err)
	}
	first := activeCredential(t, store, create.Owner, "vol1")
	unexportIntent, _ := json.Marshal(UnexportIntent{Force: true})
	unexport := Request{Owner: create.Owner, VolumeID: "vol1", Action: ActionUnexport, Intent: unexportIntent}
	if _, err := manager.Run(context.Background(), unexport); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Run(context.Background(), export); err != nil {
		t.Fatalf("reattach export: %v", err)
	}
	second := activeCredential(t, store, create.Owner, "vol1")
	if first.ID == second.ID {
		t.Fatalf("credential was not rotated: %q", first.ID)
	}
	if exporter.exportCalls != 2 {
		t.Fatalf("export calls = %d, want 2", exporter.exportCalls)
	}
}

func TestExportRotationDifferentInitiator(t *testing.T) {
	manager, _, exporter, _, store := newTestManager(t)
	create := createRequest(t, "vol1", "resource-1", 64<<20, false)
	if _, err := manager.Run(context.Background(), create); err != nil {
		t.Fatal(err)
	}
	firstIntent := ExportIntent{
		Protocol: "iscsi", Initiator: "iqn.one", TargetName: "iqn.target", Portal: "127.0.0.1:3260",
		RotateCredential: true, AttachmentID: "node-a",
	}
	if _, err := manager.Run(context.Background(), exportRequest(t, create.Owner, "vol1", firstIntent)); err != nil {
		t.Fatal(err)
	}
	first := activeCredential(t, store, create.Owner, "vol1")
	secondIntent := firstIntent
	secondIntent.Initiator = "iqn.two"
	secondIntent.AttachmentID = "node-b"
	if _, err := manager.Run(context.Background(), exportRequest(t, create.Owner, "vol1", secondIntent)); err != nil {
		t.Fatalf("rotate to new initiator: %v", err)
	}
	second := activeCredential(t, store, create.Owner, "vol1")
	if first.ID == second.ID {
		t.Fatalf("credential was not rotated: %q", first.ID)
	}
	if exporter.exportCalls != 2 {
		t.Fatalf("export calls = %d, want 2", exporter.exportCalls)
	}
}

func TestExportRotationRequiresForceForActiveSession(t *testing.T) {
	manager, _, exporter, _, _ := newTestManager(t)
	create := createRequest(t, "vol1", "resource-1", 64<<20, false)
	if _, err := manager.Run(context.Background(), create); err != nil {
		t.Fatal(err)
	}
	intent := ExportIntent{
		Protocol: "iscsi", Initiator: "iqn.one", TargetName: "iqn.target", Portal: "127.0.0.1:3260",
		RotateCredential: true, AttachmentID: "node-a",
	}
	if _, err := manager.Run(context.Background(), exportRequest(t, create.Owner, "vol1", intent)); err != nil {
		t.Fatal(err)
	}
	exporter.observation.SessionState = target.SessionActive
	intent.Initiator = "iqn.two"
	intent.AttachmentID = "node-b"
	if _, err := manager.Run(context.Background(), exportRequest(t, create.Owner, "vol1", intent)); !errors.Is(err, ErrSessionActive) {
		t.Fatalf("unforced rotation = %v, want ErrSessionActive", err)
	}
	intent.Force = true
	if _, err := manager.Run(context.Background(), exportRequest(t, create.Owner, "vol1", intent)); err != nil {
		t.Fatalf("forced rotation: %v", err)
	}
}

func TestExportRotationIsIdempotentForSameAttachment(t *testing.T) {
	manager, _, exporter, _, store := newTestManager(t)
	create := createRequest(t, "vol1", "resource-1", 64<<20, false)
	if _, err := manager.Run(context.Background(), create); err != nil {
		t.Fatal(err)
	}
	intent := ExportIntent{
		Protocol: "iscsi", Initiator: "iqn.one", TargetName: "iqn.target", Portal: "127.0.0.1:3260",
		RotateCredential: true, AttachmentID: "node-a",
	}
	export := exportRequest(t, create.Owner, "vol1", intent)
	if _, err := manager.Run(context.Background(), export); err != nil {
		t.Fatal(err)
	}
	first := activeCredential(t, store, create.Owner, "vol1")
	if _, err := manager.Run(context.Background(), export); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	second := activeCredential(t, store, create.Owner, "vol1")
	if first.ID != second.ID {
		t.Fatalf("idempotent retry rotated %q -> %q", first.ID, second.ID)
	}
	if exporter.exportCalls != 1 {
		t.Fatalf("export calls = %d, want 1", exporter.exportCalls)
	}
}

func TestIntentParsersAndInternalHelpers(t *testing.T) {
	if _, err := parseCreateIntent([]byte(`{}`)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid create = %v", err)
	}
	if _, err := parseExportIntent([]byte(`{}`)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid export = %v", err)
	}
	if _, err := parseResizeIntent([]byte(`{}`)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid resize = %v", err)
	}
	if _, err := parseUnexportIntent([]byte(`{`)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid unexport = %v", err)
	}
	if _, err := parseDeleteIntent([]byte(`{`)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("invalid delete = %v", err)
	}
	if intent, err := parseUnexportIntent(nil); err != nil || intent.Force {
		t.Fatalf("empty unexport = %#v, %v", intent, err)
	}
	for _, test := range []struct {
		err  error
		code string
	}{
		{target.ErrSessionActive, "SESSION_ACTIVE"},
		{target.ErrSessionUnknown, "SESSION_UNKNOWN"},
		{lvm.ErrResourceExhausted, "RESOURCE_EXHAUSTED"},
		{state.ErrNotFound, "NOT_FOUND"},
		{context.DeadlineExceeded, "DEADLINE_EXCEEDED"},
		{lvm.ErrInvalid, "CONFLICT"},
		{errors.New("other"), "UNAVAILABLE"},
	} {
		if code, _, _ := classifyFailure(test.err); code != test.code {
			t.Fatalf("classify %v = %q, want %q", test.err, code, test.code)
		}
	}
	manager, _, _, _, store := newTestManager(t)
	if err := manager.Resume(context.Background()); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if _, err := manager.targetFor("nvme-tcp"); err == nil {
		t.Fatal("unconfigured NVMe target did not fail")
	}
	if exporter, err := manager.targetFor("iscsi"); err != nil || exporter == nil {
		t.Fatalf("iSCSI target = %#v, %v", exporter, err)
	}
	if _, err := manager.targetFor("other"); err == nil {
		t.Fatal("unknown target did not fail")
	}
	if (systemClock{}).Now().IsZero() {
		t.Fatal("system clock is zero")
	}
	converted := stateToCredential(state.Credential{ID: "id", Owner: "owner", Protocol: "iscsi"})
	if converted.ID != "id" || converted.Protocol != credential.ProtocolISCSI {
		t.Fatalf("state credential = %#v", converted)
	}
	_ = store
}

func TestOperationManagerErrorPaths(t *testing.T) {
	manager, _, _, _, _ := newTestManager(t)
	ctx := context.Background()
	if _, err := manager.Run(ctx, Request{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty request = %v", err)
	}
	if _, err := manager.Run(ctx, Request{Owner: "owner", VolumeID: "vol", Action: "unknown", Intent: []byte(`{}`)}); !errors.Is(err, ErrUnsupportedAction) {
		t.Fatalf("unknown action = %v", err)
	}
	if _, err := manager.Run(ctx, Request{Owner: "owner", VolumeID: "vol", Action: ActionCreate, Intent: []byte(`{`)}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("bad create intent = %v", err)
	}
	if _, err := manager.Run(ctx, Request{Owner: "owner", VolumeID: "vol", Action: ActionUnexport, Intent: []byte(`{}`)}); !errors.Is(err, ErrUnsupportedAction) && !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("missing unexport = %v", err)
	}
	if _, err := manager.Run(ctx, Request{
		Owner: "owner", VolumeID: "vol", Action: ActionResize,
		Intent: mustJSON(t, ResizeIntent{SizeBytes: 1}),
	}); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("missing resize = %v", err)
	}
	if _, err := manager.Run(ctx, Request{
		Owner: "owner", VolumeID: "vol", Action: ActionDelete,
		Intent: mustJSON(t, DeleteIntent{Force: true}),
	}); err != nil {
		t.Fatalf("missing delete = %v", err)
	}
	if _, err := manager.Retry(ctx, "", "vol"); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty retry = %v", err)
	}
	if _, err := manager.Retry(ctx, "owner", "vol"); !errors.Is(err, ErrOperationFailed) {
		t.Fatalf("missing retry = %v", err)
	}
}

func TestResizeAndDeleteGuards(t *testing.T) {
	manager, lvmBackend, _, _, store := newTestManager(t)
	create := createRequest(t, "vol1", "resource-1", 64<<20, false)
	if _, err := manager.Run(context.Background(), create); err != nil {
		t.Fatal(err)
	}
	lvmBackend.capacity.AllocatableBytes = 0
	if _, err := manager.Run(context.Background(), Request{
		Owner: create.Owner, VolumeID: "vol1", Action: ActionResize,
		Intent: mustJSON(t, ResizeIntent{SizeBytes: 128 << 20}),
	}); !errors.Is(err, ErrResourceExhausted) {
		t.Fatalf("resize without capacity = %v", err)
	}
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		volume, err := tx.GetVolume(create.Owner, "vol1")
		if err != nil {
			return err
		}
		volume.State = "drift"
		return tx.PutVolume(volume)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Run(context.Background(), Request{
		Owner: create.Owner, VolumeID: "vol1", Action: ActionResize,
		Intent: mustJSON(t, ResizeIntent{SizeBytes: 128 << 20}),
	}); !errors.Is(err, ErrVolumeInDrift) {
		t.Fatalf("drift resize = %v", err)
	}
	if _, err := manager.Run(context.Background(), Request{
		Owner: create.Owner, VolumeID: "vol1", Action: ActionDelete,
		Intent: mustJSON(t, DeleteIntent{Force: true}),
	}); !errors.Is(err, ErrVolumeInDrift) {
		t.Fatalf("drift delete = %v", err)
	}
	if _, err := manager.Run(context.Background(), Request{
		Owner: create.Owner, VolumeID: "vol1", Action: ActionUnexport,
		Intent: mustJSON(t, UnexportIntent{Force: true}),
	}); !errors.Is(err, ErrVolumeInDrift) {
		t.Fatalf("drift unexport = %v", err)
	}
}

func TestOperationErrorAndDefaults(t *testing.T) {
	plain := &OperationError{Code: "CODE", Err: errors.New("cause")}
	if plain.Error() == "" || !errors.Is(plain, plain.Err) {
		t.Fatal("plain operation error did not expose its cause")
	}
	withID := &OperationError{Code: "CODE", OperationID: "op", Err: errors.New("cause")}
	if withID.Error() == "" {
		t.Fatal("operation error with ID is empty")
	}
	manager := New(nil, nil, nil, nil, nil, Options{})
	if manager.opts.MaxAttempts <= 0 || manager.opts.TotalTimeout <= 0 || manager.opts.Clock == nil {
		t.Fatalf("defaults = %#v", manager.opts)
	}
}

func TestCreateCapacityGuard(t *testing.T) {
	manager, lvmBackend, _, _, _ := newTestManager(t)
	lvmBackend.capacity.AllocatableBytes = 0
	request := createRequest(t, "vol-capacity", "resource-capacity", 1<<20, false)
	if _, err := manager.Run(context.Background(), request); !errors.Is(err, ErrResourceExhausted) {
		t.Fatalf("capacity guard = %v", err)
	}
}

func TestResizeExportFailure(t *testing.T) {
	manager, _, exporter, _, _ := newTestManager(t)
	create := createRequest(t, "vol-resize", "resource-resize", 64<<20, true)
	if _, err := manager.Run(context.Background(), create); err != nil {
		t.Fatal(err)
	}
	exporter.resizeErr = errors.New("resize target failed")
	if _, err := manager.Run(context.Background(), Request{
		Owner: create.Owner, VolumeID: "vol-resize", Action: ActionResize,
		Intent: mustJSON(t, ResizeIntent{SizeBytes: 128 << 20}),
	}); err == nil {
		t.Fatal("target resize failure did not fail")
	}
}
