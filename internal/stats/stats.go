// Package stats holds concurrency-safe counters shared across drivers and
// transforms during a single sync run.
package stats

import (
	"fmt"
	"sync/atomic"
)

// Kind identifies a counter.
type Kind int

// Counter kinds. The order is stable and part of the public API.
const (
	Found Kind = iota
	Copied
	Skipped
	Failed
	Converted
	ConvertSkipped
	numKinds
)

var kindNames = [...]string{
	Found:          "found",
	Copied:         "copied",
	Skipped:        "skipped",
	Failed:         "failed",
	Converted:      "converted",
	ConvertSkipped: "convert-skipped",
}

// String returns the human-readable name of a kind.
func (k Kind) String() string {
	if k < 0 || int(k) >= len(kindNames) {
		return fmt.Sprintf("kind(%d)", int(k))
	}
	return kindNames[k]
}

// Counters is a set of atomic counters. The zero value is not usable because
// atomic.Int64 must not be copied; construct with New.
type Counters struct {
	counts [numKinds]atomic.Int64
}

// New returns an empty Counters.
func New() *Counters { return &Counters{} }

// Inc adds n to the counter for k. A nil receiver is a no-op.
func (s *Counters) Inc(k Kind, n int64) {
	if s == nil || k < 0 || int(k) >= int(numKinds) {
		return
	}
	s.counts[k].Add(n)
}

// Get loads the current value of the counter for k.
func (s *Counters) Get(k Kind) int64 {
	if s == nil || k < 0 || int(k) >= int(numKinds) {
		return 0
	}
	return s.counts[k].Load()
}

// Snapshot returns a copy of all counters.
func (s *Counters) Snapshot() map[Kind]int64 {
	out := make(map[Kind]int64, numKinds)
	for k := Kind(0); k < numKinds; k++ {
		out[k] = s.Get(k)
	}
	return out
}

// String returns the one-line summary used by the orchestrator.
func (s *Counters) String() string {
	return fmt.Sprintf(
		"found=%d copied=%d skipped=%d failed=%d converted=%d convert-skipped=%d",
		s.Get(Found), s.Get(Copied), s.Get(Skipped),
		s.Get(Failed), s.Get(Converted), s.Get(ConvertSkipped),
	)
}
