package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voidstarit/kouja/gen/kouja/v1"
	"github.com/voidstarit/kouja/internal/auth"
	"github.com/voidstarit/kouja/internal/config"
	"github.com/voidstarit/kouja/internal/state"
)

func TestAgentProtoMappings(t *testing.T) {
	observation := toProtoObservation(state.Observation{
		LVPresent: true, ObservedSizeBytes: 10, DevicePath: "/dev/x", ObservedAt: time.Now(), Source: "lvm",
	}, state.ExportObservation{
		TargetPresent: true, NamespacePresent: true, ACLPresent: true, ActiveSessions: 1,
		SessionState: "active", ObservedInitiator: "iqn",
	})
	if !observation.GetLvPresent() || !observation.GetTargetPresent() || observation.GetSessionState() != koujav1.SessionState_SESSION_STATE_ACTIVE {
		t.Fatalf("observation = %#v", observation)
	}
	for input, expected := range map[string]koujav1.VolumeState{
		"creating": koujav1.VolumeState_VOLUME_STATE_CREATING, "available": koujav1.VolumeState_VOLUME_STATE_AVAILABLE,
		"exporting": koujav1.VolumeState_VOLUME_STATE_EXPORTING, "exported": koujav1.VolumeState_VOLUME_STATE_EXPORTED,
		"resizing": koujav1.VolumeState_VOLUME_STATE_RESIZING, "deleting": koujav1.VolumeState_VOLUME_STATE_DELETING,
		"deleted": koujav1.VolumeState_VOLUME_STATE_DELETED, "failed": koujav1.VolumeState_VOLUME_STATE_FAILED,
		"drift": koujav1.VolumeState_VOLUME_STATE_DRIFT, "other": koujav1.VolumeState_VOLUME_STATE_UNSPECIFIED,
	} {
		if volumeState(input) != expected {
			t.Fatalf("volumeState(%q) = %v", input, volumeState(input))
		}
	}
	for input, expected := range map[string]koujav1.ExportState{
		"exporting": koujav1.ExportState_EXPORT_STATE_EXPORTING, "exported": koujav1.ExportState_EXPORT_STATE_EXPORTED,
		"unexporting": koujav1.ExportState_EXPORT_STATE_UNEXPORTING, "unexported": koujav1.ExportState_EXPORT_STATE_UNEXPORTED,
		"failed": koujav1.ExportState_EXPORT_STATE_FAILED, "drift": koujav1.ExportState_EXPORT_STATE_DRIFT,
		"other": koujav1.ExportState_EXPORT_STATE_UNSPECIFIED,
	} {
		if exportState(input) != expected {
			t.Fatalf("exportState(%q) = %v", input, exportState(input))
		}
	}
	for input, expected := range map[string]koujav1.SessionState{
		"none": koujav1.SessionState_SESSION_STATE_NONE, "active": koujav1.SessionState_SESSION_STATE_ACTIVE,
		"unknown": koujav1.SessionState_SESSION_STATE_UNKNOWN, "other": koujav1.SessionState_SESSION_STATE_UNSPECIFIED,
	} {
		if sessionState(input) != expected {
			t.Fatalf("sessionState(%q) = %v", input, sessionState(input))
		}
	}
	if len(driftReasons([]string{"lv_absent", "size_mismatch", "target_absent", "namespace_absent", "acl_absent", "initiator_mismatch", "unattributed_session"})) != 7 {
		t.Fatal("drift reason mapping incomplete")
	}
	admission := NewAdmission(0, 0, 0)
	if admission == nil {
		t.Fatal("nil admission")
	}
}

func TestCheckDependenciesWithFakeTools(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"lvs", "vgs", "pvs", "lvcreate", "lvremove", "lvchange", "udevadm", "findmnt", "blockdev", "systemctl"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // executable test fixture
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	cfg := config.Default()
	cfg.Targets.ISCSI.Enabled = false
	cfg.Targets.NVMeTCP.Enabled = false
	if err := CheckDependencies(context.Background(), &cfg); err != nil {
		t.Fatalf("CheckDependencies: %v", err)
	}
	if err := requirePath(filepath.Join(dir, "lvs"), "tool"); err != nil {
		t.Fatalf("requirePath: %v", err)
	}
}

func TestAgentAdmissionAndIdentityErrors(t *testing.T) {
	manager := &fakeCredentialManager{err: errors.New("missing")}
	service := NewVolumeService(newAgentStore(t), &fakeLVM{}, &fakeRunner{}, manager, ServiceOptions{})
	if _, err := service.GetInfo(context.Background(), &koujav1.GetInfoRequest{}); err != nil {
		t.Fatalf("GetInfo without identity: %v", err)
	}
	if _, err := service.GetInfo(WithIdentity(context.Background(), auth.Identity{Owner: "owner", Role: auth.RoleCaller}), &koujav1.GetInfoRequest{}); err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	if _, err := service.GetCredential(callerContext("owner"), &koujav1.GetCredentialRequest{VolumeId: "missing"}); err == nil {
		t.Fatal("missing credential did not fail")
	}
	if _, err := service.GetCapacity(context.Background(), &koujav1.GetCapacityRequest{}); err != nil {
		t.Fatalf("GetCapacity: %v", err)
	}
	_ = WithDriftRetrier(nil)
}

