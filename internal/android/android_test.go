package android

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/snonux/syncmaster/internal/clock"
	"github.com/snonux/syncmaster/internal/config"
	"github.com/snonux/syncmaster/internal/driver"
	"github.com/snonux/syncmaster/internal/fs"
	"github.com/snonux/syncmaster/internal/shell"
	"github.com/snonux/syncmaster/internal/stats"
)

const srcDir = "/sdcard/Notes/Vault/Quicklog"

// newEnvADB builds an Env whose Runner is a fake adb that lists the given
// files (size|mtime|path lines), lists one device, and "pulls" by writing
// each file's content to the local fs with its remote mtime (mimicking
// `adb pull -a`).
func newEnvADB(t *testing.T, local *fs.Mem, st *stats.Counters, files map[string]struct {
	Content []byte
	Mtime   int64
}) *driver.Env {
	t.Helper()
	f := shell.NewFake()
	f.Register("adb", func(_ context.Context, args []string) ([]byte, error) {
		a := args
		if len(a) >= 2 && a[0] == "-s" {
			a = a[2:]
		}
		switch a[0] {
		case "devices":
			return []byte("List of devices attached\nPIXEL123         device usb:1-2\nOFFLINE00         offline\n\n"), nil
		case "shell":
			var lines []byte
			for p, fi := range files {
				lines = append(lines, []byte(fmt.Sprintf("%d|%d|regular file|%s\n", len(fi.Content), fi.Mtime, p))...)
			}
			return lines, nil
		case "pull":
			// a = ["pull", "-a", src, dst]
			src, dst := a[2], a[3]
			fi := files[src]
			local.WriteFileAt(dst, fi.Content, time.Unix(fi.Mtime, 0))
			return []byte("1 file pulled."), nil
		}
		return nil, nil
	})
	cfg := config.Defaults("/home/u", 1000)
	cfg.AndroidSource = srcDir
	cfg.AndroidDest = "/home/u/Notes/Quicklog"
	return &driver.Env{
		Config: &cfg, Runner: f, Local: local, Clock: clock.Fixed{T: time.Unix(0, 0)},
		Stats: st, Out: new(bytes.Buffer), Err: new(bytes.Buffer),
	}
}

func TestDetectUnconfiguredHasNoDevices(t *testing.T) {
	cfg := config.Defaults("/home/u", 1000)
	cfg.AndroidSource = ""
	env := &driver.Env{Config: &cfg, Runner: shell.NewFake()}
	devs, err := (Driver{}).Detect(context.Background(), env)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(devs) != 0 {
		t.Fatalf("unconfigured driver should detect nothing, got %v", devs)
	}
}

func TestDetectReturnsAuthorizedDevices(t *testing.T) {
	local := fs.NewMem()
	env := newEnvADB(t, local, stats.New(), map[string]struct {
		Content []byte
		Mtime   int64
	}{"/sdcard/Notes/Vault/Quicklog/a.md": {Content: []byte("x"), Mtime: 1}})
	devs, err := (Driver{}).Detect(context.Background(), env)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if len(devs) != 1 || devs[0].Driver != "android" || devs[0].Extra["serial"] != "PIXEL123" {
		t.Fatalf("devs = %+v", devs)
	}
	if devs[0].Source != srcDir {
		t.Fatalf("Source = %q, want %q", devs[0].Source, srcDir)
	}
}

func TestSyncCopiesAndThenSkips(t *testing.T) {
	local := fs.NewMem()
	const mtime int64 = 1787386697
	files := map[string]struct {
		Content []byte
		Mtime   int64
	}{
		srcDir + "/a.md": {Content: []byte("quicklog entry"), Mtime: mtime}, // 14 bytes
		srcDir + "/b.md": {Content: []byte("second entry!"), Mtime: mtime},
	}

	// Run 1: both files copied.
	st := stats.New()
	env := newEnvADB(t, local, st, files)
	devs, err := (Driver{}).Detect(context.Background(), env)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if err := (Driver{}).Sync(context.Background(), devs[0], env); err != nil {
		t.Fatalf("Sync run 1: %v", err)
	}
	if got := st.Get(stats.Copied); got != 2 {
		t.Fatalf("run 1 Copied = %d, want 2", got)
	}
	for _, p := range []string{"/home/u/Notes/Quicklog/a.md", "/home/u/Notes/Quicklog/b.md"} {
		got, err := local.ReadFile(context.Background(), p)
		if err != nil {
			t.Fatalf("missing %s: %v", p, err)
		}
		if string(got) != string(files[srcDir+"/"+filepath.Base(p)].Content) {
			t.Fatalf("%s content = %q", p, got)
		}
	}

	// Run 2: size+mtime match -> skip both (adb pull -a preserved mtime).
	st2 := stats.New()
	env.Stats = st2
	if err := (Driver{}).Sync(context.Background(), devs[0], env); err != nil {
		t.Fatalf("Sync run 2: %v", err)
	}
	if got := st2.Get(stats.Copied); got != 0 {
		t.Fatalf("run 2 Copied = %d, want 0 (should skip)", got)
	}
	if got := st2.Get(stats.Skipped); got != 2 {
		t.Fatalf("run 2 Skipped = %d, want 2", got)
	}
}

