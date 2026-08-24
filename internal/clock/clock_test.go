package clock

import (
	"testing"
	"time"
)

func TestRealNow(t *testing.T) {
	before := time.Now()
	got := Real{}.Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("Real.Now() = %v not in [%v,%v]", got, before, after)
	}
}

func TestFixedNow(t *testing.T) {
	want := time.Unix(1700000000, 0)
	f := Fixed{T: want}
	for i := 0; i < 3; i++ {
		if got := f.Now(); !got.Equal(want) {
			t.Fatalf("Fixed.Now() = %v, want %v", got, want)
		}
	}
}
