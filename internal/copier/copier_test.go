package copier

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"syncmaster/internal/fs"
	"syncmaster/internal/stats"
)

// memSource is a fake copier.Source backed by an in-memory tree.
type memSource struct {
	tree map[string][]Entry // dir -> entries
	data map[string][]byte  // file path -> content (for Copy)
}

func newMemSource() *memSource {
	return &memSource{tree: map[string][]Entry{}, data: map[string][]byte{}}
}

func (s *memSource) addDir(dir, name string) {
	s.tree[dir] = append(s.tree[dir], Entry{Name: name, IsDir: true})
	sub := filepath.Join(dir, name)
	s.tree[sub] = nil
}

func (s *memSource) addFile(dir, name string, size int64, mt time.Time, content []byte) {
	s.tree[dir] = append(s.tree[dir], Entry{Name: name, Size: size, Modified: mt})
	s.data[filepath.Join(dir, name)] = content
}

func (s *memSource) List(_ context.Context, dir string) ([]Entry, error) {
	es, ok := s.tree[dir]
	if !ok {
		return nil, errors.New("not found")
	}
	return es, nil
}

func (s *memSource) Copy(_ context.Context, src, dst string) error {
	_, ok := s.data[src]
	if !ok {
		return errors.New("source missing")
	}
	return nil // dest written via Local in test hook below
}

// copyWritingSource wraps a Source so Copy also writes the file to the local FS.
type copyWritingSource struct {
	*memSource
	local fs.FS
}

func (s copyWritingSource) Copy(ctx context.Context, src, dst string) error {
	if err := s.memSource.Copy(ctx, src, dst); err != nil {
		return err
	}
	content := s.data[src]
	return s.local.WriteFile(dst, content, 0o644)
}

func buildTree(t *testing.T) *memSource {
	t.Helper()
	s := newMemSource()
	mt := time.Unix(1000, 0)
	s.addDir("/src", "sub")
	s.addFile("/src", "a.jpg", 3, mt, []byte("aaa"))
	s.addFile("/src", "b.txt", 2, mt, []byte("bb"))
	s.addFile("/src/sub", "c.jpg", 4, mt, []byte("cccc"))
	return s
}

func TestCopyTreeCopiesAll(t *testing.T) {
	src := buildTree(t)
	local := fs.NewMem()
	st := stats.New()
	c := &Copier{
		Src:   copyWritingSource{src, local},
		Local: local,
		Stats: st,
	}
	if err := c.CopyTree(context.Background(), "/src", "/dst"); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	if g := st.Get(stats.Found); g != 3 {
		t.Fatalf("Found = %d, want 3", g)
	}
	if g := st.Get(stats.Copied); g != 3 {
		t.Fatalf("Copied = %d, want 3", g)
	}
	for _, p := range []string{"/dst/a.jpg", "/dst/b.txt", "/dst/sub/c.jpg"} {
		if _, err := local.Stat(p); err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
	}
}

func TestCopyTreeResolverExcludes(t *testing.T) {
	src := buildTree(t)
	local := fs.NewMem()
	st := stats.New()
	c := &Copier{
		Src:   copyWritingSource{src, local},
		Local: local,
		Stats: st,
		Resolve: func(e Entry) (string, bool) {
			// include only .jpg files
			if filepath.Ext(e.Name) == ".jpg" {
				return e.Name, true
			}
			return "", false
		},
	}
	if err := c.CopyTree(context.Background(), "/src", "/dst"); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	if g := st.Get(stats.Found); g != 2 {
		t.Fatalf("Found = %d, want 2 (jpg only)", g)
	}
	if _, err := local.Stat("/dst/b.txt"); err == nil {
		t.Fatal("b.txt should be excluded")
	}
	if _, err := local.Stat("/dst/a.jpg"); err != nil {
		t.Fatalf("a.jpg missing: %v", err)
	}
}

func TestCopyTreeSkipExistingName(t *testing.T) {
	src := buildTree(t)
	local := fs.NewMem()
	_ = local.WriteFile("/dst/a.jpg", []byte("x"), 0o644)
	st := stats.New()
	c := &Copier{
		Src:   copyWritingSource{src, local},
		Local: local,
		Stats: st,
		Skip:  SkipExistingName,
	}
	if err := c.CopyTree(context.Background(), "/src", "/dst"); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	if g := st.Get(stats.Skipped); g != 1 {
		t.Fatalf("Skipped = %d, want 1", g)
	}
	if g := st.Get(stats.Copied); g != 2 {
		t.Fatalf("Copied = %d, want 2", g)
	}
}

