package bdd_test

import (
	"context"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/voidstarit/kouja/internal/auth"
	"github.com/voidstarit/kouja/internal/credential"
	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/target"
)

func TestBDD(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "kouja BDD Suite")
}

type fakeLVM struct {
	volumes  map[string]lvm.LV
	capacity lvm.Capacity
}

func (backend *fakeLVM) Create(_ context.Context, vg, name string, bytes int64, _ map[string]string) (lvm.LV, error) {
	volume := lvm.LV{VG: vg, Name: name, SizeBytes: bytes, DevicePath: "/dev/" + vg + "/" + name}
	backend.volumes[vg+"/"+name] = volume
	return volume, nil
}

func (backend *fakeLVM) Extend(_ context.Context, vg, name string, bytes int64) (lvm.LV, error) {
	volume := backend.volumes[vg+"/"+name]
	volume.SizeBytes = bytes
	backend.volumes[vg+"/"+name] = volume
	return volume, nil
}

func (backend *fakeLVM) Shrink(ctx context.Context, vg, name string, bytes int64) (lvm.LV, error) {
	return backend.Extend(ctx, vg, name, bytes)
}

func (backend *fakeLVM) Remove(_ context.Context, vg, name string) error {
	delete(backend.volumes, vg+"/"+name)
	return nil
}

func (backend *fakeLVM) Activate(context.Context, string, string) error { return nil }

func (backend *fakeLVM) Observe(_ context.Context, vg, name string) (lvm.LV, error) {
	volume, ok := backend.volumes[vg+"/"+name]
	if !ok {
		return lvm.LV{}, lvm.ErrNotFound
	}
	return volume, nil
}

func (backend *fakeLVM) ListManaged(context.Context) ([]lvm.LV, error) {
	result := make([]lvm.LV, 0, len(backend.volumes))
	for _, volume := range backend.volumes {
		result = append(result, volume)
	}
	return result, nil
}

func (backend *fakeLVM) ListCandidates(context.Context) ([]lvm.LV, error) {
	return backend.ListManaged(context.Background())
}

func (backend *fakeLVM) SetOwnerTag(context.Context, string, string, string) error { return nil }

func (backend *fakeLVM) Capacity(context.Context, string) (lvm.Capacity, error) {
	return backend.capacity, nil
}

func (backend *fakeLVM) Eligibility(context.Context, string) error { return nil }

type fakeExporter struct {
	exported bool
	sessions target.SessionState
}

func (exporter *fakeExporter) Export(_ context.Context, spec target.ExportSpec, secret target.Credential) (target.ExportResult, error) {
	exporter.exported = true
	return target.ExportResult{TargetName: spec.TargetName, Portal: spec.Portal, Credential: secret}, nil
}

func (exporter *fakeExporter) Unexport(_ context.Context, _ target.ExportSpec, force bool) error {
	if exporter.exported && exporter.sessions == target.SessionActive && !force {
		return target.ErrSessionActive
	}
	exporter.exported = false
	return nil
}

func (exporter *fakeExporter) Observe(context.Context, target.ExportSpec) (target.ExportObservation, error) {
	return target.ExportObservation{
		TargetPresent: exporter.exported, ACLPresent: exporter.exported, SessionState: exporter.sessions,
	}, nil
}

func (exporter *fakeExporter) ListSessions(context.Context) ([]target.Session, error) {
	return nil, nil
}
func (exporter *fakeExporter) Restore(context.Context, target.ExportSpec, target.Credential) error {
	return nil
}
func (exporter *fakeExporter) Delete(ctx context.Context, spec target.ExportSpec, force bool) error {
	return exporter.Unexport(ctx, spec, force)
}

type fakeCredentials struct {
	generated int
}

func (manager *fakeCredentials) Generate(_ context.Context, owner, volumeID, resourceUUID string, protocol credential.Protocol) (credential.Credential, error) {
	manager.generated++
	return credential.Credential{
		ID: "credential-" + resourceUUID, Owner: owner, VolumeID: volumeID,
		Protocol: protocol, SecretCiphertext: []byte("cipher"), Nonce: []byte("nonce"),
		KeyID: "key", KeyFingerprint: "fingerprint", State: "active", CreatedAt: time.Now().UTC(),
	}, nil
}

func (manager *fakeCredentials) Reveal(context.Context, credential.Credential) (target.Credential, error) {
	return target.Credential{Username: "kouja-user", Secret: "kouja-secret", Keyring: ".nvme"}, nil
}

func (manager *fakeCredentials) Get(context.Context, auth.Identity, string, string) (credential.Credential, error) {
	return credential.Credential{}, nil
}

func (manager *fakeCredentials) Rotate(context.Context, auth.Identity, string, string) (credential.Credential, error) {
	return credential.Credential{}, nil
}

func (manager *fakeCredentials) Install(context.Context, target.ExportSpec, credential.Credential) error {
	return nil
}

func (manager *fakeCredentials) Revoke(context.Context, target.ExportSpec) error { return nil }
