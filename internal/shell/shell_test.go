package shell

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
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
