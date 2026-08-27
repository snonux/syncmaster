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
	"strconv"
	"time"

	"github.com/snonux/syncmaster/internal/clock"
	"github.com/snonux/syncmaster/internal/fs"
	"github.com/snonux/syncmaster/internal/stats"
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
	Local    fs.Store
	Clock    clock.Clock
}

// SkipPolicy decides whether to skip copying an entry. Returning true skips.
type SkipPolicy func(sc SkipCtx) (bool, error)

// DestResolver maps an entry to its destination root and the path relative
// to that root (relPath). include=false excludes the entry entirely (not
// counted as found). A single CopyTree pass can route entries to multiple
// roots (e.g. RAW files to rawDest, JPEG/video to jpegDest) by returning the
// appropriate root per entry; an empty root falls back to the CopyTree's
// dstRoot. When Resolve is nil, entries copy to dstRoot at their
// source-relative path (e.RelPath), mirroring the source tree structure.
type DestResolver func(e Entry) (root, relPath string, include bool)

// copyTmpSuffix is the temp-file suffix copyOne copies into before renaming
// over the dest, so a failed/cancelled copy never destroys the previous
// backup at the dest path.
const copyTmpSuffix = ".syncmaster.tmp"

// Tree performs a recursive copy from a Source to a local root.
type Tree struct {
	Src      Source
	Local    fs.Store
	Clock    clock.Clock
	Skip     SkipPolicy
	Resolve  DestResolver
	OnCopied func(destPath string, e Entry) // optional, invoked after a successful copy
	OnSkip   func(destPath string, e Entry) // optional, invoked when an entry is skipped by the SkipPolicy
	DryRun   bool                           // true = log the plan without copying (no Source.Copy, no fs mutation)
	Stats    *stats.Counters
	Log      func(format string, args ...any) // optional progress logger
}

// CopyTree recursively copies srcDir into dstRoot. dstRoot is the default
// destination root used when Resolve is nil or returns an empty root; a
// Resolve that returns its own root routes an entry elsewhere (single-pass
// multi-root).
func (c *Tree) CopyTree(ctx context.Context, srcDir, dstRoot string) error {
	if c.Src == nil {
		return fmt.Errorf("copier: nil Source")
	}
	if c.Local == nil {
		return fmt.Errorf("copier: nil Local fs")
	}
	if c.Stats == nil {
		return fmt.Errorf("copier: nil Stats")
	}
	if err := c.Local.MkdirAll(ctx, dstRoot, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dstRoot, err)
	}
	return c.copyDir(ctx, srcDir, "", dstRoot)
}

func (c *Tree) copyDir(ctx context.Context, srcDir, rel, dstRoot string) error {
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
			// When no custom resolver is set, mirror the source tree structure
			// under the default root — including empty dirs — to preserve
			// faithful backups (e.g. Supernote Note/Document folders). Custom
			// multi-root resolvers create their destination dirs on demand per
			// copied file instead.
			if c.Resolve == nil {
				dstSub := joinPath(dstRoot, e.RelPath)
				if err := c.Local.MkdirAll(ctx, dstSub, 0o755); err != nil {
					return fmt.Errorf("mkdir %s: %w", dstSub, err)
				}
			}
			if err := c.copyDir(ctx, srcPath, e.RelPath, dstRoot); err != nil {
				return err
			}
			continue
		}
		if err := c.copyOne(ctx, e, srcPath, dstRoot); err != nil {
			return err
		}
	}
	return nil
}

