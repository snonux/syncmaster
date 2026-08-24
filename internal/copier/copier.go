// Package copier provides a generic, device-agnostic recursive tree copier.
// Drivers compose it with a Source (remote tree), a SkipPolicy, a DestResolver,
// and an OnCopied hook — keeping all copy/skip/dedup logic in one reusable
// place.
package copier

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"syncmaster/internal/clock"
	"syncmaster/internal/fs"
	"syncmaster/internal/stats"
)

// Entry is a file or directory in a source tree. Name is the base name;
// RelPath (set by the copier) is the slash path relative to the copy root.
type Entry struct {
	Name     string
	RelPath  string
	IsDir    bool
	Size     int64
	Modified time.Time
}

// Source lists a directory of a remote tree and copies a file to a local path.
type Source interface {
	List(ctx context.Context, dir string) ([]Entry, error)
	Copy(ctx context.Context, src, dst string) error
}

// SkipCtx bundles everything a SkipPolicy may need. Policies read only the
// fields they care about; adding a field here does not change any policy's
// signature, so new dependencies do not force every policy to widen.
type SkipCtx struct {
	Ctx      context.Context
	Src      Source
	Entry    Entry
	DestPath string
	Local    fs.FS
	Clock    clock.Clock
}

// SkipPolicy decides whether to skip copying an entry. Returning true skips.
type SkipPolicy func(sc SkipCtx) (bool, error)

// DestResolver maps an entry to a destination path relative to the copy root.
// include=false excludes the entry entirely (not counted as found).
type DestResolver func(e Entry) (destRelPath string, include bool)

// Copier performs a recursive copy from a Source to a local root.
type Copier struct {
	Src      Source
	Local    fs.FS
	Clock    clock.Clock
	Skip     SkipPolicy
	Resolve  DestResolver
	OnCopied func(destPath string, e Entry) // optional, invoked after a successful copy
	Stats    *stats.Stats
	Log      func(format string, args ...any) // optional progress logger
}

// CopyTree recursively copies srcDir into dstRoot.
func (c *Copier) CopyTree(ctx context.Context, srcDir, dstRoot string) error {
	if c.Src == nil {
		return fmt.Errorf("copier: nil Source")
	}
	if c.Local == nil {
		return fmt.Errorf("copier: nil Local fs")
	}
	if c.Stats == nil {
		return fmt.Errorf("copier: nil Stats")
	}
	if err := c.Local.MkdirAll(dstRoot, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dstRoot, err)
	}
	return c.copyDir(ctx, srcDir, dstRoot, "")
}

func (c *Copier) copyDir(ctx context.Context, srcDir, dstDir, rel string) error {
	entries, err := c.Src.List(ctx, srcDir)
	if err != nil {
		return fmt.Errorf("list %s: %w", srcDir, err)
	}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		e.RelPath = joinRel(rel, e.Name)
		srcPath := joinPath(srcDir, e.Name)

		if e.IsDir {
			dstSub := joinPath(dstDir, e.Name)
			if err := c.Local.MkdirAll(dstSub, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", dstSub, err)
			}
			if err := c.copyDir(ctx, srcPath, dstSub, e.RelPath); err != nil {
				return err
			}
			continue
		}
		if err := c.copyOne(ctx, e, srcPath, dstDir); err != nil {
			return err
		}
	}
	return nil
}

// copyOne handles a single non-directory entry: resolve its destination name,
// apply the skip policy, replace any existing file, then copy. A copy failure
// is counted as stats.Failed and returns nil so the parent loop continues.
func (c *Copier) copyOne(ctx context.Context, e Entry, srcPath, dstDir string) error {
	destRel, include := e.Name, true
	if c.Resolve != nil {
		destRel, include = c.Resolve(e)
	}
	if !include {
		return nil
	}
	c.Stats.Inc(stats.Found, 1)

	destPath := joinPath(dstDir, destRel)
	if err := c.Local.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("mkdir parent %s: %w", filepath.Dir(destPath), err)
	}

	skip, err := c.applySkip(ctx, e, destPath)
	if err != nil {
		return err
	}
	if skip {
		c.logf("skip existing: %s", e.RelPath)
		c.Stats.Inc(stats.Skipped, 1)
		return nil
	}

	// Replace any existing destination file before copying.
	if _, statErr := c.Local.Stat(destPath); statErr == nil {
		if err := c.Local.Remove(destPath); err != nil {
			return fmt.Errorf("remove %s: %w", destPath, err)
		}
	}

	c.logf("copy: %s -> %s", e.RelPath, destPath)
	if err := c.Src.Copy(ctx, srcPath, destPath); err != nil {
		c.Stats.Inc(stats.Failed, 1)
		c.logf("copy failed: %s: %v", e.RelPath, err)
		return nil
	}
	c.Stats.Inc(stats.Copied, 1)
	if c.OnCopied != nil {
		c.OnCopied(destPath, e)
	}
	return nil
}

func (c *Copier) applySkip(ctx context.Context, e Entry, destPath string) (bool, error) {
	if c.Skip == nil {
		return false, nil
	}
	clk := c.Clock
	if clk == nil {
		clk = zeroClock{}
	}
	return c.Skip(SkipCtx{Ctx: ctx, Src: c.Src, Entry: e, DestPath: destPath, Local: c.Local, Clock: clk})
}

func (c *Copier) logf(format string, args ...any) {
	if c.Log != nil {
		c.Log(format, args...)
	}
}

// zeroClock is used when no Clock is provided; its Now value is unused by the
// built-in skip policies.
type zeroClock struct{}

func (zeroClock) Now() time.Time { return time.Time{} }

// SkipExistingName skips when a file with the same name already exists at
// the destination (Fujifilm behavior).
func SkipExistingName(sc SkipCtx) (bool, error) {
	_, err := sc.Local.Stat(sc.DestPath)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("stat %s: %w", sc.DestPath, err)
}

// SkipExistingSize skips when the destination exists with the same size as
// the source entry. If the destination is missing or has a different size it
// is copied (Fujifilm re-sync behavior).
func SkipExistingSize(sc SkipCtx) (bool, error) {
	de, err := sc.Local.Stat(sc.DestPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", sc.DestPath, err)
	}
	return de.Size == sc.Entry.Size, nil
}

// SkipUnchangedSizeMtime skips when the destination exists with the same size
// and modification time (unix seconds) as the source (Supernote behavior).
func SkipUnchangedSizeMtime(sc SkipCtx) (bool, error) {
	de, err := sc.Local.Stat(sc.DestPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", sc.DestPath, err)
	}
	if de.Size != sc.Entry.Size {
		return false, nil
	}
	return de.ModTime.Unix() == sc.Entry.Modified.Unix(), nil
}

func joinRel(rel, name string) string {
	if rel == "" {
		return name
	}
	return rel + "/" + name
}

func joinPath(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + string(filepath.Separator) + name
}
