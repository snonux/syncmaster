package driver

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeDriver struct {
	name    string
	devices []Device
	syncErr error
	synced  []Device
}

func (f *fakeDriver) Name() string        { return f.name }
func (f *fakeDriver) Description() string { return "fake driver for tests" }
func (f *fakeDriver) Detect(context.Context, *Env) ([]Device, error) {
	return f.devices, nil
}
func (f *fakeDriver) Sync(_ context.Context, dev Device, _ *Env) error {
	f.synced = append(f.synced, dev)
	return f.syncErr
}

func TestRegistryRegisterAndLookup(t *testing.T) {
	r := NewRegistry()
	d := &fakeDriver{name: "x", devices: []Device{{Driver: "x"}}}
	if err := r.Register(d); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, ok := r.Lookup("x")
	if !ok || got != d {
		t.Fatalf("Lookup = %v, %v", got, ok)
	}
	if _, ok := r.Lookup("missing"); ok {
		t.Fatal("missing should not be found")
	}
}

func TestRegistryDuplicate(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&fakeDriver{name: "x"})
	err := r.Register(&fakeDriver{name: "x"})
	if !errors.Is(err, ErrDuplicateDriver) {
		t.Fatalf("err = %v, want ErrDuplicateDriver", err)
	}
}

func TestRegistryNilDriver(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(nil); err == nil {
		t.Fatal("expected error for nil driver")
	}
}

func TestRegistryAllSorted(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&fakeDriver{name: "c"})
	_ = r.Register(&fakeDriver{name: "a"})
	_ = r.Register(&fakeDriver{name: "b"})
	all := r.All()
	if len(all) != 3 {
		t.Fatalf("len = %d", len(all))
	}
	if all[0].Name() != "a" || all[1].Name() != "b" || all[2].Name() != "c" {
		t.Fatalf("order = %v,%v,%v", all[0].Name(), all[1].Name(), all[2].Name())
	}
}

func TestDefaultRegistryReset(t *testing.T) {
	Reset()
	if err := Register(&fakeDriver{name: "tmp"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, ok := Lookup("tmp"); !ok {
		t.Fatal("expected to find tmp")
	}
	Reset()
	if _, ok := Lookup("tmp"); ok {
		t.Fatal("expected tmp gone after reset")
	}
}

// fakeTransform records that it ran and can fail.
type fakeTransform struct {
	name string
	err  error
	ran  bool
}

func (f *fakeTransform) Name() string { return f.name }
func (f *fakeTransform) Apply(_ context.Context, tctx *TransformCtx) error {
	f.ran = true
	return f.err
}

func TestRunTransformsAppliesInOrderStopsOnError(t *testing.T) {
	t1 := &fakeTransform{name: "t1"}
	t2 := &fakeTransform{name: "t2", err: errors.New("boom")}
	t3 := &fakeTransform{name: "t3"}
	tctx := &TransformCtx{}
	err := RunTransforms(context.Background(), tctx, t1, t2, t3)
	if err == nil || !strings.Contains(err.Error(), "t2") {
		t.Fatalf("err = %v, want t2 wrapped", err)
	}
	if !t1.ran || !t2.ran {
		t.Fatal("t1 and t2 should have run")
	}
	if t3.ran {
		t.Fatal("t3 must not run after t2 failed")
	}
	if tctx.Scratch == nil {
		t.Fatal("RunTransforms should initialize Scratch")
	}
}

func TestRunTransformsEmptyAndNilTctx(t *testing.T) {
	if err := RunTransforms(context.Background(), nil); err != nil {
		t.Fatalf("empty nil-tctx: %v", err)
	}
	if err := RunTransforms(context.Background(), &TransformCtx{}); err != nil {
		t.Fatalf("empty: %v", err)
	}
}
