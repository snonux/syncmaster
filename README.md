# syncmaster

Import files from supported USB devices mounted through GVFS — Fujifilm
cameras, RICOH GR cameras, and Supernote Nomad — with geotagging and
`.note`→PDF conversion.

A Go port of the original `~/scripts/usbimport` bash script, built on a
pluggable driver architecture so new sync sources can be added by writing one
package and registering it in a single place.

**Status**: v0.5.1 — dry run by default; pass `--run` (or `SYNCMASTER_RUN=1`) to execute.

## Prerequisites

- Go 1.26+
- `gio` (gvfs package) — for discovering mounted USB devices
- `rsync` — for backing up Supernote files
- `exiftool` — for geotagging images (optional, skipped if missing)
- `supernote-tool` — for `.note`→PDF conversion (only for Supernote sync)

## Build

    mage            # build (default target)
    mage test       # run tests with -race -cover
    mage lint       # go vet + gofmt + errcheck + golangci-lint
    mage install    # build and install to GOPATH/bin

    go install .    # install to GOBIN

## Usage

By default syncmaster is a **dry run**: it prints the plan and changes nothing.
Pass `--run` (or set `SYNCMASTER_RUN=1`) to actually perform the sync. Always use
`--run` / `SYNCMASTER_RUN=1` in automation; without it, scripted invocations are
plan-only.

    syncmaster [auto|<driver>|selftest|help] [destination]   # driver modes come from the registry
    syncmaster -version
    syncmaster --run                      # actually perform the sync
    syncmaster -verbose                   # print verbose progress
    syncmaster --allow-missing-gps        # allow import when no GPS match (still dry-run unless --run)
    syncmaster --device fujifilm          # pick a device when multiple connected

### Examples

    syncmaster                        # dry-run plan for auto-detected devices
    syncmaster --run                  # auto-detect and import connected devices
    syncmaster --run fujifilm ~/Photos # import Fujifilm photos to ~/Photos
    syncmaster --run ricoh ~/Photos    # import RICOH GR photos to ~/Photos
    syncmaster --run supernote        # back up Supernote data
    syncmaster --run --allow-missing-gps  # execute even when geotag finds no GPS match
    SYNCMASTER_RUN=1 syncmaster auto  # same as --run, for env-based automation

### Exit codes

| Code | Meaning                                    |
|------|--------------------------------------------|
| 0    | Success                                    |
| 1    | Runtime error (missing tool, I/O, etc.)    |
| 2    | Usage error (bad flag, unknown mode)       |

### Environment variables

| Variable              | Description                    | Default                          |
|-----------------------|--------------------------------|----------------------------------|
| `SYNCMASTER_RUN`      | Non-empty except `0`/`false` = execute; unset/`0`/`false` = dry run | unset (dry run) |
| `FUJIFILM_DEST`       | JPEG/video destination         | `~/Pictures/Fujifilm.Inbox`      |
| `FUJIFILM_RAW_DEST`   | RAW file destination           | `~/Pictures/Fujifilm.RAW`        |
| `RICOH_DEST`          | JPEG/video destination         | `~/Pictures/Ricoh.Inbox`         |
| `RICOH_RAW_DEST`      | RAW file destination           | `~/Pictures/Ricoh.RAW`           |
| `GPX_DIR`             | GPX tracks for geotagging      | `~/Documents/GPX`                |
| `SUPERNOTE_DEST`      | Supernote import destination   | `~/Documents/Inbox/Supernote`    |
| `CONVERT_PARALLELISM` | Parallel note conversions      | `3`                              |
| `GVFS_ROOT`           | GVFS mount root                | `/run/user/<uid>/gvfs`           |

Supernote sync uses rsync to back up the device's `Note` and `Document` folders
without deleting local backup files. Nothing is uploaded to the Supernote. A
normal dry run only reports the backup changes.

Camera sync (Fujifilm, RICOH) imports media **flat** into the destination
roots — the camera's `DCIM/xxx` sub-dirs are not mirrored, so files land
directly as `~/Pictures/Fujifilm.Inbox/DSC_xxxx.jpg` (RAW to
`~/Pictures/Fujifilm.RAW/`). Imported images are geotagged from the GPX
tracks in `GPX_DIR` when a GPS match exists. Camera basenames are assumed
unique (cameras number files globally across card sub-dirs); on a collision
a same-size file is silently skipped and a different-size one silently
overwrites the existing import. The first run
after switching to flat import re-imports previously nested files into the
flat roots; the old `DCIM/xxx` trees are left in place for manual cleanup.

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
internal/ricoh/                   RICOH GR driver (compose copier + gpx.Geotag)
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
