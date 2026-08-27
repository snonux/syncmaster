package copier

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/snonux/syncmaster/internal/fs"
	"github.com/snonux/syncmaster/internal/stats"
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
	local fs.Store
}

func (s copyWritingSource) Copy(ctx context.Context, src, dst string) error {
	if err := s.memSource.Copy(ctx, src, dst); err != nil {
		return err
	}
	content := s.data[src]
	return s.local.WriteFile(ctx, dst, content, 0o644)
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
	c := &Tree{
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
		if _, err := local.Stat(context.Background(), p); err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
	}
}

func TestCopyTreeMirrorsEmptyDirsWithNilResolver(t *testing.T) {
	// With no custom resolver, empty source dirs are mirrored under the
	// default root to preserve faithful backups (e.g. Supernote folders).
	src := newMemSource()
	src.addDir("/src", "empty")
	src.addFile("/src", "a.txt", 1, time.Unix(1, 0), []byte("a"))
	local := fs.NewMem()
	st := stats.New()
	c := &Tree{Src: copyWritingSource{src, local}, Local: local, Stats: st}
	if err := c.CopyTree(context.Background(), "/src", "/dst"); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	if _, err := local.Stat(context.Background(), "/dst/empty"); err != nil {
		t.Fatalf("empty source dir should be mirrored: %v", err)
	}
}

func TestCopyTreeResolverEmptyRelPathErrors(t *testing.T) {
	src := newMemSource()
	src.addFile("/src", "a.txt", 1, time.Unix(1, 0), []byte("a"))
	local := fs.NewMem()
	st := stats.New()
	c := &Tree{
		Src:     copyWritingSource{src, local},
		Local:   local,
		Stats:   st,
		Resolve: func(e Entry) (string, string, bool) { return "/dst", "", true },
	}
	if err := c.CopyTree(context.Background(), "/src", "/dst"); err == nil {
		t.Fatal("expected error for resolver returning empty relPath")
	}
}

func TestCopyTreeResolverExcludes(t *testing.T) {
	src := buildTree(t)
	local := fs.NewMem()
	st := stats.New()
	c := &Tree{
		Src:   copyWritingSource{src, local},
		Local: local,
		Stats: st,
		Resolve: func(e Entry) (string, string, bool) {
			// include only .jpg files, mirroring them under the default dstRoot.
			if filepath.Ext(e.Name) == ".jpg" {
				return "", e.RelPath, true
			}
			return "", "", false
		},
	}
	if err := c.CopyTree(context.Background(), "/src", "/dst"); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	if g := st.Get(stats.Found); g != 2 {
		t.Fatalf("Found = %d, want 2 (jpg only)", g)
	}
	if _, err := local.Stat(context.Background(), "/dst/b.txt"); err == nil {
		t.Fatal("b.txt should be excluded")
	}
	if _, err := local.Stat(context.Background(), "/dst/a.jpg"); err != nil {
		t.Fatalf("a.jpg missing: %v", err)
	}
	// Nested .jpg mirrors under the default root via e.RelPath.
	if _, err := local.Stat(context.Background(), "/dst/sub/c.jpg"); err != nil {
		t.Fatalf("nested c.jpg missing: %v", err)
	}
}

