package gpx

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"syncmaster/internal/driver"
	"syncmaster/internal/fs"
	"syncmaster/internal/stats"
)

// recordingRunner records every Run call's args and returns configurable
// output per the -if flag (geotag vs missing-scan).
type recordingRunner struct {
	lookPath   map[string]bool
	geotagOut  []byte
	geotagErr  error
	missingOut []byte
	missingErr error
	args       [][]string // all calls
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.args = append(r.args, append([]string{name}, args...))
	if len(args) > 0 && args[0] == "-if" {
		return r.missingOut, r.missingErr
	}
	return r.geotagOut, r.geotagErr
}

func (r *recordingRunner) LookPath(name string) (string, error) {
	if r.lookPath[name] {
		return "/usr/bin/" + name, nil
	}
	return "", errors.New("not found")
}

func hasPair(args []string, val string) bool {
	for i, a := range args {
		if a == "-geotag" && i+1 < len(args) && args[i+1] == val {
			return true
		}
	}
	return false
}

func newEnv(st *stats.Counters) *driver.Env {
	return &driver.Env{Local: fs.NewMem(), Stats: st, Out: new(bytes.Buffer), Err: new(bytes.Buffer)}
}

func withGPX(env *driver.Env, files ...string) {
	_ = env.Local.MkdirAll(context.Background(), "/gpx", 0o755)
	for _, f := range files {
		_ = env.Local.WriteFile(context.Background(), "/gpx/"+f, []byte("x"), 0o644)
	}
}

