package supernote

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"syncmaster/internal/clock"
	"syncmaster/internal/config"
	"syncmaster/internal/copier"
	"syncmaster/internal/driver"
	"syncmaster/internal/fs"
	"syncmaster/internal/note"
	"syncmaster/internal/shell"
	"syncmaster/internal/stats"
)

type fakeTree struct {
	dirs   map[string][]copier.Entry
	files  map[string][]byte
	mounts []string
	exists map[string]bool
}

func newFakeTree() *fakeTree {
	return &fakeTree{dirs: map[string][]copier.Entry{}, files: map[string][]byte{}, exists: map[string]bool{}}
}

func (t *fakeTree) addDir(dir, name string) {
	t.dirs[dir] = append(t.dirs[dir], copier.Entry{Name: name, IsDir: true})
	t.dirs[filepath.Join(dir, name)] = nil
}
func (t *fakeTree) addFile(dir, name string, content []byte) {
	t.dirs[dir] = append(t.dirs[dir], copier.Entry{Name: name, Size: int64(len(content)), Modified: time.Unix(1000, 0)})
	t.files[filepath.Join(dir, name)] = content
}

func (t *fakeTree) List(context.Context, string) ([]copier.Entry, error) { return nil, nil }
func (t *fakeTree) Copy(context.Context, string, string) error           { return nil }
func (t *fakeTree) FindMounts(context.Context, string) ([]string, error) { return t.mounts, nil }
func (t *fakeTree) Exists(_ context.Context, path string) (bool, error)  { return t.exists[path], nil }
func (t *fakeTree) ModifiedTime(context.Context, string) (time.Time, error) {
	return time.Unix(1000, 0), nil
}

// writingSource lists from the tree and writes copies into a local fs.
type writingSource struct {
	*fakeTree
	local fs.FS
}

func (w writingSource) List(_ context.Context, dir string) ([]copier.Entry, error) {
	return w.dirs[dir], nil
}
func (w writingSource) Copy(_ context.Context, src, dst string) error {
	return w.local.WriteFile(dst, w.files[src], 0o644)
}

func newEnv(t *testing.T, cfg config.Config, st *stats.Stats) *driver.Env {
	t.Helper()
	return &driver.Env{
		Config: &cfg,
		Local:  fs.NewMem(),
		Clock:  clock.Fixed{T: time.Unix(1000, 0)},
		Runner: shell.NewFake(),
		Stats:  st,
		Out:    new(bytes.Buffer),
		Err:    new(bytes.Buffer),
	}
}

func baseCfg() config.Config {
	c := config.Defaults("/home/p", 1000)
	return c
}

func TestDetectPrefersInternalStorage(t *testing.T) {
	mounts := newFakeTree()
	mounts.mounts = []string{"/gvfs/mtp:Supernote"}
	mounts.exists["/gvfs/mtp:Supernote/Internal shared storage"] = true
	devs, err := (Driver{}).Detect(context.Background(), &driver.Env{Mounts: mounts})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(devs) != 1 || devs[0].Source != "/gvfs/mtp:Supernote/Internal shared storage" {
		t.Fatalf("devs = %+v", devs)
	}
}

func TestDetectFallsBackToRoot(t *testing.T) {
	mounts := newFakeTree()
	mounts.mounts = []string{"/gvfs/mtp:Supernote"}
	mounts.exists["/gvfs/mtp:Supernote"] = true
	devs, err := (Driver{}).Detect(context.Background(), &driver.Env{Mounts: mounts})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(devs) != 1 || devs[0].Source != "/gvfs/mtp:Supernote" {
		t.Fatalf("devs = %+v", devs)
	}
}

