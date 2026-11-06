package lvm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const (
	defaultPollInterval  = 250 * time.Millisecond
	defaultSettleTimeout = 30 * time.Second
	maxVolumeBytes       = int64(1 << 40)
)

var (
	ErrNotFound          = errors.New("lvm: not found")
	ErrInvalid           = errors.New("lvm: invalid argument")
	ErrIneligible        = errors.New("lvm: volume group is not eligible")
	ErrResourceExhausted = errors.New("lvm: insufficient space")
)

// CommandError preserves the command and stderr without exposing secrets in
// logs. LVM arguments contain paths and names, not credentials.
type CommandError struct {
	Name     string
	Args     []string
	ExitCode int
	Stderr   string
	Err      error
}

func (e *CommandError) Error() string {
	stderr := strings.TrimSpace(e.Stderr)
	if stderr == "" {
		return fmt.Sprintf("lvm: %s %s failed: %v", e.Name, strings.Join(e.Args, " "), e.Err)
	}
	return fmt.Sprintf("lvm: %s %s failed (exit %d): %s", e.Name, strings.Join(e.Args, " "), e.ExitCode, stderr)
}

func (e *CommandError) Unwrap() error {
	return e.Err
}

// Runner executes a host command. The LVM backend never builds a shell string.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (stdout, stderr []byte, err error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	command := exec.CommandContext(ctx, name, args...) //nolint:gosec // explicit argv, never a shell string
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

var _ Backend = (*commandBackend)(nil)

type commandBackend struct {
	runner           Runner
	devRoot          string
	criticalPaths    []string
	headroomPercent  int64
	headroomMinBytes int64
	pollInterval     time.Duration
	settleTimeout    time.Duration
	probe            DeviceProbe
}

type DeviceProbe func(ctx context.Context, vg, lv string) (path string, sizeBytes int64, err error)

type Option func(*commandBackend)

// New returns an LVM backend that executes the standard LVM command-line tools.
func New(opts ...Option) Backend {
	backend := &commandBackend{
		runner:        execRunner{},
		devRoot:       "/dev",
		criticalPaths: []string{"/"},
	}
	for _, option := range opts {
		option(backend)
	}
	if backend.pollInterval <= 0 {
		backend.pollInterval = defaultPollInterval
	}
	if backend.settleTimeout == 0 {
		backend.settleTimeout = defaultSettleTimeout
	}
	if backend.probe == nil {
		backend.probe = backend.commandDeviceProbe
	}
	return backend
}

// WithRunner replaces the host command runner. It is intended for tests and
// for embedding.
func WithRunner(runner Runner) Option {
	return func(backend *commandBackend) {
		if runner != nil {
			backend.runner = runner
		}
	}
}

// WithDevRoot changes the root used to locate /dev/<vg>/<lv>.
func WithDevRoot(root string) Option {
	return func(backend *commandBackend) {
		if root != "" {
			backend.devRoot = root
		}
	}
}

// WithCriticalPaths sets the paths checked by Eligibility. Pass an empty slice
// to skip the mount-source check in a unit test.
func WithCriticalPaths(paths []string) Option {
	return func(backend *commandBackend) {
		backend.criticalPaths = append([]string(nil), paths...)
	}
}

// WithHeadroom configures the capacity reserve applied by Capacity.
func WithHeadroom(percent int64, minBytes int64) Option {
	return func(backend *commandBackend) {
		backend.headroomPercent = percent
		backend.headroomMinBytes = minBytes
	}
}

// WithPollInterval changes the device polling interval.
func WithPollInterval(interval time.Duration) Option {
	return func(backend *commandBackend) {
		if interval > 0 {
			backend.pollInterval = interval
		}
	}
}

// WithSettleTimeout changes the udev/device readiness timeout. A negative value
// disables udevadm settle; zero keeps the default.
func WithSettleTimeout(timeout time.Duration) Option {
	return func(backend *commandBackend) {
		backend.settleTimeout = timeout
	}
}

// WithDeviceProbe replaces device readiness probing. It is intended for tests.
func WithDeviceProbe(probe DeviceProbe) Option {
	return func(backend *commandBackend) {
		backend.probe = probe
	}
}

func (backend *commandBackend) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	stdout, stderr, err := backend.runner.Run(ctx, name, args...)
	if err == nil {
		return stdout, nil
	}
	exitCode := -1
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		exitCode = exitError.ExitCode()
	}
	commandError := &CommandError{
		Name:     name,
		Args:     append([]string(nil), args...),
		ExitCode: exitCode,
		Stderr:   strings.TrimSpace(string(stderr)),
		Err:      err,
	}
	return nil, mapLVMError(commandError)
}

func mapLVMError(err error) error {
	var commandError *CommandError
	if errors.As(err, &commandError) && commandError.ExitCode == 5 {
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "failed to find logical volume") ||
		strings.Contains(message, "failed to find volume group") ||
		strings.Contains(message, "not found") {
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	if strings.Contains(message, "insufficient free space") ||
		strings.Contains(message, "not enough space") ||
		strings.Contains(message, "insufficient space") ||
		strings.Contains(message, "exceeds the size of the volume group") ||
		strings.Contains(message, "exceeds the size of the logical volume") {
		return fmt.Errorf("%w: %w", ErrResourceExhausted, err)
	}
	return err
}

var namePattern = regexp.MustCompile(`^[A-Za-z0-9+_.-]{1,127}$`)

func validateName(kind, value string) error {
	if !namePattern.MatchString(value) {
		return fmt.Errorf("%w: invalid %s %q", ErrInvalid, kind, value)
	}
	return nil
}

func validateVolumeName(vg, lv string) error {
	if err := validateName("volume group", vg); err != nil {
		return err
	}
	if err := validateName("logical volume", lv); err != nil {
		return err
	}
	return nil
}
