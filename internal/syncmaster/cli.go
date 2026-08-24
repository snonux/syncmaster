// Package syncmaster is the top-level orchestrator. This file holds the
// program entry point (Main) and the exit-code mapping (ExitCode) so the
// cmd/syncmaster package stays a one-line wrapper and the wiring is unit
// testable.
package syncmaster

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"syncmaster/internal"
	"syncmaster/internal/clock"
	"syncmaster/internal/config"
	"syncmaster/internal/driver"
	"syncmaster/internal/drivers"
	"syncmaster/internal/fs"
	"syncmaster/internal/gvfs"
	"syncmaster/internal/media"
	"syncmaster/internal/shell"
	"syncmaster/internal/stats"
)

// Main is the program entry point: it parses flags, builds the config and
// the dependency-injection environment, installs the SIGINT/SIGTERM handler,
// runs the sync, and returns the process exit code. cmd/syncmaster is a
// one-line caller of this so the wiring is unit-testable.
func Main(args []string, stdout, stderr io.Writer) int {
	fsFlags := flag.NewFlagSet("syncmaster", flag.ContinueOnError)
	fsFlags.SetOutput(stderr)

	showVersion := fsFlags.Bool("version", false, "print version and exit")
	verbose := fsFlags.Bool("verbose", false, "print verbose progress")
	allowMissingGPS := fsFlags.Bool("allow-missing-gps", false, "import images even without GPS")
	device := fsFlags.String("device", "", "select a specific device when multiple are connected")
	ioTimeout := fsFlags.Duration("io-timeout", 0, "per-operation timeout for external tools (gio/exiftool/supernote-tool); 0 uses IO_TIMEOUT env / default")

	if err := fsFlags.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		_, _ = fmt.Fprintln(stdout, internal.Version)
		return 0
	}

	rest := fsFlags.Args()
	mode := "auto"
	dest := ""
	if len(rest) >= 1 {
		mode = rest[0]
	}
	if len(rest) >= 2 {
		dest = rest[1]
	}

	home, uid := config.HomeAndUID()
	cfg := config.FromEnv(os.Getenv, home, uid)
	cfg.Mode = mode
	cfg.DestOverride = dest
	cfg.AllowMissingGPS = *allowMissingGPS
	cfg.Verbose = *verbose
	cfg.Device = *device
	if *ioTimeout != 0 {
		cfg.IOTimeout = *ioTimeout
	}
	if err := cfg.Validate(); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}

	st := stats.New()
	reg := driver.NewRegistry()
	drivers.RegisterAll(reg)
	mreg := media.NewRegistry()
	media.RegisterDefaults(mreg)
	gio := &gvfs.Gio{Runner: shell.Exec{Timeout: cfg.IOTimeout}, Root: cfg.GVFSRoot}
	env := &driver.Env{
		Config:  &cfg,
		Source:  gio,
		Mounts:  gio,
		Local:   fs.OS{},
		Clock:   clock.Real{},
		Runner:  shell.Exec{Timeout: cfg.IOTimeout},
		Stats:   st,
		Out:     stdout,
		Err:     stderr,
		Drivers: reg,
		Media:   mreg,
	}
	app := &App{Env: env}

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		// First signal: graceful abort. Second signal: force exit. When the
		// run finishes normally, ctx.Done() lets the goroutine exit without
		// leaking (and without the test harness needing to send a signal).
		select {
		case <-sigCh:
			_, _ = fmt.Fprintln(stderr, "Aborting... (Ctrl+C again to force)")
			stop()
			<-sigCh
			os.Exit(130)
		case <-ctx.Done():
			signal.Stop(sigCh)
		}
	}()

	return ExitCode(app.Run(ctx), app.Finish())
}

// ExitCode maps one or more run/finish errors to a process exit code: nil
// errors → 0; usage/device errors → 2; anything else → 1. The first non-nil
// error decides.
func ExitCode(errs ...error) int {
	for _, err := range errs {
		if err == nil {
			continue
		}
		switch {
		case errors.Is(err, ErrUsage),
			errors.Is(err, ErrMultipleDevices),
			errors.Is(err, ErrNoDevice):
			return 2
		default:
			return 1
		}
	}
	return 0
}
