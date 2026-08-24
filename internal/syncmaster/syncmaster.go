// Package syncmaster implements the core synchronization logic of the
// syncmaster application. The binary in cmd/syncmaster is a thin wrapper
// around this package.
package syncmaster

import (
	"context"
	"fmt"
	"io"
)

// Config holds the runtime configuration for a SyncMaster instance. It is
// populated by the caller (typically the main package from flags or env) and
// passed to New.
type Config struct {
	// Source is the location to sync from.
	Source string
	// Destination is the location to sync to.
	Destination string
	// Verbose enables progress output on Out.
	Verbose bool
}

// SyncMaster coordinates a single synchronization run. Construct it with New
// and drive it with Run. All exported methods are safe for the lifetime of a
// single Run call; do not reuse an instance across concurrent runs.
type SyncMaster struct {
	cfg Config
	out io.Writer
}

// Compile-time assertion that SyncMaster satisfies any future Runner
// interface contract at the package boundary.
var _ Runner = (*SyncMaster)(nil)

// Runner is the small surface the application exposes to its callers. Keeping
// it here lets the main package depend on an interface while this package
// returns a concrete SyncMaster.
type Runner interface {
	Run(ctx context.Context) error
}

// New constructs a SyncMaster from the given Config. It validates the
// configuration and returns an error wrapping the reason when invalid.
func New(cfg Config, out io.Writer) (*SyncMaster, error) {
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	return &SyncMaster{cfg: cfg, out: out}, nil
}

// Run performs one synchronization pass from Source to Destination. It
// respects ctx cancellation and returns a wrapped error on failure.
func (s *SyncMaster) Run(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("sync aborted before start: %w", err)
	}
	if s.cfg.Verbose {
		_, _ = fmt.Fprintf(s.out, "syncing %s -> %s\n", s.cfg.Source, s.cfg.Destination)
	}
	// TODO: implement the actual sync logic.
	return nil
}

// validate reports the first configuration problem it finds.
func (c Config) validate() error {
	if c.Source == "" {
		return fmt.Errorf("source must be set")
	}
	if c.Destination == "" {
		return fmt.Errorf("destination must be set")
	}
	return nil
}
