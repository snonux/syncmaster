// Package fujifilm implements the Fujifilm camera sync driver: it copies
// JPEG/video and RAW files from a GVFS gphoto2 mount into separate
// destinations, then geotags imported images via the gpx Transform.
package fujifilm

import (
	"context"
	"fmt"

	"syncmaster/internal/copier"
	"syncmaster/internal/driver"
	"syncmaster/internal/gpx"
	"syncmaster/internal/media"
)

// Driver syncs from a Fujifilm camera mounted via GVFS (gphoto2). Media,
// when set, overrides the file-class registry; otherwise the driver uses
// env.Media (injected by main), falling back to media.Default().
type Driver struct {
	Media *media.Registry
}

var _ driver.Driver = (*Driver)(nil)

// Name returns the driver name.
func (d *Driver) Name() string { return "fujifilm" }

// Description returns the human-readable summary shown in usage/help.
func (d *Driver) Description() string {
	return "Copy JPEG, RAW, and video files from a Fuji/Fujifilm camera."
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

// Detect finds reachable gphoto2 mounts.
func (d *Driver) Detect(ctx context.Context, env *driver.Env) ([]driver.Device, error) {
	mounts, err := env.Mounts.FindMounts(ctx, "gphoto2:*")
	if err != nil {
		return nil, fmt.Errorf("fujifilm: find mounts: %w", err)
	}
	devs := make([]driver.Device, 0, len(mounts))
	for _, m := range mounts {
		devs = append(devs, driver.Device{
			Driver: "fujifilm",
			Label:  "Fujifilm camera",
			Source: m,
		})
	}
	return devs, nil
}

// Sync copies media from the camera and geotags imported images.
func (d *Driver) Sync(ctx context.Context, dev driver.Device, env *driver.Env) error {
	cfg := env.Config
	jpegDest := cfg.FujifilmJPEGDest()
	rawDest := cfg.FujifilmRAWDest
	reg := d.registry(env)

	if err := env.Local.MkdirAll(jpegDest, 0o755); err != nil {
		return fmt.Errorf("fujifilm: mkdir %s: %w", jpegDest, err)
	}
	if err := env.Local.MkdirAll(rawDest, 0o755); err != nil {
		return fmt.Errorf("fujifilm: mkdir %s: %w", rawDest, err)
	}

	_, _ = fmt.Fprintf(env.Out, "Device: Fujifilm camera\nSource: %s\nJPEG/video destination: %s\nRAW destination: %s\n",
		dev.Source, jpegDest, rawDest)

	var imported []string
	onCopied := func(p string, e copier.Entry) {
		if reg.IsA("fujifilm-image", e.Name) {
			imported = append(imported, p)
		}
	}
	log := func(format string, args ...any) { _, _ = fmt.Fprintf(env.Out, format+"\n", args...) }

	// Pass 1: RAW files -> rawDest.
	rawResolve := func(e copier.Entry) (string, bool) {
		if reg.IsA("raw", e.Name) {
			return e.Name, true
		}
		return "", false
	}
	if err := (&copier.Copier{
		Src: env.Source, Local: env.Local, Clock: env.Clock,
		Skip: copier.SkipExistingSize, Resolve: rawResolve,
		OnCopied: onCopied, Stats: env.Stats, Log: log,
	}).CopyTree(ctx, dev.Source, rawDest); err != nil {
		return fmt.Errorf("fujifilm: copy raw: %w", err)
	}

	// Pass 2: JPEG/video -> jpegDest (RAW already handled).
	jpegResolve := func(e copier.Entry) (string, bool) {
		if reg.IsA("fujifilm-media", e.Name) && !reg.IsA("raw", e.Name) {
			return e.Name, true
		}
		return "", false
	}
	if err := (&copier.Copier{
		Src: env.Source, Local: env.Local, Clock: env.Clock,
		Skip: copier.SkipExistingSize, Resolve: jpegResolve,
		OnCopied: onCopied, Stats: env.Stats, Log: log,
	}).CopyTree(ctx, dev.Source, jpegDest); err != nil {
		return fmt.Errorf("fujifilm: copy jpeg: %w", err)
	}

	// Post-copy transform: geotag imported images.
	geotag := &gpx.Geotag{
		Runner:       env.Runner,
		GPXDir:       cfg.GPXDir,
		AllowMissing: cfg.AllowMissingGPS,
	}
	tctx := &driver.TransformCtx{
		Env:      env,
		DestRoot: jpegDest,
		Imported: imported,
		Device:   dev,
	}
	if err := geotag.Apply(ctx, tctx); err != nil {
		return fmt.Errorf("fujifilm: %w", err)
	}
	_, _ = fmt.Fprintf(env.Out, "Imported files to: %s (JPEG/video), %s (RAW)\n", jpegDest, rawDest)
	return nil
}
