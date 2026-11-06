package lvm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

type fakeCall struct {
	name string
	args []string
}

type fakeRunner struct {
	calls   []fakeCall
	handler func(name string, args []string) ([]byte, []byte, error)
}

func (runner *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	runner.calls = append(runner.calls, fakeCall{name: name, args: append([]string(nil), args...)})
	if runner.handler == nil {
		return nil, nil, nil
	}
	return runner.handler(name, args)
}

func (runner *fakeRunner) find(name string) []fakeCall {
	calls := make([]fakeCall, 0)
	for _, call := range runner.calls {
		if call.name == name {
			calls = append(calls, call)
		}
	}
	return calls
}

func jsonLV(name, vg, size, tags, dmPath string) []byte {
	return []byte(`{"report":[{"lv":[{"lv_name":"` + name + `","vg_name":"` + vg +
		`","lv_uuid":"uuid-` + name + `","lv_size":"` + size +
		`","lv_attr":"-wi-a-----","lv_tags":"` + tags +
		`","lv_dm_path":"` + dmPath + `","lv_path":"/dev/` + vg + `/` + name +
		`","devices":"/dev/sda"}]}]}`)
}

func jsonVG(name, attr, size, free, extent, missing string) []byte {
	return []byte(`{"report":[{"vg":[{"vg_name":"` + name +
		`","vg_uuid":"uuid-` + name + `","vg_attr":"` + attr +
		`","vg_size":"` + size + `","vg_free":"` + free +
		`","vg_extent_size":"` + extent + `","vg_missing_pv_count":"` + missing + `"}]}]}`)
}

func newTestBackend(runner Runner, options ...Option) *commandBackend {
	options = append([]Option{WithRunner(runner), WithCriticalPaths(nil), WithSettleTimeout(-1)}, options...)
	return New(options...).(*commandBackend)
}

