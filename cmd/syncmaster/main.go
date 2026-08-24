// Command syncmaster is the entry point. It parses flags, wires the built-in
// drivers, builds the dependency-injection environment, and delegates to the
// internal syncmaster orchestrator.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"syscall"

	"syncmaster/internal"
	"syncmaster/internal/clock"
	"syncmaster/internal/config"
	"syncmaster/internal/driver"
	"syncmaster/internal/drivers"
	"syncmaster/internal/fs"
	"syncmaster/internal/gvfs"
	"syncmaster/internal/shell"
	"syncmaster/internal/stats"
	"syncmaster/internal/syncmaster"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	fsFlags := flag.NewFlagSet("syncmaster", flag.ContinueOnError)
	fsFlags.SetOutput(stderr)

	showVersion := fsFlags.Bool("version", false, "print version and exit")
	verbose := fsFlags.Bool("verbose", false, "print verbose progress")
	allowMissingGPS := fsFlags.Bool("allow-missing-gps", false, "import images even without GPS")
	device := fsFlags.String("device", "", "select a specific device when multiple are connected")

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

	home, uid := homeAndUID()
	cfg := config.FromEnv(os.Getenv, home, uid)
	cfg.Mode = mode
	cfg.DestOverride = dest
	cfg.AllowMissingGPS = *allowMissingGPS
	cfg.Verbose = *verbose
	cfg.Device = *device
	if err := cfg.Validate(); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}

	st := stats.New()
	gio := &gvfs.Gio{Runner: shell.Exec{}, Root: cfg.GVFSRoot}
	env := &driver.Env{
		Config: &cfg,
		Source: gio,
		Mounts: gio,
		Local:  fs.OS{},
		Clock:  clock.Real{},
		Runner: shell.Exec{},
		Stats:  st,
		Out:    stdout,
		Err:    stderr,
	}

	drivers.RegisterAll()
	app := &syncmaster.App{Env: env}

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh // first signal: graceful abort
		_, _ = fmt.Fprintln(stderr, "Aborting... (Ctrl+C again to force)")
		stop()
		<-sigCh // second signal: hard exit
		os.Exit(130)
	}()

	runErr := app.Run(ctx)
	finishErr := app.Finish()
	return exitCode(runErr, finishErr)
}

func exitCode(errs ...error) int {
	for _, err := range errs {
		if err == nil {
			continue
		}
		switch {
		case errors.Is(err, syncmaster.ErrUsage),
			errors.Is(err, syncmaster.ErrMultipleDevices),
			errors.Is(err, syncmaster.ErrNoDevice):
			return 2
		default:
			return 1
		}
	}
	return 0
}

func homeAndUID() (string, int) {
	home := ""
	if u, err := user.Current(); err == nil {
		home = u.HomeDir
		if uid, err := strconv.Atoi(u.Uid); err == nil {
			return home, uid
		}
	}
	return home, os.Getuid()
}
