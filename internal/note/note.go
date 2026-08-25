// Package note implements the note→PDF Transform: it converts Supernote .note
// files to PDF using supernote-tool, skipping PDFs that are already current per
// a .note-meta signature sidecar, and runs conversions in a bounded worker
// pool.
package note

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"syncmaster/internal/driver"
	"syncmaster/internal/fs"
	"syncmaster/internal/shell"
	"syncmaster/internal/stats"
)

// Converter converts a single .note file to an output PDF path.
type Converter interface {
	Convert(ctx context.Context, notePath, outPath string) error
}

// Convert is a driver.Transform that converts .note files to PDFs.
type Convert struct {
	Runner  shell.Runner // used when Conv is nil (supernote-tool)
	Conv    Converter    // optional override for tests
	Workers int          // parallel conversions; defaults to 1
}

var _ driver.Transform = (*Convert)(nil)

// Name returns the transform name.
func (c *Convert) Name() string { return "note-to-pdf" }

// Apply walks tctx.DestRoot for .note files and converts stale ones.
func (c *Convert) Apply(ctx context.Context, tctx *driver.TransformCtx) error {
	if c == nil || tctx == nil {
		return fmt.Errorf("note: nil context")
	}
	if err := tctx.Resolve(); err != nil {
		return fmt.Errorf("note: %w", err)
	}
	local := tctx.Local
	root := tctx.DestRoot
	conv := c.Conv
	if conv == nil {
		if _, err := c.Runner.LookPath("supernote-tool"); err != nil {
			tctx.Stats.Inc(stats.Failed, 1)
			return fmt.Errorf("note: supernote-tool not found: %w", err)
		}
		conv = toolConverter{c.Runner}
	}

	if err := local.MkdirAll(ctx, root, 0o755); err != nil {
		return fmt.Errorf("note: mkdir %s: %w", root, err)
	}
	if err := cleanStaleTemp(ctx, local, root); err != nil {
		return fmt.Errorf("note: clean tmp: %w", err)
	}

	jobs, err := c.enqueue(ctx, local, root, tctx)
	if err != nil {
		return fmt.Errorf("note: scan notes: %w", err)
	}
	c.runWorkers(ctx, local, conv, jobs, tctx)
	return nil
}

type job struct {
	note string
	pdf  string
	meta string
}

func (c *Convert) enqueue(ctx context.Context, local fs.Store, root string, tctx *driver.TransformCtx) ([]job, error) {
	var jobs []job
	err := local.WalkDir(ctx, root, func(path string, e fs.Entry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.IsDir {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".note") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		pdfPath := changeNoteExt(root, rel)
		metaPath := pdfPath + ".note-meta"
		if err := local.MkdirAll(ctx, filepath.Dir(pdfPath), 0o755); err != nil {
			return err
		}
		if pdfCurrent(ctx, local, path, pdfPath, metaPath) {
			tctx.Stats.Inc(stats.ConvertSkipped, 1)
			return nil
		}
		jobs = append(jobs, job{note: path, pdf: pdfPath, meta: metaPath})
		return nil
	})
	return jobs, err
}

// changeNoteExt replaces the .note suffix in rel with .pdf and joins it under
// root.
func changeNoteExt(root, rel string) string {
	ext := filepath.Ext(rel)
	base := strings.TrimSuffix(rel, ext)
	return filepath.Join(root, base+".pdf")
}

func pdfCurrent(ctx context.Context, local fs.Store, notePath, pdfPath, metaPath string) bool {
	if _, err := local.Stat(ctx, pdfPath); err != nil {
		return false
	}
	meta, err := local.ReadFile(ctx, metaPath)
	if err != nil {
		return false
	}
	ne, err := local.Stat(ctx, notePath)
	if err != nil {
		return false
	}
	return string(meta) == signature(ne.Size, ne.ModTime.Unix())
}

func signature(size int64, mtime int64) string {
	return fmt.Sprintf("size=%d\nmtime=%d\n", size, mtime)
}