// copyOne handles a single non-directory entry: resolve its destination root
// and relative path, apply the skip policy, then copy to a temp in the dest
// directory and atomically rename it over the dest on success (preserving the
// previous backup on failure). A copy or rename failure is counted as
// stats.Failed and returns nil so the parent loop continues.
func (c *Tree) copyOne(ctx context.Context, e Entry, srcPath, dstRoot string) error {
	root, relPath, include := "", e.RelPath, true
	if c.Resolve != nil {
		root, relPath, include = c.Resolve(e)
	}
	if !include {
		return nil
	}
	if relPath == "" {
		return fmt.Errorf("copier: resolver returned empty relPath for %q", e.Name)
	}
	c.Stats.Inc(stats.Found, 1)
	if root == "" {
		root = dstRoot
	}

	destPath := joinPath(root, relPath)

	// Dry run: show the plan without mutating. applySkip is a read, so it still
	// runs to distinguish would-copy from already-backed-up; nothing is copied,
	// removed, or renamed, and stats.Copied is not incremented.
	if c.DryRun {
		skip, err := c.applySkip(ctx, e, destPath)
		if err != nil {
			return err
		}
		if skip {
			c.logf("skip existing: %s", e.RelPath)
			c.Stats.Inc(stats.Skipped, 1)
			if c.OnSkip != nil {
				c.OnSkip(destPath, e)
			}
			return nil
		}
		c.logf("would copy: %s -> %s", e.RelPath, destPath)
		if c.OnCopied != nil {
			c.OnCopied(destPath, e)
		}
		return nil
	}

	if err := c.Local.MkdirAll(ctx, filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("mkdir parent %s: %w", filepath.Dir(destPath), err)
	}

	skip, err := c.applySkip(ctx, e, destPath)
	if err != nil {
		return err
	}
	if skip {
		c.logf("skip existing: %s", e.RelPath)
		c.Stats.Inc(stats.Skipped, 1)
		if c.OnSkip != nil {
			c.OnSkip(destPath, e)
		}
		return nil
	}

	// Copy to a temp path in the same directory, then atomically rename over
	// the dest on success. This preserves the previous backup at destPath when
	// the copy fails or the run is cancelled — removing the dest first would
	// lose it (z41). Any stale temp from a prior aborted copy is cleaned up.
	tmpPath := destPath + copyTmpSuffix
	_ = c.Local.Remove(ctx, tmpPath)

	c.logf("copy: %s -> %s", e.RelPath, destPath)
	if err := c.Src.Copy(ctx, srcPath, tmpPath); err != nil {
		_ = c.Local.Remove(ctx, tmpPath)
		c.Stats.Inc(stats.Failed, 1)
		c.logf("copy failed: %s: %v", e.RelPath, err)
		return nil
	}
	if err := c.Local.Rename(ctx, tmpPath, destPath); err != nil {
		_ = c.Local.Remove(ctx, tmpPath)
		c.Stats.Inc(stats.Failed, 1)
		c.logf("install %s -> %s: %v", tmpPath, destPath, err)
		return nil
	}
	c.Stats.Inc(stats.Copied, 1)
	if c.OnCopied != nil {
		c.OnCopied(destPath, e)
	}
	return nil
}

func (c *Tree) applySkip(ctx context.Context, e Entry, destPath string) (bool, error) {
	if c.Skip == nil {
		return false, nil
	}
	clk := c.Clock
	if clk == nil {
		clk = zeroClock{}
	}
	return c.Skip(SkipCtx{Ctx: ctx, Src: c.Src, Entry: e, DestPath: destPath, Local: c.Local, Clock: clk})
}

func (c *Tree) logf(format string, args ...any) {
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
	if sc.Ctx == nil {
		sc.Ctx = context.Background()
	}
	_, err := sc.Local.Stat(sc.Ctx, sc.DestPath)
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
	if sc.Ctx == nil {
		sc.Ctx = context.Background()
	}
	de, err := sc.Local.Stat(sc.Ctx, sc.DestPath)
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
	if sc.Ctx == nil {
		sc.Ctx = context.Background()
	}
	de, err := sc.Local.Stat(sc.Ctx, sc.DestPath)
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

// ImportMetaSuffix is the sidecar written at import time (by a driver's
// OnCopied hook) recording the source entry's size. SkipExistingImportMeta
// uses it to dedup on the ORIGINAL source size, so a post-copy transform that
// rewrites the dest in place (e.g. exiftool geotag, which drifts the dest
// size) does not force a re-copy of a stable camera file every run.
//
// Once a file is imported it will NOT be re-processed by a post-copy
// transform even if the conditions for that transform change later (e.g. GPX
// tracks added after the fact): the sidecar makes the skip stick. Delete the
// sidecar (or the dest file) to force a re-import. Pre-existing files that
// predate this sidecar are re-imported once on the first run after deploy
// (no sidecar -> re-copy -> sidecar written) and are stable thereafter. An
// orphan sidecar left beside a missing dest (e.g. after a rollback removed
// the dest) is ignored — a missing dest always re-copies.
const ImportMetaSuffix = ".import-meta"

// SkipExistingImportMeta skips when the dest exists and either its current
// size matches the source (the file was not rewritten in place) or an
// ImportMetaSuffix sidecar records that the source size matched the last
// import. The sidecar leg survives an in-place rewrite that changes the dest
// size. A missing dest, a size mismatch with no sidecar, or a sidecar whose
// recorded size differs from the current source (the camera file changed)
// triggers a re-copy.
func SkipExistingImportMeta(sc SkipCtx) (bool, error) {
	if sc.Ctx == nil {
		sc.Ctx = context.Background()
	}
	de, err := sc.Local.Stat(sc.Ctx, sc.DestPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", sc.DestPath, err)
	}
	if de.Size == sc.Entry.Size {
		return true, nil // unchanged on disk (no in-place rewrite)
	}
	meta, err := sc.Local.ReadFile(sc.Ctx, sc.DestPath+ImportMetaSuffix)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil // no sidecar: re-copy
		}
		return false, fmt.Errorf("read import-meta %s: %w", sc.DestPath+ImportMetaSuffix, err)
	}
	recorded, err := strconv.ParseInt(string(meta), 10, 64)
	if err != nil {
		return false, nil // corrupt sidecar: re-copy
	}
	return recorded == sc.Entry.Size, nil
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
