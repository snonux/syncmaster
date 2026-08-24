package syncmaster

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"syncmaster/internal/config"
	"syncmaster/internal/driver"
	"syncmaster/internal/fs"
	"syncmaster/internal/shell"
	"syncmaster/internal/stats"
)

// fakeDriver is a minimal driver for orchestrator tests.
type fakeDriver struct {
	name    string
	devices []driver.Device
	syncErr error
	detect  int
	synced  []driver.Device
}

func (f *fakeDriver) Name() string        { return f.name }
func (f *fakeDriver) Description() string { return "fake driver for tests" }
func (f *fakeDriver) Detect(context.Context, *driver.Env) ([]driver.Device, error) {
	f.detect++
	return f.devices, nil
}
func (f *fakeDriver) Sync(_ context.Context, dev driver.Device, env *driver.Env) error {
	f.synced = append(f.synced, dev)
	env.Stats.Inc(stats.Copied, 1)
	return f.syncErr
}

// blockingDriver's Sync blocks until ctx is done, to prove the orchestrator
// aborts a run when the context is cancelled.
type blockingDriver struct{ name string }

func (b blockingDriver) Name() string        { return b.name }
func (b blockingDriver) Description() string { return "blocking driver for tests" }
func (b blockingDriver) Detect(context.Context, *driver.Env) ([]driver.Device, error) {
	return []driver.Device{{Driver: b.name, Label: "block"}}, nil
}
func (b blockingDriver) Sync(ctx context.Context, _ driver.Device, _ *driver.Env) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestRunAbortDuringSync(t *testing.T) {
	app := newApp(stats.New(), config.Config{Mode: "block", GVFSRoot: "/x", ConvertParallelism: 1, IOTimeout: config.DefaultIOTimeout})
	_ = app.Env.Drivers.Register(blockingDriver{name: "block"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not abort within 2s")
	}
}

func newApp(st *stats.Stats, cfg config.Config) *App {
	return &App{Env: &driver.Env{
		Config:  &cfg,
		Drivers: driver.NewRegistry(),
		Local:   fs.NewMem(),
		Runner:  shell.NewFake(),
		Stats:   st,
		Out:     new(bytes.Buffer),
		Err:     new(bytes.Buffer),
	}}
}

func TestRunExplicitMode(t *testing.T) {
	d := &fakeDriver{name: "fake", devices: []driver.Device{{Driver: "fake", Label: "Fake", Source: "/x"}}}
	st := stats.New()
	app := newApp(st, config.Config{Mode: "fake", GVFSRoot: "/x", ConvertParallelism: 1, IOTimeout: config.DefaultIOTimeout})
	if err := app.Env.Drivers.Register(d); err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(d.synced) != 1 {
		t.Fatalf("synced = %d", len(d.synced))
	}
}

func TestRunUnknownMode(t *testing.T) {
	app := newApp(stats.New(), config.Config{Mode: "nope", GVFSRoot: "/x", ConvertParallelism: 1, IOTimeout: config.DefaultIOTimeout})
	err := app.Run(context.Background())
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
}

func TestRunExplicitNoDevice(t *testing.T) {
	app := newApp(stats.New(), config.Config{Mode: "fake", GVFSRoot: "/x", ConvertParallelism: 1, IOTimeout: config.DefaultIOTimeout})
	_ = app.Env.Drivers.Register(&fakeDriver{name: "fake", devices: nil})
	if err := app.Run(context.Background()); !errors.Is(err, ErrNoDevice) {
		t.Fatalf("err = %v, want ErrNoDevice", err)
	}
}

func TestRunExplicitMultipleDevices(t *testing.T) {
	app := newApp(stats.New(), config.Config{Mode: "fake", GVFSRoot: "/x", ConvertParallelism: 1, IOTimeout: config.DefaultIOTimeout})
	_ = app.Env.Drivers.Register(&fakeDriver{name: "fake", devices: []driver.Device{{Driver: "fake"}, {Driver: "fake"}}})
	if err := app.Run(context.Background()); !errors.Is(err, ErrMultipleDevices) {
		t.Fatalf("err = %v, want ErrMultipleDevices", err)
	}
}

func TestRunAutoSingle(t *testing.T) {
	d := &fakeDriver{name: "fake", devices: []driver.Device{{Driver: "fake", Label: "Fake"}}}
	app := newApp(stats.New(), config.Config{Mode: "auto", GVFSRoot: "/x", ConvertParallelism: 1, IOTimeout: config.DefaultIOTimeout})
	_ = app.Env.Drivers.Register(d)
	if err := app.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(d.synced) != 1 {
		t.Fatalf("synced = %d", len(d.synced))
	}
}

func TestRunAutoNone(t *testing.T) {
	app := newApp(stats.New(), config.Config{Mode: "auto", GVFSRoot: "/x", ConvertParallelism: 1, IOTimeout: config.DefaultIOTimeout})
	_ = app.Env.Drivers.Register(&fakeDriver{name: "fake", devices: nil})
	if err := app.Run(context.Background()); !errors.Is(err, ErrNoDevice) {
		t.Fatalf("err = %v, want ErrNoDevice", err)
	}
}

func TestRunAutoMultiple(t *testing.T) {
	app := newApp(stats.New(), config.Config{Mode: "auto", GVFSRoot: "/x", ConvertParallelism: 1, IOTimeout: config.DefaultIOTimeout})
	_ = app.Env.Drivers.Register(&fakeDriver{name: "a", devices: []driver.Device{{Driver: "a", Label: "A"}}})
	_ = app.Env.Drivers.Register(&fakeDriver{name: "b", devices: []driver.Device{{Driver: "b", Label: "B"}}})
	err := app.Run(context.Background())
	if !errors.Is(err, ErrMultipleDevices) {
		t.Fatalf("err = %v, want ErrMultipleDevices", err)
	}
}

func TestRunAutoDeviceSelector(t *testing.T) {
	da := &fakeDriver{name: "a", devices: []driver.Device{{Driver: "a", Label: "A"}}}
	db := &fakeDriver{name: "b", devices: []driver.Device{{Driver: "b", Label: "B"}}}
	app := newApp(stats.New(), config.Config{Mode: "auto", Device: "b", GVFSRoot: "/x", ConvertParallelism: 1, IOTimeout: config.DefaultIOTimeout})
	_ = app.Env.Drivers.Register(da)
	_ = app.Env.Drivers.Register(db)
	if err := app.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(db.synced) != 1 || len(da.synced) != 0 {
		t.Fatalf("selector should run only b: a=%d b=%d", len(da.synced), len(db.synced))
	}
}

func TestRunMissingRegistry(t *testing.T) {
	// No Drivers wired: the orchestrator must surface a clear error instead
	// of reaching for the package-level default registry.
	app := &App{Env: &driver.Env{Config: &config.Config{Mode: "auto"}}}
	if err := app.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "registry not configured") {
		t.Fatalf("auto err = %v, want registry-not-configured", err)
	}
	app = &App{Env: &driver.Env{Config: &config.Config{Mode: "fake"}}}
	if err := app.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "registry not configured") {
		t.Fatalf("explicit err = %v, want registry-not-configured", err)
	}
}

