package target

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func nvmeSpec() ExportSpec {
	return ExportSpec{
		Owner: "owner", VolumeID: "volume", ResourceUUID: "resource-1",
		VGName: "vg1", LVName: "lv1", Protocol: "nvme-tcp",
		Initiator:  "nqn.2026-11.io.kouja:consumer",
		TargetName: "nqn.2026-11.io.kouja:host:resource-1",
		Portal:     "10.10.0.1:4420",
	}
}

func bddTestNVMeExportObserveUnexport(t testing.TB) {
	fs := &removableFS{ConfigFS: NewConfigFS(t.TempDir())}
	adapter := NewNVMe(fs)
	spec := nvmeSpec()
	result, err := adapter.Export(context.Background(), spec, Credential{})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if result.TargetName != spec.TargetName || result.NSID != 1 || result.Portal != spec.Portal {
		t.Fatalf("result = %#v", result)
	}
	subsystem := "nvmet/subsystems/" + spec.TargetName
	for _, path := range []string{
		subsystem + "/attr_allow_any_host",
		subsystem + "/namespaces/1/device_path",
		subsystem + "/namespaces/1/enable",
		"nvmet/ports/1/addr_tsas",
		"nvmet/ports/1/addr_treq",
		"nvmet/ports/1/subsystems/" + spec.TargetName,
	} {
		if !fs.Exists(path) {
			t.Fatalf("missing %s", path)
		}
	}
	trsvcid, err := fs.ReadFile("nvmet/ports/1/addr_trsvcid")
	if err != nil || trsvcid != "4420" {
		t.Fatalf("trsvcid = %q, %v", trsvcid, err)
	}
	observation, err := adapter.Observe(context.Background(), spec)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !observation.TargetPresent || !observation.NamespacePresent || !observation.ACLPresent ||
		observation.SessionState != SessionUnknown {
		t.Fatalf("observation = %#v", observation)
	}
	if err := adapter.Unexport(context.Background(), spec, false); !errors.Is(err, ErrSessionUnknown) {
		t.Fatalf("Unexport without force = %v, want ErrSessionUnknown", err)
	}
	if err := adapter.Unexport(context.Background(), spec, true); err != nil {
		t.Fatalf("forced Unexport: %v", err)
	}
	if fs.Exists(subsystem) {
		t.Fatal("subsystem remains after Unexport")
	}
}

