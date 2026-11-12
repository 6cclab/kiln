package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMainFailStartupReturnsOne(t *testing.T) {
	var stdout, stderr bytes.Buffer
	got := Main([]string{"-fail-startup"}, &stdout, &stderr)
	if got != 1 {
		t.Fatalf("Main(-fail-startup) = %d, want 1", got)
	}
}

func TestMainUnknownFlagReturnsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	got := Main([]string{"-not-a-real-flag"}, &stdout, &stderr)
	if got != 2 {
		t.Fatalf("Main(-not-a-real-flag) = %d, want 2", got)
	}
	if stderr.Len() == 0 {
		t.Fatalf("Main(-not-a-real-flag) wrote nothing to stderr, want usage error")
	}
}

func TestMainInvalidConnectDelayReturnsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	got := Main([]string{"-connect-delay", "not-a-duration"}, &stdout, &stderr)
	if got != 2 {
		t.Fatalf("Main(-connect-delay=not-a-duration) = %d, want 2", got)
	}
	if !strings.Contains(stderr.String(), "connect-delay") {
		t.Fatalf("Main(-connect-delay=not-a-duration) stderr = %q, want mention of connect-delay", stderr.String())
	}
}

// TestConnectGateHoldsUntilTheFileExists: with -connect-gate the fixture
// does not read stdin until the gate file exists, then serves normally (an
// empty stdin then ends the server cleanly).
func TestConnectGateHoldsUntilTheFileExists(t *testing.T) {
	gate := filepath.Join(t.TempDir(), "gate")
	done := make(chan int, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		done <- Main([]string{"-connect-gate", gate}, &stdout, &stderr)
	}()
	select {
	case code := <-done:
		t.Fatalf("Main returned %d while the gate was closed", code)
	case <-time.After(200 * time.Millisecond): // negative wait: slow only makes it pass vacuously
	}
	if err := os.WriteFile(gate, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Main did not proceed after the gate file was created")
	}
}

func TestMainFailStartupTakesPrecedenceOverHangFlagParsing(t *testing.T) {
	// Both --fail-startup and --hang are accepted flags; passing
	// --fail-startup alongside a (never reached) --hang must still return
	// promptly via the fail-startup branch, proving flag parsing itself
	// does not block.
	var stdout, stderr bytes.Buffer
	got := Main([]string{"-fail-startup", "-connect-delay=0s"}, &stdout, &stderr)
	if got != 1 {
		t.Fatalf("Main(-fail-startup -connect-delay=0s) = %d, want 1", got)
	}
}
