// Package syncmaster is the top-level orchestrator. It depends only on the
// driver package: it dispatches to registered drivers by mode, detects
// devices in auto mode, prints the summary, and flushes to disk.
package syncmaster

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/snonux/syncmaster/internal/driver"
	"github.com/snonux/syncmaster/internal/fssync"
	"github.com/snonux/syncmaster/internal/stats"
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
		_, _ = fmt.Fprint(a.Env.Out, a.usage())
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

func (a *App) runOne(ctx context.Context, d driver.Plugin) error {
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

// usage builds the help text, enumerating driver modes from the injected
// registry so adding a driver does not require editing this text. The
// framework meta-modes (auto/selftest/help) are listed alongside them.
func (a *App) usage() string {
	type modeLine struct{ name, desc string }
	modes := []modeLine{
		{"auto", "Import from the single supported device currently connected."},
	}
	if reg, err := a.drivers(); err == nil {
		for _, d := range reg.All() {
			modes = append(modes, modeLine{d.Name(), d.Description()})
		}
		// If the registry is not wired, driver modes are silently omitted;
		// help degrades gracefully rather than failing. Production main
		// always wires a registry, so this only affects bare App{Env:...}.
	}
	modes = append(modes,
		modeLine{"selftest", "Run go vet/build self-checks."},
		modeLine{"help", "Print this help."},
	)

	names := make([]string, len(modes))
	for i, m := range modes {
		names[i] = m.name
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Usage: syncmaster [--allow-missing-gps] [--device NAME] [--io-timeout DURATION] [--verbose] [%s] [destination]\n\n",
		strings.Join(names, "|"))
	b.WriteString("Import files from supported USB devices mounted through GVFS.\n\n")
	b.WriteString("Modes:\n")
	for _, m := range modes {
		fmt.Fprintf(&b, "  %-10s %s\n", m.name, m.desc)
	}
	b.WriteString("\nOptions:\n")
	for _, o := range [][2]string{
		{"--allow-missing-gps", "Import camera images even when no GPS coordinates match."},
		{"--device NAME", "Select a specific device when multiple are connected."},
		{"--io-timeout DURATION", "Per-operation timeout for external tools (gio/exiftool/supernote-tool); 0 uses IO_TIMEOUT env / default."},
		{"--verbose", "Print verbose progress."},
	} {
		fmt.Fprintf(&b, "  %-23s %s\n", o[0], o[1])
	}
	b.WriteString("\nEnvironment overrides:\n")
	b.WriteString("  FUJIFILM_DEST, FUJIFILM_RAW_DEST, GPX_DIR, SUPERNOTE_DEST,\n")
	b.WriteString("  CONVERT_PARALLELISM, GVFS_ROOT, IO_TIMEOUT\n")
	return b.String()
}
