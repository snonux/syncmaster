package syncmaster

import (
	"bytes"
	"context"
	"errors"
	"testing"

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

func (f *fakeDriver) Name() string { return f.name }
func (f *fakeDriver) Detect(context.Context, *driver.Env) ([]driver.Device, error) {
	f.detect++
	return f.devices, nil
}
func (f *fakeDriver) Sync(_ context.Context, dev driver.Device, env *driver.Env) error {
	f.synced = append(f.synced, dev)
	env.Stats.Inc(stats.Copied, 1)
	return f.syncErr
}

func newApp(st *stats.Stats, cfg config.Config) *App {
	return &App{Env: &driver.Env{
		Config: &cfg,
		Local:  fs.NewMem(),
		Runner: shell.NewFake(),
		Stats:  st,
		Out:    new(bytes.Buffer),
		Err:    new(bytes.Buffer),
	}}
}

func resetDrivers() {
	driver.Reset()
}

func TestRunExplicitMode(t *testing.T) {
	resetDrivers()
	defer resetDrivers()
	d := &fakeDriver{name: "fake", devices: []driver.Device{{Driver: "fake", Label: "Fake", Source: "/x"}}}
	if err := driver.Register(d); err != nil {
		t.Fatal(err)
	}
	st := stats.New()
	app := newApp(st, config.Config{Mode: "fake", GVFSRoot: "/x", ConvertParallelism: 1})
	if err := app.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(d.synced) != 1 {
		t.Fatalf("synced = %d", len(d.synced))
	}
}

func TestRunUnknownMode(t *testing.T) {
	resetDrivers()
	defer resetDrivers()
	app := newApp(stats.New(), config.Config{Mode: "nope", GVFSRoot: "/x", ConvertParallelism: 1})
	err := app.Run(context.Background())
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("err = %v, want ErrUsage", err)
	}
}

func TestRunExplicitNoDevice(t *testing.T) {
	resetDrivers()
	defer resetDrivers()
	d := &fakeDriver{name: "fake", devices: nil}
	_ = driver.Register(d)
	app := newApp(stats.New(), config.Config{Mode: "fake", GVFSRoot: "/x", ConvertParallelism: 1})
	if err := app.Run(context.Background()); !errors.Is(err, ErrNoDevice) {
		t.Fatalf("err = %v, want ErrNoDevice", err)
	}
}

func TestRunExplicitMultipleDevices(t *testing.T) {
	resetDrivers()
	defer resetDrivers()
	d := &fakeDriver{name: "fake", devices: []driver.Device{{Driver: "fake"}, {Driver: "fake"}}}
	_ = driver.Register(d)
	app := newApp(stats.New(), config.Config{Mode: "fake", GVFSRoot: "/x", ConvertParallelism: 1})
	if err := app.Run(context.Background()); !errors.Is(err, ErrMultipleDevices) {
		t.Fatalf("err = %v, want ErrMultipleDevices", err)
	}
}

func TestRunAutoSingle(t *testing.T) {
	resetDrivers()
	defer resetDrivers()
	d := &fakeDriver{name: "fake", devices: []driver.Device{{Driver: "fake", Label: "Fake"}}}
	_ = driver.Register(d)
	st := stats.New()
	app := newApp(st, config.Config{Mode: "auto", GVFSRoot: "/x", ConvertParallelism: 1})
	if err := app.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(d.synced) != 1 {
		t.Fatalf("synced = %d", len(d.synced))
	}
}

func TestRunAutoNone(t *testing.T) {
	resetDrivers()
	defer resetDrivers()
	_ = driver.Register(&fakeDriver{name: "fake", devices: nil})
	app := newApp(stats.New(), config.Config{Mode: "auto", GVFSRoot: "/x", ConvertParallelism: 1})
	if err := app.Run(context.Background()); !errors.Is(err, ErrNoDevice) {
		t.Fatalf("err = %v, want ErrNoDevice", err)
	}
}

func TestRunAutoMultiple(t *testing.T) {
	resetDrivers()
	defer resetDrivers()
	_ = driver.Register(&fakeDriver{name: "a", devices: []driver.Device{{Driver: "a", Label: "A"}}})
	_ = driver.Register(&fakeDriver{name: "b", devices: []driver.Device{{Driver: "b", Label: "B"}}})
	app := newApp(stats.New(), config.Config{Mode: "auto", GVFSRoot: "/x", ConvertParallelism: 1})
	err := app.Run(context.Background())
	if !errors.Is(err, ErrMultipleDevices) {
		t.Fatalf("err = %v, want ErrMultipleDevices", err)
	}
}

func TestRunAutoDeviceSelector(t *testing.T) {
	resetDrivers()
	defer resetDrivers()
	da := &fakeDriver{name: "a", devices: []driver.Device{{Driver: "a", Label: "A"}}}
	db := &fakeDriver{name: "b", devices: []driver.Device{{Driver: "b", Label: "B"}}}
	_ = driver.Register(da)
	_ = driver.Register(db)
	app := newApp(stats.New(), config.Config{Mode: "auto", Device: "b", GVFSRoot: "/x", ConvertParallelism: 1})
	if err := app.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(db.synced) != 1 || len(da.synced) != 0 {
		t.Fatalf("selector should run only b: a=%d b=%d", len(da.synced), len(db.synced))
	}
}

func TestRunHelp(t *testing.T) {
	app := newApp(stats.New(), config.Config{Mode: "help"})
	out := new(bytes.Buffer)
	app.Env.Out = out
	if err := app.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Len() == 0 {
		t.Fatal("help should print usage")
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
