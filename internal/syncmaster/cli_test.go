package syncmaster

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		errs []error
		want int
	}{
		{"no errors", nil, 0},
		{"all nil", []error{nil, nil}, 0},
		{"usage", []error{ErrUsage}, 2},
		{"no device", []error{ErrNoDevice}, 2},
		{"multiple devices", []error{ErrMultipleDevices}, 2},
		{"usage after nil", []error{nil, ErrUsage}, 2},
		{"generic", []error{errors.New("boom")}, 1},
		{"first non-nil wins", []error{ErrUsage, errors.New("boom")}, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExitCode(tc.errs...); got != tc.want {
				t.Fatalf("ExitCode = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestMainVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if got := Main([]string{"-version"}, &out, &errOut); got != 0 {
		t.Fatalf("exit = %d, want 0", got)
	}
	if strings.TrimSpace(out.String()) == "" {
		t.Fatal("expected version on stdout")
	}
	if errOut.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", errOut.String())
	}
}

func TestMainHelpPrintsRegisteredDrivers(t *testing.T) {
	var out, errOut bytes.Buffer
	if got := Main([]string{"help"}, &out, &errOut); got != 0 {
		t.Fatalf("exit = %d, want 0", got)
	}
	got := out.String()
	for _, want := range []string{"fujifilm", "supernote", "--io-timeout"} {
		if !strings.Contains(got, want) {
			t.Fatalf("help text missing %q:\n%s", want, got)
		}
	}
}

func TestMainUnknownFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	if got := Main([]string{"--no-such-flag"}, &out, &errOut); got != 2 {
		t.Fatalf("exit = %d, want 2", got)
	}
}

func TestMainBadMode(t *testing.T) {
	// A typo mode passes config.Validate (deferred to the registry) but
	// App.Run returns ErrUsage → exit 2. Run from a temp HOME so HomeAndUID
	// and env wiring succeed without a real device.
	t.Setenv("HOME", t.TempDir())
	var out, errOut bytes.Buffer
	got := Main([]string{"supernot"}, &out, &errOut)
	if got != 2 {
		t.Fatalf("exit = %d, want 2", got)
	}
}
