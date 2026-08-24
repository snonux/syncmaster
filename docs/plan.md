# syncmaster — port plan: `~/scripts/usbimport` → Go (pluggable architecture)

Goal: reimplement every feature of the bash `usbimport` script as the
`syncmaster` Go application, **and** make the architecture extensible so new
sync features (more devices, more file types, more post-copy transforms) can be
added later by writing a single driver package + one registration line — no
changes to the orchestrator.

Guiding principle: **drivers are plugins; everything else is a reusable,
composable building block.** The orchestrator knows nothing about Fujifilm or
Supernote; it only knows the `Driver` interface and a registry.

## 1. Feature inventory (what the script does today)

### Modes + flags
- `auto` (default): detect the single supported device and run its importer.
- `fujifilm`, `supernote`: run a specific importer.
- `--test`: self-test (`bash -n` + `shellcheck`).
- `-h|--help`: usage.
- `--allow-missing-gps`: import camera images even when no GPS match.
- Optional trailing `destination` arg overrides the mode's default dest.

### Device discovery (GVFS)
- `GVFS_ROOT` default `/run/user/$(id -u)/gvfs`.
- Fujifilm: first `gphoto2:*` answering `gio info`.
- Supernote: first `mtp:*Supernote*` whose `Internal shared storage` (or root) answers `gio info`.

### Fujifilm import
- Recursive `gio list -a standard::name,standard::type` walk.
- Classify by extension:
  - RAW: 3FR ARW CR2 CR3 DCR DNG ERF GPR IIQ KDC MEF MOS MRW NEF NRW ORF PEF RAF RAW RW2 RWL SR2 SRF SRW X3F
  - Fujifilm media = RAW ∪ {JPG JPEG MOV MP4 AVI}
  - Fujifilm image  = RAW ∪ {JPG JPEG}  (videos not geotagged)
- Copy JPEG/video → `FUJIFILM_DEST`, RAW → `FUJIFILM_RAW_DEST`; skip if dest exists.
- Track copied image paths for geotagging.
- Geotag with `exiftool -overwrite_original -P -geotag <gpx>… <images>`:
  - Collect `*.gpx` under `GPX_DIR` (sorted). None → fail unless `--allow-missing-gps`.
  - Rescan `exiftool -if 'not (defined $GPSLatitude and defined $GPSLongitude)' -p '$FilePath'` for images still missing GPS → fail unless allowed.
- On geotag failure: **rollback** — delete copied images, decrement copied count.
- Final `sync` flush; print summary.

### Supernote import
- Mount root = `Internal shared storage`.
- `Note` → `SUPERNOTE_DEST` (recursive); `Document` → `SUPERNOTE_DEST/KOReader` (KOReader books + `.sdr` sidecars), if present.
- Skip-unchanged: source `size` + `time::modified` (gio) vs local `stat` size + mtime; else `rm -f` + `gio copy`.
- `.note` → PDF via `supernote-tool convert -a -t pdf`, parallel (`CONVERT_PARALLELISM`, default 3) via `xargs -0 -P`; staleness via `.note-meta` sidecar (`size=`/`mtime=` of the note); tmp file + `mv` on success; pre-clean `*.pdf.tmp.*`.

### Cross-cutting
- Counters: found, copied, skipped, failed, converted, convert-skipped.
- `trap cleanup EXIT` removes temp dirs.
- Exit codes: `0` ok; `1` failures; `2` usage / multiple devices.
- Required externals: `gio`, `exiftool` (fujifilm), `supernote-tool` (supernote), `shellcheck`+`bash` (`--test`).
- Env: `FUJIFILM_DEST`, `FUJIFILM_RAW_DEST`, `GPX_DIR`, `SUPERNOTE_DEST`, `CONVERT_PARALLELISM`, `GVFS_ROOT`.
- Logs to stdout, errors to stderr.

## 2. Pluggable architecture

### Core abstractions