// staleTempRe matches exactly the orphaned temp files this package creates:
// "<stem>.pdf.tmp.<uint>" (convertOne writes fmt.Sprintf("%s.tmp.%d", j.pdf, n),
// and j.pdf ends in ".pdf"). The anchored integer suffix avoids deleting a
// legitimate user file that merely contains ".pdf.tmp." somewhere in its
// name (silent data loss in the user's backup destination). A user file that
// happens to match this exact shape would still be reaped; distinguishing it
// from a real orphan would require recording the temp paths created this run.
var staleTempRe = regexp.MustCompile(`^.*\.pdf\.tmp\.[0-9]+$`)

func cleanStaleTemp(ctx context.Context, local fs.Store, root string) error {
	return local.WalkDir(ctx, root, func(path string, e fs.Entry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.IsDir {
			return nil
		}
		if staleTempRe.MatchString(filepath.Base(path)) {
			_ = local.Remove(ctx, path)
		}
		return nil
	})
}

func (c *Convert) runWorkers(ctx context.Context, local fs.Store, conv Converter, jobs []job, tctx *driver.TransformCtx) {
	if len(jobs) == 0 {
		return
	}
	// tctx.Logger is concurrency-safe (WriterLogger serializes writes), so the
	// N parallel workers can log through it directly without a local mutex.
	ce := &convertEnv{local: local, conv: conv, tctx: tctx, counter: &atomic.Int64{}}

	n := c.Workers
	if n < 1 {
		n = 1
	}
	if n > len(jobs) {
		n = len(jobs)
	}

	work := make(chan job, len(jobs))
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range work {
				c.convertOne(ctx, ce, j)
			}
		}()
	}

	for _, j := range jobs {
		select {
		case <-ctx.Done():
			// Drain remaining jobs as failed.
			tctx.Stats.Inc(stats.Failed, int64(len(jobs)-len(work)))
			close(work)
			wg.Wait()
			return
		case work <- j:
		}
	}
	close(work)
	wg.Wait()
}

// convertEnv bundles the per-worker dependencies handed to convertOne so its
// signature stays small.
type convertEnv struct {
	local   fs.Store
	conv    Converter
	tctx    *driver.TransformCtx
	counter *atomic.Int64
}

// convertOne converts a single job's .note to PDF and records the outcome.
// A failure increments stats.Failed and returns; only a fully successful
// convert+rename+meta increments stats.Converted.
func (c *Convert) convertOne(ctx context.Context, ce *convertEnv, j job) {
	if err := ctx.Err(); err != nil {
		ce.tctx.Stats.Inc(stats.Failed, 1)
		return
	}
	tmp := fmt.Sprintf("%s.tmp.%d", j.pdf, ce.counter.Add(1))
	if err := ce.conv.Convert(ctx, j.note, tmp); err != nil {
		_ = ce.local.Remove(ctx, tmp)
		ce.tctx.Logger.Error("failed to convert: %s: %v", j.note, err)
		ce.tctx.Stats.Inc(stats.Failed, 1)
		return
	}
	if err := ce.local.Rename(ctx, tmp, j.pdf); err != nil {
		_ = ce.local.Remove(ctx, tmp)
		ce.tctx.Stats.Inc(stats.Failed, 1)
		return
	}
	ne, err := ce.local.Stat(ctx, j.note)
	if err != nil {
		ce.tctx.Stats.Inc(stats.Failed, 1)
		return
	}
	if err := ce.local.WriteFile(ctx, j.meta, []byte(signature(ne.Size, ne.ModTime.Unix())), 0o644); err != nil {
		ce.tctx.Stats.Inc(stats.Failed, 1)
		return
	}
	ce.tctx.Logger.Info("convert: %s -> %s", j.note, j.pdf)
	ce.tctx.Stats.Inc(stats.Converted, 1)
}

// toolConverter calls supernote-tool to convert a .note to a PDF.
type toolConverter struct{ r shell.Runner }

var _ Converter = (*toolConverter)(nil)

func (t toolConverter) Convert(ctx context.Context, notePath, outPath string) error {
	_, err := t.r.Run(ctx, "supernote-tool", "convert", "-a", "-t", "pdf", notePath, outPath)
	if err != nil {
		return fmt.Errorf("supernote-tool convert: %w", err)
	}
	return nil
}
