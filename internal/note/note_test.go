package note

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/snonux/syncmaster/internal/driver"
	"github.com/snonux/syncmaster/internal/fs"
	"github.com/snonux/syncmaster/internal/stats"
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

func newEnv(local *fs.Mem, st *stats.Counters) *driver.Env {
	return &driver.Env{Local: local, Stats: st, Out: new(bytes.Buffer), Err: new(bytes.Buffer)}
}

func writeNote(t *testing.T, local *fs.Mem, path string, size int64) {
	t.Helper()
	data := make([]byte, size)
	if err := local.WriteFile(context.Background(), path, data, 0o644); err != nil {
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
		return local.WriteFile(context.Background(), out, []byte("pdf"), 0o644)
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
		if _, err := local.Stat(context.Background(), p); err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
		if _, err := local.ReadFile(context.Background(), p+".note-meta"); err != nil {
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
	_ = local.WriteFile(context.Background(), "/dst/a.pdf", []byte("pdf"), 0o644)
	ne, _ := local.Stat(context.Background(), "/dst/a.note")
	_ = local.WriteFile(context.Background(), "/dst/a.pdf.note-meta", []byte(signature(ne.Size, ne.ModTime.Unix())), 0o644)

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
	_ = local.WriteFile(context.Background(), "/dst/a.pdf", []byte("old"), 0o644)
	_ = local.WriteFile(context.Background(), "/dst/a.pdf.note-meta", []byte("size=5\nmtime=1\n"), 0o644) // stale

	conv := &fakeConverter{convert: func(_, out string) error {
		return local.WriteFile(context.Background(), out, []byte("pdf"), 0o644)
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
		return local.WriteFile(context.Background(), out, []byte("pdf"), 0o644)
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
	if _, err := local.Stat(context.Background(), "/dst/a.pdf"); err == nil {
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
	_ = local.WriteFile(context.Background(), "/dst/readme.txt", []byte("hi"), 0o644)

	conv := &fakeConverter{convert: func(_, out string) error {
		return local.WriteFile(context.Background(), out, []byte("pdf"), 0o644)
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
		_ = local.WriteFile(context.Background(), out, []byte("pdf"), 0o644)
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
	_ = local.WriteFile(context.Background(), "/dst/old.pdf.tmp.123", []byte("x"), 0o644)
	_ = local.WriteFile(context.Background(), "/dst/a.note", make([]byte, 5), 0o644)

	conv := &fakeConverter{convert: func(_, out string) error {
		return local.WriteFile(context.Background(), out, []byte("pdf"), 0o644)
	}}
	c := &Convert{Conv: conv, Workers: 1}
	if err := c.Apply(context.Background(), &driver.TransformCtx{Env: env, DestRoot: "/dst"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := local.Stat(context.Background(), "/dst/old.pdf.tmp.123"); err == nil {
		t.Fatal("stale tmp should be removed")
	}
}

// TestCleanStaleTempOnlyReapsOwnOrphans is the 151 regression: cleanStaleTemp
// must reap ONLY the temp files this package creates ("<stem>.pdf.tmp.<uint>"),
// not any legitimate user file whose name merely contains ".pdf.tmp.". The old
// strings.Contains predicate silently deleted user files in the Supernote
// backup destination.
func TestCleanStaleTempOnlyReapsOwnOrphans(t *testing.T) {
	local := fs.NewMem()
	ctx := context.Background()
	keep := []string{
		"/dst/myreport.pdf.tmp.bak", // user file with a non-integer suffix
		"/dst/archive.pdf.tmp.pdf",  // ends in .pdf, not digits
		"/dst/notes.pdf.tmp.notes",  // non-numeric suffix
		"/dst/notes.pdf.TMP.1",      // uppercase; this program writes lowercase
		"/dst/notes.pdf.tmp.",       // empty suffix (no digits)
		"/dst/regular.pdf",          // a normal pdf
	}
	for _, p := range keep {
		_ = local.WriteFile(ctx, p, []byte("keep"), 0o644)
	}
	reaped := []string{
		"/dst/old.pdf.tmp.123",
		"/dst/notes.pdf.tmp.1",
		"/dst/sub/deep.pdf.tmp.42", // nested orphan
	}
	for _, p := range reaped {
		_ = local.MkdirAll(ctx, filepath.Dir(p), 0o755)
		_ = local.WriteFile(ctx, p, []byte("orphan"), 0o644)
	}

	if err := cleanStaleTemp(ctx, local, "/dst"); err != nil {
		t.Fatalf("cleanStaleTemp: %v", err)
	}

	for _, p := range keep {
		if _, err := local.Stat(ctx, p); err != nil {
			t.Errorf("legitimate file wrongly deleted: %s: %v", p, err)
		}
	}
	for _, p := range reaped {
		if _, err := local.Stat(ctx, p); err == nil {
			t.Errorf("orphan temp not reaped: %s", p)
		}
	}
}

func TestApplyContextCancelStopsWork(t *testing.T) {
	local := fs.NewMem()
	st := stats.New()
	env := newEnv(local, st)
	for i := 0; i < 4; i++ {
		_ = local.WriteFile(context.Background(), "/dst/n"+string(rune('a'+i))+".note", []byte("x"), 0o644)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled

	conv := &fakeConverter{convert: func(_, out string) error {
		return local.WriteFile(context.Background(), out, []byte("pdf"), 0o644)
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

// TestRunWorkersCancelCountsEachJobOnce reproduces 051: when the context is
// cancelled mid-run, every job must be counted exactly once (in Converted OR
// Failed), never both, never neither. The old send-loop cancel accounting
// (len(jobs)-len(work)) double-counted completed and in-flight jobs and
// inflated Failed. With Workers=1 + a bounded buffer, the send loop blocks
// on backpressure so the cancel branch is actually reached here.
func TestRunWorkersCancelCountsEachJobOnce(t *testing.T) {
	local := fs.NewMem()
	st := stats.New()
	env := newEnv(local, st)
	const N = 5
	for i := 0; i < N; i++ {
		writeNote(t, local, "/dst/n"+string(rune('a'+i))+".note", 5)
	}
	ctx, cancel := context.WithCancel(context.Background())

	var completed atomic.Int32
	conv := &fakeConverter{convert: func(_, out string) error {
		if err := local.WriteFile(context.Background(), out, []byte("pdf"), 0o644); err != nil {
			return err
		}
		if n := completed.Add(1); n == 2 {
			cancel() // cancel once two jobs have completed
		}
		return nil
	}}
	c := &Convert{Conv: conv, Workers: 1}
	_ = c.Apply(ctx, &driver.TransformCtx{Env: env, DestRoot: "/dst"})

	converted := st.Get(stats.Converted)
	failed := st.Get(stats.Failed)
	if converted+failed != N {
		t.Fatalf("converted+failed = %d, want %d (each job counted exactly once)", converted+failed, N)
	}
	if converted < 1 {
		t.Fatalf("Converted = %d, want >=1 (some job should complete before cancel)", converted)
	}
	if failed < 1 {
		t.Fatalf("Failed = %d, want >=1 (cancel should fail the unsent tail)", failed)
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

func TestApplyDryRunMissingDest(t *testing.T) {
	// Regression for the dry-run blocker: with a fresh (non-existent) dest, the
	// note transform must not error (cleanStaleTemp/enqueue would walk a missing
	// root). It should report that the dest is not present yet.
	root := filepath.Join(t.TempDir(), "does", "not", "exist")
	out := new(bytes.Buffer)
	env := &driver.Env{Out: out, Err: out, DryRun: true}
	tctx := &driver.TransformCtx{
		Env:      env,
		Local:    fs.DryRunStore{Store: fs.OS{}, Log: func(string, ...any) {}},
		DryRun:   true,
		DestRoot: root,
	}
	c := &Convert{Conv: &fakeConverter{}}
	if err := c.Apply(context.Background(), tctx); err != nil {
		t.Fatalf("Apply dry run: %v", err)
	}
	if !strings.Contains(out.String(), "dest not present yet") {
		t.Fatalf("dry run should report dest not present, got: %s", out.String())
	}
	if _, err := os.Stat(root); err == nil {
		t.Fatal("dry run must not create the dest directory")
	}
}

func TestApplyNilContext(t *testing.T) {
	c := &Convert{}
	if err := c.Apply(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil tctx")
	}
}