func TestSyncDeleteSourceMovesBackedUpFiles(t *testing.T) {
	local := fs.NewMem()
	type fi struct {
		Content []byte
		Mtime   int64
	}
	files := map[string]fi{
		srcDir + "/a.md": {Content: []byte("note a"), Mtime: 1787386697}, // copied
		srcDir + "/b.md": {Content: []byte("note b"), Mtime: 1787386698}, // already backed up -> skipped
		srcDir + "/c.md": {Content: []byte("note c"), Mtime: 1787386699}, // copy fails -> kept
	}
	// Pre-create b.md in the destination with matching size+mtime so it is skipped.
	local.WriteFileAt("/home/u/Notes/Quicklog/b.md", files[srcDir+"/b.md"].Content, time.Unix(files[srcDir+"/b.md"].Mtime, 0))

	var deleted []string
	f := shell.NewFake()
	f.Register("adb", func(_ context.Context, args []string) ([]byte, error) {
		a := args
		if len(a) >= 2 && a[0] == "-s" {
			a = a[2:]
		}
		switch a[0] {
		case "devices":
			return []byte("List of devices attached\nPIXEL123         device\n\n"), nil
		case "shell":
			if len(a) >= 2 && a[1] == "rm" {
				// a = [shell, rm, -f, 'path']; record the quoted path (strip quotes).
				p := strings.Trim(a[len(a)-1], "'")
				deleted = append(deleted, p)
				return nil, nil
			}
			// stat listing for the source dir.
			var lines []byte
			for p, info := range files {
				lines = append(lines, []byte(fmt.Sprintf("%d|%d|regular file|%s\n", len(info.Content), info.Mtime, p))...)
			}
			return lines, nil
		case "pull":
			src, dst := a[2], a[3]
			if strings.HasSuffix(src, "/c.md") {
				return nil, errors.New("adb pull: failed")
			}
			info := files[src]
			local.WriteFileAt(dst, info.Content, time.Unix(info.Mtime, 0))
			return []byte("1 file pulled."), nil
		}
		return nil, nil
	})
	cfg := config.Defaults("/home/u", 1000)
	cfg.AndroidSource = srcDir
	cfg.AndroidDest = "/home/u/Notes/Quicklog"
	cfg.AndroidDeleteSource = true
	env := &driver.Env{
		Config: &cfg, Runner: f, Local: local, Clock: clock.Fixed{T: time.Unix(0, 0)},
		Stats: stats.New(), Out: new(bytes.Buffer), Err: new(bytes.Buffer),
	}
	d := (Driver{}).Sync(context.Background(), driver.Device{Driver: "android", Source: srcDir, Extra: map[string]any{"serial": "PIXEL123"}}, env)
	if d != nil {
		t.Fatalf("Sync: %v", d)
	}
	// a.md (copied) and b.md (skipped) were confirmed backed up -> deleted from phone.
	want := map[string]bool{srcDir + "/a.md": true, srcDir + "/b.md": true}
	if len(deleted) != 2 {
		t.Fatalf("deleted = %v, want 2 entries %v", deleted, want)
	}
	for _, p := range deleted {
		if !want[p] {
			t.Errorf("unexpected delete of %s", p)
		}
	}
	// c.md failed to copy -> must NOT be deleted.
	for _, p := range deleted {
		if p == srcDir+"/c.md" {
			t.Error("c.md was deleted despite a failed copy")
		}
	}
	// a.md landed locally; c.md did not.
	if _, err := local.Stat(context.Background(), "/home/u/Notes/Quicklog/a.md"); err != nil {
		t.Errorf("a.md missing locally: %v", err)
	}
	if _, err := local.Stat(context.Background(), "/home/u/Notes/Quicklog/c.md"); err == nil {
		t.Error("c.md should not exist locally (copy failed)")
	}
}

func TestSyncReCopiesChangedFile(t *testing.T) {
	local := fs.NewMem()
	files := map[string]struct {
		Content []byte
		Mtime   int64
	}{srcDir + "/a.md": {Content: []byte("original-v1"), Mtime: 1787386697}}
	st := stats.New()
	env := newEnvADB(t, local, st, files)
	devs, err := (Driver{}).Detect(context.Background(), env)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if err := (Driver{}).Sync(context.Background(), devs[0], env); err != nil {
		t.Fatalf("Sync run 1: %v", err)
	}

	// The file changed on the phone (different content/mtime).
	files[srcDir+"/a.md"] = struct {
		Content []byte
		Mtime   int64
	}{Content: []byte("original-v2-longer"), Mtime: 1787687012}
	st2 := stats.New()
	env.Stats = st2
	if err := (Driver{}).Sync(context.Background(), devs[0], env); err != nil {
		t.Fatalf("Sync run 2: %v", err)
	}
	if got := st2.Get(stats.Copied); got != 1 {
		t.Fatalf("run 2 Copied = %d, want 1 (changed file re-copied)", got)
	}
	got, _ := local.ReadFile(context.Background(), "/home/u/Notes/Quicklog/a.md")
	if string(got) != "original-v2-longer" {
		t.Fatalf("content = %q, want the updated note", got)
	}
}
