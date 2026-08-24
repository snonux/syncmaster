// Package syncmaster is the top-level orchestrator. It depends only on the
// driver package: it dispatches to registered drivers by mode, detects
// devices in auto mode, prints the summary, and flushes to disk.
package syncmaster

import (
	"context"
	"errors"
	"fmt"

	"syncmaster/internal/driver"
	"syncmaster/internal/fssync"
	"syncmaster/internal/stats"
)

// Exit-code sentinels, mapped to os.Exit codes by main.
var (
	ErrUsage           = errors.New("usage error")
	ErrNoDevice        = errors.New("no supported device found")
	ErrMultipleDevices = errors.New("multiple supported devices are connected")
	ErrFailed          = errors.New("import finished with failures")
)

// App is the syncmaster orchestrator.
type App struct {
	Env *driver.Env
}

// Run dispatches by configured mode.
func (a *App) Run(ctx context.Context) error {
	switch a.Env.Config.Mode {
	case "help":
		_, _ = fmt.Fprint(a.Env.Out, Usage)
		return nil
	case "selftest":
		return a.selftest(ctx)
	case "auto":
		return a.runAuto(ctx)
	default:
		reg, err := a.drivers()
		if err != nil {
			return err
		}
		d, ok := reg.Lookup(a.Env.Config.Mode)
		if !ok {
			return fmt.Errorf("%w: unknown mode %q", ErrUsage, a.Env.Config.Mode)
		}
		return a.runOne(ctx, d)
	}
}

// drivers returns the injected driver registry, or a clear error if the Env
// was not wired with one. The orchestrator depends on this injected
// abstraction rather than the package-level default registry.
func (a *App) drivers() (*driver.Registry, error) {
	if a.Env == nil || a.Env.Drivers == nil {
		return nil, fmt.Errorf("internal: driver registry not configured")
	}
	return a.Env.Drivers, nil
}

func (a *App) runOne(ctx context.Context, d driver.Driver) error {
	devs, err := d.Detect(ctx, a.Env)
	if err != nil {
		return err
	}
	if len(devs) == 0 {
		return ErrNoDevice
	}
	if len(devs) > 1 {
		return ErrMultipleDevices
	}
	return d.Sync(ctx, devs[0], a.Env)
}

func (a *App) runAuto(ctx context.Context) error {
	reg, err := a.drivers()
	if err != nil {
		return err
	}
	var all []driver.Device
	for _, d := range reg.All() {
		ds, err := d.Detect(ctx, a.Env)
		if err != nil {
			_, _ = fmt.Fprintf(a.Env.Err, "%s: detect: %v\n", d.Name(), err)
			continue
		}
		all = append(all, ds...)
	}
	if sel := a.Env.Config.Device; sel != "" {
		var filtered []driver.Device
		for _, dev := range all {
			if dev.Driver == sel {
				filtered = append(filtered, dev)
			}
		}
		all = filtered
	}
	switch len(all) {
	case 0:
		return ErrNoDevice
	case 1:
		d, ok := reg.Lookup(all[0].Driver)
		if !ok {
			return fmt.Errorf("internal: driver %q not registered", all[0].Driver)
		}
		_, _ = fmt.Fprintf(a.Env.Out, "Device: %s\n", all[0].Label)
		return d.Sync(ctx, all[0], a.Env)
	default:
		_, _ = fmt.Fprintln(a.Env.Err, "Multiple supported devices are connected. Specify one explicitly:")
		for _, dev := range all {
			_, _ = fmt.Fprintf(a.Env.Err, "  %s\n", dev.Driver)
		}
		return ErrMultipleDevices
	}
}

// Finish prints the summary, flushes to disk, and returns ErrFailed when any
// failures were recorded. It should be called once after Run.
func (a *App) Finish() error {
	_, _ = fmt.Fprintf(a.Env.Out, "Summary: %s\n", a.Env.Stats)
	fssync.Sync()
	if a.Env.Stats.Get(stats.Failed) > 0 {
		_, _ = fmt.Fprintln(a.Env.Err, "Import finished with failures.")
		return ErrFailed
	}
	_, _ = fmt.Fprintln(a.Env.Out, "Import complete. It is safe to unplug the USB device.")
	return nil
}

func (a *App) selftest(ctx context.Context) error {
	r := a.Env.Runner
	if r == nil {
		return fmt.Errorf("selftest: nil runner")
	}
	for _, cmd := range [][]string{
		{"go", "vet", "./..."},
		{"go", "build", "./..."},
	} {
		if _, err := r.Run(ctx, cmd[0], cmd[1:]...); err != nil {
			return fmt.Errorf("selftest: %s: %w", cmd[0], err)
		}
	}
	_, _ = fmt.Fprintln(a.Env.Out, "Self-test passed.")
	return nil
}

// Usage is the help text.
const Usage = `Usage: syncmaster [--allow-missing-gps] [--device NAME] [--verbose] [auto|fujifilm|supernote|selftest|help] [destination]

Import files from supported USB devices mounted through GVFS.

Modes:
  auto       Import from the single supported device currently connected.
  fujifilm   Copy JPEG, RAW, and video files from a Fuji/Fujifilm camera.
  supernote  Copy the Supernote Note folder (convert .note to PDF) and the
             Document folder (KOReader books + .sdr sidecar data).
  selftest   Run go vet/build self-checks.
  help       Print this help.

Options:
  --allow-missing-gps  Import camera images even when no GPS coordinates match.
  --device NAME        Select a specific device when multiple are connected.
  --verbose            Print verbose progress.

Environment overrides:
  FUJIFILM_DEST, FUJIFILM_RAW_DEST, GPX_DIR, SUPERNOTE_DEST,
  CONVERT_PARALLELISM, GVFS_ROOT
`
