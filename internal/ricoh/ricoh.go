// Package ricoh implements the RICOH GR camera sync driver: it copies
// JPEG/video and RAW files from a GVFS MTP mount into separate flat
// destinations (no DCIM/xxx camera sub-dirs), then geotags imported images
// via the gpx Transform.
//
// Unlike the Fujifilm driver, the GR series mounts as MTP (e.g.
// "mtp:host=RICOH_IMAGING_COMPANY__LTD._RICOH_GR_IV_0024081"), not gphoto2.
package ricoh

import (
	"context"
	"fmt"
	"strconv"

	"github.com/snonux/syncmaster/internal/copier"
	"github.com/snonux/syncmaster/internal/driver"
	"github.com/snonux/syncmaster/internal/gpx"
	"github.com/snonux/syncmaster/internal/media"
)

// Driver syncs from a RICOH GR camera mounted via GVFS (MTP). Media, when
// set, overrides the file-class registry; otherwise the driver uses
// env.Media (injected by main), falling back to media.Default().
type Driver struct {
	Media *media.Registry
}

var _ driver.Plugin = (*Driver)(nil)

// Name returns the driver name.
func (d *Driver) Name() string { return "ricoh" }

// Description returns the human-readable summary shown in usage/help.
func (d *Driver) Description() string {
	return "Copy JPEG, RAW, and video files from a RICOH GR camera."
}

// registry resolves the file-class registry in DI order: the driver's own
// field (test seam), then env.Media (injected by main), then the package
// default.
func (d *Driver) registry(env *driver.Env) *media.Registry {
	if d.Media != nil {
		return d.Media
	}
	if env != nil && env.Media != nil {
		return env.Media
	}
	return media.Default()
}

// Detect finds reachable MTP mounts for RICOH cameras. The source is the
// mount root, so the whole storage tree is walked: internal memory today,
// plus any SD-card storage the camera exposes later.
func (d *Driver) Detect(ctx context.Context, env *driver.Env) ([]driver.Device, error) {
	mounts, err := env.Mounts.FindMounts(ctx, "mtp:*RICOH*")
	if err != nil {
		return nil, fmt.Errorf("ricoh: find mounts: %w", err)
	}
	devs := make([]driver.Device, 0, len(mounts))
	for _, m := range mounts {
		devs = append(devs, driver.Device{
			Driver: "ricoh",
			Label:  "RICOH GR camera",
			Source: m,
		})
	}
	return devs, nil
}

// Sync copies media from the camera and geotags imported images.
func (d *Driver) Sync(ctx context.Context, dev driver.Device, env *driver.Env) error {
	cfg := env.Config
	jpegDest := cfg.RicohJPEGDest()
	rawDest := cfg.RicohRAWDest
	reg := d.registry(env)

	// A dry run must mutate nothing, so the destination roots are created only
	// for real runs.
	if !env.DryRun {
		if err := env.Local.MkdirAll(ctx, jpegDest, 0o755); err != nil {
			return fmt.Errorf("ricoh: mkdir %s: %w", jpegDest, err)
		}
		if err := env.Local.MkdirAll(ctx, rawDest, 0o755); err != nil {
			return fmt.Errorf("ricoh: mkdir %s: %w", rawDest, err)
		}
	}

	_, _ = fmt.Fprintf(env.Out, "Device: RICOH GR camera\nSource: %s\nJPEG/video destination: %s\nRAW destination: %s\n",
		dev.Source, jpegDest, rawDest)

	var imported []string
	onCopied := func(p string, e copier.Entry) {
		if reg.IsA("ricoh-image", e.Name) {
			imported = append(imported, p)
			// Dry run must mutate nothing (plan only), so no sidecar is
			// written; `imported` still feeds the gpx dry-run plan line.
			if env.DryRun {
				return
			}
			// Record the original source size so a later run can dedup despite
			// the in-place geotag rewrite (gpx.Geotag runs exiftool
			// -overwrite_original -P on every imported image/RAW), which drifts
			// the dest size and would otherwise defeat size-based skip and
			// force a re-copy of every geotagged file every run (s51). A failed
			// sidecar write is logged but non-fatal: the next run re-copies.
			if err := env.Local.WriteFile(ctx, p+copier.ImportMetaSuffix, []byte(strconv.FormatInt(e.Size, 10)), 0o644); err != nil {
				_, _ = fmt.Fprintf(env.Err, "ricoh: write import-meta %s: %v\n", p+copier.ImportMetaSuffix, err)
			}
		}
	}
	log := func(format string, args ...any) { _, _ = fmt.Fprintf(env.Out, format+"\n", args...) }

	// Single pass over the camera tree: route each file to its destination root
	// (RAW -> rawDest, JPEG/video -> jpegDest) via the resolver, halving the
	// MTP list round-trips a two-pass walk would make. Files are flattened
	// into the destination root (base name only), so camera sub-dirs like
	// "Internal Memory/DCIM/100RICOH" are not mirrored into the inbox.
	resolve := func(e copier.Entry) (string, string, bool) {
		if reg.IsA("raw", e.Name) {
			return rawDest, e.Name, true
		}
		if reg.IsA("ricoh-media", e.Name) { // raw already routed above
			return jpegDest, e.Name, true
		}
		return "", "", false
	}
	if err := (&copier.Tree{
		Src: env.Source, Local: env.Local, Clock: env.Clock,
		Skip: copier.SkipExistingImportMeta, Resolve: resolve,
		DryRun:   env.DryRun,
		OnCopied: onCopied, Stats: env.Stats, Log: log,
	}).CopyTree(ctx, dev.Source, jpegDest); err != nil {
		return fmt.Errorf("ricoh: copy: %w", err)
	}

	// Post-copy transforms (declared in one place; execution is centralized
	// in driver.RunTransforms so adding a transform is a localized change).
	tctx := &driver.TransformCtx{
		Env:      env,
		DestRoot: jpegDest,
		Imported: imported,
		Device:   dev,
		DryRun:   env.DryRun,
	}
	if err := driver.RunTransforms(ctx, tctx, d.transforms(env)...); err != nil {
		return fmt.Errorf("ricoh: %w", err)
	}
	_, _ = fmt.Fprintf(env.Out, "Imported files to: %s (JPEG/video), %s (RAW)\n", jpegDest, rawDest)
	return nil
}

// transforms returns the driver's ordered post-copy transforms, constructed
// from the run environment/config.
func (d *Driver) transforms(env *driver.Env) []driver.Transform {
	cfg := env.Config
	return []driver.Transform{
		&gpx.Geotag{Runner: env.Runner, GPXDir: cfg.GPXDir, AllowMissing: cfg.AllowMissingGPS},
	}
}
