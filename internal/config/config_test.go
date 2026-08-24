package config

import "testing"

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
		{"ok", Config{Mode: "auto", GVFSRoot: "/x", ConvertParallelism: 1}, false},
		{"bad mode", Config{Mode: "nope", GVFSRoot: "/x", ConvertParallelism: 1}, true},
		{"low parallelism", Config{Mode: "auto", GVFSRoot: "/x", ConvertParallelism: 0}, true},
		{"empty gvfs", Config{Mode: "auto", ConvertParallelism: 1}, true},
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
