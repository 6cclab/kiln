package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersonalMemoryGlobalRequiresExplicitCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ctx := context.Background()
	call := func(positional ...string) (int, string, string) {
		t.Helper()
		var out, stderr bytes.Buffer
		code := PersonalMemory(ctx, Args{Positional: positional}, &out, &stderr)
		return code, out.String(), stderr.String()
	}
	if code, _, stderr := call("global", "remember", "runner", "Use go test"); code != 0 {
		t.Fatalf("global remember: %s", stderr)
	}
	if code, output, stderr := call("global", "export"); code != 0 || !strings.Contains(output, "Use go test") {
		t.Fatalf("global export: %s %s", output, stderr)
	}
	if code, output, stderr := call("export"); code != 0 || strings.Contains(output, "Use go test") {
		t.Fatalf("project export leaked global: %s %s", output, stderr)
	}
	if code, _, _ := call("global", "remember", "runner", "text", "file.txt"); code != 2 {
		t.Fatal("global source file accepted")
	}
	if code, _, stderr := call("global", "forget", "runner"); code != 0 {
		t.Fatalf("global forget: %s", stderr)
	}
	if code, output, stderr := call("global", "export"); code != 0 || !strings.Contains(output, "[]") {
		t.Fatalf("global forget failed: %s %s", output, stderr)
	}
	if _, err := os.Stat(filepath.Join(home, ".kiln", "memory.db")); err != nil {
		t.Fatal(err)
	}
}
