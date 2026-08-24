// Command syncmaster is the entry point for the syncmaster synchronization
// tool. It parses flags and delegates to the internal syncmaster package.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"syncmaster/internal"
	"syncmaster/internal/syncmaster"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run is the testable core of main: it parses args, builds the application,
// and runs one sync pass. It returns an error instead of calling os.Exit so
// tests can assert on it.
func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("syncmaster", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		source      = fs.String("source", "", "location to sync from")
		destination = fs.String("destination", "", "location to sync to")
		verbose     = fs.Bool("verbose", false, "print progress to stdout")
		showVersion = fs.Bool("version", false, "print version and exit")
	)

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parsing flags: %w", err)
	}

	if *showVersion {
		_, _ = fmt.Fprintln(stdout, internal.Version)
		return nil
	}

	cfg := syncmaster.Config{
		Source:      *source,
		Destination: *destination,
		Verbose:     *verbose,
	}

	sm, err := syncmaster.New(cfg, stdout)
	if err != nil {
		return fmt.Errorf("building syncmaster: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := sm.Run(ctx); err != nil {
		return fmt.Errorf("sync failed: %w", err)
	}
	return nil
}
