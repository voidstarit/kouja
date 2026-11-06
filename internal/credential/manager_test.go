package credential

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/voidstarit/kouja/internal/auth"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
)

func openCredentialStore(t testing.TB) *state.Store {
	t.Helper()
	store, err := state.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func putCredentialVolume(t testing.TB, store *state.Store, owner, volumeID, resourceUUID string) {
	t.Helper()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	err := store.Transact(context.Background(), func(tx state.Tx) error {
		if err := tx.PutVG(state.VG{Name: "vg1", UUID: "vg-uuid", State: "active", RegisteredAt: now, UpdatedAt: now}); err != nil {
			return err
		}
		return tx.PutVolume(state.Volume{
			Owner: owner, VolumeID: volumeID, ResourceUUID: resourceUUID,
			LVName: "kouja-" + resourceUUID, VGName: "vg1",
			RequestedSizeBytes: 1 << 20, ActualSizeBytes: 1 << 20,
			State: "available", CreatedAt: now, UpdatedAt: now,
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func bddTestGenerateRevealAndKeyFile(t testing.TB) {
	store := openCredentialStore(t)
	keyPath := filepath.Join(t.TempDir(), "secret.key")
	manager, err := Open(store, keyPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if info.Mode().Perm() != 0o600 || info.Size() != keySize {
		t.Fatalf("key file mode=%o size=%d", info.Mode().Perm(), info.Size())
	}
	record, err := manager.Generate(context.Background(), "owner", "volume", "resource-1", ProtocolISCSI)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if record.State != "active" || record.KeyID == "" || record.KeyFingerprint == "" ||
		len(record.SecretCiphertext) == 0 || len(record.Nonce) != 12 {
		t.Fatalf("credential = %#v", record)
	}
	plain, err := manager.Reveal(context.Background(), record)
	if err != nil {
		t.Fatalf("Reveal: %v", err)
	}
	if plain.Username != "kouja-resource-1" || plain.Secret == "" {
		t.Fatalf("plaintext = %#v", plain)
	}
	corrupt := record
	corrupt.KeyFingerprint = "different"
	if _, err := manager.Reveal(context.Background(), corrupt); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("wrong key fingerprint = %v, want ErrKeyMismatch", err)
	}
	reopened, err := Open(store, keyPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := reopened.Reveal(context.Background(), record); err != nil {
		t.Fatalf("reveal after reopen: %v", err)
	}
}

func bddTestGetAuthorizationAndGrants(t testing.TB) {
	store := openCredentialStore(t)
	owner := "spiffe://kouja/caller/owner"
	putCredentialVolume(t, store, owner, "volume", "resource-1")
	manager, err := Open(store, filepath.Join(t.TempDir(), "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := manager.Generate(context.Background(), owner, "volume", "resource-1", ProtocolISCSI)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		return tx.PutCredential(toState(record))
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Get(context.Background(), auth.Identity{Owner: owner, Role: auth.RoleCaller}, owner, "volume"); err != nil {
		t.Fatalf("owner Get: %v", err)
	}
	if _, err := manager.Get(context.Background(), auth.Identity{Owner: "spiffe://kouja/caller/other", Role: auth.RoleCaller}, owner, "volume"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("non-owner Get = %v, want ErrUnauthorized", err)
	}
	if _, err := manager.Get(context.Background(), auth.Identity{Owner: "spiffe://kouja/admin/one", Role: auth.RoleAdmin}, owner, "volume"); err != nil {
		t.Fatalf("admin Get: %v", err)
	}
	node := "spiffe://kouja/node/one"
	now := time.Now().UTC()
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		return tx.PutGrant(state.Grant{
			Owner: owner, VolumeID: "volume", NodeID: "node-1", NodeCallerID: node,
			ExpiresAt: now.Add(time.Hour), CreatedAt: now,
		})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Get(context.Background(), auth.Identity{Owner: node, Role: auth.RoleNode}, owner, "volume"); err != nil {
		t.Fatalf("node Get: %v", err)
	}
	expired := node + "-expired"
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		return tx.PutGrant(state.Grant{
			Owner: owner, VolumeID: "volume", NodeID: "node-2", NodeCallerID: expired,
			ExpiresAt: now.Add(-time.Hour), CreatedAt: now.Add(-2 * time.Hour),
		})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Get(context.Background(), auth.Identity{Owner: expired, Role: auth.RoleNode}, owner, "volume"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired grant = %v, want ErrUnauthorized", err)
	}
}

func bddTestRotateReplacesActiveCredential(t testing.TB) {
	store := openCredentialStore(t)
	owner := "spiffe://kouja/caller/owner"
	putCredentialVolume(t, store, owner, "volume", "resource-1")
	manager, err := Open(store, filepath.Join(t.TempDir(), "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	old, err := manager.Generate(context.Background(), owner, "volume", "resource-1", ProtocolISCSI)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		return tx.PutCredential(toState(old))
	}); err != nil {
		t.Fatal(err)
	}
	replacement, err := manager.Rotate(context.Background(), auth.Identity{Owner: owner, Role: auth.RoleCaller}, owner, "volume")
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if replacement.ID == old.ID {
		t.Fatal("Rotate returned the old ID")
	}
	if _, err := manager.Reveal(context.Background(), replacement); err != nil {
		t.Fatalf("reveal replacement: %v", err)
	}
	if _, err := manager.Rotate(context.Background(), auth.Identity{Owner: "spiffe://kouja/caller/other", Role: auth.RoleCaller}, owner, "volume"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("non-owner Rotate = %v, want ErrUnauthorized", err)
	}
	var active state.Credential
	if err := store.Transact(context.Background(), func(tx state.Tx) error {
		value, err := tx.GetActiveCredential(owner, "volume")
		active = value
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if active.ID != replacement.ID {
		t.Fatalf("active credential = %s, want %s", active.ID, replacement.ID)
	}
}

func bddTestInstallRevokeAndNVMeGeneration(t testing.TB) {
	store := openCredentialStore(t)
	owner := "spiffe://kouja/caller/owner"
	putCredentialVolume(t, store, owner, "volume", "resource-1")
	bin := t.TempDir()
	script := filepath.Join(bin, "nvme")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nif [ \"$1\" = gen-tls-key ]; then echo 'NVMeTLSkey-1:01:abc:'; fi\nexit 0\n"), 0o755); err != nil { //nolint:gosec // executable test fixture
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	manager, err := Open(store, filepath.Join(t.TempDir(), "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := manager.Generate(context.Background(), owner, "volume", "resource-1", ProtocolNVMeTCP)
	if err != nil {
		t.Fatalf("NVMe Generate: %v", err)
	}
	if record.Protocol != ProtocolNVMeTCP {
		t.Fatalf("protocol = %q", record.Protocol)
	}
	if err := manager.Install(context.Background(), targetSpec(owner, "volume"), record); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := manager.Revoke(context.Background(), targetSpec(owner, "volume")); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil { //nolint:gosec // executable test fixture
		t.Fatal(err)
	}
	if err := manager.Install(context.Background(), targetSpec(owner, "volume"), record); err == nil {
		t.Fatal("failing nvme install did not fail")
	}
	if _, err := manager.Generate(context.Background(), owner, "volume", "resource-1", Protocol("bogus")); err == nil {
		t.Fatal("unsupported protocol did not fail")
	}
}

func targetSpec(owner, volumeID string) target.ExportSpec {
	return target.ExportSpec{
		Owner: owner, VolumeID: volumeID, ResourceUUID: "resource-1", VGName: "vg1",
		LVName: "kouja-resource-1", Protocol: "nvme-tcp", Initiator: "nqn.initiator",
		TargetName: "nqn.target", Portal: "127.0.0.1:4420",
	}
}

func bddTestCredentialErrorPaths(t testing.TB) {
	store := openCredentialStore(t)
	if _, err := Open(nil, filepath.Join(t.TempDir(), "key")); err == nil {
		t.Fatal("nil store Open did not fail")
	}
	if _, err := Open(store, ""); err == nil {
		t.Fatal("empty key path Open did not fail")
	}
	badKey := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(badKey, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(store, badKey); err == nil {
		t.Fatal("short key did not fail")
	}
	manager, err := Open(store, filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty"))
	if _, err := manager.Generate(context.Background(), "owner", "volume", "resource", ProtocolNVMeTCP); err == nil {
		t.Fatal("missing nvme command did not fail")
	}
	record, err := manager.Generate(context.Background(), "owner", "volume", "resource", ProtocolISCSI)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := record
	corrupt.SecretCiphertext = []byte("corrupt")
	if _, err := manager.Reveal(context.Background(), corrupt); err == nil {
		t.Fatal("corrupt ciphertext did not fail")
	}
	install := record
	install.Protocol = ProtocolISCSI
	spec := targetSpec("owner", "volume")
	spec.Protocol = "iscsi"
	if err := manager.Install(context.Background(), spec, install); err != nil {
		t.Fatalf("iSCSI Install: %v", err)
	}
}

func bddTestCredentialFileOpenErrors(t testing.TB) {
	store := openCredentialStore(t)
	directory := t.TempDir()
	if _, err := Open(store, directory); err == nil {
		t.Fatal("directory key path did not fail")
	}
	manager, err := Open(store, filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Generate(context.Background(), "", "", "", ProtocolISCSI); err == nil {
		t.Fatal("invalid generate request did not fail")
	}
}

func bddTestCredentialKeyDirectoryErrors(t testing.TB) {
	store := openCredentialStore(t)
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(store, filepath.Join(blocker, "key")); err == nil {
		t.Fatal("key below a file did not fail")
	}
}

func bddTestRotateWithoutCredential(t testing.TB) {
	store := openCredentialStore(t)
	owner := "spiffe://kouja/caller/owner"
	putCredentialVolume(t, store, owner, "volume", "resource-1")
	manager, err := Open(store, filepath.Join(t.TempDir(), "key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Rotate(context.Background(), auth.Identity{Owner: owner, Role: auth.RoleCaller}, owner, "volume"); err == nil {
		t.Fatal("rotate without credential did not fail")
	}
}