func TestCopyTreeMultiRootRouting(t *testing.T) {
	// A single CopyTree pass routes entries to multiple destination roots via
	// the resolver (e.g. RAW -> /raw, JPEG/video -> /jpg), preserving the
	// source tree structure under each root. The default dstRoot is unused
	// because the resolver always returns an explicit root.
	src := newMemSource()
	mt := time.Unix(1000, 0)
	src.addDir("/src", "DCIM")
	src.addFile("/src", "DSC0001.RAF", 3, mt, []byte("raw"))
	src.addFile("/src", "DSC0002.JPG", 3, mt, []byte("jpg"))
	src.addFile("/src/DCIM", "DSC0003.RAF", 3, mt, []byte("raw3"))
	src.addFile("/src/DCIM", "clip.MOV", 3, mt, []byte("mov"))
	src.addFile("/src", "readme.txt", 4, mt, []byte("skip"))
	local := fs.NewMem()
	st := stats.New()
	c := &Tree{
		Src:   copyWritingSource{src, local},
		Local: local,
		Stats: st,
		Skip:  SkipExistingSize,
		Resolve: func(e Entry) (string, string, bool) {
			if filepath.Ext(e.Name) == ".RAF" {
				return "/raw", e.RelPath, true
			}
			if filepath.Ext(e.Name) == ".JPG" || filepath.Ext(e.Name) == ".MOV" {
				return "/jpg", e.RelPath, true
			}
			return "", "", false
		},
	}
	if err := c.CopyTree(context.Background(), "/src", "/unused"); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	if g := st.Get(stats.Found); g != 4 {
		t.Fatalf("Found = %d, want 4", g)
	}
	if g := st.Get(stats.Copied); g != 4 {
		t.Fatalf("Copied = %d, want 4", g)
	}
	for _, p := range []string{"/raw/DSC0001.RAF", "/raw/DCIM/DSC0003.RAF", "/jpg/DSC0002.JPG", "/jpg/DCIM/clip.MOV"} {
		if _, err := local.Stat(context.Background(), p); err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
	}
	// Excluded and the unused default root should not receive anything.
	if _, err := local.Stat(context.Background(), "/unused/readme.txt"); err == nil {
		t.Fatal("readme.txt should be excluded")
	}
}

