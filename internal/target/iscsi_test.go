package target

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type removableFS struct {
	*ConfigFS
}

func (fs *removableFS) Remove(rel string) error {
	path, err := fs.Path(rel)
	if err != nil {
		return err
	}
	return os.RemoveAll(path)
}

func newTestISCSI(t *testing.T) (*ISCSI, *removableFS) {
	t.Helper()
	fs := &removableFS{ConfigFS: NewConfigFS(t.TempDir())}
	return NewISCSI(fs), fs
}

func testSpec() ExportSpec {
	return ExportSpec{
		Owner:        "owner",
		VolumeID:     "volume",
		ResourceUUID: "resource-1",
		VGName:       "vg1",
		LVName:       "lv1",
		Protocol:     "iscsi",
		Initiator:    "iqn.2026-11.io.kouja:initiator",
		TargetName:   "iqn.2026-11.io.kouja:target",
		Portal:       "127.0.0.1:3260",
	}
}

func testCredential() Credential {
	return Credential{Username: "kouja-user", Secret: "kouja-secret", Keyring: ""}
}

func addSessionFile(t *testing.T, fs *ConfigFS, targetName, value string) {
	t.Helper()
	dir := filepath.ToSlash(filepath.Join("target/iscsi", targetName, "fabric_statistics/iscsi_instance"))
	if err := fs.Mkdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := fs.Write(filepath.ToSlash(filepath.Join(dir, "sessions")), value); err != nil {
		t.Fatal(err)
	}
}

func TestISCSIExportObserveUnexportDelete(t *testing.T) {
	adapter, fs := newTestISCSI(t)
	spec := testSpec()
	result, err := adapter.Export(context.Background(), spec, testCredential())
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if result.TargetName != spec.TargetName || result.LUN != 0 || result.Credential.Username != "kouja-user" {
		t.Fatalf("Export result: %#v", result)
	}

	control, err := fs.ReadFile("target/core/iblock_0/lv1/control")
	if err != nil {
		t.Fatalf("read control: %v", err)
	}
	if control != "udev_path=/dev/vg1/lv1\n" {
		t.Fatalf("control = %q", control)
	}
	enable, err := fs.ReadFile("target/core/iblock_0/lv1/enable")
	if err != nil || enable != "1" {
		t.Fatalf("backstore enable = %q, %v", enable, err)
	}
	user, err := fs.ReadFile("target/iscsi/iqn.2026-11.io.kouja:target/tpgt_1/acls/iqn.2026-11.io.kouja:initiator/auth/userid")
	if err != nil {
		t.Fatalf("read userid: %v", err)
	}
	if user != "kouja-user" || strings.HasSuffix(user, "\n") {
		t.Fatalf("CHAP userid = %q, want exact bytes without newline", user)
	}
	password, err := fs.ReadFile("target/iscsi/iqn.2026-11.io.kouja:target/tpgt_1/acls/iqn.2026-11.io.kouja:initiator/auth/password")
	if err != nil || password != "kouja-secret" {
		t.Fatalf("CHAP password = %q, %v", password, err)
	}
	mappedLun := "target/iscsi/iqn.2026-11.io.kouja:target/tpgt_1/acls/iqn.2026-11.io.kouja:initiator/lun_0/resource-1"
	if !fs.Exists(mappedLun) {
		t.Fatal("ACL mapped LUN link is missing")
	}
	if !fs.Exists("target/iscsi/iqn.2026-11.io.kouja:target/tpgt_1/np/127.0.0.1:3260") {
		t.Fatal("portal is missing")
	}

	addSessionFile(t, fs.ConfigFS, spec.TargetName, "0\n")
	observation, err := adapter.Observe(context.Background(), spec)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !observation.TargetPresent || !observation.ACLPresent || observation.SessionState != SessionNone {
		t.Fatalf("observation = %#v", observation)
	}

	if err := fs.Write("target/iscsi/iqn.2026-11.io.kouja:target/fabric_statistics/iscsi_instance/sessions", "2\n"); err != nil {
		t.Fatal(err)
	}
	observation, err = adapter.Observe(context.Background(), spec)
	if err != nil {
		t.Fatalf("Observe active: %v", err)
	}
	if observation.SessionState != SessionActive || observation.ActiveSessions != 2 {
		t.Fatalf("active observation = %#v", observation)
	}
	if err := adapter.Unexport(context.Background(), spec, false); !errors.Is(err, ErrSessionActive) {
		t.Fatalf("Unexport without force = %v, want ErrSessionActive", err)
	}
	if err := adapter.Unexport(context.Background(), spec, true); err != nil {
		t.Fatalf("Unexport with force: %v", err)
	}
	if fs.Exists("target/iscsi/iqn.2026-11.io.kouja:target") {
		t.Fatal("target remains after Unexport")
	}
	if !fs.Exists("target/core/iblock_0/lv1") {
		t.Fatal("Unexport removed the backstore")
	}

	if err := adapter.Delete(context.Background(), spec, false); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if fs.Exists("target/core/iblock_0/lv1") {
		t.Fatal("Delete left the backstore")
	}
}

