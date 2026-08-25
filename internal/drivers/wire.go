// Package drivers wires the built-in drivers into a driver registry. This is
// the single place to enable a new sync feature. main constructs the registry
// and passes it here so the orchestrator depends on an injected abstraction
// rather than the package-level default registry.
package drivers

import (
	"github.com/snonux/syncmaster/internal/driver"
	"github.com/snonux/syncmaster/internal/fujifilm"
	"github.com/snonux/syncmaster/internal/supernote"
)

// RegisterAll registers every built-in driver on r. Call once at startup with
// the registry wired into driver.Env.Drivers.
func RegisterAll(r *driver.Registry) {
	_ = r.Register(&fujifilm.Driver{})
	_ = r.Register(supernote.Driver{})
	// future: _ = r.Register(gopro.Driver{})
}
