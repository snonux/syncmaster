package fs

import (
	"context"
	"fmt"
	"os"
)

// DryRunStore wraps an fs.Store: reads (Stat/ReadFile/ReadDir/WalkDir) execute
// on the underlying Store so a dry run can still compute the plan; writes
// (MkdirAll/Remove/Rename/WriteFile) are logged via Log and skipped, so a dry
// run mutates nothing on the local filesystem.
type DryRunStore struct {
	Store
	Log func(format string, args ...any) // invoked for each skipped mutation
}

// ensure DryRunStore still satisfies fs.Store (reads delegate via the
// embedded Store; writes are overridden below).
var _ Store = DryRunStore{}

func (d DryRunStore) logf(format string, args ...any) {
	if d.Log != nil {
		d.Log(format, args...)
	}
}

// MkdirAll records the directory creation and skips it.
func (d DryRunStore) MkdirAll(_ context.Context, path string, _ os.FileMode) error {
	d.logf("DRY: mkdir %s", path)
	return nil
}

// Remove records the deletion and skips it.
func (d DryRunStore) Remove(_ context.Context, path string) error {
	d.logf("DRY: remove %s", path)
	return nil
}

// Rename records the rename and skips it.
func (d DryRunStore) Rename(_ context.Context, old, new string) error {
	d.logf("DRY: rename %s -> %s", old, new)
	return nil
}

// WriteFile records the write and skips it.
func (d DryRunStore) WriteFile(_ context.Context, path string, _ []byte, _ os.FileMode) error {
	d.logf("DRY: write %s", path)
	return nil
}

// String makes a DryRunStore log a recognizable tag if fmt-printed.
func (d DryRunStore) String() string { return fmt.Sprintf("DryRunStore(%T)", d.Store) }
