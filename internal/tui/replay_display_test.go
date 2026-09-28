package tui

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDisplayArg: collapsed paths are relative to the working directory
// (or ~/ under home), verbose ones absolute, and non-path arguments and
// relative paths pass through untouched.
func TestDisplayArg(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	cwd := filepath.Join(home, "proj")
	cases := []struct {
		name, arg string
		verbose   bool
		want      string
	}{
		{"read", filepath.Join(cwd, "web/src/api.ts"), false, "web/src/api.ts"},
		{"read", filepath.Join(home, "other/x.go"), false, "~/other/x.go"},
		{"read", "/etc/hosts", false, "/etc/hosts"},
		{"read", "web/src/api.ts", false, "web/src/api.ts"},
		{"read", "web/src/api.ts", true, filepath.Join(cwd, "web/src/api.ts")},
		{"read", filepath.Join(cwd, "a.go"), true, filepath.Join(cwd, "a.go")},
		{"edit", filepath.Join(cwd, "README.md"), false, "README.md"},
		{"grep", "/api/tasks", false, "/api/tasks"},
		{"bash", filepath.Join(cwd, "run.sh"), false, filepath.Join(cwd, "run.sh")},
	}
	for _, c := range cases {
		if got := DisplayArg(c.name, c.arg, cwd, c.verbose); got != c.want {
			t.Errorf("DisplayArg(%q, %q, verbose=%v) = %q, want %q", c.name, c.arg, c.verbose, got, c.want)
		}
	}
}
