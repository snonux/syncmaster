package gvfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"syncmaster/internal/shell"
)

func TestParseList(t *testing.T) {
	raw := []byte("IMG_0001.JPG\tregular\t12345\t1700000000\n" +
		"DCIM\tdirectory\t0\t1700000001\n" +
		"clip.mov\tregular\t9999\t1700000002\n")
	entries := ParseList(raw)
	if len(entries) != 3 {
		t.Fatalf("len = %d, want 3", len(entries))
	}
	if entries[0].Name != "IMG_0001.JPG" || entries[0].IsDir || entries[0].Size != 12345 {
		t.Fatalf("entry0 = %+v", entries[0])
	}
	if !entries[0].Modified.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("entry0 mtime = %v", entries[0].Modified)
	}
	if entries[1].Name != "DCIM" || !entries[1].IsDir {
		t.Fatalf("entry1 = %+v", entries[1])
	}
	if entries[2].Size != 9999 {
		t.Fatalf("entry2 size = %d", entries[2].Size)
	}
}

func TestParseListEmpty(t *testing.T) {
	if got := ParseList(nil); len(got) != 0 {
		t.Fatalf("len = %d", len(got))
	}
}

func TestFindMountsAndExists(t *testing.T) {
	f := shell.NewFake()
	f.RegisterLookPath("gio", true)
	// gio info succeeds for the gphoto2 mount, fails for the other.
	f.Register("gio", func(_ context.Context, args []string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "info" {
			path := args[len(args)-1]
			if path == "/gvfs/gphoto2:usb" {
				return []byte("ok"), nil
			}
			return nil, errors.New("unreachable")
		}
		return []byte(""), nil
	})
	g := &Gio{Runner: f, Root: "/gvfs", ReadDir: func(_ string) ([]string, error) {
		return []string{"gphoto2:usb", "mtp:other"}, nil
	}}
	mounts, err := g.FindMounts(context.Background(), "gphoto2:*")
	if err != nil {
		t.Fatalf("FindMounts: %v", err)
	}
	if len(mounts) != 1 || mounts[0] != filepath.Join("/gvfs", "gphoto2:usb") {
		t.Fatalf("mounts = %v", mounts)
	}

	ok, err := g.Exists(context.Background(), "/gvfs/gphoto2:usb")
	if err != nil || !ok {
		t.Fatalf("Exists gphoto2 = %v, %v", ok, err)
	}
	ok, err = g.Exists(context.Background(), "/gvfs/mtp:other")
	if err != nil || ok {
		t.Fatalf("Exists mtp:other = %v, %v", ok, err)
	}
}

func TestModifiedTime(t *testing.T) {
	f := shell.NewFake()
	f.Register("gio", func(_ context.Context, args []string) ([]byte, error) {
		return []byte("time::modified: 1699999999\n"), nil
	})
	g := &Gio{Runner: f, Root: "/gvfs"}
	mt, err := g.ModifiedTime(context.Background(), "/x")
	if err != nil {
		t.Fatalf("ModifiedTime: %v", err)
	}
	if !mt.Equal(time.Unix(1699999999, 0)) {
		t.Fatalf("mtime = %v", mt)
	}
}

func TestModifiedTimeMissing(t *testing.T) {
	f := shell.NewFake()
	f.Register("gio", func(context.Context, []string) ([]byte, error) {
		return []byte("nothing here\n"), nil
	})
	g := &Gio{Runner: f, Root: "/gvfs"}
	if _, err := g.ModifiedTime(context.Background(), "/x"); err == nil {
		t.Fatal("expected error when attribute missing")
	}
}

func TestList(t *testing.T) {
	f := shell.NewFake()
	f.Register("gio", func(_ context.Context, args []string) ([]byte, error) {
		if args[0] == "list" {
			return []byte("a.jpg\tregular\t10\t1000\n"), nil
		}
		return nil, nil
	})
	g := &Gio{Runner: f, Root: "/gvfs"}
	entries, err := g.List(context.Background(), "/x")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "a.jpg" || entries[0].Size != 10 {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestCopy(t *testing.T) {
	f := shell.NewFake()
	var gotArgs []string
	f.Register("gio", func(_ context.Context, args []string) ([]byte, error) {
		if args[0] == "copy" {
			gotArgs = args
			return nil, nil
		}
		return nil, nil
	})
	g := &Gio{Runner: f, Root: "/gvfs"}
	if err := g.Copy(context.Background(), "/src", "/dst"); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if len(gotArgs) != 3 || gotArgs[1] != "/src" || gotArgs[2] != "/dst" {
		t.Fatalf("args = %v", gotArgs)
	}
}

func TestDefaultReadDir(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "gphoto2:usb"), []byte("x"), 0o644)
	g := &Gio{Runner: shell.NewFake(), Root: dir}
	names, err := g.listRoot()
	if err != nil {
		t.Fatalf("listRoot: %v", err)
	}
	found := false
	for _, n := range names {
		if n == "gphoto2:usb" {
			found = true
		}
	}
	if !found {
		t.Fatalf("names = %v", names)
	}
}
