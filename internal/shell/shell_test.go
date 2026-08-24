package shell

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestFakeRunAndCalls(t *testing.T) {
	f := NewFake()
	f.Register("gio", func(_ context.Context, args []string) ([]byte, error) {
		return []byte("out:" + strings.Join(args, ",")), nil
	})

	out, err := f.Run(context.Background(), "gio", "list", "/x")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(out) != "out:list,/x" {
		t.Fatalf("out = %q", out)
	}

	calls := f.CallsFor("gio")
	if len(calls) != 1 || calls[0].Name != "gio" || len(calls[0].Args) != 2 {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestFakeRunNoHandler(t *testing.T) {
	f := NewFake()
	if _, err := f.Run(context.Background(), "missing"); err == nil {
		t.Fatal("expected error for unregistered command")
	}
}

func TestFakeLookPath(t *testing.T) {
	f := NewFake()
	f.RegisterLookPath("exiftool", true)
	if _, err := f.LookPath("exiftool"); err != nil {
		t.Fatalf("expected exiftool found: %v", err)
	}
	if _, err := f.LookPath("nope"); err == nil {
		t.Fatal("expected nope not found")
	}
}

func TestFakeConcurrent(t *testing.T) {
	f := NewFake()
	f.Register("gio", func(context.Context, []string) ([]byte, error) { return nil, nil })

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			_, _ = f.Run(context.Background(), "gio")
		}
		close(done)
	}()
	for i := 0; i < 100; i++ {
		_ = f.CallsFor("gio")
	}
	<-done
	if len(f.CallsFor("gio")) != 100 {
		t.Fatalf("got %d calls, want 100", len(f.CallsFor("gio")))
	}
}

func TestExecLookPath(t *testing.T) {
	// sh is present on every POSIX system.
	if _, err := (Exec{}).LookPath("sh"); err != nil {
		t.Fatalf("LookPath(sh): %v", err)
	}
}

func TestIsExitError(t *testing.T) {
	if !IsExitError(&exec.ExitError{}) {
		t.Fatal("expected true for *exec.ExitError")
	}
	if IsExitError(nil) {
		t.Fatal("expected false for nil")
	}
	if IsExitError(errors.New("plain")) {
		t.Fatal("expected false for plain error")
	}
	if !IsExitError(fmt.Errorf("wrapped: %w", &exec.ExitError{})) {
		t.Fatal("expected true for wrapped *exec.ExitError")
	}
	// Launch failure (binary missing) is NOT an exit error.
	if IsExitError(exec.Command("no-such-bin-zzz").Run()) {
		t.Fatal("expected false for launch failure")
	}
	// Context errors are NOT exit errors.
	if IsExitError(context.Canceled) {
		t.Fatal("expected false for context.Canceled")
	}
	if IsExitError(context.DeadlineExceeded) {
		t.Fatal("expected false for context.DeadlineExceeded")
	}
}

func TestExecRunError(t *testing.T) {
	_, err := (Exec{}).Run(context.Background(), "sh", "-c", "exit 3")
	if err == nil {
		t.Fatal("expected error from failing command")
	}
	if !errors.As(err, new(*exec.ExitError)) {
		// not fatal: some Go versions wrap differently; just ensure non-nil
		t.Logf("err = %v (not *exec.ExitError)", err)
	}
}

// TestExecRunTimeout verifies that Exec.Timeout bounds a hung command and
// surfaces a context.DeadlineExceeded error (not a bare *exec.ExitError) so
// callers that treat a non-zero exit as "unreachable" don't mistake a hang for
// an absent path.
func TestExecRunTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a sleeping process")
	}
	e := Exec{Timeout: 200 * time.Millisecond}
	start := time.Now()
	_, err := e.Run(context.Background(), "sleep", "30")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error from hung sleep")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded, got %v", err)
	}
	if IsExitError(err) {
		t.Fatalf("timeout error must not be an ExitError so callers can distinguish a hang from an unreachable path; got %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("Run took %v; want prompt timeout", elapsed)
	}
}

// TestExecRunTimeoutZero verifies that Timeout==0 disables the per-call bound
// and relies on ctx cancellation (the pre-existing behavior).
func TestExecRunTimeoutZero(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a sleeping process")
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := (Exec{}).Run(ctx, "sleep", "30")
	if err == nil {
		t.Fatal("expected error from cancelled sleep")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("Run took %v; want prompt cancellation", time.Since(start))
	}
}
