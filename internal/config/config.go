// Package config holds the runtime configuration for a syncmaster run. Only
// FromEnv touches the environment; tests construct Config literals directly.
package config

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Defaults for the supported sync modes.
const (
	DefaultConvertParallelism = 3
	// DefaultIOTimeout bounds each external command (gio/exiftool/
	// supernote-tool) so a hung operation cannot block the import until
	// manual Ctrl+C. Override with IO_TIMEOUT env or --io-timeout.
	DefaultIOTimeout = 120 * time.Second
)

// Config is the full runtime configuration.
type Config struct {
	Mode            string // auto | <driver name> | selftest | help (driver names resolved from the registry)
	DestOverride    string // positional destination arg
	Device          string // -device selector
	AllowMissingGPS bool
	Verbose         bool

	GVFSRoot            string
	FujifilmDest        string
	FujifilmRAWDest     string
	GPXDir              string
	SupernoteDest       string
	AndroidSource       string
	AndroidDest         string
	AndroidDeleteSource bool
	Run                 bool // true = execute (default false = dry run)
	ConvertParallelism  int
	IOTimeout           time.Duration // per-operation bound for external tools
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
	if v := getenv("ANDROID_SOURCE"); v != "" {
		c.AndroidSource = v
	}
	if v := getenv("ANDROID_DEST"); v != "" {
		c.AndroidDest = v
	}
	if v := getenv("ANDROID_DELETE_SOURCE"); v != "" {
		c.AndroidDeleteSource = !(v == "0" || strings.EqualFold(v, "false"))
	}
	if v := getenv("SYNCMASTER_RUN"); v != "" {
		c.Run = !(v == "0" || strings.EqualFold(v, "false"))
	}
	if v := getenv("CONVERT_PARALLELISM"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			c.ConvertParallelism = n
		}
	}
	if v := getenv("IO_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			c.IOTimeout = d
		} else if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.IOTimeout = time.Duration(n) * time.Second
		}
	}
	c.Mode = "auto"
	return c
}

// HomeAndUID resolves the current user's home directory and unix uid for
// default config paths. It does user.Current I/O and is kept here (rather
// than in cmd) so the wiring that builds a Config lives in one package.
func HomeAndUID() (string, int) {
	home := ""
	if u, err := user.Current(); err == nil {
		home = u.HomeDir
		if uid, err := strconv.Atoi(u.Uid); err == nil {
			return home, uid
		}
	}
	return home, os.Getuid()
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
		AndroidSource:      "/sdcard/Notes/Vault/Quicklog",
		AndroidDest:        filepath.Join(home, "Notes", "Quicklog"),
		ConvertParallelism: DefaultConvertParallelism,
		IOTimeout:          DefaultIOTimeout,
	}
}

// Validate reports the first configuration problem. Driver-name modes
// (e.g. "fujifilm", "supernote") are NOT validated here: they are resolved
// against the driver registry at dispatch time, so adding a driver does not
// require editing this switch. Only the framework meta-modes are known here.
func (c Config) Validate() error {
	switch c.Mode {
	case "auto", "help", "selftest":
		// framework meta-modes handled by the orchestrator
	case "":
		return fmt.Errorf("mode must be set")
	default:
		// a driver name; validated against the registry in App.Run
	}
	if c.ConvertParallelism < 1 {
		return fmt.Errorf("convert parallelism must be >= 1")
	}
	if c.IOTimeout <= 0 {
		return fmt.Errorf("io timeout must be > 0")
	}
	if strings.TrimSpace(c.GVFSRoot) == "" {
		return fmt.Errorf("gvfs root must be set")
	}
	if strings.HasPrefix(c.DestOverride, "--") {
		return fmt.Errorf("destination %q looks like a misplaced flag; flags must precede the mode (e.g. `syncmaster --delete-source android`)", c.DestOverride)
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

// AndroidDestEffective returns the effective Android destination, honoring
// an override.
func (c Config) AndroidDestEffective() string {
	if c.DestOverride != "" {
		return c.DestOverride
	}
	return c.AndroidDest
}
