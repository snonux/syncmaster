package adb

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/snonux/syncmaster/internal/copier"
	"github.com/snonux/syncmaster/internal/shell"
)

// newFakeADB registers a single "adb" handler on a fresh shell.Fake that
// dispatches on the arg tail.
func newFakeADB(t *testing.T) *shell.Fake {
	t.Helper()
	f := shell.NewFake()
	f.Register("adb", func(_ context.Context, args []string) ([]byte, error) {
		// args is everything after "adb"; drop a leading -s <serial>.
		a := args
		if len(a) >= 2 && a[0] == "-s" {
			a = a[2:]
		}
		switch a[0] {
		case "devices":
			return []byte("List of devices attached\nSERIAL123         device usb:1-2 product:x model:y\nOFFLINE00         offline\n\n"), nil
		case "pull":
			return []byte("1 file pulled."), nil
		case "shell":
			switch {
			case len(a) >= 3 && a[1] == "test" && a[2] == "-e":
				return nil, &exec.ExitError{} // pretend absent
			case len(a) >= 3 && a[1] == "test" && a[2] == "-d":
				return nil, nil // pretend directory exists
			case len(a) >= 4 && a[1] == "stat" && a[3] == "%Y":
				return []byte("1787687012"), nil
			case len(a) >= 4 && a[1] == "stat":
				return []byte("14|1787386697|regular file|/sdcard/x/ql-a.md\n" +
					"0|1787386698|directory|/sdcard/x/sub\n" +
					"18|1787687012|regular file|/sdcard/x/ql-b.md\n"), nil
			}
		}
		return nil, errors.New("fake adb: unexpected " + strings.Join(args, " "))
	})
	return f
}

func TestFindMounts(t *testing.T) {
	c := Client{Runner: newFakeADB(t)}
	got, err := c.FindMounts(context.Background(), "")
	if err != nil {
		t.Fatalf("FindMounts: %v", err)
	}
	if len(got) != 1 || got[0] != "SERIAL123" {
		t.Fatalf("serials = %v, want [SERIAL123] (offline excluded)", got)
	}
}

func TestExistsAbsent(t *testing.T) {
	c := Client{Runner: newFakeADB(t), Serial: "SERIAL123"}
	ok, err := c.Exists(context.Background(), "/sdcard/missing")
	if err != nil || ok {
		t.Fatalf("Exists missing: ok=%v err=%v, want false/nil", ok, err)
	}
}

func TestModifiedTime(t *testing.T) {
	c := Client{Runner: newFakeADB(t), Serial: "SERIAL123"}
	tm, err := c.ModifiedTime(context.Background(), "/sdcard/x/ql-a.md")
	if err != nil {
		t.Fatalf("ModifiedTime: %v", err)
	}
	if want := time.Unix(1787687012, 0); !tm.Equal(want) {
		t.Fatalf("mtime = %v, want %v", tm, want)
	}
}

func TestListParsesStat(t *testing.T) {
	c := Client{Runner: newFakeADB(t), Serial: "SERIAL123"}
	got, err := c.List(context.Background(), "/sdcard/x")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []copier.Entry{
		{Name: "ql-a.md", Size: 14, Modified: time.Unix(1787386697, 0)},
		{Name: "sub", IsDir: true, Modified: time.Unix(1787386698, 0)},
		{Name: "ql-b.md", Size: 18, Modified: time.Unix(1787687012, 0)},
	}
	if len(got) != len(want) {
		t.Fatalf("List = %+v, want %+v", got, want)
	}
	for i, e := range got {
		w := want[i]
		if e.Name != w.Name || e.IsDir != w.IsDir || e.Size != w.Size || !e.Modified.Equal(w.Modified) {
			t.Fatalf("entry %d = %+v, want %+v", i, e, w)
		}
	}
}

func TestListEmptyDirectory(t *testing.T) {
	// stat on an empty dir (glob no match) errors; dirExists returns true ->
	// empty listing, no error.
	f := shell.NewFake()
	f.Register("adb", func(_ context.Context, args []string) ([]byte, error) {
		a := args
		if len(a) >= 2 && a[0] == "-s" {
			a = a[2:]
		}
		if len(a) >= 3 && a[0] == "shell" && a[1] == "test" && a[2] == "-d" {
			return nil, nil
		}
		return nil, &exec.ExitError{} // stat fails (no matches)
	})
	c := Client{Runner: f, Serial: "S"}
	got, err := c.List(context.Background(), "/sdcard/empty")
	if err != nil {
		t.Fatalf("List empty dir: %v", err)
	}
	if got != nil && len(got) != 0 {
		t.Fatalf("List empty dir = %v, want nil/empty", got)
	}
}

func TestListPartialStatOutput(t *testing.T) {
	// stat exits non-zero if any entry fails but still emits the entries it
	// could stat; List must return that partial listing, not discard it.
	f := shell.NewFake()
	f.Register("adb", func(_ context.Context, args []string) ([]byte, error) {
		return []byte("14|1787386697|regular file|/d/a.md\n"), errors.New("stat: permission denied on one entry")
	})
	c := Client{Runner: f, Serial: "S"}
	got, err := c.List(context.Background(), "/d")
	if err != nil {
		t.Fatalf("List: %v (should accept partial output)", err)
	}
	if len(got) != 1 || got[0].Name != "a.md" {
		t.Fatalf("List = %+v, want one partial entry a.md", got)
	}
}

func TestCopyUsesPullA(t *testing.T) {
	fb := newFakeADB(t)
	c := Client{Runner: fb, Serial: "SERIAL123"}
	if err := c.Copy(context.Background(), "/sdcard/x/ql-a.md", "/tmp/out.md"); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	// Last call should be: adb -s SERIAL123 pull -a <src> <dst>.
	last := fb.Calls[len(fb.Calls)-1]
	if last.Name != "adb" {
		t.Fatalf("name = %q, want adb", last.Name)
	}
	want := []string{"-s", "SERIAL123", "pull", "-a", "/sdcard/x/ql-a.md", "/tmp/out.md"}
	if strings.Join(last.Args, " ") != strings.Join(want, " ") {
		t.Fatalf("Copy args = %v, want %v", last.Args, want)
	}
}

func TestListSerialTargeting(t *testing.T) {
	// With a serial, List runs "adb -s <serial> shell stat ...".
	fb := newFakeADB(t)
	c := Client{Runner: fb, Serial: "SXYZ"}
	if _, err := c.List(context.Background(), "/d"); err != nil {
		t.Fatalf("List: %v", err)
	}
	var adbCall shell.Call
	for _, call := range fb.Calls {
		if call.Name == "adb" && len(call.Args) > 0 && call.Args[0] == "-s" {
			adbCall = call
			break
		}
	}
	if adbCall.Name == "" {
		t.Fatal("no adb -s call recorded")
	}
	if adbCall.Args[0] != "-s" || adbCall.Args[1] != "SXYZ" {
		t.Fatalf("serial args = %v, want -s SXYZ", adbCall.Args[:2])
	}
}
