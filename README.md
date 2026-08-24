# syncmaster

A minimal synchronization tool skeleton.

## Build

    mage            # build (default target)
    mage test       # run tests
    mage install    # build and install to GOPATH/bin

## Usage

    ./syncmaster -source /path/from -destination /path/to -verbose
    ./syncmaster -version

## Layout

```
cmd/syncmaster/main.go     thin entry point: flags + delegation
internal/version.go        single source of truth for the version
internal/syncmaster/        core synchronization logic + tests
Magefile.go                build/install/test targets
```