func TestSyncCopiesNoteAndDocumentAndConverts(t *testing.T) {
	tree := newFakeTree()
	storage := "/dev/Supernote/Internal shared storage"
	tree.exists[filepath.Join(storage, "Note")] = true
	tree.exists[filepath.Join(storage, "Document")] = true

	tree.addDir(filepath.Join(storage, "Note"), "Inbox")
	tree.addFile(filepath.Join(storage, "Note"), "a.note", []byte("note"))
	tree.addFile(filepath.Join(storage, "Note", "Inbox"), "b.note", []byte("note2"))
	tree.addDir(filepath.Join(storage, "Document"), "Books")
	tree.addFile(filepath.Join(storage, "Document"), "book.epub", []byte("epub"))

	st := stats.New()
	cfg := baseCfg()
	env := newEnv(t, cfg, st)
	env.Source = writingSource{tree, env.Local}
	env.Mounts = tree

	// Provide a fake converter via a transform swap: we drive conversion by
	// injecting a note.Convert with a fake Conv through a wrapper. Since the
	// driver constructs its own Convert, we instead make supernote-tool
	// "present" and let it be a no-op runner: the fake runner's Run returns
	// nil, and we post-check that .pdf files were *not* created by the tool
	// (the tool is faked). To verify conversion ran, register a converter via
	// the runner writing a pdf.
	fake := shell.NewFake()
	fake.RegisterLookPath("supernote-tool", true)
	fake.Register("supernote-tool", func(_ context.Context, args []string) ([]byte, error) {
		// args: convert -a -t pdf <note> <out>
		out := args[len(args)-1]
		_ = env.Local.WriteFile(out, []byte("pdf"), 0o644)
		return nil, nil
	})
	env.Runner = fake

	if err := (Driver{}).Sync(context.Background(), driver.Device{Source: storage}, env); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// Note files copied.
	if _, err := env.Local.Stat(filepath.Join(cfg.SupernoteDestEffective(), "a.note")); err != nil {
		t.Fatalf("a.note not copied: %v", err)
	}
	if _, err := env.Local.Stat(filepath.Join(cfg.SupernoteDestEffective(), "Inbox", "b.note")); err != nil {
		t.Fatalf("b.note not copied: %v", err)
	}
	// KOReader file copied.
	if _, err := env.Local.Stat(filepath.Join(cfg.SupernoteDestEffective(), "KOReader", "book.epub")); err != nil {
		t.Fatalf("book.epub not copied: %v", err)
	}
	// PDFs converted from notes.
	if _, err := env.Local.Stat(filepath.Join(cfg.SupernoteDestEffective(), "a.pdf")); err != nil {
		t.Fatalf("a.pdf not converted: %v", err)
	}
	if got := st.Get(stats.Converted); got != 2 {
		t.Fatalf("Converted = %d, want 2", got)
	}
}

func TestSyncMissingNoteFolderErrors(t *testing.T) {
	tree := newFakeTree()
	storage := "/dev/Supernote/Internal shared storage"
	tree.exists[filepath.Join(storage, "Note")] = false
	tree.exists[storage] = true

	env := newEnv(t, baseCfg(), stats.New())
	env.Mounts = tree
	err := (Driver{}).Sync(context.Background(), driver.Device{Source: storage}, env)
	if err == nil {
		t.Fatal("expected error for missing Note folder")
	}
}

func TestSyncSkipsUnchangedFiles(t *testing.T) {
	tree := newFakeTree()
	storage := "/dev/Supernote/Internal shared storage"
	tree.exists[filepath.Join(storage, "Note")] = true
	tree.addFile(filepath.Join(storage, "Note"), "a.note", []byte("note"))

	st := stats.New()
	cfg := baseCfg()

	// Pre-create the destination file matching size+mtime so it's skipped.
	local := fs.NewMem()
	mt := time.Unix(1000, 0)
	dest := filepath.Join(cfg.SupernoteDestEffective(), "a.note")
	_ = local.MkdirAll(filepath.Dir(dest), 0o755)
	local.WriteFileAt(dest, []byte("note"), mt)
	env := newEnv(t, cfg, st)
	env.Local = local
	env.Source = writingSource{tree, local}
	env.Mounts = tree

	// No supernote-tool needed: the note is skipped from copy, and conversion
	// has no .note to convert? The note exists and will be considered for
	// conversion. Register a no-op tool so LookPath passes.
	fake := shell.NewFake()
	fake.RegisterLookPath("supernote-tool", true)
	env.Runner = fake

	if err := (Driver{}).Sync(context.Background(), driver.Device{Source: storage}, env); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if g := st.Get(stats.Skipped); g != 1 {
		t.Fatalf("Skipped = %d, want 1", g)
	}
	if g := st.Get(stats.Copied); g != 0 {
		t.Fatalf("Copied = %d, want 0", g)
	}
}

// Ensure the note import is referenced (avoids unused import in some builds).
var _ = note.Convert{}