func bddTestNVMeValidation(t testing.TB) {
	adapter := NewNVMe(&removableFS{ConfigFS: NewConfigFS(t.TempDir())})
	bad := nvmeSpec()
	bad.Portal = "not-a-portal"
	if _, err := adapter.Export(context.Background(), bad, Credential{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad portal = %v, want ErrInvalid", err)
	}
	bad = nvmeSpec()
	bad.Initiator = "bad/initiator"
	if _, err := adapter.Export(context.Background(), bad, Credential{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad initiator = %v, want ErrInvalid", err)
	}
}

func bddTestNVMeResizeDeleteRestoreAndSessions(t testing.TB) {
	fs := &removableFS{ConfigFS: NewConfigFS(t.TempDir())}
	adapter := NewNVMe(fs)
	spec := nvmeSpec()
	if _, err := adapter.Export(context.Background(), spec, Credential{}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Resize(context.Background(), spec); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if err := adapter.Delete(context.Background(), spec, true); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := adapter.Delete(context.Background(), spec, true); err != nil {
		t.Fatalf("second Delete: %v", err)
	}
	if sessions, err := adapter.ListSessions(context.Background()); err != nil || len(sessions) != 0 {
		t.Fatalf("sessions = %#v, %v", sessions, err)
	}
	if err := adapter.Restore(context.Background(), spec, Credential{}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if !fs.Exists("nvmet/subsystems/" + spec.TargetName) {
		t.Fatal("Restore did not create subsystem")
	}
	if err := adapter.Resize(context.Background(), nvmeSpec()); err != nil {
		t.Fatalf("resize missing subsystem: %v", err)
	}
}

func bddTestConfigFSPathAndRemove(t testing.TB) {
	fs := NewConfigFS(t.TempDir())
	if fs.Root() == "" {
		t.Fatal("empty root")
	}
	if err := fs.Mkdir("a/b"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Write("a/b/value", "x"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Symlink("a/b", "a/link"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Symlink("a/b", "a/link"); err != nil {
		t.Fatalf("idempotent symlink: %v", err)
	}
	if entries, err := fs.ReadDir("a"); err != nil || len(entries) < 2 {
		t.Fatalf("ReadDir = %#v, %v", entries, err)
	}
	if err := fs.Remove("a/link"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Remove("a/link"); err != nil {
		t.Fatalf("idempotent remove: %v", err)
	}
	if err := fs.Remove("a/b/value"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Remove("a/b"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Remove("a"); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Path("../escape"); err == nil {
		t.Fatal("path escape did not fail")
	}
	if err := fs.Mkdir("/absolute"); err == nil {
		t.Fatal("absolute mkdir did not fail")
	}
	if err := fs.Write("missing/parent", "x"); err == nil {
		t.Fatal("write missing parent did not fail")
	}
	if _, err := fs.ReadFile("missing"); err == nil {
		t.Fatal("read missing file did not fail")
	}
}

func bddTestConfigFSErrorBranches(t testing.TB) {
	fs := NewConfigFS(t.TempDir())
	if _, err := fs.Path(""); err == nil {
		t.Fatal("empty path did not fail")
	}
	if _, err := fs.Path("../escape"); err == nil {
		t.Fatal("escape path did not fail")
	}
	if err := fs.Remove(""); err == nil {
		t.Fatal("empty remove did not fail")
	}
	if _, err := fs.ReadDir("missing"); err == nil {
		t.Fatal("missing read dir did not fail")
	}
	if err := fs.Mkdir("a"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Write("a/value", "x"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Symlink("a", "a/link"); err != nil {
		t.Fatal(err)
	}
	if err := fs.Symlink("a", "a/link"); err != nil {
		t.Fatalf("same symlink: %v", err)
	}
	if err := fs.Symlink("a/value", "a/link"); err == nil {
		t.Fatal("conflicting symlink did not fail")
	}
	if err := fs.Mkdir("/absolute"); err == nil {
		t.Fatal("absolute path did not fail")
	}
	if err := fs.Write("missing/value", "x"); err == nil {
		t.Fatal("missing write did not fail")
	}
	if _, err := fs.ReadFile("missing"); err == nil {
		t.Fatal("missing read did not fail")
	}
	if fs.Exists("missing") {
		t.Fatal("missing path exists")
	}
}

type failingFS struct {
	FileSystem
	marker string
}

func (fs failingFS) Mkdir(rel string) error {
	if strings.Contains(rel, fs.marker) {
		return errors.New("injected mkdir failure")
	}
	return fs.FileSystem.Mkdir(rel)
}
func (fs failingFS) Write(rel, value string) error {
	if strings.Contains(rel, fs.marker) {
		return errors.New("injected write failure")
	}
	return fs.FileSystem.Write(rel, value)
}
func (fs failingFS) Symlink(target, link string) error {
	if strings.Contains(link, fs.marker) {
		return errors.New("injected symlink failure")
	}
	return fs.FileSystem.Symlink(target, link)
}

func bddTestNVMeExportAndResizeErrorBranches(t testing.TB) {
	for _, marker := range []string{"attr_allow_any_host", "allowed_hosts", "namespaces/1/device_path", "subsystems/"} {
		base := &removableFS{ConfigFS: NewConfigFS(t.TempDir())}
		adapter := NewNVMe(failingFS{FileSystem: base, marker: marker})
		if _, err := adapter.Export(context.Background(), nvmeSpec(), Credential{}); err == nil {
			t.Fatalf("export with %q failure succeeded", marker)
		}
	}
	base := &removableFS{ConfigFS: NewConfigFS(t.TempDir())}
	failing := failingFS{FileSystem: base, marker: "namespaces/1/enable"}
	adapter := NewNVMe(failing)
	if _, err := adapter.Export(context.Background(), nvmeSpec(), Credential{}); err == nil {
		t.Fatal("enable failure succeeded")
	}
	if err := adapter.Resize(context.Background(), nvmeSpec()); err == nil {
		t.Fatal("resize with write failure succeeded")
	}
}

func bddTestISCSIExportErrorBranches(t testing.TB) {
	base := &removableFS{ConfigFS: NewConfigFS(t.TempDir())}
	adapter := NewISCSI(failingFS{FileSystem: base, marker: "target/iscsi"})
	if _, err := adapter.Export(context.Background(), testSpec(), testCredential()); err == nil {
		t.Fatal("iSCSI target failure succeeded")
	}
	base = &removableFS{ConfigFS: NewConfigFS(t.TempDir())}
	adapter = NewISCSI(failingFS{FileSystem: base, marker: "auth/password"})
	if _, err := adapter.Export(context.Background(), testSpec(), testCredential()); err == nil {
		t.Fatal("iSCSI auth failure succeeded")
	}
}

func bddTestTargetConstructorsAndValidation(t testing.TB) {
	if NewConfigFS("").Root() == "" {
		t.Fatal("default configfs root is empty")
	}
	if NewISCSI(nil) == nil || NewNVMe(nil) == nil {
		t.Fatal("nil filesystem constructors failed")
	}
	if _, _, err := splitPortal("bad"); err == nil {
		t.Fatal("bad portal split did not fail")
	}
	if _, _, err := splitPortal("127.0.0.1:bad"); err == nil {
		t.Fatal("bad portal port did not fail")
	}
}

func bddTestTargetNoopTeardown(t testing.TB) {
	nvme := NewNVMe(&removableFS{ConfigFS: NewConfigFS(t.TempDir())})
	if err := nvme.Unexport(context.Background(), nvmeSpec(), true); err != nil {
		t.Fatalf("missing NVMe unexport: %v", err)
	}
	iscsiFS := &removableFS{ConfigFS: NewConfigFS(t.TempDir())}
	_ = iscsiFS.Mkdir("target/iscsi")
	iscsi := NewISCSI(iscsiFS)
	if err := iscsi.Unexport(context.Background(), testSpec(), true); err != nil {
		t.Fatalf("missing iSCSI unexport: %v", err)
	}
	if _, err := iscsi.ListSessions(context.Background()); err != nil {
		t.Fatalf("iSCSI sessions: %v", err)
	}
}

func bddTestIdempotentExportBranches(t testing.TB) {
	nvme := NewNVMe(&removableFS{ConfigFS: NewConfigFS(t.TempDir())})
	if _, err := nvme.Export(context.Background(), nvmeSpec(), Credential{}); err != nil {
		t.Fatal(err)
	}
	if _, err := nvme.Export(context.Background(), nvmeSpec(), Credential{}); err != nil {
		t.Fatalf("second NVMe export: %v", err)
	}
	iscsi, _ := newTestISCSI(t)
	if _, err := iscsi.Export(context.Background(), testSpec(), testCredential()); err != nil {
		t.Fatal(err)
	}
	if _, err := iscsi.Export(context.Background(), testSpec(), testCredential()); err != nil {
		t.Fatalf("second iSCSI export: %v", err)
	}
}

func bddTestPortalAndComponentValidation(t testing.TB) {
	if _, err := portalDirectory("bad"); err == nil {
		t.Fatal("bad portal did not fail")
	}
	if _, err := portalDirectory("127.0.0.1:0"); err == nil {
		t.Fatal("zero portal port did not fail")
	}
	if directory, err := portalDirectory("127.0.0.1:3260"); err != nil || directory != "127.0.0.1:3260" {
		t.Fatalf("portal directory = %q, %v", directory, err)
	}
	if err := validateComponent("name", "bad/name"); err == nil {
		t.Fatal("slash component did not fail")
	}
	if err := validateComponent("name", "good-name"); err != nil {
		t.Fatalf("good component: %v", err)
	}
}

func bddTestDefaultConfigFSRoot(t testing.TB) {
	fs := NewConfigFS("")
	if fs.Root() != "/sys/kernel/config" {
		t.Fatalf("root = %q", fs.Root())
	}
	if fs.Exists("") {
		t.Fatal("empty path should not exist")
	}
}
