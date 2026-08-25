// Command syncmaster is the entry point. It is a one-line wrapper around
// syncmaster.Main so all flag parsing, wiring, and exit-code mapping live in
// the internal package and are unit-testable.
package main

import (
	"os"

	"github.com/snonux/syncmaster/internal/syncmaster"
)

func main() {
	os.Exit(syncmaster.Main(os.Args[1:], os.Stdout, os.Stderr))
}
