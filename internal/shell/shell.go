// Package shell is the seam over external command execution. Every shell-out
// (gio, exiftool, supernote-tool, go) goes through Runner, so packages are
// unit-testable without those binaries installed.
package shell

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

// Runner runs an external command and returns its stdout, and resolves
// binaries on PATH.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	LookPath(name string) (string, error)
}

// Exec is the production Runner backed by os/exec.
type Exec struct{}

var _ Runner = (*Exec)(nil)

// Run executes name with args, capturing stdout. A non-zero exit is reported
// as an *exec.ExitError. On context cancellation the process is killed
// (SIGKILL); WaitDelay ensures Run never hangs on inherited pipes.
func (Exec) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 2 * time.Second
	return cmd.Output()
}

// LookPath reports whether name is available on PATH.
func (Exec) LookPath(name string) (string, error) { return exec.LookPath(name) }

// IsExitError reports whether err indicates a command that ran and exited with
// a non-zero status (*exec.ExitError), as opposed to a failure to launch the
// command (e.g. the binary is missing) or an I/O / context error. Callers that
// treat a non-zero exit as an expected outcome — for example, "gio info"
// exiting non-zero means the path is unreachable — use this to separate that
// case from a genuine failure they must surface.
func IsExitError(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee)
}

// Call records one invocation of a fake Runner.
type Call struct {
	Name string
	Args []string
}

// Handler returns stdout/error for a command invoked with args.
type Handler func(ctx context.Context, args []string) ([]byte, error)

// Fake is a test Runner. Register per-command Handlers; every call is
// recorded in Calls. Fake is safe for concurrent use.
type Fake struct {
	mu       sync.Mutex
	Calls    []Call
	handlers map[string]Handler
	paths    map[string]bool
}

var _ Runner = (*Fake)(nil)

// NewFake returns an empty Fake.
func NewFake() *Fake {
	return &Fake{handlers: map[string]Handler{}, paths: map[string]bool{}}
}

// Register associates a handler with a command name.
func (f *Fake) Register(name string, h Handler) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[name] = h
}

// RegisterLookPath configures LookPath results. ok=true means found.
func (f *Fake) RegisterLookPath(name string, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths[name] = ok
}

// Run dispatches to the registered handler for name.
func (f *Fake) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, Call{Name: name, Args: append([]string(nil), args...)})
	h, ok := f.handlers[name]
	f.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("fake: no handler registered for %q", name)
	}
	return h(ctx, args)
}

// LookPath returns a synthetic path when name was registered as present.
func (f *Fake) LookPath(name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.paths[name] {
		return "/usr/bin/" + name, nil
	}
	return "", fmt.Errorf("fake: %q not found", name)
}

// CallsFor returns the recorded calls for a command name.
func (f *Fake) CallsFor(name string) []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Call
	for _, c := range f.Calls {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}
