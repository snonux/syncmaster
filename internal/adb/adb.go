// Package adb wraps the adb command-line tool to access files on an
// Android phone. Client implements both copier.Source (List/Copy) and
// driver.MountFS (FindMounts/Exists/ModifiedTime). A Client with an empty
// Serial addresses the default device (or all devices for FindMounts); a
// Client with a Serial targets that device via "adb -s <serial>".
package adb

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/snonux/syncmaster/internal/copier"
	"github.com/snonux/syncmaster/internal/driver"
	"github.com/snonux/syncmaster/internal/shell"
)

// Client is an adb client. Runner execs the adb binary; Serial selects a
// device via "adb -s <serial>" for per-device calls. FindMounts always
// enumerates every attached device regardless of Serial.
type Client struct {
	Runner shell.Runner
	Serial string
}

var _ copier.Source = Client{}
var _ driver.MountFS = Client{}

// run execs adb with the given subcommand args, prepending -s Serial when set.
func (c Client) run(ctx context.Context, args ...string) ([]byte, error) {
	if c.Serial != "" {
		args = append([]string{"-s", c.Serial}, args...)
	}
	return c.Runner.Run(ctx, "adb", args...)
}

// FindMounts returns the serials of all attached, authorized devices. The
// glob is ignored (adb enumerates devices, not mount names).
func (c Client) FindMounts(ctx context.Context, _ string) ([]string, error) {
	out, err := c.Runner.Run(ctx, "adb", "devices", "-l")
	if err != nil {
		return nil, fmt.Errorf("adb devices: %w", err)
	}
	var serials []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of devices") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != "device" {
			continue // skip offline/unauthorized/other states
		}
		serials = append(serials, fields[0])
	}
	return serials, nil
}

// Exists reports whether path exists on the device per "adb shell test -e".
// A non-zero exit (path absent) is (false, nil); a genuine failure or context
// cancellation is returned as an error.
func (c Client) Exists(ctx context.Context, path string) (bool, error) {
	_, err := c.run(ctx, "shell", "test", "-e", shellQuote(path))
	if err == nil {
		return true, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	if shell.IsExitError(err) {
		return false, nil
	}
	return false, fmt.Errorf("adb test -e %s: %w", path, err)
}

// dirExists reports whether path is a directory on the device.
func (c Client) dirExists(ctx context.Context, path string) bool {
	_, err := c.run(ctx, "shell", "test", "-d", shellQuote(path))
	return err == nil
}

// ModifiedTime returns the mtime of path as a unix time.
func (c Client) ModifiedTime(ctx context.Context, path string) (time.Time, error) {
	out, err := c.run(ctx, "shell", "stat", "-c", "%Y", shellQuote(path))
	if err != nil {
		return time.Time{}, fmt.Errorf("adb stat %s: %w", path, err)
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("adb parse mtime %q: %w", out, err)
	}
	return time.Unix(sec, 0), nil
}

// List lists the non-hidden entries in dir, returning copier entries. It runs
// one "adb shell stat -c '%s|%Y|%F|%n' <dir>/*" per directory. stat exits
// non-zero when the glob has no match (empty dir) or any single entry fails,
// but still emits the entries it could stat; that partial output is parsed.
// When nothing was statted and the directory itself exists, an empty listing
// is returned. Hidden entries (dotfiles) are not enumerated by the glob.
func (c Client) List(ctx context.Context, dir string) ([]copier.Entry, error) {
	dir = path.Clean(dir)
	out, err := c.run(ctx, "shell", "stat", "-c", "'%s|%Y|%F|%n'", shellQuote(dir)+"/*")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("adb stat %s: %w", dir, ctxErr)
		}
		if len(out) > 0 {
			return parseStat(out), nil // partial listing; stat logged the failure
		}
		if c.dirExists(ctx, dir) {
			return nil, nil // empty directory (no non-hidden entries to stat)
		}
		return nil, fmt.Errorf("adb stat %s: %w", dir, err)
	}
	return parseStat(out), nil
}

// Copy pulls src from the device to dst locally. -a preserves the remote
// mtime so a size+mtime skip policy can dedup across runs.
func (c Client) Copy(ctx context.Context, src, dst string) error {
	if _, err := c.run(ctx, "pull", "-a", src, dst); err != nil {
		return fmt.Errorf("adb pull %s -> %s: %w", src, dst, err)
	}
	return nil
}

// parseStat parses "stat -c '%s|%Y|%F|%n'" output (one entry per line:
// size|mtime|type|fullpath) into copier entries.
func parseStat(raw []byte) []copier.Entry {
	var out []copier.Entry
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// SplitN keeps the fullpath (parts[3]) intact even if it contains '|'.
		parts := strings.SplitN(line, "|", 4)
		if len(parts) != 4 {
			continue
		}
		size, _ := strconv.ParseInt(parts[0], 10, 64)
		mtime, _ := strconv.ParseInt(parts[1], 10, 64)
		out = append(out, copier.Entry{
			Name:     path.Base(parts[3]),
			IsDir:    parts[2] == "directory",
			Size:     size,
			Modified: time.Unix(mtime, 0),
		})
	}
	return out
}

// shellQuote single-quotes s for the device shell (adb joins "shell" args
// into one command line parsed by the device's sh).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
