//go:build darwin || linux

package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A sandboxed `go build` in a fresh module works without the command
// setting GOCACHE: kiln points Go's caches at its own cache under the
// sandbox temp directory, since the real ones (~/Library/Caches/go-build,
// ~/go/pkg/mod) are not writable there. A GOCACHE the user set is kept,
// so a build with one outside the sandbox's reach fails, as it should.
func TestRealSandboxToolCaches(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go toolchain")
	}
	for _, k := range []string{"GOCACHE", "GOMODCACHE", "GOTMPDIR", "XDG_CACHE_HOME", "GOFLAGS"} {
		t.Setenv(k, "")
	}
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOENV", "off")
	r := newRealRig(t, Config{}, nil)
	// A home with no cache directories yet, as on a fresh machine: an
	// existing ~/Library/Caches/go-build lets go build run without
	// writing to it, which would hide what this tests.
	t.Setenv("HOME", r.home)
	os.WriteFile(filepath.Join(r.ws, "go.mod"), []byte("module example.com/m\n\ngo 1.19\n"), 0o644)
	os.WriteFile(filepath.Join(r.ws, "main.go"), []byte("package main\n\nfunc main() { println(\"hi\") }\n"), 0o644)

	cache := realPath(filepath.Join(r.scratch, "tmp", "cache"))
	out, code := r.run(`go env GOCACHE GOMODCACHE && go build -o built . && echo build-ok`)
	if code != 0 || !strings.Contains(out, "build-ok") {
		t.Fatalf("sandboxed go build: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, filepath.Join(cache, "go-build")) || !strings.Contains(out, filepath.Join(cache, "go-mod")) {
		t.Errorf("Go's caches are not kiln's: %s", out)
	}
	if out, _ := r.run(`printf '%s\n' "$npm_config_cache" "$PIP_CACHE_DIR" "$XDG_CACHE_HOME"`); !strings.Contains(out, filepath.Join(cache, "npm")) ||
		!strings.Contains(out, filepath.Join(cache, "pip")) || !strings.Contains(out, filepath.Join(cache, "xdg")) {
		t.Errorf("other tool caches: %s", out)
	}

	userCache := filepath.Join(r.outside, "gocache")
	t.Setenv("GOCACHE", userCache)
	out, code = r.run(`go env GOCACHE; go build -o built2 . && echo build-ok`)
	if !strings.Contains(out, userCache) {
		t.Errorf("a user-set GOCACHE was replaced: %s", out)
	}
	if code == 0 || strings.Contains(out, "build-ok") {
		t.Errorf("a build with GOCACHE outside the sandbox's reach succeeded: %s", out)
	}
}
