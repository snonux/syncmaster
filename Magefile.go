//go:build mage

// Magefile for syncmaster. Build, install, and test targets follow the same
// style as other Go projects in this workspace.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
)

const binaryName = "syncmaster"

// Default runs Build when no target is given.
func Default() {
	mg.Deps(Build)
}

// Build compiles the syncmaster binary into the project root.
func Build() error {
	return sh.RunV("go", "build", "-o", binaryName, "./cmd/"+binaryName)
}

// Test runs the full test suite with the race detector and coverage.
func Test() error {
	return sh.RunV("go", "test", "-race", "-cover", "./...")
}

// Coverage fails when total coverage drops below the threshold.
func Coverage() error {
	return sh.RunV("go", "test", "-race", "-coverprofile=/tmp/syncmaster.cover", "./...")
}

// Lint runs go vet, gofmt, errcheck, and golangci-lint when available.
func Lint() error {
	if err := sh.RunV("go", "vet", "./..."); err != nil {
		return err
	}
	if out, err := sh.Output("gofmt", "-l", "."); err != nil {
		return err
	} else if out != "" {
		return fmt.Errorf("gofmt needs to format:\n%s", out)
	}
	if err := sh.RunV("errcheck", "./..."); err != nil {
		return err
	}
	if _, err := exec.LookPath("golangci-lint"); err == nil {
		if err := sh.RunV("golangci-lint", "run"); err != nil {
			return err
		}
	}
	return nil
}

// Install builds and copies the binary into GOPATH/bin.
func Install() error {
	mg.Deps(Build)

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving home directory: %w", err)
	}
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		gopath = filepath.Join(home, "go")
	}
	binDir := filepath.Join(gopath, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", binDir, err)
	}
	return sh.RunV("cp", "-v", binaryName, filepath.Join(binDir, binaryName))
}

// Uninstall removes the installed binary from GOPATH/bin.
func Uninstall() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving home directory: %w", err)
	}
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		gopath = filepath.Join(home, "go")
	}
	return sh.RunV("rm", "-f", filepath.Join(gopath, "bin", binaryName))
}
