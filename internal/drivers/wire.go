// Package drivers wires the built-in drivers into the default driver
// registry. This is the single place to enable a new sync feature.
package drivers

import (
	"syncmaster/internal/driver"
	"syncmaster/internal/fujifilm"
	"syncmaster/internal/supernote"
)

// RegisterAll registers every built-in driver. Call once at startup.
func RegisterAll() {
	_ = driver.Register(&fujifilm.Driver{})
	_ = driver.Register(supernote.Driver{})
	// future: _ = driver.Register(gopro.Driver{})
}
