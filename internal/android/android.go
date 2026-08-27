// Package android implements the Android sync driver: it copies a configured
// folder (e.g. /sdcard/Notes/Vault/Quicklog) from an Android phone connected
// over adb into a local destination, using the generic copier.
package android

import (
	"context"
	"fmt"
	"path"

	"github.com/snonux/syncmaster/internal/adb"
	"github.com/snonux/syncmaster/internal/copier"
	"github.com/snonux/syncmaster/internal/driver"
)

// Driver syncs a folder from an Android phone over adb. AndroidSource (the
// remote folder) and AndroidDest (the local destination) come from the run
// config; the remote folder is read via the adb-backed copier.Source.
type Driver struct{}

var _ driver.Plugin = (*Driver)(nil)

// Name returns the driver name.
func (Driver) Name() string { return "android" }

// Description returns the human-readable summary shown in usage/help.
func (Driver) Description() string {
	return "Sync a folder from an Android phone over adb."
}

// Detect lists connected, authorized Android devices. It returns no devices
// (not an error) when AndroidSource is unset, so the driver is inert until
// configured.
func (Driver) Detect(ctx context.Context, env *driver.Env) ([]driver.Device, error) {
	src := env.Config.AndroidSource
	if src == "" {
		return nil, nil
	}
	serials, err := (adb.Client{Runner: env.Runner}).FindMounts(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("android: %w", err)
	}
	devs := make([]driver.Device, 0, len(serials))
	for _, s := range serials {
		devs = append(devs, driver.Device{
			Driver: "android",
			Label:  "Android (" + s + ")",
			Source: src,
			Extra:  map[string]any{"serial": s},
		})
	}
	return devs, nil
}

// Sync copies the remote folder to the local destination, deduping by
// size+mtime (adb pull -a preserves the remote mtime). When AndroidDeleteSource
// is set, files confirmed backed up (copied or already-present) are deleted
// from the phone after the copy phase completes cleanly — move semantics.
func (Driver) Sync(ctx context.Context, dev driver.Device, env *driver.Env) error {
	serial, _ := dev.Extra["serial"].(string)
	client := adb.Client{Runner: env.Runner, Serial: serial}
	dest := env.Config.AndroidDestEffective()
	deleteSource := env.Config.AndroidDeleteSource

	if err := env.Local.MkdirAll(ctx, dest, 0o755); err != nil {
		return fmt.Errorf("android: mkdir %s: %w", dest, err)
	}

	_, _ = fmt.Fprintf(env.Out, "Device: Android (%s)\nSource: %s\nDestination: %s\n", serial, dev.Source, dest)
	if deleteSource {
		_, _ = fmt.Fprintln(env.Out, "Mode: move (deleting notes from the phone after backup)")
	}
	log := func(format string, args ...any) { _, _ = fmt.Fprintf(env.Out, format+"\n", args...) }
	log("Importing the Android folder over adb.")

	// backedUp collects the source paths of files confirmed copied or already
	// present at the destination (skipped). It drives opt-in move semantics.
	var backedUp []string
	remember := func(_ string, e copier.Entry) {
		backedUp = append(backedUp, path.Join(dev.Source, e.RelPath))
	}

	copyErr := (&copier.Tree{
		Src: client, Local: env.Local, Clock: env.Clock,
		Skip:     copier.SkipUnchangedSizeMtime,
		OnCopied: remember, OnSkip: remember,
		DryRun: env.DryRun,
		Stats:  env.Stats, Log: log,
	}).CopyTree(ctx, dev.Source, dest)

	// Only delete from the phone once the copy phase is clean, and only files
	// confirmed backed up (copied/skipped). A file whose copy failed is NOT in
	// backedUp, so it stays on the phone — no data loss on a partial sync. In
	// dry run, log what would be deleted and touch nothing.
	if deleteSource && copyErr == nil {
		deleted := 0
		for _, src := range backedUp {
			if env.DryRun {
				log("DRY: would delete from phone: %s", src)
				continue
			}
			if err := client.RemoveRemote(ctx, src); err != nil {
				_, _ = fmt.Fprintf(env.Err, "android: delete %s: %v\n", src, err)
				continue
			}
			deleted++
		}
		if env.DryRun {
			log("DRY: would delete %d file(s) from the phone.", len(backedUp))
		} else {
			log("Deleted %d file(s) from the phone.", deleted)
		}
	}

	if copyErr != nil {
		return fmt.Errorf("android: copy: %w", copyErr)
	}
	log("Android folder copied locally.")
	_, _ = fmt.Fprintf(env.Out, "Imported files to: %s\n", dest)
	return nil
}
