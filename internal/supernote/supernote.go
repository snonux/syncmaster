// Package supernote implements the Supernote Nomad sync driver: it copies the
// Note and Document (KOReader) folders from a GVFS MTP mount, then converts
// .note files to PDF via the note Transform.
package supernote

import (
	"context"
	"fmt"
	"path/filepath"

	"syncmaster/internal/copier"
	"syncmaster/internal/driver"
	"syncmaster/internal/note"
)

// Driver syncs from a Supernote Nomad mounted via GVFS (MTP).
type Driver struct{}

var _ driver.Plugin = (*Driver)(nil)

// Name returns the driver name.
func (Driver) Name() string { return "supernote" }

// Description returns the human-readable summary shown in usage/help.
func (Driver) Description() string {
	return "Copy the Supernote Note folder (convert .note to PDF) and the Document folder (KOReader books + .sdr sidecar data)."
}

// Detect finds reachable Supernote MTP mounts, preferring "Internal shared
// storage" as the source.
func (Driver) Detect(ctx context.Context, env *driver.Env) ([]driver.Device, error) {
	seen := map[string]bool{}
	var devs []driver.Device
	for _, glob := range []string{"mtp:*Supernote*", "mtp:*supernote*"} {
		mounts, err := env.Mounts.FindMounts(ctx, glob)
		if err != nil {
			return nil, fmt.Errorf("supernote: find mounts: %w", err)
		}
		for _, m := range mounts {
			if seen[m] {
				continue
			}
			seen[m] = true
			source := m
			storage := filepath.Join(m, "Internal shared storage")
			ok, err := env.Mounts.Exists(ctx, storage)
			if err != nil {
				return nil, fmt.Errorf("supernote: stat storage %s: %w", storage, err)
			}
			if ok {
				source = storage
			} else {
				// Storage subfolder absent; confirm the mount root is usable.
				mountOK, err := env.Mounts.Exists(ctx, m)
				if err != nil {
					return nil, fmt.Errorf("supernote: stat mount %s: %w", m, err)
				}
				if !mountOK {
					continue
				}
			}
			devs = append(devs, driver.Device{
				Driver: "supernote",
				Label:  "Supernote Nomad",
				Source: source,
			})
		}
	}
	return devs, nil
}

// Sync copies Note/Document folders and converts .note files to PDF.
func (d Driver) Sync(ctx context.Context, dev driver.Device, env *driver.Env) error {
	cfg := env.Config
	dest := cfg.SupernoteDestEffective()
	log := func(format string, args ...any) { _, _ = fmt.Fprintf(env.Out, format+"\n", args...) }

	noteRoot := filepath.Join(dev.Source, "Note")
	ok, err := env.Mounts.Exists(ctx, noteRoot)
	if err != nil {
		return fmt.Errorf("supernote: check Note folder: %w", err)
	}
	if !ok {
		return fmt.Errorf("supernote: Note folder not found at %s", noteRoot)
	}

	if err := env.Local.MkdirAll(ctx, dest, 0o755); err != nil {
		return fmt.Errorf("supernote: mkdir %s: %w", dest, err)
	}

	_, _ = fmt.Fprintf(env.Out, "Device: Supernote Nomad\nSource: %s\nDestination: %s\n", noteRoot, dest)
	log("Importing the Supernote Note folder.")

	cc := &copier.Tree{
		Src:   env.Source,
		Local: env.Local,
		Clock: env.Clock,
		Skip:  copier.SkipUnchangedSizeMtime,
		Stats: env.Stats,
		Log:   log,
	}
	if err := cc.CopyTree(ctx, noteRoot, dest); err != nil {
		return fmt.Errorf("supernote: copy Note: %w", err)
	}
	log("Supernote note files are copied locally.")

	documentRoot := filepath.Join(dev.Source, "Document")
	docOK, err := env.Mounts.Exists(ctx, documentRoot)
	if err != nil {
		return fmt.Errorf("supernote: check Document folder: %w", err)
	}
	if docOK {
		koDest := filepath.Join(dest, "KOReader")
		if err := env.Local.MkdirAll(ctx, koDest, 0o755); err != nil {
			return fmt.Errorf("supernote: mkdir %s: %w", koDest, err)
		}
		_, _ = fmt.Fprintf(env.Out, "Source: %s\nKOReader destination: %s\n", documentRoot, koDest)
		log("Importing the Supernote Document folder (KOReader books + .sdr sidecars).")
		if err := (&copier.Tree{
			Src: env.Source, Local: env.Local, Clock: env.Clock,
			Skip: copier.SkipUnchangedSizeMtime, Stats: env.Stats, Log: log,
		}).CopyTree(ctx, documentRoot, koDest); err != nil {
			return fmt.Errorf("supernote: copy Document: %w", err)
		}
		log("KOReader books and reading-progress sidecar data are copied locally.")
	} else {
		log("No Document folder found on the Supernote; skipping KOReader backup.")
	}

	log("It is safe to unplug the Supernote now if you eject/unmount it safely.")

	tctx := &driver.TransformCtx{Env: env, DestRoot: dest, Device: dev}
	if err := driver.RunTransforms(ctx, tctx, d.transforms(env)...); err != nil {
		return fmt.Errorf("supernote: %w", err)
	}
	_, _ = fmt.Fprintf(env.Out, "Imported files to: %s\n", dest)
	return nil
}

// transforms returns the driver's ordered post-copy transforms, constructed
// from the run environment/config.
func (Driver) transforms(env *driver.Env) []driver.Transform {
	return []driver.Transform{
		&note.Convert{Runner: env.Runner, Workers: env.Config.ConvertParallelism},
	}
}