func bddTestCreateBuildsCommandAndObservesAllocatedSize(t testing.TB) {
	runner := &fakeRunner{}
	runner.handler = func(name string, args []string) ([]byte, []byte, error) {
		switch name {
		case "lvcreate", "udevadm":
			return nil, nil, nil
		case "lvs":
			return jsonLV("lv1", "vg1", "2097152", "kouja.managed=true,kouja.owner=hash", "/dev/mapper/vg1-lv1"), nil, nil
		default:
			t.Fatalf("unexpected command %s %v", name, args)
			return nil, nil, nil
		}
	}
	backend := newTestBackend(runner, WithDeviceProbe(func(_ context.Context, vg, lv string) (string, int64, error) {
		return "/dev/" + vg + "/" + lv, 2 << 20, nil
	}))
	volume, err := backend.Create(context.Background(), "vg1", "lv1", 1<<20, map[string]string{
		"kouja.managed": "true",
		"kouja.volume":  "resource",
		"kouja.owner":   "hash",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if volume.SizeBytes != 2<<20 || volume.DevicePath != "/dev/mapper/vg1-lv1" {
		t.Fatalf("Create returned %#v", volume)
	}
	createCalls := runner.find("lvcreate")
	if len(createCalls) != 1 {
		t.Fatalf("lvcreate calls: %#v", createCalls)
	}
	want := []string{"--yes", "--size", "1048576b", "--name", "lv1",
		"--addtag", "kouja.managed=true", "--addtag", "kouja.owner=hash",
		"--addtag", "kouja.volume=resource", "vg1"}
	if !reflect.DeepEqual(createCalls[0].args, want) {
		t.Fatalf("lvcreate args = %#v, want %#v", createCalls[0].args, want)
	}
	if len(runner.find("udevadm")) != 0 {
		t.Fatalf("udevadm should be skipped when settle is disabled")
	}
}

func bddTestCreateMapsResourceExhausted(t testing.TB) {
	runner := &fakeRunner{handler: func(name string, args []string) ([]byte, []byte, error) {
		if name == "lvcreate" {
			return nil, []byte("insufficient free space: 0 extents needed"), errors.New("exit status 5")
		}
		return nil, nil, nil
	}}
	backend := newTestBackend(runner, WithDeviceProbe(func(context.Context, string, string) (string, int64, error) {
		return "", 0, ErrNotFound
	}))
	_, err := backend.Create(context.Background(), "vg1", "lv1", 1<<20, nil)
	if !errors.Is(err, ErrResourceExhausted) {
		t.Fatalf("Create error = %v, want ErrResourceExhausted", err)
	}
	var commandError *CommandError
	if !errors.As(err, &commandError) || commandError.ExitCode != -1 {
		t.Fatalf("Create error did not preserve CommandError: %#v", err)
	}
}

func bddTestObserveParsesTagsAndNotFound(t testing.TB) {
	runner := &fakeRunner{handler: func(name string, args []string) ([]byte, []byte, error) {
		if len(args) > 0 && args[len(args)-1] == "vg1/missing" {
			return []byte(`{"report":[{"lv":[]}]}`), nil, nil
		}
		return jsonLV("lv1", "vg1", "1024", "kouja.managed=true,kouja.owner=old", "/dev/mapper/vg1-lv1"), nil, nil
	}}
	backend := newTestBackend(runner)
	volume, err := backend.Observe(context.Background(), "vg1", "lv1")
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if volume.UUID != "uuid-lv1" || volume.Tags["kouja.owner"] != "old" || volume.Tags["kouja.managed"] != "true" {
		t.Fatalf("Observe returned %#v", volume)
	}
	if _, err := backend.Observe(context.Background(), "vg1", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing Observe = %v, want ErrNotFound", err)
	}
}

func bddTestCapacityAppliesHeadroom(t testing.TB) {
	runner := &fakeRunner{handler: func(name string, args []string) ([]byte, []byte, error) {
		return jsonVG("vg1", "wz--n-", "1000", "800", "4", "0"), nil, nil
	}}
	backend := newTestBackend(runner, WithHeadroom(10, 50))
	capacity, err := backend.Capacity(context.Background(), "vg1")
	if err != nil {
		t.Fatalf("Capacity: %v", err)
	}
	if capacity.TotalBytes != 1000 || capacity.FreeBytes != 800 || capacity.AllocatableBytes != 700 ||
		capacity.MaxVolumeBytes != 700 || capacity.ExtentSizeBytes != 4 || capacity.VGState != "active" {
		t.Fatalf("Capacity = %#v", capacity)
	}
	if capacity.ObservedAt.IsZero() {
		t.Fatal("Capacity observed_at is zero")
	}

	runner.handler = func(name string, args []string) ([]byte, []byte, error) {
		return jsonVG("vg1", "wz--n-", "1000", "800", "4", "1"), nil, nil
	}
	capacity, err = backend.Capacity(context.Background(), "vg1")
	if err != nil {
		t.Fatalf("Capacity unavailable: %v", err)
	}
	if capacity.VGState != "unavailable" {
		t.Fatalf("VGState = %q, want unavailable", capacity.VGState)
	}

	runner.handler = func(name string, args []string) ([]byte, []byte, error) {
		return jsonVG("other", "wz--n-", "1000", "800", "4", "0"), nil, nil
	}
	if _, err := backend.Capacity(context.Background(), "vg1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing VG Capacity = %v, want ErrNotFound", err)
	}
}

func bddTestEligibilityRejectsThinClusteredMissingAndRootBacked(t testing.TB) {
	normalVG := jsonVG("vg1", "wz--n-", "1000", "800", "4", "0")
	normalLV := jsonLV("lv1", "vg1", "100", "", "/dev/mapper/vg1-lv1")
	tests := []struct {
		name          string
		vg            []byte
		lv            []byte
		findmnt       string
		criticalPaths []string
		want          error
	}{
		{name: "normal", vg: normalVG, lv: normalLV, want: nil},
		{name: "thin", vg: normalVG, lv: []byte(`{"report":[{"lv":[{"lv_name":"thin","vg_name":"vg1","lv_attr":"twi-a-tz--","lv_tags":"","lv_dm_path":"/dev/mapper/vg1-thin"}]}]}`), want: ErrIneligible},
		{name: "clustered", vg: jsonVG("vg1", "wz--nc", "1000", "800", "4", "0"), lv: normalLV, want: ErrIneligible},
		{name: "missing-pv", vg: jsonVG("vg1", "wz--n-", "1000", "800", "4", "1"), lv: normalLV, want: ErrIneligible},
		{name: "root-backed", vg: normalVG, lv: []byte(`{"report":[{"lv":[{"lv_name":"root","vg_name":"vg1","lv_attr":"-wi-ao----","lv_tags":"","lv_dm_path":"/dev/mapper/vg1-root"}]}]}`), findmnt: "/dev/mapper/vg1-root\n", criticalPaths: []string{"/"}, want: ErrIneligible},
	}
	for _, test := range tests {
		bddSubtest(t, test.name, func(t testing.TB) {
			runner := &fakeRunner{handler: func(name string, args []string) ([]byte, []byte, error) {
				switch name {
				case "vgs":
					return test.vg, nil, nil
				case "lvs":
					return test.lv, nil, nil
				case "findmnt":
					return []byte(test.findmnt), nil, nil
				default:
					t.Fatalf("unexpected command %s %v", name, args)
					return nil, nil, nil
				}
			}}
			options := []Option{WithRunner(runner), WithCriticalPaths(test.criticalPaths), WithSettleTimeout(-1)}
			backend := New(options...).(*commandBackend)
			err := backend.Eligibility(context.Background(), "vg1")
			if test.want == nil && err != nil {
				t.Fatalf("Eligibility = %v, want nil", err)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("Eligibility = %v, want %v", err, test.want)
			}
		})
	}
}

func bddTestSetOwnerTagReplacesExistingOwnerTag(t testing.TB) {
	runner := &fakeRunner{}
	runner.handler = func(name string, args []string) ([]byte, []byte, error) {
		if name == "lvs" {
			return jsonLV("lv1", "vg1", "1024", "kouja.managed=true,kouja.owner=old", "/dev/mapper/vg1-lv1"), nil, nil
		}
		return nil, nil, nil
	}
	backend := newTestBackend(runner)
	if err := backend.SetOwnerTag(context.Background(), "vg1", "lv1", "new"); err != nil {
		t.Fatalf("SetOwnerTag: %v", err)
	}
	calls := runner.find("lvchange")
	if len(calls) != 1 {
		t.Fatalf("lvchange calls: %#v", calls)
	}
	want := []string{"--deltag", "kouja.owner=old", "--addtag", "kouja.owner=new", "vg1/lv1"}
	if !reflect.DeepEqual(calls[0].args, want) {
		t.Fatalf("lvchange args = %#v, want %#v", calls[0].args, want)
	}
}

func bddTestListManagedAndCandidates(t testing.TB) {
	runner := &fakeRunner{handler: func(name string, args []string) ([]byte, []byte, error) {
		return []byte(`{"report":[{"lv":[
			{"lv_name":"managed","vg_name":"vg1","lv_size":"1","lv_tags":"kouja.managed=true","lv_dm_path":"/dev/mapper/vg1-managed"},
			{"lv_name":"other","vg_name":"vg1","lv_size":"2","lv_tags":"","lv_dm_path":"/dev/mapper/vg1-other"}
		]}]}`), nil, nil
	}}
	backend := newTestBackend(runner)
	managed, err := backend.ListManaged(context.Background())
	if err != nil {
		t.Fatalf("ListManaged: %v", err)
	}
	if len(managed) != 1 || managed[0].Name != "managed" {
		t.Fatalf("managed = %#v", managed)
	}
	candidates, err := backend.ListCandidates(context.Background())
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates = %#v", candidates)
	}
}