func TestCopyTreeSkipExistingName(t *testing.T) {
	src := buildTree(t)
	local := fs.NewMem()
	_ = local.WriteFile(context.Background(), "/dst/a.jpg", []byte("x"), 0o644)
	st := stats.New()
	c := &Tree{
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

func TestCopyTreeSkipExistingSize(t *testing.T) {
	mt := time.Unix(1000, 0)
	src := newMemSource()
	src.addFile("/src", "a.jpg", 3, mt, []byte("aaa"))
	src.addFile("/src", "b.jpg", 5, mt, []byte("bbbbb"))
	local := fs.NewMem()
	// a.jpg: same size (3) — should be skipped.
	local.WriteFileAt("/dst/a.jpg", []byte("xxx"), mt)
	// b.jpg: different size (2 vs 5) — should be re-copied.
	local.WriteFileAt("/dst/b.jpg", []byte("yy"), mt)
	st := stats.New()
	c := &Tree{
		Src:   copyWritingSource{src, local},
		Local: local,
		Stats: st,
		Skip:  SkipExistingSize,
	}
	if err := c.CopyTree(context.Background(), "/src", "/dst"); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	if g := st.Get(stats.Skipped); g != 1 {
		t.Fatalf("Skipped = %d, want 1", g)
	}
	if g := st.Get(stats.Copied); g != 1 {
		t.Fatalf("Copied = %d, want 1", g)
	}
	// b.jpg was replaced with new content.
	got, _ := local.ReadFile(context.Background(), "/dst/b.jpg")
	if string(got) != "bbbbb" {
		t.Fatalf("b.jpg content = %q, want bbbbb", got)
	}
}

func TestCopyTreeSkipUnchangedSizeMtime(t *testing.T) {
	mt := time.Unix(1000, 0)
	src := newMemSource()
	src.addFile("/src", "a.txt", 3, mt, []byte("aaa"))
	local := fs.NewMem()
	local.WriteFileAt("/dst/a.txt", []byte("aaa"), mt) // matches size+mtime
	st := stats.New()
	c := &Tree{
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
	c := &Tree{
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
	got, _ := local.ReadFile(context.Background(), "/dst/a.txt")
	if string(got) != "aaaaa" {
		t.Fatalf("content = %q, want aaaaa", got)
	}
}

func TestCopyTreeDryRun(t *testing.T) {
	src := buildTree(t)
	local := fs.NewMem()
	st := stats.New()
	var logs []string
	copied := 0
	c := &Tree{
		Src:      copyWritingSource{src, local},
		Local:    local,
		Stats:    st,
		DryRun:   true,
		Log:      func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
		OnCopied: func(string, Entry) { copied++ },
	}
	if err := c.CopyTree(context.Background(), "/src", "/dst"); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	// Nothing was actually copied.
	if st.Get(stats.Copied) != 0 {
		t.Fatalf("Copied = %d, want 0 (dry run)", st.Get(stats.Copied))
	}
	if st.Get(stats.Found) != 3 {
		t.Fatalf("Found = %d, want 3", st.Get(stats.Found))
	}
	for _, p := range []string{"/dst/a.jpg", "/dst/b.txt", "/dst/sub/c.jpg"} {
		if _, err := local.Stat(context.Background(), p); err == nil {
			t.Fatalf("%s should not exist (dry run did not copy)", p)
		}
	}
	// OnCopied fired for the plan, and the log shows "would copy".
	if copied != 3 {
		t.Fatalf("OnCopied fired %d times, want 3 (plan)", copied)
	}
	var would int
	for _, l := range logs {
		if strings.HasPrefix(l, "would copy") {
			would++
		}
	}
	if would != 3 {
		t.Fatalf("'would copy' lines = %d, want 3, logs=%v", would, logs)
	}
}

func TestCopyTreeOnCopied(t *testing.T) {
	src := buildTree(t)
	local := fs.NewMem()
	st := stats.New()
	var copied []string
	c := &Tree{
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

func TestCopyTreeOnSkip(t *testing.T) {
	src := buildTree(t)
	local := fs.NewMem()
	st := stats.New()
	// Pre-create a.jpg and sub/c.jpg with matching sizes/mtimes so they are skipped.
	mt := time.Unix(1000, 0)
	local.WriteFileAt("/dst/a.jpg", []byte("aaa"), mt)
	local.WriteFileAt("/dst/sub/c.jpg", []byte("cccc"), mt)
	var skipped []string
	c := &Tree{
		Src:    copyWritingSource{src, local},
		Local:  local,
		Stats:  st,
		Skip:   SkipUnchangedSizeMtime,
		OnSkip: func(p string, _ Entry) { skipped = append(skipped, p) },
	}
	if err := c.CopyTree(context.Background(), "/src", "/dst"); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	// a.jpg and sub/c.jpg match -> skipped; b.txt dest absent -> copied.
	if len(skipped) != 2 {
		t.Fatalf("OnSkip called %d times, want 2 (a.jpg, sub/c.jpg), got %v", len(skipped), skipped)
	}
}

func TestCopyTreeCopyFailureIncrementsFailed(t *testing.T) {
	src := newMemSource()
	src.addFile("/src", "a.txt", 3, time.Unix(1, 0), []byte("aaa"))
	local := fs.NewMem()
	st := stats.New()
	// Source whose Copy always fails.
	failing := &failingSource{src}
	c := &Tree{Src: failing, Local: local, Stats: st}
	if err := c.CopyTree(context.Background(), "/src", "/dst"); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	if g := st.Get(stats.Failed); g != 1 {
		t.Fatalf("Failed = %d, want 1", g)
	}
}

// TestCopyTreePreservesBackupOnCopyFailure reproduces z41: when a file changed
// on the source (skip=false) and the new copy fails, the previous backup at
// the dest must survive — the copier copies to a temp and renames over the
// dest only on success, instead of removing the dest first.
func TestCopyTreePreservesBackupOnCopyFailure(t *testing.T) {
	src := newMemSource()
	src.addFile("/src", "a.note", 9, time.Unix(2000, 0), []byte("newcontent"))
	local := fs.NewMem()
	_ = local.WriteFile(context.Background(), "/dst/a.note", []byte("OLDBACKUP"), 0o644)
	st := stats.New()
	c := &Tree{Src: &failingSource{src}, Local: local, Stats: st, Skip: SkipUnchangedSizeMtime}
	if err := c.CopyTree(context.Background(), "/src", "/dst"); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	if g := st.Get(stats.Failed); g != 1 {
		t.Fatalf("Failed = %d, want 1", g)
	}
	if g := st.Get(stats.Copied); g != 0 {
		t.Fatalf("Copied = %d, want 0", g)
	}
	// The previous backup must still be intact.
	got, err := local.ReadFile(context.Background(), "/dst/a.note")
	if err != nil {
		t.Fatalf("previous backup lost: %v", err)
	}
	if string(got) != "OLDBACKUP" {
		t.Fatalf("dest = %q, want OLDBACKUP", got)
	}
	// The temp must have been cleaned up.
	if _, err := local.Stat(context.Background(), "/dst/a.note"+copyTmpSuffix); err == nil {
		t.Fatal("stale temp should be removed after a failed copy")
	}
}

// renameFailFS wraps an fs.Store but makes Rename always fail, to exercise the
// install-failure branch of copyOne.
type renameFailFS struct {
	fs.Store
	err error
}

func (r renameFailFS) Rename(context.Context, string, string) error { return r.err }

// TestCopyTreePreservesBackupOnRenameFailure verifies that when the copy
// succeeds but the atomic install (rename) fails, the previous backup survives
// and the temp is cleaned up.
func TestCopyTreePreservesBackupOnRenameFailure(t *testing.T) {
	src := newMemSource()
	src.addFile("/src", "a.txt", 3, time.Unix(2000, 0), []byte("new"))
	mem := fs.NewMem()
	_ = mem.WriteFile(context.Background(), "/dst/a.txt", []byte("OLDBACKUP"), 0o644)
	st := stats.New()
	local := renameFailFS{Store: mem, err: errors.New("rename boom")}
	c := &Tree{Src: copyWritingSource{src, mem}, Local: local, Stats: st, Skip: SkipUnchangedSizeMtime}
	if err := c.CopyTree(context.Background(), "/src", "/dst"); err != nil {
		t.Fatalf("CopyTree: %v", err)
	}
	if g := st.Get(stats.Failed); g != 1 {
		t.Fatalf("Failed = %d, want 1", g)
	}
	if g := st.Get(stats.Copied); g != 0 {
		t.Fatalf("Copied = %d, want 0", g)
	}
	got, err := mem.ReadFile(context.Background(), "/dst/a.txt")
	if err != nil {
		t.Fatalf("previous backup lost: %v", err)
	}
	if string(got) != "OLDBACKUP" {
		t.Fatalf("dest = %q, want OLDBACKUP", got)
	}
	if _, err := mem.Stat(context.Background(), "/dst/a.txt"+copyTmpSuffix); err == nil {
		t.Fatal("stale temp should be removed after a failed rename")
	}
}

func TestCopyTreeContextCancel(t *testing.T) {
	src := buildTree(t)
	local := fs.NewMem()
	st := stats.New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &Tree{Src: copyWritingSource{src, local}, Local: local, Stats: st}
	if err := c.CopyTree(ctx, "/src", "/dst"); err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestCopyTreeNilDeps(t *testing.T) {
	c := &Tree{Local: fs.NewMem(), Stats: stats.New()}
	if err := c.CopyTree(context.Background(), "/src", "/dst"); err == nil {
		t.Fatal("expected error for nil Src")
	}
	c2 := &Tree{Src: newMemSource(), Stats: stats.New()}
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
	c := &Tree{Src: blockingSource{src}, Local: local, Stats: st}

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

func TestSkipCtxCustomPolicy(t *testing.T) {
	// A custom policy can read only the fields it needs (here just Entry.Size)
	// — the struct signature does not force it to take every dep.
	local := fs.NewMem()
	_ = local.WriteFile(context.Background(), "/dst/f.jpg", []byte("x"), 0o644)
	skipIfSmall := func(sc SkipCtx) (bool, error) {
		return sc.Entry.Size < 10, nil
	}
	skip, err := skipIfSmall(SkipCtx{Entry: Entry{Size: 5}, DestPath: "/dst/f.jpg", Local: local})
	if err != nil || !skip {
		t.Fatalf("small entry should skip: skip=%v err=%v", skip, err)
	}
	skip, err = skipIfSmall(SkipCtx{Entry: Entry{Size: 50}, DestPath: "/dst/f.jpg", Local: local})
	if err != nil || skip {
		t.Fatalf("large entry should not skip: skip=%v err=%v", skip, err)
	}

	// The built-in policies accept a SkipCtx directly.
	s, err := SkipExistingSize(SkipCtx{DestPath: "/dst/f.jpg", Local: local, Entry: Entry{Size: 1}})
	if err != nil || !s {
		t.Fatalf("existing dest with same size should be skipped by SkipExistingSize: skip=%v err=%v", s, err)
	}
	s, err = SkipExistingSize(SkipCtx{DestPath: "/dst/f.jpg", Local: local, Entry: Entry{Size: 99}})
	if err != nil || s {
		t.Fatalf("existing dest with different size should be copied: skip=%v err=%v", s, err)
	}
}

func TestSkipExistingImportMeta(t *testing.T) {
	ctx := context.Background()
	writeDest := func(size int64) *fs.Mem {
		l := fs.NewMem()
		_ = l.WriteFile(ctx, "/dst/f.jpg", make([]byte, size), 0o644)
		return l
	}
	writeSidecar := func(l *fs.Mem, srcSize int64) {
		_ = l.WriteFile(ctx, "/dst/f.jpg"+ImportMetaSuffix, []byte(strconv.FormatInt(srcSize, 10)), 0o644)
	}

	// (1) dest size matches source: skip even without a sidecar (file not rewritten).
	if s, err := SkipExistingImportMeta(SkipCtx{Ctx: ctx, DestPath: "/dst/f.jpg", Local: writeDest(10), Entry: Entry{Size: 10}}); err != nil || !s {
		t.Fatalf("same size, no sidecar: skip=%v err=%v", s, err)
	}

	// (2) dest size drifted (in-place geotag rewrite) but sidecar records the
	// original source size: skip — this is the s51 repro.
	l := writeDest(8)   // dest rewritten smaller
	writeSidecar(l, 10) // original source size was 10
	if s, err := SkipExistingImportMeta(SkipCtx{Ctx: ctx, DestPath: "/dst/f.jpg", Local: l, Entry: Entry{Size: 10}}); err != nil || !s {
		t.Fatalf("drifted dest + matching sidecar: skip=%v err=%v", s, err)
	}

	// (3) dest drifted and no sidecar: re-copy.
	if s, err := SkipExistingImportMeta(SkipCtx{Ctx: ctx, DestPath: "/dst/f.jpg", Local: writeDest(8), Entry: Entry{Size: 10}}); err != nil || s {
		t.Fatalf("drifted dest, no sidecar: skip=%v err=%v (want false)", s, err)
	}

	// (4) sidecar recorded size differs from current source (camera file
	// changed): re-copy.
	l = writeDest(8)
	writeSidecar(l, 10) // old source size; current source is now 12
	if s, err := SkipExistingImportMeta(SkipCtx{Ctx: ctx, DestPath: "/dst/f.jpg", Local: l, Entry: Entry{Size: 12}}); err != nil || s {
		t.Fatalf("changed source vs sidecar: skip=%v err=%v (want false)", s, err)
	}

	// (5) dest missing (rolled back / user-deleted): re-copy, even if an orphan
	// sidecar lingers.
	l = fs.NewMem()
	writeSidecar(l, 10)
	if s, err := SkipExistingImportMeta(SkipCtx{Ctx: ctx, DestPath: "/dst/f.jpg", Local: l, Entry: Entry{Size: 10}}); err != nil || s {
		t.Fatalf("missing dest + orphan sidecar: skip=%v err=%v (want false)", s, err)
	}
}
