// Package config holds the runtime configuration for a syncmaster run. Only
// FromEnv touches the environment; tests construct Config literals directly.
package config

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// Defaults for the supported sync modes.
const (
	DefaultConvertParallelism = 3
)

// Config is the full runtime configuration.
type Config struct {
	Mode            string // auto|fujifilm|supernote|selftest|help
	DestOverride    string // positional destination arg
	Device          string // -device selector
	AllowMissingGPS bool
	Verbose         bool

	GVFSRoot           string
	FujifilmDest       string
	FujifilmRAWDest    string
	GPXDir             string
	SupernoteDest      string
	ConvertParallelism int
}

// FromEnv builds a Config from defaults plus environment overrides. getenv
// and home/uid are parameters so tests never touch os.Getenv or $HOME.
func FromEnv(getenv func(string) string, home string, uid int) Config {
	c := Defaults(home, uid)
	if v := getenv("GVFS_ROOT"); v != "" {
		c.GVFSRoot = v
	}
	if v := getenv("FUJIFILM_DEST"); v != "" {
		c.FujifilmDest = v
	}
	if v := getenv("FUJIFILM_RAW_DEST"); v != "" {
		c.FujifilmRAWDest = v
	}
	if v := getenv("GPX_DIR"); v != "" {
		c.GPXDir = v
	}
	if v := getenv("SUPERNOTE_DEST"); v != "" {
		c.SupernoteDest = v
	}
	if v := getenv("CONVERT_PARALLELISM"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			c.ConvertParallelism = n
		}
	}
	c.Mode = "auto"
	return c
}

// Defaults returns the built-in default configuration for the given home and
// uid (used to resolve GVFS_ROOT and destination paths).
func Defaults(home string, uid int) Config {
	return Config{
		Mode:               "auto",
		GVFSRoot:           fmt.Sprintf("/run/user/%d/gvfs", uid),
		FujifilmDest:       filepath.Join(home, "Pictures", "Fujifilm.Inbox"),
		FujifilmRAWDest:    filepath.Join(home, "Pictures", "Fujifilm.RAW"),
		GPXDir:             filepath.Join(home, "Documents", "GPX"),
		SupernoteDest:      filepath.Join(home, "Documents", "Inbox", "Supernote"),
		ConvertParallelism: DefaultConvertParallelism,
	}
}

// Validate reports the first configuration problem.
func (c Config) Validate() error {
	switch c.Mode {
	case "auto", "fujifilm", "supernote", "selftest", "help":
	default:
		return fmt.Errorf("unknown mode %q", c.Mode)
	}
	if c.ConvertParallelism < 1 {
		return fmt.Errorf("convert parallelism must be >= 1")
	}
	if strings.TrimSpace(c.GVFSRoot) == "" {
		return fmt.Errorf("gvfs root must be set")
	}
	return nil
}

// FujifilmJPEGDest returns the effective JPEG/video destination, honoring an
// override.
func (c Config) FujifilmJPEGDest() string {
	if c.DestOverride != "" {
		return c.DestOverride
	}
	return c.FujifilmDest
}

// SupernoteDestEffective returns the effective Supernote destination, honoring
// an override.
func (c Config) SupernoteDestEffective() string {
	if c.DestOverride != "" {
		return c.DestOverride
	}
	return c.SupernoteDest
}
