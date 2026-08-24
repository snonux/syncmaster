//go:build mage

// Magefile for syncmaster. Build, install, and test targets follow the same
// style as other Go projects in this workspace.
package main

import (
	"fmt"
	"os"
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

// Test runs the full test suite.
func Test() error {
	return sh.RunV("go", "test", "./...")
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
