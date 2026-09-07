package supernote

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/snonux/syncmaster/internal/clock"
	"github.com/snonux/syncmaster/internal/config"
	"github.com/snonux/syncmaster/internal/driver"
	"github.com/snonux/syncmaster/internal/fs"
	"github.com/snonux/syncmaster/internal/shell"
	"github.com/snonux/syncmaster/internal/stats"
)

type fakeTree struct {
	mounts    []string
	exists    map[string]bool
	existsErr map[string]error
}

func newFakeTree() *fakeTree {
	return &fakeTree{exists: map[string]bool{}, existsErr: map[string]error{}}
}

func (t *fakeTree) FindMounts(context.Context, string) ([]string, error) { return t.mounts, nil }
func (t *fakeTree) Exists(_ context.Context, path string) (bool, error) {
	if err, ok := t.existsErr[path]; ok {
		return false, err
	}
	return t.exists[path], nil
}
func (t *fakeTree) ModifiedTime(context.Context, string) (time.Time, error) {
	return time.Unix(1000, 0), nil
}
func newEnv(t *testing.T, cfg config.Config, st *stats.Counters) *driver.Env {
	t.Helper()
	runner := shell.NewFake()
	runner.Register("rsync", func(context.Context, []string) ([]byte, error) { return nil, nil })
	return &driver.Env{
		Config: &cfg,
		Local:  fs.NewMem(),
		Clock:  clock.Fixed{T: time.Unix(1000, 0)},
		Runner: runner,
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

func TestDetectSurfacesExistsError(t *testing.T) {
	mounts := newFakeTree()
	mounts.mounts = []string{"/gvfs/mtp:Supernote"}
	mounts.existsErr["/gvfs/mtp:Supernote/Internal shared storage"] = errors.New("mtp i/o error")
	if _, err := (Driver{}).Detect(context.Background(), &driver.Env{Mounts: mounts}); err == nil {
		t.Fatal("expected error when storage Exists fails")
	}
}

func TestDetectSurfacesMountError(t *testing.T) {
	mounts := newFakeTree()
	mounts.mounts = []string{"/gvfs/mtp:Supernote"}
	mounts.existsErr["/gvfs/mtp:Supernote"] = errors.New("mtp i/o error")
	if _, err := (Driver{}).Detect(context.Background(), &driver.Env{Mounts: mounts}); err == nil {
		t.Fatal("expected error when mount Exists fails")
	}
}

func TestInboundRsyncArguments(t *testing.T) {
	env := newEnv(t, baseCfg(), stats.New())
	env.DryRun = true
	d := Driver{}
	if err := d.rsyncTree(context.Background(), "/device/Note", "/backup/Note", env); err != nil {
		t.Fatalf("note rsync: %v", err)
	}
	if err := d.rsyncTree(context.Background(), "/device/Document", "/backup/KOReader", env); err != nil {
		t.Fatalf("document rsync: %v", err)
	}
	calls := env.Runner.(*shell.Fake).CallsFor("rsync")
	want := [][]string{
		{"--recursive", "--mkpath", "--size-only", "--itemize-changes", "--dry-run", "--", "/device/Note/", "/backup/Note/"},
		{"--recursive", "--mkpath", "--size-only", "--itemize-changes", "--dry-run", "--", "/device/Document/", "/backup/KOReader/"},
	}
	if len(calls) != len(want) {
		t.Fatalf("rsync calls = %d, want %d", len(calls), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(calls[i].Args, want[i]) {
			t.Fatalf("rsync call %d args = %q, want %q", i, calls[i].Args, want[i])
		}
	}
}

func TestRsyncTreeSurfacesError(t *testing.T) {
	env := newEnv(t, baseCfg(), stats.New())
	fake := env.Runner.(*shell.Fake)
	fake.Register("rsync", func(context.Context, []string) ([]byte, error) {
		return []byte("rsync output\n"), errors.New("transfer failed")
	})
	if err := (Driver{}).rsyncTree(context.Background(), "/device/Note", "/backup/Note", env); err == nil {
		t.Fatal("expected rsync error")
	}
	if got := env.Out.(*bytes.Buffer).String(); got != "rsync output\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestSyncSurfacesNoteExistsError(t *testing.T) {
	tree := newFakeTree()
	storage := "/dev/Supernote/Internal shared storage"
	tree.existsErr[filepath.Join(storage, "Note")] = errors.New("mtp i/o error")
	env := newEnv(t, baseCfg(), stats.New())
	env.Mounts = tree
	if err := (Driver{}).Sync(context.Background(), driver.Device{Source: storage}, env); err == nil {
		t.Fatal("expected error when Note Exists fails")
	}
}

func TestSyncSurfacesDocumentExistsError(t *testing.T) {
	tree := newFakeTree()
	storage := "/dev/Supernote/Internal shared storage"
	tree.exists[filepath.Join(storage, "Note")] = true
	tree.existsErr[filepath.Join(storage, "Document")] = errors.New("mtp i/o error")
	env := newEnv(t, baseCfg(), stats.New())
	env.Mounts = tree
	if err := (Driver{}).Sync(context.Background(), driver.Device{Source: storage}, env); err == nil {
		t.Fatal("expected error when Document Exists fails")
	}
}

func TestSyncCopiesNoteAndDocumentAndConverts(t *testing.T) {
	tree := newFakeTree()
	storage := "/dev/Supernote/Internal shared storage"
	tree.exists[filepath.Join(storage, "Note")] = true
	tree.exists[filepath.Join(storage, "Document")] = true

	st := stats.New()
	cfg := baseCfg()
	env := newEnv(t, cfg, st)
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
	fake.Register("rsync", func(ctx context.Context, args []string) ([]byte, error) {
		source, destination := args[len(args)-2], args[len(args)-1]
		switch source {
		case filepath.Join(storage, "Note") + "/":
			_ = env.Local.MkdirAll(ctx, destination, 0o755)
			_ = env.Local.MkdirAll(ctx, filepath.Join(destination, "Inbox"), 0o755)
			_ = env.Local.WriteFile(ctx, filepath.Join(destination, "a.note"), []byte("note"), 0o644)
			_ = env.Local.WriteFile(ctx, filepath.Join(destination, "Inbox", "b.note"), []byte("note2"), 0o644)
		case filepath.Join(storage, "Document") + "/":
			_ = env.Local.MkdirAll(ctx, destination, 0o755)
			_ = env.Local.WriteFile(ctx, filepath.Join(destination, "book.epub"), []byte("epub"), 0o644)
		}
		return nil, nil
	})
	fake.Register("supernote-tool", func(_ context.Context, args []string) ([]byte, error) {
		// args: convert -a -t pdf <note> <out>
		out := args[len(args)-1]
		_ = env.Local.WriteFile(context.Background(), out, []byte("pdf"), 0o644)
		return nil, nil
	})
	env.Runner = fake

	if err := (Driver{}).Sync(context.Background(), driver.Device{Source: storage}, env); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// Note files copied.
	if _, err := env.Local.Stat(context.Background(), filepath.Join(cfg.SupernoteDestEffective(), "a.note")); err != nil {
		t.Fatalf("a.note not copied: %v", err)
	}
	if _, err := env.Local.Stat(context.Background(), filepath.Join(cfg.SupernoteDestEffective(), "Inbox", "b.note")); err != nil {
		t.Fatalf("b.note not copied: %v", err)
	}
	// KOReader file copied.
	if _, err := env.Local.Stat(context.Background(), filepath.Join(cfg.SupernoteDestEffective(), "KOReader", "book.epub")); err != nil {
		t.Fatalf("book.epub not copied: %v", err)
	}
	// PDFs converted from notes.
	if _, err := env.Local.Stat(context.Background(), filepath.Join(cfg.SupernoteDestEffective(), "a.pdf")); err != nil {
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
