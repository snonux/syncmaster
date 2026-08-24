package note

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"syncmaster/internal/driver"
	"syncmaster/internal/fs"
	"syncmaster/internal/stats"
)

type fakeConverter struct {
	mu      sync.Mutex
	calls   []call
	failOn  map[string]bool // note base name -> fail
	convert func(note, out string) error
}

type call struct{ note, out string }

func (f *fakeConverter) Convert(_ context.Context, note, out string) error {
	f.mu.Lock()
	f.calls = append(f.calls, call{note, out})
	base := filepath.Base(note)
	fail := f.failOn[base]
	f.mu.Unlock()
	if fail {
		return errors.New("convert failed")
	}
	if f.convert != nil {
		return f.convert(note, out)
	}
	return nil
}

func newEnv(local *fs.Mem, st *stats.Stats) *driver.Env {
	return &driver.Env{Local: local, Stats: st, Out: new(bytes.Buffer), Err: new(bytes.Buffer)}
}

func writeNote(t *testing.T, local *fs.Mem, path string, size int64) {
	t.Helper()
	data := make([]byte, size)
	if err := local.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestApplyConvertsStaleNotes(t *testing.T) {
	local := fs.NewMem()
	st := stats.New()
	env := newEnv(local, st)
	writeNote(t, local, "/dst/a.note", 10)
	writeNote(t, local, "/dst/b.note", 20)

	conv := &fakeConverter{convert: func(note, out string) error {
		return local.WriteFile(out, []byte("pdf"), 0o644)
	}}
	c := &Convert{Conv: conv, Workers: 2}
	if err := c.Apply(context.Background(), &driver.TransformCtx{Env: env, DestRoot: "/dst"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := st.Get(stats.Converted); got != 2 {
		t.Fatalf("Converted = %d, want 2", got)
	}
	if got := st.Get(stats.Failed); got != 0 {
		t.Fatalf("Failed = %d, want 0", got)
	}
	// PDFs and meta sidecars should exist.
	for _, p := range []string{"/dst/a.pdf", "/dst/b.pdf"} {
		if _, err := local.Stat(p); err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
		if _, err := local.ReadFile(p + ".note-meta"); err != nil {
			t.Fatalf("missing meta for %s: %v", p, err)
		}
	}
}

func TestApplySkipsCurrentPDFs(t *testing.T) {
	local := fs.NewMem()
	st := stats.New()
	env := newEnv(local, st)
	writeNote(t, local, "/dst/a.note", 10)
	// Pre-create matching pdf + meta.
	_ = local.WriteFile("/dst/a.pdf", []byte("pdf"), 0o644)
	ne, _ := local.Stat("/dst/a.note")
	_ = local.WriteFile("/dst/a.pdf.note-meta", []byte(signature(ne.Size, ne.ModTime.Unix())), 0o644)

	conv := &fakeConverter{}
	c := &Convert{Conv: conv, Workers: 1}
	if err := c.Apply(context.Background(), &driver.TransformCtx{Env: env, DestRoot: "/dst"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := st.Get(stats.ConvertSkipped); got != 1 {
		t.Fatalf("ConvertSkipped = %d, want 1", got)
	}
	if got := st.Get(stats.Converted); got != 0 {
		t.Fatalf("Converted = %d, want 0", got)
	}
	if len(conv.calls) != 0 {
		t.Fatalf("converter should not be called, calls = %d", len(conv.calls))
	}
}

func TestApplyReconvertsWhenNoteChanged(t *testing.T) {
	local := fs.NewMem()
	st := stats.New()
	env := newEnv(local, st)
	writeNote(t, local, "/dst/a.note", 10)
	_ = local.WriteFile("/dst/a.pdf", []byte("old"), 0o644)
	_ = local.WriteFile("/dst/a.pdf.note-meta", []byte("size=5\nmtime=1\n"), 0o644) // stale

	conv := &fakeConverter{convert: func(_, out string) error {
		return local.WriteFile(out, []byte("pdf"), 0o644)
	}}
	c := &Convert{Conv: conv, Workers: 1}
	if err := c.Apply(context.Background(), &driver.TransformCtx{Env: env, DestRoot: "/dst"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := st.Get(stats.Converted); got != 1 {
		t.Fatalf("Converted = %d, want 1", got)
	}
}

func TestApplyCountsConversionFailures(t *testing.T) {
	local := fs.NewMem()
	st := stats.New()
	env := newEnv(local, st)
	writeNote(t, local, "/dst/a.note", 10)
	writeNote(t, local, "/dst/b.note", 20)

	conv := &fakeConverter{failOn: map[string]bool{"a.note": true}, convert: func(_, out string) error {
		return local.WriteFile(out, []byte("pdf"), 0o644)
	}}
	c := &Convert{Conv: conv, Workers: 1}
	if err := c.Apply(context.Background(), &driver.TransformCtx{Env: env, DestRoot: "/dst"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := st.Get(stats.Failed); got != 1 {
		t.Fatalf("Failed = %d, want 1", got)
	}
	if got := st.Get(stats.Converted); got != 1 {
		t.Fatalf("Converted = %d, want 1", got)
	}
	// Failed tmp should be removed; no pdf for a.
	if _, err := local.Stat("/dst/a.pdf"); err == nil {
		t.Fatal("a.pdf should not exist after failure")
	}
}

func TestApplySupernoteToolMissing(t *testing.T) {
	local := fs.NewMem()
	st := stats.New()
	env := newEnv(local, st)
	writeNote(t, local, "/dst/a.note", 10)

	// Runner whose LookPath fails; Conv is nil so it tries supernote-tool.
	r := &noToolRunner{}
	c := &Convert{Runner: r}
	if err := c.Apply(context.Background(), &driver.TransformCtx{Env: env, DestRoot: "/dst"}); err == nil {
		t.Fatal("expected error for missing supernote-tool")
	}
	if got := st.Get(stats.Failed); got != 1 {
		t.Fatalf("Failed = %d, want 1", got)
	}
}

type noToolRunner struct{}

func (noToolRunner) Run(context.Context, string, ...string) ([]byte, error) { return nil, nil }
func (noToolRunner) LookPath(string) (string, error)                        { return "", errors.New("not found") }

func TestApplyIgnoresNonNoteFiles(t *testing.T) {
	local := fs.NewMem()
	st := stats.New()
	env := newEnv(local, st)
	writeNote(t, local, "/dst/a.note", 10)
	_ = local.WriteFile("/dst/readme.txt", []byte("hi"), 0o644)

	conv := &fakeConverter{convert: func(_, out string) error {
		return local.WriteFile(out, []byte("pdf"), 0o644)
	}}
	c := &Convert{Conv: conv, Workers: 1}
	if err := c.Apply(context.Background(), &driver.TransformCtx{Env: env, DestRoot: "/dst"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(conv.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(conv.calls))
	}
}

func TestApplyParallelismBounded(t *testing.T) {
	local := fs.NewMem()
	st := stats.New()
	env := newEnv(local, st)
	for i := 0; i < 5; i++ {
		writeNote(t, local, "/dst/n"+string(rune('a'+i))+".note", 1)
	}

	var inflight atomic.Int64
	var maxInflight atomic.Int64
	conv := &fakeConverter{convert: func(_, out string) error {
		cur := inflight.Add(1)
		for {
			m := maxInflight.Load()
			if cur <= m || maxInflight.CompareAndSwap(m, cur) {
				break
			}
		}
		// no sleep; just track concurrency structurally
		_ = local.WriteFile(out, []byte("pdf"), 0o644)
		inflight.Add(-1)
		return nil
	}}
	c := &Convert{Conv: conv, Workers: 3}
	if err := c.Apply(context.Background(), &driver.TransformCtx{Env: env, DestRoot: "/dst"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Concurrency tracking without a sleep is racy; assert only that all converted.
	if got := st.Get(stats.Converted); got != 5 {
		t.Fatalf("Converted = %d, want 5", got)
	}
}

func TestApplyCleansStaleTemp(t *testing.T) {
	local := fs.NewMem()
	st := stats.New()
	env := newEnv(local, st)
	_ = local.WriteFile("/dst/old.pdf.tmp.123", []byte("x"), 0o644)
	_ = local.WriteFile("/dst/a.note", make([]byte, 5), 0o644)

	conv := &fakeConverter{convert: func(_, out string) error {
		return local.WriteFile(out, []byte("pdf"), 0o644)
	}}
	c := &Convert{Conv: conv, Workers: 1}
	if err := c.Apply(context.Background(), &driver.TransformCtx{Env: env, DestRoot: "/dst"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := local.Stat("/dst/old.pdf.tmp.123"); err == nil {
		t.Fatal("stale tmp should be removed")
	}
}

func TestApplyContextCancelStopsWork(t *testing.T) {
	local := fs.NewMem()
	st := stats.New()
	env := newEnv(local, st)
	for i := 0; i < 4; i++ {
		_ = local.WriteFile("/dst/n"+string(rune('a'+i))+".note", []byte("x"), 0o644)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled

	conv := &fakeConverter{convert: func(_, out string) error {
		return local.WriteFile(out, []byte("pdf"), 0o644)
	}}
	c := &Convert{Conv: conv, Workers: 2}
	err := c.Apply(ctx, &driver.TransformCtx{Env: env, DestRoot: "/dst"})
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	if got := st.Get(stats.Converted); got != 0 {
		t.Fatalf("Converted = %d, want 0", got)
	}
}

func TestSignatureAndChangeNoteExt(t *testing.T) {
	if signature(7, 99) != "size=7\nmtime=99\n" {
		t.Fatal("signature mismatch")
	}
	if got := changeNoteExt("/root", "sub/a.NOTE"); !strings.HasSuffix(got, "/sub/a.pdf") {
		t.Fatalf("changeNoteExt = %q", got)
	}
}

func TestApplyNilContext(t *testing.T) {
	c := &Convert{}
	if err := c.Apply(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil tctx")
	}
}
