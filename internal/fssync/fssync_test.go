package fssync

import "testing"

// Sync must be safe to call (no-op or syscall) and not panic.
func TestSyncSafe(t *testing.T) {
	Sync()
}
