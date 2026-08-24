package config

import (
	"testing"
	"time"
)

func env(map_ map[string]string) func(string) string {
	return func(k string) string { return map_[k] }
}

func TestDefaults(t *testing.T) {
	c := Defaults("/home/p", 1000)
	if c.GVFSRoot != "/run/user/1000/gvfs" {
		t.Fatalf("GVFSRoot = %q", c.GVFSRoot)
	}
	if c.FujifilmDest != "/home/p/Pictures/Fujifilm.Inbox" {
		t.Fatalf("FujifilmDest = %q", c.FujifilmDest)
	}
	if c.ConvertParallelism != 3 {
		t.Fatalf("parallelism = %d", c.ConvertParallelism)
	}
	if c.IOTimeout != DefaultIOTimeout {
		t.Fatalf("IOTimeout = %v, want default %v", c.IOTimeout, DefaultIOTimeout)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	c := FromEnv(env(map[string]string{
		"GVFS_ROOT":           "/custom/gvfs",
		"FUJIFILM_DEST":       "/pic",
		"FUJIFILM_RAW_DEST":   "/raw",
		"GPX_DIR":             "/gpx",
		"SUPERNOTE_DEST":      "/sn",
		"CONVERT_PARALLELISM": "5",
	}), "/h", 1)
	if c.GVFSRoot != "/custom/gvfs" || c.FujifilmDest != "/pic" || c.FujifilmRAWDest != "/raw" {
		t.Fatalf("cfg = %+v", c)
	}
	if c.GPXDir != "/gpx" || c.SupernoteDest != "/sn" || c.ConvertParallelism != 5 {
		t.Fatalf("cfg = %+v", c)
	}
	if c.Mode != "auto" {
		t.Fatalf("Mode = %q", c.Mode)
	}
}

func TestFromEnvIOTimeout(t *testing.T) {
	// duration string
	c := FromEnv(env(map[string]string{"IO_TIMEOUT": "90s"}), "/h", 1)
	if c.IOTimeout != 90*time.Second {
		t.Fatalf("IOTimeout = %v", c.IOTimeout)
	}
	// bare integer seconds
	c = FromEnv(env(map[string]string{"IO_TIMEOUT": "45"}), "/h", 1)
	if c.IOTimeout != 45*time.Second {
		t.Fatalf("IOTimeout = %v", c.IOTimeout)
	}
	// bad value falls back to default
	c = FromEnv(env(map[string]string{"IO_TIMEOUT": "oops"}), "/h", 1)
	if c.IOTimeout != DefaultIOTimeout {
		t.Fatalf("IOTimeout = %v, want default", c.IOTimeout)
	}
	c = FromEnv(env(map[string]string{"IO_TIMEOUT": "0"}), "/h", 1)
	if c.IOTimeout != DefaultIOTimeout {
		t.Fatalf("IOTimeout = %v, want default for 0", c.IOTimeout)
	}
}

func TestFromEnvIgnoresBadParallelism(t *testing.T) {
	c := FromEnv(env(map[string]string{"CONVERT_PARALLELISM": "oops"}), "/h", 1)
	if c.ConvertParallelism != 3 {
		t.Fatalf("parallelism = %d", c.ConvertParallelism)
	}
	c = FromEnv(env(map[string]string{"CONVERT_PARALLELISM": "0"}), "/h", 1)
	if c.ConvertParallelism != 3 {
		t.Fatalf("parallelism = %d", c.ConvertParallelism)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{"ok", Config{Mode: "auto", GVFSRoot: "/x", ConvertParallelism: 1, IOTimeout: DefaultIOTimeout}, false},
		{"bad mode", Config{Mode: "nope", GVFSRoot: "/x", ConvertParallelism: 1, IOTimeout: DefaultIOTimeout}, true},
		{"low parallelism", Config{Mode: "auto", GVFSRoot: "/x", ConvertParallelism: 0, IOTimeout: DefaultIOTimeout}, true},
		{"empty gvfs", Config{Mode: "auto", ConvertParallelism: 1, IOTimeout: DefaultIOTimeout}, true},
		{"zero io timeout", Config{Mode: "auto", GVFSRoot: "/x", ConvertParallelism: 1}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestDestOverrides(t *testing.T) {
	c := Defaults("/h", 1)
	c.DestOverride = "/override"
	if c.FujifilmJPEGDest() != "/override" {
		t.Fatalf("jpeg dest = %q", c.FujifilmJPEGDest())
	}
	if c.SupernoteDestEffective() != "/override" {
		t.Fatalf("sn dest = %q", c.SupernoteDestEffective())
	}
	c.DestOverride = ""
	if c.FujifilmJPEGDest() != c.FujifilmDest {
		t.Fatalf("jpeg dest should fall back")
	}
}
