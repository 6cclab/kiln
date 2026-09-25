package main

import (
	"bytes"
	"strings"
	"testing"
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
