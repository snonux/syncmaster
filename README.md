# syncmaster

Import files from supported USB devices mounted through GVFS — Fujifilm
cameras and Supernote Nomad — with geotagging and `.note`→PDF conversion.

A Go port of the original `~/scripts/usbimport` bash script, built on a
pluggable driver architecture so new sync sources can be added by writing one
package and registering it in a single place.

## Build

    mage            # build (default target)
    mage test       # run tests with -race -cover
    mage lint       # go vet + gofmt + errcheck + golangci-lint
    mage install    # build and install to GOPATH/bin

## Usage

    syncmaster [auto|fujifilm|supernote|selftest|help] [destination]
    syncmaster -version
    syncmaster --allow-missing-gps        # import images even without GPS
    syncmaster --device fujifilm          # pick a device when multiple connected

### Environment overrides

    FUJIFILM_DEST, FUJIFILM_RAW_DEST, GPX_DIR, SUPERNOTE_DEST,
    CONVERT_PARALLELISM, GVFS_ROOT

## Architecture (pluggable drivers)

The orchestrator knows only the `Driver` interface and a registry; it knows
nothing about Fujifilm or Supernote. New sync features = implement `Driver` +
register one line in `internal/drivers/wire.go`.

```
cmd/syncmaster/main.go            flags/subcommands, signal ctx, DI wiring
internal/version.go               Version constant
internal/config/                  Config + env defaults + validation
internal/driver/                  Driver/Device/Env/Transform/Registry seams
internal/drivers/wire.go          RegisterAll() — single registration point
internal/shell/                   Runner interface (exec + fake) for shell-outs
internal/fs/                      FS interface (OS + in-memory Mem)
internal/clock/                   Clock interface (real + fixed)
internal/stats/                   concurrency-safe counters
internal/copier/                  generic recursive CopyTree + skip policies
internal/media/                   extension-class registry (raw/image/video/…)
internal/gvfs/                    gio wrapper (implements copier.Source + MountFS)
internal/gpx/                     Geotag Transform (exiftool + GPX)
internal/note/                    note→PDF Convert Transform (supernote-tool)
internal/fssync/                  Linux sync(2) flush
internal/fujifilm/                Fujifilm driver (compose copier + gpx.Geotag)
internal/supernote/               Supernote driver (compose copier + note.Convert)
internal/syncmaster/              orchestrator: dispatch, auto-detect, summary
docs/plan.md                      full design + implementation plan
```

### Adding a sync feature later

1. Write `internal/<thing>` implementing `driver.Driver` — usually by composing
   `copier.Copier` with existing/new `Transform`s and a `Source`/`MountFS`.
2. Add `driver.Register(<thing>.Driver{})` in `internal/drivers/wire.go`.
3. Add its config keys/env (typed struct in `internal/config`).
4. `auto` discovers and runs it; `syncmaster <thing>` runs it explicitly.

No orchestrator or other-driver changes required.

## Testing

Every package is unit-testable with zero external dependencies — no devices,
no `gio`/`exiftool`/`supernote-tool`, no `$HOME`. All side effects go through
injected interfaces (`shell.Runner`, `fs.FS`, `copier.Source`, `driver.Driver`,
`driver.Transform`, `clock.Clock`) with in-memory fakes.

    mage test       # go test -race -cover ./...

Coverage: most packages 80–100%.