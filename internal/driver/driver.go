// Package driver defines the plugin seams every sync source implements, plus
// the registry and the shared environment (dependency-injection bag) passed to
// drivers and transforms. The orchestrator depends only on this package; it
// knows nothing about Fujifilm or Supernote.
package driver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"syncmaster/internal/clock"
	"syncmaster/internal/config"
	"syncmaster/internal/copier"
	"syncmaster/internal/fs"
	"syncmaster/internal/media"
	"syncmaster/internal/shell"
	"syncmaster/internal/stats"
)

// Device is a discovered, connected source a driver can sync from.
type Device struct {
	Driver string         // driver name
	Label  string         // human-readable label
	Source string         // mount root / source path
	Extra  map[string]any // driver-specific data
}

// MountFS is the device-discovery surface (GVFS/gio). It is split from
// copier.Source so drivers that don't use GVFS need not implement it.
type MountFS interface {
	FindMounts(ctx context.Context, glob string) ([]string, error)
	// Exists reports whether path exists/is reachable. It returns (false, nil)
	// when path is confirmed absent or unreachable; a non-nil error is a real
	// failure (I/O, missing tool, context) that callers must handle.
	Exists(ctx context.Context, path string) (bool, error)
	ModifiedTime(ctx context.Context, path string) (time.Time, error)
}

// Env is the dependency-injection bag passed to every driver and transform.
// Drivers take what they need; none reach for globals. Drivers is the driver
// registry the orchestrator dispatches through; Media is the file-class
// registry drivers classify files with. main constructs both so the
// orchestrator depends on injected abstractions, not package-level globals.
type Env struct {
	Config  *config.Config  // runtime config
	Source  copier.Source   // remote tree (GVFS)
	Mounts  MountFS         // device discovery
	Local   fs.FS           // local filesystem (dest side, meta, rollback)
	Clock   clock.Clock     // deterministic time
	Runner  shell.Runner    // external commands (gio/exiftool/supernote-tool)
	Stats   *stats.Stats    // shared, concurrency-safe
	Out     io.Writer       // progress log
	Err     io.Writer       // error log
	Drivers *Registry       // driver registry (dispatch); required for Run
	Media   *media.Registry // file-class registry; drivers fall back to Default() if nil
}

// Driver discovers devices and syncs from one. Implement this + Register to
// add a sync feature. Description is shown in the help/usage text.
type Driver interface {
	Name() string
	Description() string
	Detect(ctx context.Context, env *Env) ([]Device, error)
	Sync(ctx context.Context, dev Device, env *Env) error
}

// Transform is a post-copy step (geotag, note→PDF, …). A driver declares an
// ordered slice of transforms applied after its copy phase.
type Transform interface {
	Name() string
	Apply(ctx context.Context, tctx *TransformCtx) error
}

// TransformCtx is what a transform operates on.
type TransformCtx struct {
	Env      *Env
	DestRoot string   // where copied files landed
	Imported []string // files of interest (e.g. images to geotag)
	Device   Device
	Scratch  map[string]any // per-run scratch space shared between transforms
}

// RunTransforms applies ts in order over tctx, stopping at the first error
// (wrapped with the transform's Name). It is the single execution path for a
// driver's post-copy transforms so adding one is a localized change to the
// driver's transform declaration, not a re-edit of the copy phase. Scratch
// is initialized so transforms can share per-run state.
func RunTransforms(ctx context.Context, tctx *TransformCtx, ts ...Transform) error {
	if tctx != nil && tctx.Scratch == nil {
		tctx.Scratch = map[string]any{}
	}
	for _, t := range ts {
		if err := t.Apply(ctx, tctx); err != nil {
			return fmt.Errorf("%s: %w", t.Name(), err)
		}
	}
	return nil
}

// ErrDuplicateDriver is returned by Register for a repeated driver name.
var ErrDuplicateDriver = errors.New("driver: duplicate registration")

// Registry holds registered drivers. The package-level Register/All/Lookup
// use a default registry.
type Registry struct {
	mu      sync.Mutex
	drivers map[string]Driver
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{drivers: map[string]Driver{}}
}

// Register adds a driver. Re-registering a name returns ErrDuplicateDriver.
func (r *Registry) Register(d Driver) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d == nil {
		return fmt.Errorf("driver: nil driver")
	}
	name := d.Name()
	if _, ok := r.drivers[name]; ok {
		return fmt.Errorf("%w: %s", ErrDuplicateDriver, name)
	}
	r.drivers[name] = d
	return nil
}

// Lookup returns a driver by name.
func (r *Registry) Lookup(name string) (Driver, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.drivers[name]
	return d, ok
}

// All returns every registered driver, sorted by name for deterministic
// detection order in auto mode.
func (r *Registry) All() []Driver {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Driver, 0, len(r.drivers))
	for _, d := range r.drivers {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

var defaultRegistry = NewRegistry()

// Register adds a driver to the default registry.
func Register(d Driver) error { return defaultRegistry.Register(d) }

// Lookup returns a driver from the default registry.
func Lookup(name string) (Driver, bool) { return defaultRegistry.Lookup(name) }

// All returns all drivers from the default registry.
func All() []Driver { return defaultRegistry.All() }

// Reset clears the default registry (tests only).
func Reset() {
	defaultRegistry = NewRegistry()
}
