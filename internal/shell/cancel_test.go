package shell

import (
	"context"
	"testing"
	"time"
)

// TestExecRunCancellation verifies that Exec.Run aborts a long external
// command promptly when its context is cancelled.
func TestExecRunCancellation(t *testing.T) {
	if testing.Short() {
		t.Skip("sleep test")
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := (Exec{}).Run(ctx, "sleep", "30")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error from cancelled sleep")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Run took %v to abort; want < 2s (immediate)", elapsed)
	}
	t.Logf("aborted in %v, err=%v", elapsed, err)
}