**`Driver`** — the only thing the orchestrator depends on. Each device/source
type is a driver. Adding a sync feature = implement this + register.
```go
package driver

// Device is a discovered, connected source a driver can sync from.
type Device struct {
    Driver string // driver name
    Label  string // human label, e.g. "Fujifilm camera"
    Source string // mount root / source path
    Extra  map[string]any // driver-specific (e.g. note root, document root)
}

// Driver discovers devices and syncs from one.
type Driver interface {
    Name() string
    Detect(ctx context.Context, env *Env) ([]Device, error)   // 0, 1, or many
    Sync(ctx context.Context, dev Device, env *Env) (*Stats, error)
}
```

**`Env`** — the dependency-injection bag passed to every driver, holding shared
services and config. Drivers never reach for globals; they take what they need
from `Env`.
```go
type Env struct {
    Ctx     context.Context
    Config  *config.Config
    FS      gvfs.FS            // GVFS/gio access (nil if driver doesn't use it)
    LocalFS fs.FS              // local filesystem (dest side, meta, rollback)
    Clock   time.Clock         // deterministic time
    Runner  shell.Runner       // exec wrapper for gio/exiftool/supernote-tool
    Out     io.Writer          // progress log
    Err     io.Writer          // error log
    Stats   *Stats             // shared, concurrency-safe
}
```

**Registry** — drivers self-register; orchestrator is agnostic.
```go
package driver

func Register(d Driver)             // called from a wiring package
func All() []Driver
func Lookup(name string) (Driver, bool)
```
Built-in drivers are registered **explicitly** in `internal/drivers/wire.go`
(one line each) for determinism — no `init()` ordering surprises, and it's the
single place to see what's enabled.

**`Transform`** — the post-copy pipeline, the main extensibility seam. A driver
declares an ordered slice of transforms applied after the copy phase. Geotag
and note→PDF are just two transforms; future ones (e.g. re-encode, upload,
checksum) drop in without touching drivers.
```go
type Transform interface {
    Name() string
    Apply(ctx context.Context, tctx *TransformCtx) error
}

// TransformCtx is what a transform operates on.
type TransformCtx struct {
    Env       *Env
    DestRoot  string                 // where files landed
    Imported  []string               // files of interest (e.g. images to geotag)
    Device    Device
    // transforms may add fields to a per-run scratch map
    Scratch   map[string]any
}
```
- `gpx.Geotag` implements `Transform` (fujifilm uses it; rollback is handled
  inside it via a `Rollback` hook it registers on `Env`).
- `note.Convert` implements `Transform` (supernote uses it).

### Composable building blocks (reusable by every driver)

These are generic and unaware of any specific device — a future driver composes
them instead of reinventing copy/skip/classify logic.

**`internal/copier`** — generic recursive tree copier, parameterised by policies:
```go
type Source interface {
    List(ctx, dir string) ([]Entry, error)
    Copy(ctx, src, dst string) error
}
type Entry struct { Name string; RelPath string; IsDir bool; Size int64; Modified time.Time }

type SkipPolicy  func(ctx, src Source, e Entry, destPath string) (bool, error) // true=skip
type Classify    func(e Entry) (bucket string, ok bool)                         // route to dest subpath
type DestResolver func(e Entry) (destPath string, include bool)

type Copier struct {
    Src      Source
    Skip     SkipPolicy       // SkipExistingName | SkipUnchangedSizeMtime | nil
    Resolve  DestResolver     // maps an entry to a destination path (or excludes)
    OnCopied func(destPath string, e Entry)   // hook: e.g. collect images for geotag
    Stats    *Stats
}
func (c *Copier) CopyTree(ctx, srcDir, dstRoot string) error
```
- Prebuilt skip policies: `SkipExistingName` (fujifilm), `SkipUnchangedSizeMtime` (supernote).
- `OnCopied` is how fujifilm collects `Imported` image paths for the geotag transform — no special casing in the copier.

