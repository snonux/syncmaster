// Package gvfs wraps the gio command-line tool to access GVFS-mounted devices
// (gphoto2 cameras, MTP devices). Gio implements both copier.Source (List/Copy)
// and driver.MountFS (FindMounts/Exists/ModifiedTime).
package gvfs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/snonux/syncmaster/internal/copier"
	"github.com/snonux/syncmaster/internal/driver"
	"github.com/snonux/syncmaster/internal/shell"
)

// Gio is a GVFS/gio client. ReadDir lists the GVFS root directory; it defaults
// to os.ReadDir and is overridable for tests.
type Gio struct {
	Runner  shell.Runner
	Root    string
	ReadDir func(name string) ([]string, error) // names of entries under name
}

var _ copier.Source = (*Gio)(nil)
var _ driver.MountFS = (*Gio)(nil)

// FindMounts returns reachable mount paths under Root whose name matches glob
// (e.g. "gphoto2:*", "mtp:*Supernote*"). A mount is reachable when gio info
// succeeds on it.
func (g *Gio) FindMounts(ctx context.Context, glob string) ([]string, error) {
	names, err := g.listRoot()
	if err != nil {
		return nil, fmt.Errorf("list gvfs root %s: %w", g.Root, err)
	}
	var mounts []string
	for _, name := range names {
		match, err := filepath.Match(glob, name)
		if err != nil {
			return nil, fmt.Errorf("bad glob %q: %w", glob, err)
		}
		if !match {
			continue
		}
		path := filepath.Join(g.Root, name)
		ok, err := g.Exists(ctx, path)
		if err != nil {
			return nil, err
		}
		if ok {
			mounts = append(mounts, path)
		}
	}
	return mounts, nil
}

// Exists reports whether path is reachable per "gio info". A non-zero exit
// from "gio info" means the path is unreachable and is reported as
// (false, nil); a genuine failure (missing gio, I/O error, or context
// cancellation) is returned as an error so callers can distinguish it from an
// absent path instead of silently dropping the device.
func (g *Gio) Exists(ctx context.Context, path string) (bool, error) {
	_, err := g.Runner.Run(ctx, "gio", "info", path)
	if err == nil {
		return true, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	if shell.IsExitError(err) {
		// gio info exits non-zero when the path is unreachable.
		return false, nil
	}
	return false, fmt.Errorf("gio info %s: %w", path, err)
}

// ModifiedTime returns the time::modified attribute of path as a unix time.
func (g *Gio) ModifiedTime(ctx context.Context, path string) (time.Time, error) {
	out, err := g.Runner.Run(ctx, "gio", "info", "-a", "time::modified", path)
	if err != nil {
		return time.Time{}, fmt.Errorf("gio info %s: %w", path, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "time::modified:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "time::modified:"))
			sec, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				return time.Time{}, fmt.Errorf("parse time::modified %q: %w", val, err)
			}
			return time.Unix(sec, 0), nil
		}
	}
	return time.Time{}, fmt.Errorf("time::modified not found for %s", path)
}

// List lists a directory, returning copier entries. It requests
// standard::name,standard::type,standard::size,standard::time::modified and
// parses tab-separated output tolerantly.
func (g *Gio) List(ctx context.Context, dir string) ([]copier.Entry, error) {
	out, err := g.Runner.Run(ctx, "gio", "list", "-a",
		"standard::name,standard::type,standard::size,standard::time::modified", dir)
	if err != nil {
		return nil, fmt.Errorf("gio list %s: %w", dir, err)
	}
	return ParseList(out), nil
}

// Copy copies src to dst via gio copy.
func (g *Gio) Copy(ctx context.Context, src, dst string) error {
	if _, err := g.Runner.Run(ctx, "gio", "copy", src, dst); err != nil {
		return fmt.Errorf("gio copy %s -> %s: %w", src, dst, err)
	}
	return nil
}

func (g *Gio) listRoot() ([]string, error) {
	if g.ReadDir != nil {
		return g.ReadDir(g.Root)
	}
	return defaultReadDir(g.Root)
}

func defaultReadDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names, nil
}

// ParseList parses gio list output into entries. Each line is split on tabs;
// the first field is the name, subsequent fields are inspected for a type
// token ("directory"/"regular") and numeric fields (size then mtime, in
// encounter order).
func ParseList(raw []byte) []copier.Entry {
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	var out []copier.Entry
	for _, line := range lines {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) == 0 {
			continue
		}
		e := copier.Entry{Name: strings.TrimSpace(fields[0])}
		if e.Name == "" {
			continue
		}
		var numerics []int64
		isDir := false
		seenType := false
		for _, f := range fields[1:] {
			f = strings.TrimSpace(f)
			switch {
			case strings.Contains(f, "directory"):
				isDir = true
				seenType = true
			case strings.Contains(f, "regular"):
				isDir = false
				seenType = true
			}
			if n, ok := parseInt(f); ok {
				numerics = append(numerics, n)
			}
		}
		e.IsDir = isDir
		_ = seenType
		if len(numerics) >= 1 {
			e.Size = numerics[0]
		}
		if len(numerics) >= 2 {
			e.Modified = time.Unix(numerics[1], 0)
		}
		out = append(out, e)
	}
	return out
}

func parseInt(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