func bddTestResizeActivateRemoveCommands(t testing.TB) {
	runner := &fakeRunner{handler: func(name string, args []string) ([]byte, []byte, error) {
		if name == "lvs" {
			return jsonLV("lv1", "vg1", "2097152", "", "/dev/mapper/vg1-lv1"), nil, nil
		}
		return nil, nil, nil
	}}
	backend := newTestBackend(runner, WithDeviceProbe(func(_ context.Context, vg, lv string) (string, int64, error) {
		return "/dev/" + vg + "/" + lv, 2 << 20, nil
	}))
	if _, err := backend.Extend(context.Background(), "vg1", "lv1", 2<<20); err != nil {
		t.Fatalf("Extend: %v", err)
	}
	if _, err := backend.Shrink(context.Background(), "vg1", "lv1", 1<<20); err != nil {
		t.Fatalf("Shrink: %v", err)
	}
	if err := backend.Activate(context.Background(), "vg1", "lv1"); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if err := backend.Remove(context.Background(), "vg1", "lv1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	extend := runner.find("lvextend")
	if len(extend) != 1 || !reflect.DeepEqual(extend[0].args, []string{"--yes", "--size", "2097152b", "vg1/lv1"}) {
		t.Fatalf("lvextend calls: %#v", extend)
	}
	shrink := runner.find("lvreduce")
	if len(shrink) != 1 || !reflect.DeepEqual(shrink[0].args, []string{"--yes", "--size", "1048576b", "vg1/lv1"}) {
		t.Fatalf("lvreduce calls: %#v", shrink)
	}
	activate := runner.find("lvchange")
	if len(activate) != 1 || !reflect.DeepEqual(activate[0].args, []string{"--activate", "y", "vg1/lv1"}) {
		t.Fatalf("lvchange calls: %#v", activate)
	}
	remove := runner.find("lvremove")
	if len(remove) != 1 || !reflect.DeepEqual(remove[0].args, []string{"--yes", "vg1/lv1"}) {
		t.Fatalf("lvremove calls: %#v", remove)
	}
}

func bddTestValidation(t testing.TB) {
	runner := &fakeRunner{}
	backend := newTestBackend(runner)
	if _, err := backend.Create(context.Background(), "../vg", "lv1", 1, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid VG = %v, want ErrInvalid", err)
	}
	if _, err := backend.Create(context.Background(), "vg1", "../lv", 1, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid LV = %v, want ErrInvalid", err)
	}
	if _, err := backend.Create(context.Background(), "vg1", "lv1", 0, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero size = %v, want ErrInvalid", err)
	}
	if _, err := backend.Create(context.Background(), "vg1", "lv1", 1<<40+1, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized = %v, want ErrInvalid", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("validation unexpectedly ran commands: %#v", runner.calls)
	}
}

func bddTestOptionsCommandErrorAndSettle(t testing.TB) {
	commandError := &CommandError{Name: "lvs", Args: []string{"x"}, ExitCode: 5, Stderr: "bad", Err: errors.New("exit")}
	if commandError.Error() == "" || !errors.Is(commandError, commandError.Err) {
		t.Fatal("command error did not expose its cause")
	}
	plainError := &CommandError{Name: "lvs", Err: errors.New("exit")}
	if plainError.Error() == "" {
		t.Fatal("plain command error is empty")
	}
	runner := &fakeRunner{}
	backend := newTestBackend(runner, WithDevRoot(t.TempDir()), WithPollInterval(time.Millisecond), WithHeadroom(5, 1), WithSettleTimeout(time.Second))
	if backend.devRoot == "" || backend.pollInterval != time.Millisecond || backend.headroomPercent != 5 {
		t.Fatalf("options not applied: %#v", backend)
	}
	runner.handler = func(string, []string) ([]byte, []byte, error) {
		return nil, []byte("not found"), errors.New("exit status 5")
	}
	if _, err := backend.run(context.Background(), "lvs"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("run error = %v, want ErrNotFound", err)
	}
	if err := backend.settle(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("settle error = %v, want ErrNotFound", err)
	}
}

func bddTestJSONAndProbeEdgeCases(t testing.TB) {
	var value flexString
	if err := value.UnmarshalJSON(nil); err != nil || value != "" {
		t.Fatalf("null flex string = %q, %v", value, err)
	}
	if err := value.UnmarshalJSON([]byte(`"text"`)); err != nil || value != "text" {
		t.Fatalf("string flex string = %q, %v", value, err)
	}
	if err := value.UnmarshalJSON([]byte(`123`)); err != nil || value != "123" {
		t.Fatalf("number flex string = %q, %v", value, err)
	}
	if parsed, err := flexString("1.5").int64(); err != nil || parsed != 1 {
		t.Fatalf("float int64 = %d, %v", parsed, err)
	}
	if _, err := flexString("bad").int64(); err == nil {
		t.Fatal("bad int64 did not fail")
	}
	if report, err := parseReport(nil); err != nil || len(report.Report) != 0 {
		t.Fatalf("empty report = %#v, %v", report, err)
	}
	if _, err := parseReport([]byte(`{`)); err == nil {
		t.Fatal("bad report did not fail")
	}
	tags := parseTags("a=b,flag,, c=d")
	if tags["a"] != "b" || tags["flag"] != "" || tags["c"] != "d" {
		t.Fatalf("tags = %#v", tags)
	}
	if row, err := rowToLV(lvRow{Name: "lv", VGName: "vg", Size: "10"}); err != nil || row.DevicePath != "/dev/vg/lv" {
		t.Fatalf("row = %#v, %v", row, err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "vg"), 0o700); err != nil {
		t.Fatal(err)
	}
	device := filepath.Join(root, "vg", "lv")
	if err := os.WriteFile(device, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{handler: func(name string, args []string) ([]byte, []byte, error) {
		return []byte("10\n"), nil, nil
	}}
	backend := newTestBackend(runner, WithDevRoot(root))
	if path, size, err := backend.commandDeviceProbe(context.Background(), "vg", "lv"); err != nil || path != device || size != 10 {
		t.Fatalf("probe = %q %d %v", path, size, err)
	}
	runner.handler = func(name string, args []string) ([]byte, []byte, error) { return []byte("bad"), nil, nil }
	if _, _, err := backend.commandDeviceProbe(context.Background(), "vg", "lv"); err == nil {
		t.Fatal("bad blockdev size did not fail")
	}
	if _, _, err := (execRunner{}).Run(context.Background(), "sh", "-c", "exit 1"); err == nil {
		t.Fatal("exec runner failure not returned")
	}
	if output, _, err := (execRunner{}).Run(context.Background(), "sh", "-c", "printf ok"); err != nil || string(output) != "ok" {
		t.Fatalf("exec runner = %q, %v", output, err)
	}
	waitBackend := newTestBackend(runner, WithSettleTimeout(time.Millisecond), WithPollInterval(time.Millisecond), WithDeviceProbe(func(context.Context, string, string) (string, int64, error) {
		return "", 0, ErrNotFound
	}))
	if _, err := waitBackend.waitForDevice(context.Background(), "vg", "lv", 1); err == nil {
		t.Fatal("waitForDevice timeout did not fail")
	}
}