func TestCopyTreeSkipUnchangedSizeMtime(t *testing.T) {
	mt := time.Unix(1000, 0)
	src := newMemSource()
	src.addFile("/src", "a.txt", 3, mt, []byte("aaa"))
	local := fs.NewMem()
	local.WriteFileAt("/dst/a.txt", []byte("aaa"), mt) // matches size+mtime
	st := stats.New()
	c := &Copier{
		Src:   copyWritingSource{src, local},
		Local: local,
		Stats: st,
		Skip:  SkipUnchangedSizeMtime,
	}
	if err := c.CopyTree(context.Background(), "/src", "/dst"); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	if g := st.Get(stats.Skipped); g != 1 {
		t.Fatalf("Skipped = %d, want 1", g)
	}
	if g := st.Get(stats.Copied); g != 0 {
		t.Fatalf("Copied = %d, want 0", g)
	}
}

func TestCopyTreeSkipUnchangedReplacesWhenChanged(t *testing.T) {
	mt := time.Unix(1000, 0)
	src := newMemSource()
	src.addFile("/src", "a.txt", 5, mt, []byte("aaaaa"))
	local := fs.NewMem()
	local.WriteFileAt("/dst/a.txt", []byte("old"), time.Unix(999, 0)) // different size+mtime
	st := stats.New()
	c := &Copier{
		Src:   copyWritingSource{src, local},
		Local: local,
		Stats: st,
		Skip:  SkipUnchangedSizeMtime,
	}
	if err := c.CopyTree(context.Background(), "/src", "/dst"); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	if g := st.Get(stats.Copied); g != 1 {
		t.Fatalf("Copied = %d, want 1", g)
	}
	got, _ := local.ReadFile("/dst/a.txt")
	if string(got) != "aaaaa" {
		t.Fatalf("content = %q, want aaaaa", got)
	}
}

func TestCopyTreeOnCopied(t *testing.T) {
	src := buildTree(t)
	local := fs.NewMem()
	st := stats.New()
	var copied []string
	c := &Copier{
		Src:      copyWritingSource{src, local},
		Local:    local,
		Stats:    st,
		OnCopied: func(p string, _ Entry) { copied = append(copied, p) },
	}
	_ = c.CopyTree(context.Background(), "/src", "/dst")
	if len(copied) != 3 {
		t.Fatalf("OnCopied called %d times, want 3", len(copied))
	}
}

func TestCopyTreeCopyFailureIncrementsFailed(t *testing.T) {
	src := newMemSource()
	src.addFile("/src", "a.txt", 3, time.Unix(1, 0), []byte("aaa"))
	local := fs.NewMem()
	st := stats.New()
	// Source whose Copy always fails.
	failing := &failingSource{src}
	c := &Copier{Src: failing, Local: local, Stats: st}
	if err := c.CopyTree(context.Background(), "/src", "/dst"); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	if g := st.Get(stats.Failed); g != 1 {
		t.Fatalf("Failed = %d, want 1", g)
	}
}

func TestCopyTreeContextCancel(t *testing.T) {
	src := buildTree(t)
	local := fs.NewMem()
	st := stats.New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &Copier{Src: copyWritingSource{src, local}, Local: local, Stats: st}
	if err := c.CopyTree(ctx, "/src", "/dst"); err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestCopyTreeNilDeps(t *testing.T) {
	c := &Copier{Local: fs.NewMem(), Stats: stats.New()}
	if err := c.CopyTree(context.Background(), "/src", "/dst"); err == nil {
		t.Fatal("expected error for nil Src")
	}
	c2 := &Copier{Src: newMemSource(), Stats: stats.New()}
	if err := c2.CopyTree(context.Background(), "/src", "/dst"); err == nil {
		t.Fatal("expected error for nil Local")
	}
}

type failingSource struct{ *memSource }

func (f failingSource) Copy(context.Context, string, string) error { return io.ErrShortBuffer }

// blockingSource blocks on ctx for the first file's Copy, then returns the ctx
// error. Used to prove cancellation aborts a copy in progress.
type blockingSource struct{ *memSource }

func (b blockingSource) Copy(ctx context.Context, src, dst string) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestCopyTreeAbortMidCopy(t *testing.T) {
	src := newMemSource()
	src.addFile("/src", "a.txt", 1, time.Unix(1, 0), []byte("a"))
	src.addFile("/src", "b.txt", 1, time.Unix(1, 0), []byte("b"))
	local := fs.NewMem()
	st := stats.New()
	ctx, cancel := context.WithCancel(context.Background())
	c := &Copier{Src: blockingSource{src}, Local: local, Stats: st}

	done := make(chan error, 1)
	go func() { done <- c.CopyTree(ctx, "/src", "/dst") }()
	cancel() // abort mid-copy
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CopyTree did not abort within 2s")
	}
	if st.Get(stats.Copied) != 0 {
		t.Fatalf("Copied = %d, want 0", st.Get(stats.Copied))
	}
}
