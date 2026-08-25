package stats

import (
	"sync"
	"testing"
)

func TestIncAndGet(t *testing.T) {
	s := New()
	s.Inc(Found, 1)
	s.Inc(Copied, 2)
	s.Inc(Failed, 3)
	if g := s.Get(Found); g != 1 {
		t.Fatalf("Found = %d, want 1", g)
	}
	if g := s.Get(Copied); g != 2 {
		t.Fatalf("Copied = %d, want 2", g)
	}
	if g := s.Get(Failed); g != 3 {
		t.Fatalf("Failed = %d, want 3", g)
	}
}

func TestIncNegative(t *testing.T) {
	s := New()
	s.Inc(Copied, 5)
	s.Inc(Copied, -2)
	if g := s.Get(Copied); g != 3 {
		t.Fatalf("Copied = %d, want 3", g)
	}
}

func TestIncOutOfBoundsAndNil(t *testing.T) {
	New().Inc(Kind(-1), 1)
	New().Inc(numKinds, 1)
	var nilStats *Counters
	nilStats.Inc(Found, 1) // must not panic
	if g := nilStats.Get(Found); g != 0 {
		t.Fatalf("nil Get = %d, want 0", g)
	}
}

func TestConcurrentIncrements(t *testing.T) {
	s := New()
	const goroutines = 16
	const per = 1000
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < per; j++ {
				s.Inc(Copied, 1)
			}
		}()
	}
	wg.Wait()
	if g := s.Get(Copied); g != goroutines*per {
		t.Fatalf("Copied = %d, want %d", g, goroutines*per)
	}
}

func TestSnapshotAndString(t *testing.T) {
	s := New()
	s.Inc(Found, 4)
	s.Inc(Converted, 7)
	snap := s.Snapshot()
	if snap[Found] != 4 || snap[Converted] != 7 {
		t.Fatalf("snapshot = %v", snap)
	}
	if s.String() != "found=4 copied=0 skipped=0 failed=0 converted=7 convert-skipped=0" {
		t.Fatalf("String = %q", s.String())
	}
}

func TestKindString(t *testing.T) {
	tests := []struct {
		k    Kind
		want string
	}{
		{Found, "found"}, {Copied, "copied"}, {Skipped, "skipped"},
		{Failed, "failed"}, {Converted, "converted"}, {ConvertSkipped, "convert-skipped"},
		{Kind(999), "kind(999)"}, {Kind(-1), "kind(-1)"},
	}
	for _, tc := range tests {
		if got := tc.k.String(); got != tc.want {
			t.Errorf("Kind(%d).String() = %q, want %q", tc.k, got, tc.want)
		}
	}
}