**`internal/media`** — extension classification as a **registry**, not hardcoded:
```go
func RegisterClass(name string, exts ...string)   // e.g. "raw", "image", "video"
func IsA(class, filename string) bool
func Classes(filename string) []string
```
- Core registers `"raw"`, `"image"`, `"video"`, `"fujifilm-media"`, `"fujifilm-image"`.
- A future driver adds its own classes (e.g. `"audio"`, `"go-pro"`) without touching others.
- Used by drivers' `DestResolver` to route files (RAW → raw dest, JPEG/video → jpeg dest).

**`internal/gvfs`** — GVFS/gio wrapper, implements `copier.Source` and device
discovery helpers. Reusable by any GVFS-mounted device (cameras, phones, MTP
readers):
```go
type FS interface {
    FindMounts(ctx, glob string) ([]string, error)      // gphoto2:*, mtp:*Supernote*, ...
    Exists(ctx, path string) (bool, error)
    List(ctx, dir string) ([]copier.Entry, error)
    Copy(ctx, src, dst string) error
    ModifiedTime(ctx, path string) (time.Time, error)
}
```
- Fujifilm driver: `FindMounts(ctx, "gphoto2:*")`.
- Supernote driver: `FindMounts(ctx, "mtp:*Supernote*")` + `mtp:*supernote*`, then probe `Internal shared storage`.
- Future MTP device: same `FS`, different glob.

**`internal/gpx`** — geotag as a reusable `Transform`:
```go
type Geotag struct {
    Runner       shell.Runner
    GPXDir       string
    AllowMissing bool
}
func (g *Geotag) Name() string { return "geotag" }
func (g *Geotag) Apply(ctx, tctx *TransformCtx) error   // find tracks, exiftool tag, scan missing, rollback on failure
```

**`internal/note`** — `.note`→PDF as a reusable `Transform`:
```go
type Convert struct {
    Runner    shell.Runner
    Workers   int
}
func (c *Convert) Name() string { return "note-to-pdf" }
func (c *Convert) Apply(ctx, tctx *TransformCtx) error   // walk *.note, staleness, parallel convert, meta sidecars
```

**`internal/shell`** — one `Runner` interface every shell-out goes through:
```go
type Runner interface {
    Run(ctx, name string, args ...string) ([]byte, error)
    LookPath(name string) (string, error)
}
```
Real impl = `exec.CommandContext`; fakes in tests → every package testable
without `gio`/`exiftool`/`supernote-tool` installed.

**`internal/stats`** — concurrency-safe counters reused by all drivers/transforms:
```go
type Stats struct { /* atomic.Int64 fields */ }
func (s *Stats) Inc(kind Kind, n int64)
func (s *Stats) Snapshot() map[Kind]int64
func (s *Stats) String() string
```

**`internal/fssync`** — Linux `sync(2)` flush behind a build tag
(`//go:build linux`, `unix.Sync()`); no-op elsewhere.

### Putting it together — the two first drivers

Both are thin compositions of the blocks above, ~the only device-specific code
is the mount glob, the `DestResolver`, and which transforms to run.

**`internal/fujifilm`**:
```go
type Driver struct{}
func (Driver) Name() string { return "fujifilm" }
func (Driver) Detect(ctx, env) ([]Device, error)   // gvfs.FindMounts("gphoto2:*")
func (Driver) Sync(ctx, dev, env) (*Stats, error) {
    var imported []string
    c := &copier.Copier{
        Src:     env.FS,
        Skip:    copier.SkipExistingName,
        Resolve: fujifilmResolver(env.Config), // RAW→raw dest, JPEG/video→jpeg dest
        OnCopied: func(p string, _ copier.Entry) {
            if media.IsA("fujifilm-image", p) { imported = append(imported, p) }
        },
        Stats: env.Stats,
    }
    if err := c.CopyTree(ctx, dev.Source, env.Config.FujifilmDest); err != nil { return err }
    for _, t := range []driver.Transform{
        &gpx.Geotag{Runner: env.Runner, GPXDir: env.Config.GPXDir, AllowMissing: env.Config.AllowMissingGPS},
    } {
        if err := t.Apply(ctx, &driver.TransformCtx{Env: env, DestRoot: env.Config.FujifilmDest, Imported: imported, Device: dev}); err != nil {
            return err
        }
    }
    return env.Stats, nil
}
```

