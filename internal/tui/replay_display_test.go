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
		{"bash", filepath.Join(cwd, "run.sh"), false, "./run.sh"},
	}
	for _, c := range cases {
		if got := DisplayArg(c.name, c.arg, cwd, c.verbose); got != c.want {
			t.Errorf("DisplayArg(%q, %q, verbose=%v) = %q, want %q", c.name, c.arg, c.verbose, got, c.want)
		}
	}
}

// TestDisplayCommand: a leading cd into the working directory is dropped
// and one below it made relative; anything else is shown as run.
func TestDisplayCommand(t *testing.T) {
	cwd := "/work/proj"
	cases := map[string]string{
		"cd /work/proj && ls -la":              "ls -la",
		"cd '/work/proj' && ls":                "ls",
		"cd /work/proj/web && npm install":     "cd web && npm install",
		"cd /work/proj/api; go test ./...":     "cd api; go test ./...",
		"cd /elsewhere && ls":                  "cd /elsewhere && ls",
		"cd api && go vet ./...":               "cd api && go vet ./...",
		"go test ./...":                        "go test ./...",
		"cd /work/proj":                        "cd .",
		"ls -A /work/proj":                     "ls -A .",
		"cat /work/proj/web/src/api.ts | head": "cat web/src/api.ts | head",
		"find /work/proj/ -name x":             "find . -name x",
		"ls /work/proj2 /work/proj":            "ls /work/proj2 .",
		"/work/proj/run.sh && echo ok":         "./run.sh && echo ok",
		"go build -o /work/proj/bin/t .":       "go build -o bin/t .",
		"cd /work/project2 && ls":              "cd /work/project2 && ls",
	}
	for in, want := range cases {
		if got := DisplayArg("bash", in, cwd, false); got != want {
			t.Errorf("collapsed %q = %q, want %q", in, got, want)
		}
		if got := DisplayArg("bash", in, cwd, true); got != in {
			t.Errorf("verbose %q = %q, want it unchanged", in, got)
		}
	}
}