func TestApplyNoImages(t *testing.T) {
	st := stats.New()
	g := &Geotag{GPXDir: "/gpx"}
	out := new(bytes.Buffer)
	env := &driver.Env{Local: fs.NewMem(), Stats: st, Out: out}
	if err := g.Apply(context.Background(), &driver.TransformCtx{Env: env}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(out.String(), "No new images") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestApplyNoGPXAllowed(t *testing.T) {
	st := stats.New()
	env := newEnv(st)
	g := &Geotag{Runner: &recordingRunner{lookPath: map[string]bool{}}, GPXDir: "/gpx", AllowMissing: true}
	if err := g.Apply(context.Background(), &driver.TransformCtx{Env: env, Imported: []string{"/dst/a.jpg"}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if st.Get(stats.Failed) != 0 {
		t.Fatalf("Failed = %d", st.Get(stats.Failed))
	}
}

func TestApplyNoGPXRollback(t *testing.T) {
	st := stats.New()
	env := newEnv(st)
	_ = env.Local.WriteFile(context.Background(), "/dst/a.jpg", []byte("x"), 0o644)
	st.Inc(stats.Copied, 1)

	g := &Geotag{Runner: &recordingRunner{lookPath: map[string]bool{}}, GPXDir: "/gpx"}
	err := g.Apply(context.Background(), &driver.TransformCtx{Env: env, Imported: []string{"/dst/a.jpg"}})
	if !errors.Is(err, ErrNoGPX) {
		t.Fatalf("err = %v, want ErrNoGPX", err)
	}
	if st.Get(stats.Failed) != 1 {
		t.Fatalf("Failed = %d, want 1", st.Get(stats.Failed))
	}
	if st.Get(stats.Copied) != 0 {
		t.Fatalf("Copied = %d, want 0 (rolled back)", st.Get(stats.Copied))
	}
	if _, err := env.Local.Stat(context.Background(), "/dst/a.jpg"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("rolled-back file should be gone: %v", err)
	}
}

func TestApplyGeotagSuccess(t *testing.T) {
	st := stats.New()
	env := newEnv(st)
	withGPX(env, "a.gpx", "b.gpx")
	_ = env.Local.WriteFile(context.Background(), "/dst/a.jpg", []byte("x"), 0o644)
	st.Inc(stats.Copied, 1)

	r := &recordingRunner{lookPath: map[string]bool{"exiftool": true}, missingOut: []byte("")}
	g := &Geotag{Runner: r, GPXDir: "/gpx"}
	if err := g.Apply(context.Background(), &driver.TransformCtx{Env: env, Imported: []string{"/dst/a.jpg"}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(r.args) != 2 {
		t.Fatalf("exiftool calls = %d, want 2", len(r.args))
	}
	geotag := r.args[0]
	if geotag[1] != "-overwrite_original" || geotag[2] != "-P" {
		t.Fatalf("geotag args = %v", geotag)
	}
	if !hasPair(geotag, "/gpx/a.gpx") || !hasPair(geotag, "/gpx/b.gpx") {
		t.Fatalf("missing -geotag tracks: %v", geotag)
	}
	if geotag[len(geotag)-1] != "/dst/a.jpg" {
		t.Fatalf("image should be last: %v", geotag)
	}
	if st.Get(stats.Failed) != 0 {
		t.Fatalf("Failed = %d", st.Get(stats.Failed))
	}
}

func TestApplyMissingGPSNotAllowedRollback(t *testing.T) {
	st := stats.New()
	env := newEnv(st)
	withGPX(env, "a.gpx")
	_ = env.Local.WriteFile(context.Background(), "/dst/a.jpg", []byte("x"), 0o644)
	_ = env.Local.WriteFile(context.Background(), "/dst/b.jpg", []byte("y"), 0o644)
	st.Inc(stats.Copied, 2)

	r := &recordingRunner{lookPath: map[string]bool{"exiftool": true}, missingOut: []byte("/dst/a.jpg\n")}
	g := &Geotag{Runner: r, GPXDir: "/gpx"}
	err := g.Apply(context.Background(), &driver.TransformCtx{Env: env, Imported: []string{"/dst/a.jpg", "/dst/b.jpg"}})
	if !errors.Is(err, ErrMissingGPS) {
		t.Fatalf("err = %v, want ErrMissingGPS", err)
	}
	if st.Get(stats.Failed) != 1 {
		t.Fatalf("Failed = %d, want 1", st.Get(stats.Failed))
	}
	if st.Get(stats.Copied) != 0 {
		t.Fatalf("Copied = %d, want 0 (rolled back)", st.Get(stats.Copied))
	}
}

func TestApplyMissingGPSAllowed(t *testing.T) {
	st := stats.New()
	env := newEnv(st)
	withGPX(env, "a.gpx")
	_ = env.Local.WriteFile(context.Background(), "/dst/a.jpg", []byte("x"), 0o644)
	st.Inc(stats.Copied, 1)

	r := &recordingRunner{lookPath: map[string]bool{"exiftool": true}, missingOut: []byte("/dst/a.jpg\n")}
	g := &Geotag{Runner: r, GPXDir: "/gpx", AllowMissing: true}
	if err := g.Apply(context.Background(), &driver.TransformCtx{Env: env, Imported: []string{"/dst/a.jpg"}}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if st.Get(stats.Failed) != 0 {
		t.Fatalf("Failed = %d", st.Get(stats.Failed))
	}
	if _, err := env.Local.Stat(context.Background(), "/dst/a.jpg"); err != nil {
		t.Fatalf("image should remain: %v", err)
	}
}

func TestApplyExiftoolMissing(t *testing.T) {
	st := stats.New()
	env := newEnv(st)
	withGPX(env, "a.gpx")
	_ = env.Local.WriteFile(context.Background(), "/dst/a.jpg", []byte("x"), 0o644)
	st.Inc(stats.Copied, 1)

	r := &recordingRunner{lookPath: map[string]bool{}}
	g := &Geotag{Runner: r, GPXDir: "/gpx"}
	if err := g.Apply(context.Background(), &driver.TransformCtx{Env: env, Imported: []string{"/dst/a.jpg"}}); err == nil {
		t.Fatal("expected error for missing exiftool")
	}
	if st.Get(stats.Copied) != 0 {
		t.Fatalf("Copied = %d, want 0 (rolled back)", st.Get(stats.Copied))
	}
}

func TestApplyGeotagRunErrorRollback(t *testing.T) {
	st := stats.New()
	env := newEnv(st)
	withGPX(env, "a.gpx")
	_ = env.Local.WriteFile(context.Background(), "/dst/a.jpg", []byte("x"), 0o644)
	st.Inc(stats.Copied, 1)

	r := &recordingRunner{lookPath: map[string]bool{"exiftool": true}, geotagErr: errors.New("boom")}
	g := &Geotag{Runner: r, GPXDir: "/gpx"}
	if err := g.Apply(context.Background(), &driver.TransformCtx{Env: env, Imported: []string{"/dst/a.jpg"}}); err == nil {
		t.Fatal("expected geotag error")
	}
	if st.Get(stats.Copied) != 0 {
		t.Fatalf("Copied = %d, want 0", st.Get(stats.Copied))
	}
}

func TestApplyNilContext(t *testing.T) {
	g := &Geotag{}
	if err := g.Apply(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil tctx")
	}
}
