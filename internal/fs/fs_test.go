package fs

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func collectWalk(t *testing.T, m FS, root string) []string {
	t.Helper()
	var paths []string
	if err := m.WalkDir(root, func(p string, _ Entry) error {
		paths = append(paths, p)
		return nil
	}); err != nil {
		t.Fatalf("WalkDir: %v", err)
	}
	sort.Strings(paths)
	return paths
}

func TestMemWriteReadStat(t *testing.T) {
	m := NewMem()
	if err := m.WriteFile("/a/b.txt", []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := m.ReadFile("/a/b.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("data = %q", got)
	}
	e, err := m.Stat("/a/b.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if e.IsDir || e.Size != 5 {
		t.Fatalf("entry = %+v", e)
	}
}

func TestMemStatMissing(t *testing.T) {
	m := NewMem()
	_, err := m.Stat("/nope")
	if !errors.Is(err, ErrNotExist) {
		t.Fatalf("err = %v, want ErrNotExist", err)
	}
}

func TestMemReadFileMissing(t *testing.T) {
	m := NewMem()
	if _, err := m.ReadFile("/nope"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
}

func TestMemRemoveAndRename(t *testing.T) {
	m := NewMem()
	_ = m.WriteFile("/x.txt", []byte("x"), 0o644)
	if err := m.Rename("/x.txt", "/y.txt"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if _, err := m.Stat("/x.txt"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("old should be gone")
	}
	if _, err := m.Stat("/y.txt"); err != nil {
		t.Fatalf("new should exist: %v", err)
	}
	if err := m.Remove("/y.txt"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := m.Stat("/y.txt"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("removed should be gone")
	}
}

func TestMemRenameMissing(t *testing.T) {
	m := NewMem()
	if err := m.Rename("/nope", "/x"); !errors.Is(err, ErrNotExist) {
		t.Fatalf("err = %v", err)
	}
}

func TestMemMkdirAllAndReadDir(t *testing.T) {
	m := NewMem()
	_ = m.MkdirAll("/d", 0o755)
	_ = m.WriteFile("/d/a.txt", []byte("a"), 0o644)
	_ = m.WriteFile("/d/b.txt", []byte("bb"), 0o644)
	_ = m.MkdirAll("/d/sub", 0o755)
	_ = m.WriteFile("/d/sub/c.txt", []byte("ccc"), 0o644)

	entries, err := m.ReadDir("/d")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name)
	}
	want := []string{"a.txt", "b.txt", "sub"}
	if len(names) != len(want) {
		t.Fatalf("ReadDir names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("ReadDir names = %v, want %v", names, want)
		}
	}
}

func TestMemWalkDir(t *testing.T) {
	m := NewMem()
	_ = m.WriteFile("/r/a.txt", []byte("a"), 0o644)
	_ = m.WriteFile("/r/sub/b.txt", []byte("b"), 0o644)
	_ = m.MkdirAll("/r/sub", 0o755)

	paths := collectWalk(t, m, "/r")
	want := []string{"/r", "/r/a.txt", "/r/sub", "/r/sub/b.txt"}
	if len(paths) != len(want) {
		t.Fatalf("walk = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("walk = %v, want %v", paths, want)
		}
	}
}

func TestMemWriteFileAtSetsModtime(t *testing.T) {
	m := NewMem()
	mt := time.Unix(1234567, 0)
	m.WriteFileAt("/f.txt", []byte("z"), mt)
	e, err := m.Stat("/f.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !e.ModTime.Equal(mt) {
		t.Fatalf("ModTime = %v, want %v", e.ModTime, mt)
	}
}

func TestOSRoundTrip(t *testing.T) {
	dir := t.TempDir()
	var o OS
	p := filepath.Join(dir, "f.txt")
	if err := o.WriteFile(p, []byte("hi"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	e, err := o.Stat(p)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if e.Size != 2 {
		t.Fatalf("size = %d", e.Size)
	}
	got, err := o.ReadFile(p)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hi" {
		t.Fatalf("data = %q", got)
	}
	entries, err := o.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "f.txt" {
		t.Fatalf("entries = %+v", entries)
	}
	paths := collectWalk(t, o, dir)
	if len(paths) < 2 {
		t.Fatalf("walk = %v", paths)
	}
	if err := o.Remove(p); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := o.Stat(p); !errors.Is(err, ErrNotExist) {
		t.Fatalf("after remove err = %v", err)
	}
}

func TestOSMkdirAll(t *testing.T) {
	var o OS
	dir := filepath.Join(t.TempDir(), "a", "b")
	if err := o.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("dir not created: %v", err)
	}
}
