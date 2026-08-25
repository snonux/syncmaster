package fujifilm

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
	"syncmaster/internal/media"
	"syncmaster/internal/shell"
	"syncmaster/internal/stats"
)

// fakeTree implements copier.Source and driver.MountFS for driver tests.
type fakeTree struct {
	dirs    map[string][]copier.Entry
	files   map[string][]byte
	mounts  []string
	exists  map[string]bool
	listErr map[string]error
}

func newFakeTree() *fakeTree {
	return &fakeTree{
		dirs:    map[string][]copier.Entry{},
		files:   map[string][]byte{},
		exists:  map[string]bool{},
		listErr: map[string]error{},
	}
}

func (t *fakeTree) addDir(dir, name string) {
	t.dirs[dir] = append(t.dirs[dir], copier.Entry{Name: name, IsDir: true})
	t.dirs[filepath.Join(dir, name)] = nil
}
func (t *fakeTree) addFile(dir, name string, content []byte) {
	t.dirs[dir] = append(t.dirs[dir], copier.Entry{Name: name, Size: int64(len(content)), Modified: time.Unix(1000, 0)})
	t.files[filepath.Join(dir, name)] = content
}

func (t *fakeTree) List(_ context.Context, dir string) ([]copier.Entry, error) {
	if err, ok := t.listErr[dir]; ok {
		return nil, err
	}
	return t.dirs[dir], nil
}
func (t *fakeTree) Copy(_ context.Context, src, dst string) error {
	return nil // dest written via env.Local in the wrapped source below
}
func (t *fakeTree) FindMounts(context.Context, string) ([]string, error) {
	return t.mounts, nil
}
func (t *fakeTree) Exists(context.Context, string) (bool, error) { return true, nil }
func (t *fakeTree) ModifiedTime(context.Context, string) (time.Time, error) {
	return time.Unix(1000, 0), nil
}

// writingSource wraps a fakeTree so Copy writes content into a local fs.
type writingSource struct {
	*fakeTree
	local fs.Store
}

func (w writingSource) Copy(ctx context.Context, src, dst string) error {
	if err := w.fakeTree.Copy(ctx, src, dst); err != nil {
		return err
	}
	return w.local.WriteFile(ctx, dst, w.files[src], 0o644)
}