func TestRunHelp(t *testing.T) {
	app := newApp(stats.New(), config.Config{Mode: "help"})
	_ = app.Env.Drivers.Register(&fakeDriver{name: "fujifilm", devices: nil})
	out := new(bytes.Buffer)
	app.Env.Out = out
	if err := app.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Len() == 0 {
		t.Fatal("help should print usage")
	}
	got := out.String()
	if !strings.Contains(got, "|fujifilm|") {
		t.Fatalf("usage synopsis should list registered driver names, got:\n%s", got)
	}
	if !strings.Contains(got, "fake driver for tests") {
		t.Fatalf("usage should include the driver Description(), got:\n%s", got)
	}
	if !strings.Contains(got, "--io-timeout") {
		t.Fatalf("usage should document --io-timeout, got:\n%s", got)
	}
}

func TestSelftestPass(t *testing.T) {
	f := shell.NewFake()
	f.Register("go", func(context.Context, []string) ([]byte, error) { return nil, nil })
	app := newApp(stats.New(), config.Config{Mode: "selftest"})
	app.Env.Runner = f
	if err := app.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(f.CallsFor("go")) != 2 {
		t.Fatalf("go calls = %d, want 2", len(f.CallsFor("go")))
	}
}

func TestSelftestFail(t *testing.T) {
	f := shell.NewFake()
	f.Register("go", func(context.Context, []string) ([]byte, error) { return nil, errors.New("fail") })
	app := newApp(stats.New(), config.Config{Mode: "selftest"})
	app.Env.Runner = f
	if err := app.Run(context.Background()); err == nil {
		t.Fatal("expected selftest error")
	}
}

func TestFinishSuccess(t *testing.T) {
	st := stats.New()
	st.Inc(stats.Copied, 3)
	app := newApp(st, config.Config{})
	out := new(bytes.Buffer)
	app.Env.Out = out
	if err := app.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if !contains(out.String(), "safe to unplug") {
		t.Fatalf("out = %q", out.String())
	}
}

func TestFinishWithFailures(t *testing.T) {
	st := stats.New()
	st.Inc(stats.Failed, 2)
	app := newApp(st, config.Config{})
	if err := app.Finish(); !errors.Is(err, ErrFailed) {
		t.Fatalf("err = %v, want ErrFailed", err)
	}
}

func contains(s, sub string) bool {
	return bytes.Contains([]byte(s), []byte(sub))
}
