package fs

import (
	"context"
	"testing"
)

func TestDryRunStoreReadsExecuteWritesSkip(t *testing.T) {
	ctx := context.Background()
	under := NewMem()
	_ = under.WriteFile(ctx, "/keep.txt", []byte("data"), 0o644)

	var logs []string
	d := DryRunStore{Store: under, Log: func(format string, args ...any) {
		logs = append(logs, format)
	}}

	// Reads execute on the underlying store.
	e, err := d.Stat(ctx, "/keep.txt")
	if err != nil || e.Size != 4 {
		t.Fatalf("Stat (read) should execute: e=%+v err=%v", e, err)
	}
	got, err := d.ReadFile(ctx, "/keep.txt")
	if err != nil || string(got) != "data" {
		t.Fatalf("ReadFile (read) should execute: got=%q err=%v", got, err)
	}

	// Writes are logged and skipped (nothing changes on the underlying store).
	if err := d.MkdirAll(ctx, "/d", 0o755); err != nil {
		t.Fatalf("MkdirAll should be a no-op, got %v", err)
	}
	if err := d.WriteFile(ctx, "/new.txt", []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile should be a no-op, got %v", err)
	}
	if _, err := under.Stat(ctx, "/new.txt"); err == nil {
		t.Fatal("WriteFile should not have created /new.txt on the underlying store")
	}
	if err := d.Remove(ctx, "/keep.txt"); err != nil {
		t.Fatalf("Remove should be a no-op, got %v", err)
	}
	if _, err := under.Stat(ctx, "/keep.txt"); err != nil {
		t.Fatal("Remove should not have deleted /keep.txt on the underlying store")
	}
	if err := d.Rename(ctx, "/keep.txt", "/moved.txt"); err != nil {
		t.Fatalf("Rename should be a no-op, got %v", err)
	}
	if _, err := under.Stat(ctx, "/keep.txt"); err != nil {
		t.Fatal("Rename should not have moved /keep.txt on the underlying store")
	}

	// Each skipped mutation was logged.
	if len(logs) != 4 {
		t.Fatalf("logged %d mutations, want 4 (mkdir/write/remove/rename): %v", len(logs), logs)
	}
}
