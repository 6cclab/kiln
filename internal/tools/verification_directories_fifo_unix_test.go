//go:build !windows

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestVerificationDirectoriesUnavailableFIFO(t *testing.T) {
	cwd := t.TempDir()
	if err := os.Mkdir(filepath.Join(cwd, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(cwd, "src", "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	allow := func(string) bool { return true }
	proof, result := directoryProof(t, cwd, allow, `{"command":"test","verificationDirectories":["src"]}`)
	if len(proof.Directories) != 0 || strings.Contains(string(result.Details), `"taskVerification":`) {
		t.Fatalf("unexpected proof: %s", result.Details)
	}
	if _, ok := VerificationAttemptFromResult(result.Details); !ok {
		t.Fatal("attempt absent")
	}
}