**`internal/supernote`**:
```go
func (Driver) Sync(ctx, dev, env) (*Stats, error) {
    c := &copier.Copier{Src: env.FS, Skip: copier.SkipUnchangedSizeMtime, Resolve: identity, Stats: env.Stats}
    _ = c.CopyTree(ctx, dev.Extra["noteRoot"].(string), env.Config.SupernoteDest)
    if docRoot, ok := dev.Extra["documentRoot"].(string); ok {
        _ = c.CopyTree(ctx, docRoot, filepath.Join(env.Config.SupernoteDest, "KOReader"))
    }
    return runTransforms(ctx, env, dev, env.Config.SupernoteDest, nil,
        &note.Convert{Runner: env.Runner, Workers: env.Config.ConvertParallelism})
}
```

### Orchestrator (`internal/syncmaster`)
Knows only `driver.All()` and the `Driver` interface:
```go
type App struct {
    Env *driver.Env
}
func (a *App) Run(ctx) error {
    switch a.Env.Config.Mode {
    case "auto":      return a.runAuto(ctx)   // detect across all drivers
    case "help":      return a.usage()
    case "selftest":  return a.selftest(ctx)
    default:
        d, ok := driver.Lookup(a.Env.Config.Mode)
        if !ok { return ErrUsage }
        return a.runOne(ctx, d, a.Env.Config.DestOverride)
    }
}
func (a *App) runAuto(ctx) error {
    var devices []driver.Device
    for _, d := range driver.All() {
        ds, _ := d.Detect(ctx, a.Env); devices = append(devices, ds...)
    }
    switch len(devices) {
    case 0:  return ErrNoDevice
    case 1:  return a.runOne(ctx, driver.Lookup devices[0]...)
    default: return ErrMultipleDevices   // exit 2; list them, allow -device <name>
    }
}
```
- `-device <name>` selector lets the user disambiguate when multiple devices are
  connected (new flexibility vs. the script's hard exit 2).
- Summary + `sync` flush printed once at the end from the orchestrator.

### Wiring (`internal/drivers/wire.go`) — the single registration point
```go
package drivers

import (
    "syncmaster/internal/driver"
    "syncmaster/internal/fujifilm"
    "syncmaster/internal/supernote"
)

func RegisterAll() {
    driver.Register(fujifilm.Driver{})
    driver.Register(supernote.Driver{})
    // future: driver.Register(gopro.Driver{})
    // future: driver.Register(phonephotos.Driver{})
}
```

### Adding a new sync feature later (the flexibility payoff)
1. Write `internal/<thing>` implementing `driver.Driver` — usually by composing
   `copier.Copier` + existing/new `Transform`s + `gvfs.FS` or a new `Source`.
2. Add one `driver.Register(<thing>.Driver{})` line in `internal/drivers/wire.go`.
3. Add its config keys/env (typed struct in `internal/config`, optional).
4. `auto` discovers and runs it automatically; `-mode <thing>` runs it explicitly.

No orchestrator changes. No other driver changes. Existing transforms
(geotag, note→pdf) and skip policies are reusable as-is.

## Testability contract (first-class)

**Every package must be unit-testable with zero external dependencies:** no
mounted device, no `gio`/`exiftool`/`supernote-tool` installed, no network, no
real `$HOME`, no wall clock, no real disk. Tests run hermetically anywhere
`go test` runs.

This is enforced by four hard rules:

### Rule 1 — every side effect is an injected interface
No package-level singletons, no `init()` side effects, no direct calls to
`os`/`exec`/`net`/`time.Now`/`os.Getenv` inside logic. Each side-effecting
surface is a small interface, constructed in `main` and passed in:

| Concern | Interface | Real impl | Fake |
|---|---|---|---|
| shell out (gio/exiftool/supernote-tool) | `shell.Runner` | `exec.CommandContext` | records args, returns canned stdout/exit |
| remote tree (GVFS/MTP) | `copier.Source` (= `gvfs.FS`) | `gvfs.Gio` | in-memory tree |
| local filesystem | `fs.FS` | `fs.OS` (wraps `os.*`) | `fs.Mem` (in-memory) |
| time / "now" | `time.Clock` | `time.Now` | fixed clock |
| device detection + sync | `driver.Driver` | real drivers | fake drivers |
| post-copy transforms | `driver.Transform` | `gpx.Geotag`, `note.Convert` | fake transforms |
| stats counters | `*stats.Stats` | atomic-backed | same (pure) |

Constructors take interfaces, return concrete types:
`copier.New(src Source, fs fs.FS, clock time.Clock, opts ...) *Copier`.

### Rule 2 — a local-filesystem interface (`internal/fs`)
The bash script mixes `os.Stat`, `os.Remove`, `os.Rename`, `mkdir`, and
`stat -c` for dest-side checks. To keep the copier's skip policies, the note
meta read/write, and the geotag rollback **disk-free testable**, all local-FS
ops go through one interface:
```go
package fs

type Entry struct { Name string; IsDir bool; Size int64; ModTime time.Time }
type FS interface {
    Stat(path string) (Entry, error)
    MkdirAll(path string, perm os.FileMode) error
    Remove(path string) error
    Rename(old, new string) error
    WriteFile(path string, data []byte, perm os.FileMode) error
    ReadFile(path string) ([]byte, error)
    ReadDir(path string) ([]Entry, error)
    WalkDir(root string, fn func(string, Entry) error) error
}
type OS struct{}   // wraps os.*
type Mem struct{}  // in-memory map; tests only
```
- `copier.SkipExistingName` and `SkipUnchangedSizeMtime` consult `fs.FS`, never `os` directly.
- `gpx.Geotag` rollback removes files via `fs.FS`.
- `note.Convert` writes `.note-meta`, renames tmp→final, pre-cleans `*.pdf.tmp.*` via `fs.FS`.
- `fs.Mem` is the single fake backing most unit tests.

### Rule 3 — deterministic time via `time.Clock`
Any logic comparing mtimes or computing "now" takes a `Clock`:
```go
type Clock interface { Now() time.Time }
```
So staleness/signature checks are fully deterministic regardless of when the
test runs.

### Rule 4 — config built literally in tests
`config.FromEnv()` is the only place that touches `os.Getenv`/`os.UserHomeDir`.
Tests construct `config.Config{...}` directly — no env mutation, no `t.Setenv`
needed for logic tests. Per-driver config is typed structs (Q6) so tests are
compile-time safe.

### What this guarantees
- `go test ./...` passes on a clean CI box with no devices, no `gio`, no
  `exiftool`, no `supernote-tool`, no `$HOME` setup.
- Real-binary behavior is covered by a small, opt-in `integration` build-tagged
  suite (skipped under `testing.Short()`) — never required for green unit tests.
- Every driver and transform has a fake-based test exercising its full flow.

## 3. Package tree

```
cmd/syncmaster/main.go            flags/subcommands, signal ctx, wire drivers, exit codes
internal/version.go               Version constant (exists)
internal/config/                  Config + env defaults + per-driver sub-config + validation
internal/shell/                   Runner interface (exec + LookPath) + fakes
internal/fs/                      FS interface (OS + Mem) for all local-FS ops
internal/time/                    Clock interface (real + fixed) for deterministic time
internal/stats/                   concurrency-safe counters
internal/gvfs/                    GVFS/gio: FindMounts, List, Copy, Exists, ModifiedTime (implements copier.Source)
internal/copier/                  generic recursive CopyTree + SkipPolicy/DestResolver/OnCopied
internal/media/                   ext-class registry (RegisterClass/IsA/Classes)
internal/gpx/                     Geotag Transform (exiftool) + GPX discovery
internal/note/                    note→PDF Convert Transform (supernote-tool) + worker pool
internal/fssync/                  Linux sync() flush (build-tagged)
internal/driver/                  Driver/Device/Env/Transform/TransformCtx + Registry
internal/drivers/wire.go          RegisterAll() — single registration point
internal/fujifilm/                Fujifilm driver (compose copier + gpx.Geotag)
internal/supernote/               Supernote driver (compose copier + note.Convert)
internal/syncmaster/              orchestrator: Run/auto-detect/dispatch/summary/exit codes
Magefile.go                       + Lint target
```

## 4. Concurrency model
- Copy phase: single-threaded per driver (GVFS/gio isn't concurrency-friendly); multiple drivers are still detected sequentially in `auto`.
- Transforms may parallelize internally (`note.Convert` worker pool, bounded by `CONVERT_PARALLELISM`).
- `Stats` uses `atomic.Int64`; safe across transform workers.
- ctx cancellation checked at dispatch points; `exec.CommandContext` kills children on cancel.

## 5. Error handling, rollback, exit codes
- Wrap with `%w`; never ignore errors (errcheck-clean).
- Fujifilm geotag rollback lives in the `gpx.Geotag` transform (it owns `Imported` via `TransformCtx`); on failure it deletes those files and decrements `Stats.Copied`.
- Exit codes via sentinel errors mapped in `main`:
  - `0` success; `1` any failed copy/convert/geotag; `2` usage / `ErrMultipleDevices` / `ErrNoDevice` (configurable).
- Temp scratch replaced by in-memory channels + `defer os.Remove`; no global trap.

## 6. Testing strategy (everything unit-testable)

Guiding rule: **logic tests never touch disk, network, devices, or external
binaries.** They inject `fs.Mem`, a fake `shell.Runner`, a fixed `Clock`, and
fake `copier.Source`/`driver.Driver`/`driver.Transform` implementations.

Per-package:
- `internal/media`: pure table tests for every extension + registry add/lookup.
- `internal/shell`: fake `Runner` returns canned stdout/exit and records args;
  `LookPath` hits/misses.
- `internal/fs`: `Mem` round-trips (write/read/stat/rename/remove/walk);
  `OS` smoke test behind `integration` tag.
- `internal/time`: fixed `Clock` returns the configured `time.Time`.
- `internal/stats`: atomic increments under concurrent goroutines; snapshot/string.
- `internal/gvfs`: parse canned `gio list`/`gio info`/`gio copy` via fake `Runner`;
  `FindMounts` glob filtering; golden-file parsing tests.
- `internal/copier`: `fs.Mem` dest + fake `Source` tree; assert every skip policy
  (`SkipExistingName`, `SkipUnchangedSizeMtime`, nil), `DestResolver` routing,
  `OnCopied` collection, recursion into subdirs, stats increments, ctx cancel.
- `internal/gpx`: fake `Runner` simulates exiftool tag + missing-GPS output;
  assert `-geotag` args, missing detection, `--allow-missing-gps` branch, and
  rollback deletes the `Imported` files via `fs.FS`.
- `internal/note`: fake `Converter` + `fs.Mem`; assert staleness skip (meta match),
  success writes `.note-meta` + renames tmp→final, failure increments Failed,
  worker parallelism bounded by a serial test mode, ctx cancel stops dispatch,
  stale `*.pdf.tmp.*` pre-clean.
- `internal/fujifilm` / `internal/supernote`: fake `copier.Source`/`gvfs.FS` +
  fake `Transform`s (record `Apply` calls and args); assert end-to-end copy,
  transform ordering, `Imported` collection, dest routing, stats.
- `internal/driver` + `internal/syncmaster`: registry add/lookup/conflict;
  `auto` branches (0/1/many devices) using fake `Driver`s; `-device` selector;
  summary/sync-flush/exit-code mapping.
- `internal/config`: `FromEnv` with a fake env map (constructor takes `map[string]string`,
  not the real `os.Getenv`); validation errors.

Conventions:
- Table-driven subtests everywhere; `t.Parallel()` where independent.
- Determinism: fixed `Clock`; parallel transform tested with a serial worker
  count (`Workers: 1`) or a fake that records call order.
- Golden files for `gio` parsing under `testdata/`.
- Hermetic: no `t.Setenv` for logic tests; no temp dirs unless the unit under
  test is literally filesystem behavior (then use `fs.Mem`).
- Real-device tests live under `//go:build integration`, skipped via
  `testing.Short()`; never required for `go test ./...`.

Coverage gate: `Magefile.Test` runs `go test -race -cover ./...`; a `Coverage`
  target fails under 60% (per the skill). `Lint` adds `golangci-lint run`.

## 7. Parity notes & deliberate differences from bash
- `--test` self-test → `mage test` + `mage lint`. Optionally a `selftest`
  subcommand running `go vet`/`go build`. (Open question Q1.)
- `sync` flush → `golang.org/x/sys/unix.Sync()` on Linux behind a build tag.
- Tilde expansion → `os.UserHomeDir()`.
- `gio list` parsing kept tolerant of missing trailing fields.
- Shell `case` globs → `media` registry (`filepath.Ext` + `strings.EqualFold`).
- `xargs -P` → Go worker pool (deterministic in tests).
- Env var names kept identical for drop-in replacement.

## 8. Magefile additions
- `Lint`: `go vet ./...`, `gofmt -l .` (fail if non-empty), `errcheck ./...`,
  `golang.org/x/sys/unix` build-tagged build, `golangci-lint run` if installed.
- Keep `Build`, `Test`, `Install`, `Uninstall`.
- Optional `Integration` target for build-tagged tests.

## 9. Dependencies
- Stdlib for the core. Optional/small:
  - `golang.org/x/sys/unix` for `Sync()` (Linux) — acceptable, build-tagged.
  - Worker pool via stdlib channel+WaitGroup (no errgroup dep) to keep `go.mod` clean.
- No CLI framework; stdlib `flag` + subcommand-style positional args.

## 10. Implementation order (phases)
1. **Seams**: `internal/driver` (Driver/Device/Env/Transform/Registry), `internal/shell` (Runner + fake), `internal/stats`. Tests for registry + stats.
2. **Building blocks**: `internal/media` (registry), `internal/copier` (CopyTree + skip policies + DestResolver + OnCopied), `internal/gvfs` (gio wrapper implementing copier.Source + FindMounts). Tests with fakes.
3. **Transforms**: `internal/gpx` (Geotag), `internal/note` (Convert, worker pool, staleness). Tests with fake Runner/converter.
4. **First drivers**: `internal/fujifilm`, `internal/supernote` composing the blocks + transforms. Tests with fake `gvfs.FS`.
5. **Orchestrator + wiring**: `internal/syncmaster` (auto-detect, dispatch, summary, sync flush, exit codes), `internal/drivers/wire.go`, `internal/config`, `internal/fssync`. Tests for auto branches.
6. **main**: flags/subcommands, signal ctx, `RegisterAll()`, DI, exit-code mapping.
7. **Magefile Lint** + guardrails; `go vet`/`errcheck`/`golangci-lint` clean.
8. **Parity validation**: run Go binary side-by-side with the bash script against a real Fujifilm mount and a real Supernote; diff destinations. Keep the bash script until parity is proven.

## 11. Open questions
- Q1: Keep a `--test`/`selftest` subcommand, or rely on `mage test`/`mage lint`?
- Q2: Add `golang.org/x/sys/unix` for `Sync()`, or skip the explicit flush (rely on close/umount)?
- Q3: Subcommands (`syncmaster fujifilm [dest]`) vs. `-mode fujifilm` flag? Subcommands match the script's UX best.
- Q4: `auto` on multiple devices — keep exit `2` (script behavior) but add `-device <name>` selector for flexibility. Keep `2` default for scriptability.
- Q5: Keep env var names identical (`FUJIFILM_DEST`, …) for drop-in replacement? **Recommend yes.**
- Q6: Per-driver config as typed structs in `internal/config` (e.g. `FujifilmConfig`) referenced by name, or a generic `map[string]any`? **Recommend typed structs** for safety + IDE support, registered against the driver name.