// Package gpx implements the geotag Transform: it tags imported camera
// images with GPS coordinates from GPX tracks using exiftool, and rolls back
// the copied images when GPS data is required but unavailable.
package gpx

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"syncmaster/internal/driver"
	"syncmaster/internal/fs"
	"syncmaster/internal/stats"
)

// ErrNoGPX is returned when no GPX tracks are found and AllowMissing is false.
var ErrNoGPX = errors.New("gpx: no GPX tracks found")

// ErrMissingGPS is returned when some images lack matching GPS coordinates and
// AllowMissing is false.
var ErrMissingGPS = errors.New("gpx: some images have no matching GPS coordinates")

// Geotag is a driver.Transform that geotags imported images with exiftool.
type Geotag struct {
	Runner       // exiftool runner
	GPXDir       string
	AllowMissing bool
}

var _ driver.Transform = (*Geotag)(nil)

// Runner is the subset of shell.Runner Geotag needs (declared here to keep the
// constructor test-friendly and decoupled from the shell package).
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	LookPath(name string) (string, error)
}

// Name returns the transform name.
func (g *Geotag) Name() string { return "geotag" }

// Apply runs the geotag pipeline over tctx.Imported: find GPX tracks, geotag
// with exiftool, then handle any images left without coordinates.
func (g *Geotag) Apply(ctx context.Context, tctx *driver.TransformCtx) error {
	if g == nil || tctx == nil || tctx.Env == nil {
		return fmt.Errorf("gpx: nil context")
	}
	log := func(format string, args ...any) { _, _ = fmt.Fprintf(tctx.Env.Out, format+"\n", args...) }

	if len(tctx.Imported) == 0 {
		log("No new images to GPS tag.")
		return nil
	}

	tracks, err := g.findTracks(ctx, tctx.Env.Local)
	if err != nil {
		return fmt.Errorf("find gpx tracks: %w", err)
	}

	if len(tracks) == 0 {
		if g.AllowMissing {
			log("No GPX files found under %s; importing without GPS coordinates.", g.GPXDir)
			return nil
		}
		tctx.Env.Stats.Inc(stats.Failed, int64(len(tctx.Imported)))
		g.rollback(tctx)
		return fmt.Errorf("%w (dir %s)", ErrNoGPX, g.GPXDir)
	}

	if _, err := g.LookPath("exiftool"); err != nil {
		tctx.Env.Stats.Inc(stats.Failed, int64(len(tctx.Imported)))
		g.rollback(tctx)
		return fmt.Errorf("gpx: exiftool not found: %w", err)
	}

	if err := g.runExiftool(ctx, tracks, tctx); err != nil {
		return err
	}

	missing, err := g.findMissing(ctx, tctx.Imported)
	if err != nil {
		return fmt.Errorf("gpx: scan missing gps: %w", err)
	}
	if len(missing) == 0 {
		return nil
	}
	return g.handleMissing(missing, tctx)
}

// runExiftool invokes exiftool to geotag the imported images from the GPX
// tracks. On failure it rolls back and records one failure.
func (g *Geotag) runExiftool(ctx context.Context, tracks []string, tctx *driver.TransformCtx) error {
	args := []string{"-overwrite_original", "-P"}
	for _, tr := range tracks {
		args = append(args, "-geotag", tr)
	}
	args = append(args, tctx.Imported...)
	log := func(format string, args ...any) { _, _ = fmt.Fprintf(tctx.Env.Out, format+"\n", args...) }
	log("GPS tagging %d image(s) from %d GPX file(s).", len(tctx.Imported), len(tracks))
	if _, err := g.Run(ctx, "exiftool", args...); err != nil {
		tctx.Env.Stats.Inc(stats.Failed, 1)
		g.rollback(tctx)
		return fmt.Errorf("gpx: exiftool geotag failed: %w", err)
	}
	return nil
}

// handleMissing resolves images that have no GPS coordinates after geotag.
// With AllowMissing they are imported as-is; otherwise they are rolled back
// and reported as ErrMissingGPS.
func (g *Geotag) handleMissing(missing []string, tctx *driver.TransformCtx) error {
	log := func(format string, args ...any) { _, _ = fmt.Fprintf(tctx.Env.Out, format+"\n", args...) }
	if g.AllowMissing {
		log("Importing %d image(s) without GPS coordinates as requested.", len(missing))
		return nil
	}
	for _, p := range missing {
		_, _ = fmt.Fprintf(tctx.Env.Err, "No GPS coordinates found: %s\n", p)
	}
	tctx.Env.Stats.Inc(stats.Failed, int64(len(missing)))
	g.rollback(tctx)
	return fmt.Errorf("%w (%d image(s))", ErrMissingGPS, len(missing))
}

func (g *Geotag) findTracks(_ context.Context, local fs.FS) ([]string, error) {
	var tracks []string
	err := local.WalkDir(g.GPXDir, func(path string, e fs.Entry) error {
		if e.IsDir {
			return nil
		}
		if strings.EqualFold(filepath.Ext(path), ".gpx") {
			tracks = append(tracks, path)
		}
		return nil
	})
	if err != nil {
		// Missing GPX dir → no tracks, not a hard error.
		return nil, nil
	}
	sort.Strings(tracks)
	return tracks, nil
}

func (g *Geotag) findMissing(ctx context.Context, images []string) ([]string, error) {
	args := []string{"-if", "not (defined $GPSLatitude and defined $GPSLongitude)", "-p", "$FilePath"}
	args = append(args, images...)
	out, err := g.Run(ctx, "exiftool", args...)
	if err != nil {
		return nil, fmt.Errorf("exiftool missing-scan: %w", err)
	}
	var missing []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			missing = append(missing, line)
		}
	}
	return missing, nil
}

func (g *Geotag) rollback(tctx *driver.TransformCtx) {
	removed := 0
	for _, p := range tctx.Imported {
		if err := tctx.Env.Local.Remove(p); err == nil {
			removed++
		}
	}
	if removed > 0 {
		tctx.Env.Stats.Inc(stats.Copied, -int64(removed))
	}
}
