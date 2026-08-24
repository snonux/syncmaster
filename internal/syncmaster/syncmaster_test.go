package syncmaster

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNew_InvalidConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name:    "missing source",
			cfg:     Config{Destination: "/dst"},
			wantErr: "source must be set",
		},
		{
			name:    "missing destination",
			cfg:     Config{Source: "/src"},
			wantErr: "destination must be set",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.cfg, &bytes.Buffer{})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestRun_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s, err := New(Config{Source: "/src", Destination: "/dst"}, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Run(ctx); err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled in error chain, got %v", err)
	}
}

func TestRun_VerboseOutput(t *testing.T) {
	var out bytes.Buffer
	s, err := New(Config{Source: "/src", Destination: "/dst", Verbose: true}, &out)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, want := out.String(), "syncing /src -> /dst\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