func TestInfoAndListBranches(t *testing.T) {
	store := newAgentStore(t)
	ready := false
	service := NewVolumeService(store, &fakeLVM{}, &fakeRunner{}, &fakeCredentialManager{}, ServiceOptions{
		HostUUID: "host", AgentVersion: "v1", APIVersion: "v1", NVMeEnabled: true, Ready: func() bool { return ready },
	})
	info, err := service.GetInfo(context.Background(), &koujav1.GetInfoRequest{})
	if err != nil || info.GetReady() || len(info.GetProtocols()) != 1 || info.GetProtocols()[0] != koujav1.Protocol_PROTOCOL_NVME_TCP {
		t.Fatalf("info = %#v, %v", info, err)
	}
	if _, err := service.ListVolumes(WithIdentity(context.Background(), auth.Identity{Owner: "admin", Role: auth.RoleAdmin}), &koujav1.ListVolumesRequest{PageSize: 1000}); err != nil {
		t.Fatalf("admin list: %v", err)
	}
	if _, err := service.GetCredential(callerContext("one"), &koujav1.GetCredentialRequest{VolumeId: "vol", Owner: "two"}); err == nil {
		t.Fatal("owner mismatch did not fail")
	}
	if _, err := service.GetVolume(callerContext("one"), &koujav1.GetVolumeRequest{VolumeId: "missing"}); err == nil {
		t.Fatal("missing volume did not fail")
	}
}

func TestCapacityAdmissionAndReadyBranches(t *testing.T) {
	store := newAgentStore(t)
	admission := NewAdmission(1, 1, 1)
	service := NewVolumeService(store, &fakeLVM{}, &fakeRunner{}, &fakeCredentialManager{}, ServiceOptions{
		HostUUID: "host", ISCSIEnabled: true, Admission: admission, Ready: func() bool { return true },
	})
	info, err := service.GetInfo(context.Background(), &koujav1.GetInfoRequest{})
	if err != nil || !info.GetReady() || len(info.GetProtocols()) != 1 {
		t.Fatalf("ready info = %#v, %v", info, err)
	}
	if _, err := service.GetCapacity(context.Background(), &koujav1.GetCapacityRequest{}); err != nil {
		t.Fatalf("first capacity: %v", err)
	}
	if _, err := service.GetCapacity(context.Background(), &koujav1.GetCapacityRequest{}); err == nil {
		t.Fatal("second capacity was not rate-limited")
	}
}

func TestVolumeServiceIdentityFailures(t *testing.T) {
	store := newAgentStore(t)
	service := NewVolumeService(store, &fakeLVM{}, &fakeRunner{}, &fakeCredentialManager{}, ServiceOptions{})
	if _, err := service.GetCredential(context.Background(), &koujav1.GetCredentialRequest{VolumeId: "vol"}); err == nil {
		t.Fatal("missing identity credential did not fail")
	}
	if _, err := service.ExportVolume(context.Background(), &koujav1.ExportVolumeRequest{VolumeId: "vol"}); err == nil {
		t.Fatal("missing identity export did not fail")
	}
	if _, err := service.ExportVolume(callerContext("owner"), &koujav1.ExportVolumeRequest{VolumeId: "missing", Initiator: "iqn", Protocol: koujav1.Protocol_PROTOCOL_ISCSI}); err == nil {
		t.Fatal("missing volume export did not fail")
	}
}

func TestCapacityUnavailableBranch(t *testing.T) {
	store := newAgentStore(t)
	addAgentVolume(t, store, "owner", "vol1", "resource-1", "available")
	service := NewVolumeService(store, &fakeLVM{capacityErr: errors.New("lvm offline")}, &fakeRunner{}, &fakeCredentialManager{}, ServiceOptions{})
	response, err := service.GetCapacity(context.Background(), &koujav1.GetCapacityRequest{})
	if err != nil || len(response.GetVgs()) != 1 || response.GetVgs()[0].GetVgState() != "unavailable" {
		t.Fatalf("capacity = %#v, %v", response, err)
	}
}

func TestRequestIDAndNilErrors(t *testing.T) {
	if statusError(nil) != nil {
		t.Fatal("nil error did not map to nil")
	}
	if requestID(context.Background(), "explicit") != "explicit" {
		t.Fatal("explicit request ID was not retained")
	}
	if requestID(WithRequestID(context.Background(), "context"), "") != "context" {
		t.Fatal("context request ID was not used")
	}
}