func newEnv(t *testing.T, src copier.Source, mounts driver.MountFS, st *stats.Counters, cfg config.Config) *driver.Env {
	t.Helper()
	return &driver.Env{
		Config: &cfg,
		Source: src,
		Mounts: mounts,
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
	c.GPXDir = "/gpx"
	c.AllowMissingGPS = true // avoid needing exiftool in unit tests
	return c
}

func TestDetect(t *testing.T) {
	mounts := &fakeTree{mounts: []string{"/gvfs/gphoto2:usb"}}
	d := &Driver{}
	devs, err := d.Detect(context.Background(), &driver.Env{Mounts: mounts})
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(devs) != 1 || devs[0].Driver != "fujifilm" || devs[0].Source != "/gvfs/gphoto2:usb" {
		t.Fatalf("devs = %+v", devs)
	}
}

func TestSyncRoutesFiles(t *testing.T) {
	tree := newFakeTree()
	tree.addDir("/src", "DCIM")
	tree.addFile("/src", "DSC0001.RAF", []byte("raw"))
	tree.addFile("/src", "DSC0002.JPG", []byte("jpg"))
	tree.addFile("/src", "clip.MOV", []byte("mov"))
	tree.addFile("/src", "readme.txt", []byte("ignore"))
	tree.mounts = []string{"/src"}
	tree.exists = nil

	st := stats.New()
	cfg := baseCfg()
	env := newEnv(t, nil, tree, st, cfg)
	env.Source = writingSource{tree, env.Local}

	d := &Driver{Media: media.Default()}
	if err := d.Sync(context.Background(), driver.Device{Driver: "fujifilm", Source: "/src"}, env); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// RAW -> rawDest; JPEG/video -> jpegDest; txt excluded.
	if _, err := env.Local.Stat(context.Background(), filepath.Join(cfg.FujifilmRAWDest, "DSC0001.RAF")); err != nil {
		t.Fatalf("RAW not copied: %v", err)
	}
	if _, err := env.Local.Stat(context.Background(), filepath.Join(cfg.FujifilmJPEGDest(), "DSC0002.JPG")); err != nil {
		t.Fatalf("JPG not copied: %v", err)
	}
	if _, err := env.Local.Stat(context.Background(), filepath.Join(cfg.FujifilmJPEGDest(), "clip.MOV")); err != nil {
		t.Fatalf("MOV not copied: %v", err)
	}
	if _, err := env.Local.Stat(context.Background(), filepath.Join(cfg.FujifilmJPEGDest(), "readme.txt")); err == nil {
		t.Fatal("readme.txt should be excluded")
	}

	if g := st.Get(stats.Found); g != 3 {
		t.Fatalf("Found = %d, want 3", g)
	}
	if g := st.Get(stats.Copied); g != 3 {
		t.Fatalf("Copied = %d, want 3", g)
	}
}

func TestSyncSkipExisting(t *testing.T) {
	tree := newFakeTree()
	tree.addFile("/src", "DSC0001.RAF", []byte("raw"))
	tree.mounts = []string{"/src"}

	st := stats.New()
	cfg := baseCfg()
	env := newEnv(t, nil, tree, st, cfg)
	env.Source = writingSource{tree, env.Local}
	// Pre-create the RAW file with the same size so SkipExistingSize skips it.
	_ = env.Local.WriteFile(context.Background(), filepath.Join(cfg.FujifilmRAWDest, "DSC0001.RAF"), []byte("xxx"), 0o644)

	d := &Driver{Media: media.Default()}
	if err := d.Sync(context.Background(), driver.Device{Source: "/src"}, env); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if g := st.Get(stats.Skipped); g != 1 {
		t.Fatalf("Skipped = %d, want 1", g)
	}
	if g := st.Get(stats.Copied); g != 0 {
		t.Fatalf("Copied = %d, want 0", g)
	}
}

func TestSyncReplacesDifferentSize(t *testing.T) {
	tree := newFakeTree()
	tree.addFile("/src", "DSC0001.RAF", []byte("raw"))
	tree.mounts = []string{"/src"}

	st := stats.New()
	cfg := baseCfg()
	env := newEnv(t, nil, tree, st, cfg)
	env.Source = writingSource{tree, env.Local}
	// Pre-create with a different size — should be re-copied, not skipped.
	_ = env.Local.WriteFile(context.Background(), filepath.Join(cfg.FujifilmRAWDest, "DSC0001.RAF"), []byte("existing data"), 0o644)

	d := &Driver{Media: media.Default()}
	if err := d.Sync(context.Background(), driver.Device{Source: "/src"}, env); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if g := st.Get(stats.Skipped); g != 0 {
		t.Fatalf("Skipped = %d, want 0", g)
	}
	if g := st.Get(stats.Copied); g != 1 {
		t.Fatalf("Copied = %d, want 1", g)
	}
}

// TestSyncSkipsGeotaggedFileNextRun reproduces s51: after an in-place geotag
// rewrites the imported file (dest size drifts), the next sync must SKIP the
// file instead of re-copying it every run. The import-meta sidecar written at
// copy time records the original source size, so the skip survives the rewrite.
func TestSyncSkipsGeotaggedFileNextRun(t *testing.T) {
	tree := newFakeTree()
	tree.addFile("/src", "DSC0001.JPG", []byte("jpg")) // source size 3
	tree.mounts = []string{"/src"}

	cfg := baseCfg()
	st := stats.New()
	env := newEnv(t, nil, tree, st, cfg)
	env.Source = writingSource{tree, env.Local}
	d := &Driver{Media: media.Default()}

	if err := d.Sync(context.Background(), driver.Device{Source: "/src"}, env); err != nil {
		t.Fatalf("Sync run 1: %v", err)
	}
	if g := st.Get(stats.Copied); g != 1 {
		t.Fatalf("run 1 Copied = %d, want 1", g)
	}
	// Simulate exiftool geotag rewriting the dest in place with a different
	// size (the real exiftool drifts ~-52KB; the magnitude is irrelevant).
	jpg := filepath.Join(cfg.FujifilmJPEGDest(), "DSC0001.JPG")
	if err := env.Local.WriteFile(context.Background(), jpg, []byte("jpg-with-gps-tags"), 0o644); err != nil {
		t.Fatalf("simulate geotag rewrite: %v", err)
	}

	// Run 2: same camera file (size 3) + drifted dest -> skip via the sidecar.
	st2 := stats.New()
	env.Stats = st2
	if err := d.Sync(context.Background(), driver.Device{Source: "/src"}, env); err != nil {
		t.Fatalf("Sync run 2: %v", err)
	}
	if g := st2.Get(stats.Copied); g != 0 {
		t.Fatalf("run 2 Copied = %d, want 0 (geotagged file should skip)", g)
	}
	if g := st2.Get(stats.Skipped); g != 1 {
		t.Fatalf("run 2 Skipped = %d, want 1", g)
	}
}

// TestSyncRecopiesChangedCameraFile verifies that a camera file that changed
// (different size) is re-copied even though an import-meta sidecar exists from
// the prior run — preserving the changed-file re-copy property.
func TestSyncRecopiesChangedCameraFile(t *testing.T) {
	tree := newFakeTree()
	tree.addFile("/src", "DSC0001.JPG", []byte("jpg")) // size 3
	tree.mounts = []string{"/src"}

	cfg := baseCfg()
	st := stats.New()
	env := newEnv(t, nil, tree, st, cfg)
	env.Source = writingSource{tree, env.Local}
	d := &Driver{Media: media.Default()}

	if err := d.Sync(context.Background(), driver.Device{Source: "/src"}, env); err != nil {
		t.Fatalf("Sync run 1: %v", err)
	}
	if g := st.Get(stats.Copied); g != 1 {
		t.Fatalf("run 1 Copied = %d, want 1", g)
	}

	// Camera file replaced with a larger one (same name, new content/size).
	tree2 := newFakeTree()
	tree2.addFile("/src", "DSC0001.JPG", []byte("jpg-now-much-larger")) // size 19
	tree2.mounts = []string{"/src"}
	env.Source = writingSource{tree2, env.Local} // same dest fs as run 1

	st2 := stats.New()
	env.Stats = st2
	if err := d.Sync(context.Background(), driver.Device{Source: "/src"}, env); err != nil {
		t.Fatalf("Sync run 2: %v", err)
	}
	if g := st2.Get(stats.Skipped); g != 0 {
		t.Fatalf("run 2 Skipped = %d, want 0 (changed file should re-copy)", g)
	}
	if g := st2.Get(stats.Copied); g != 1 {
		t.Fatalf("run 2 Copied = %d, want 1", g)
	}
	got, _ := env.Local.ReadFile(context.Background(), filepath.Join(cfg.FujifilmJPEGDest(), "DSC0001.JPG"))
	if string(got) != "jpg-now-much-larger" {
		t.Fatalf("dest content = %q, want the new camera content", got)
	}
}

func TestSyncGeotagFailureRollsBack(t *testing.T) {
	tree := newFakeTree()
	tree.addFile("/src", "DSC0001.JPG", []byte("jpg"))
	tree.mounts = []string{"/src"}

	st := stats.New()
	cfg := baseCfg()
	cfg.AllowMissingGPS = false // force geotag failure (no GPX)
	env := newEnv(t, nil, tree, st, cfg)
	env.Source = writingSource{tree, env.Local}

	d := &Driver{Media: media.Default()}
	err := d.Sync(context.Background(), driver.Device{Source: "/src"}, env)
	if err == nil {
		t.Fatal("expected geotag failure error")
	}
	// Rollback removes the copied image.
	if _, err := env.Local.Stat(context.Background(), filepath.Join(cfg.FujifilmJPEGDest(), "DSC0001.JPG")); err == nil {
		t.Fatal("JPG should have been rolled back")
	}
}

func TestRegistryResolution(t *testing.T) {
	field := media.NewRegistry()
	envReg := media.NewRegistry()

	// Driver field wins.
	d := &Driver{Media: field}
	if got := d.registry(&driver.Env{Media: envReg}); got != field {
		t.Fatal("expected driver field registry to win")
	}
	// Then env.Media.
	d = &Driver{}
	if got := d.registry(&driver.Env{Media: envReg}); got != envReg {
		t.Fatal("expected env.Media registry when driver field is nil")
	}
	// Then the package default when both are nil.
	d = &Driver{}
	if got := d.registry(&driver.Env{}); got != media.Default() {
		t.Fatal("expected media.Default() fallback when neither is set")
	}
	// nil env is safe.
	d = &Driver{}
	if got := d.registry(nil); got != media.Default() {
		t.Fatal("expected media.Default() fallback for nil env")
	}
}
