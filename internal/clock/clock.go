// Package clock provides a seam over time.Now so logic depending on "now"
// (staleness checks, signatures) is deterministic in tests.
package clock

import "time"

// Clock reports the current time.
type Clock interface {
	Now() time.Time
}

// Real is the production clock backed by time.Now.
type Real struct{}

var _ Clock = (*Real)(nil)

// Now returns the wall-clock time.
func (Real) Now() time.Time { return time.Now() }

// Fixed returns a constant time. Tests construct it with the desired value.
type Fixed struct{ T time.Time }

var _ Clock = (*Fixed)(nil)

// Now returns the configured time.
func (f Fixed) Now() time.Time { return f.T }