func TestISCSIUnexportUnknownSessionRequiresForce(t *testing.T) {
	adapter, fs := newTestISCSI(t)
	spec := testSpec()
	if _, err := adapter.Export(context.Background(), spec, testCredential()); err != nil {
		t.Fatalf("Export: %v", err)
	}
	observation, err := adapter.Observe(context.Background(), spec)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if observation.SessionState != SessionUnknown {
		t.Fatalf("session state = %q, want unknown", observation.SessionState)
	}
	if err := adapter.Unexport(context.Background(), spec, false); !errors.Is(err, ErrSessionUnknown) {
		t.Fatalf("Unexport without force = %v, want ErrSessionUnknown", err)
	}
	if err := adapter.Unexport(context.Background(), spec, true); err != nil {
		t.Fatalf("Unexport with force: %v", err)
	}
	if fs.Exists("target/iscsi/iqn.2026-11.io.kouja:target") {
		t.Fatal("target remains after forced Unexport")
	}
}

func TestISCSIListSessions(t *testing.T) {
	adapter, fs := newTestISCSI(t)
	addSessionFile(t, fs.ConfigFS, "iqn.2026-11.io.kouja:one", "0\n")
	addSessionFile(t, fs.ConfigFS, "iqn.2026-11.io.kouja:two", "1\n")
	addSessionFile(t, fs.ConfigFS, "iqn.2026-11.io.kouja:unknown", "not-a-number\n")
	sessions, err := adapter.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	states := map[string]SessionState{}
	for _, session := range sessions {
		states[session.Target] = session.State
	}
	if states["iqn.2026-11.io.kouja:one"] != SessionNone ||
		states["iqn.2026-11.io.kouja:two"] != SessionActive ||
		states["iqn.2026-11.io.kouja:unknown"] != SessionUnknown {
		t.Fatalf("session states: %#v", states)
	}
}

func TestISCSIValidationAndBackstoreConflict(t *testing.T) {
	adapter, fs := newTestISCSI(t)
	spec := testSpec()
	if _, err := adapter.Export(context.Background(), spec, Credential{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing credential = %v, want ErrInvalid", err)
	}
	badPortal := spec
	badPortal.Portal = "not-a-portal"
	if _, err := adapter.Export(context.Background(), badPortal, testCredential()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid portal = %v, want ErrInvalid", err)
	}
	badLUN := spec
	badLUN.LUN = 1
	if _, err := adapter.Export(context.Background(), badLUN, testCredential()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nonzero LUN = %v, want ErrInvalid", err)
	}
	if err := fs.Mkdir("target/core/iblock_0/lv1"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Write("target/core/iblock_0/lv1/udev_path", "/dev/other/lv\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Export(context.Background(), spec, testCredential()); !errors.Is(err, ErrConflict) {
		t.Fatalf("backstore conflict = %v, want ErrConflict", err)
	}
}

func TestConfigFSPathValidationAndSymlink(t *testing.T) {
	fs := NewConfigFS(t.TempDir())
	if _, err := fs.Path("../escape"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("escape path = %v, want ErrInvalid", err)
	}
	if _, err := fs.Path("/absolute"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("absolute path = %v, want ErrInvalid", err)
	}
	if err := fs.Mkdir("target/iscsi/iqn/tpgt_1/lun/lun_0"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Mkdir("target/core/iblock/lv"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Symlink("target/core/iblock/lv", "target/iscsi/iqn/tpgt_1/lun/lun_0/port"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Symlink("target/core/iblock/lv", "target/iscsi/iqn/tpgt_1/lun/lun_0/port"); err != nil {
		t.Fatalf("idempotent symlink: %v", err)
	}
	if err := fs.Symlink("target/core/other", "target/iscsi/iqn/tpgt_1/lun/lun_0/port"); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting symlink = %v, want ErrConflict", err)
	}
}

func TestISCSIResizeAndRestore(t *testing.T) {
	adapter, fs := newTestISCSI(t)
	spec := testSpec()
	if err := adapter.Restore(context.Background(), spec, testCredential()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !fs.Exists("target/core/iblock_0/lv1") {
		t.Fatal("restore did not create backstore")
	}
	if err := adapter.Resize(context.Background(), spec); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if err := adapter.Resize(context.Background(), ExportSpec{}); err == nil {
		t.Fatal("invalid resize did not fail")
	}
}
