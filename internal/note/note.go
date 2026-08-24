// Package note implements the note→PDF Transform: it converts Supernote .note
// files to PDF using supernote-tool, skipping PDFs that are already current per
// a .note-meta signature sidecar, and runs conversions in a bounded worker
// pool.
package note

import (
	"context"
	"fmt"
	"path/filepath"
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
	if c == nil || tctx == nil || tctx.Env == nil {
		return fmt.Errorf("note: nil context")
	}
	local := tctx.Env.Local
	root := tctx.DestRoot
	conv := c.Conv
	if conv == nil {
		if _, err := c.Runner.LookPath("supernote-tool"); err != nil {
			tctx.Env.Stats.Inc(stats.Failed, 1)
			return fmt.Errorf("note: supernote-tool not found: %w", err)
		}
		conv = toolConverter{c.Runner}
	}

	if err := local.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("note: mkdir %s: %w", root, err)
	}
	if err := cleanStaleTemp(local, root); err != nil {
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

func (c *Convert) enqueue(ctx context.Context, local fs.FS, root string, tctx *driver.TransformCtx) ([]job, error) {
	var jobs []job
	err := local.WalkDir(root, func(path string, e fs.Entry) error {
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
		if err := local.MkdirAll(filepath.Dir(pdfPath), 0o755); err != nil {
			return err
		}
		if pdfCurrent(local, path, pdfPath, metaPath) {
			tctx.Env.Stats.Inc(stats.ConvertSkipped, 1)
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

func pdfCurrent(local fs.FS, notePath, pdfPath, metaPath string) bool {
	if _, err := local.Stat(pdfPath); err != nil {
		return false
	}
	meta, err := local.ReadFile(metaPath)
	if err != nil {
		return false
	}
	ne, err := local.Stat(notePath)
	if err != nil {
		return false
	}
	return string(meta) == signature(ne.Size, ne.ModTime.Unix())
}

func signature(size int64, mtime int64) string {
	return fmt.Sprintf("size=%d\nmtime=%d\n", size, mtime)
}

func cleanStaleTemp(local fs.FS, root string) error {
	return local.WalkDir(root, func(path string, e fs.Entry) error {
		if e.IsDir {
			return nil
		}
		if strings.Contains(filepath.Base(path), ".pdf.tmp.") {
			_ = local.Remove(path)
		}
		return nil
	})
}

func (c *Convert) runWorkers(ctx context.Context, local fs.FS, conv Converter, jobs []job, tctx *driver.TransformCtx) {
	if len(jobs) == 0 {
		return
	}
	var logMu sync.Mutex
	log := func(format string, args ...any) {
		logMu.Lock()
		_, _ = fmt.Fprintf(tctx.Env.Out, format+"\n", args...)
		logMu.Unlock()
	}
	errLog := func(format string, args ...any) {
		logMu.Lock()
		_, _ = fmt.Fprintf(tctx.Env.Err, format+"\n", args...)
		logMu.Unlock()
	}

	n := c.Workers
	if n < 1 {
		n = 1
	}
	if n > len(jobs) {
		n = len(jobs)
	}

	work := make(chan job, len(jobs))
	var wg sync.WaitGroup
	var counter atomic.Int64

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range work {
				if err := ctx.Err(); err != nil {
					tctx.Env.Stats.Inc(stats.Failed, 1)
					continue
				}
				tmp := fmt.Sprintf("%s.tmp.%d", j.pdf, counter.Add(1))
				if err := conv.Convert(ctx, j.note, tmp); err != nil {
					_ = local.Remove(tmp)
					errLog("failed to convert: %s: %v", j.note, err)
					tctx.Env.Stats.Inc(stats.Failed, 1)
					continue
				}
				if err := local.Rename(tmp, j.pdf); err != nil {
					_ = local.Remove(tmp)
					tctx.Env.Stats.Inc(stats.Failed, 1)
					continue
				}
				ne, err := local.Stat(j.note)
				if err != nil {
					tctx.Env.Stats.Inc(stats.Failed, 1)
					continue
				}
				if err := local.WriteFile(j.meta, []byte(signature(ne.Size, ne.ModTime.Unix())), 0o644); err != nil {
					tctx.Env.Stats.Inc(stats.Failed, 1)
					continue
				}
				log("convert: %s -> %s", j.note, j.pdf)
				tctx.Env.Stats.Inc(stats.Converted, 1)
			}
		}()
	}

	for _, j := range jobs {
		select {
		case <-ctx.Done():
			// Drain remaining jobs as failed.
			tctx.Env.Stats.Inc(stats.Failed, int64(len(jobs)-len(work)))
			close(work)
			wg.Wait()
			return
		case work <- j:
		}
	}
	close(work)
	wg.Wait()
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
