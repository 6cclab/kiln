package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// Render snapshot harness (Phase 0 of the kiln UI pass).
//
// assertRenderGolden compares a renderer's output lines against two files
// under internal/tui/testdata/render/<name>{,.styles}.txt:
//
//   - <name>.txt: the lines with ANSI stripped (github.com/charmbracelet/x/ansi
//     Strip), for a human-legible diff of the text/layout.
//   - <name>.styles.txt: the same lines with ANSI kept, for a diff of the
//     actual colours/attributes applied.
//
// Both files are one line per element of the []string passed in, joined
// with "\n" and a trailing newline. Run with UPDATE=1 to (re)generate both
// files from the current render; without it, a mismatch fails with a diff
// against each file and a hint to rerun with UPDATE=1. This is the fast,
// in-process layer — it does not drive a PTY, so layout bugs that only
// show up in a real terminal (wrapping, resize) are NOT covered here; the
// PTY layer (test/e2e) stays the source of truth for those. Force the
// renderer's environment first with withRenderEnv so results are
// deterministic across machines (colour forced on, plain mode off, a
// fixed width).
func assertRenderGolden(t *testing.T, name string, lines []string) {
	t.Helper()

	plainPath := renderGoldenPath(name + ".txt")
	stylesPath := renderGoldenPath(name + ".styles.txt")

	gotStyles := strings.Join(lines, "\n") + "\n"
	plainLines := make([]string, len(lines))
	for i, l := range lines {
		plainLines[i] = ansi.Strip(l)
	}
	gotPlain := strings.Join(plainLines, "\n") + "\n"

	if os.Getenv("UPDATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(plainPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(plainPath, []byte(gotPlain), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(stylesPath, []byte(gotStyles), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("UPDATE=1: wrote %s and %s", plainPath, stylesPath)
		return
	}

	wantPlain, err := os.ReadFile(plainPath)
	if err != nil {
		t.Fatalf("read golden %s: %v (run `UPDATE=1 go test ./internal/tui -run %s` to create it)", plainPath, err, t.Name())
	}
	wantStyles, err := os.ReadFile(stylesPath)
	if err != nil {
		t.Fatalf("read golden %s: %v (run `UPDATE=1 go test ./internal/tui -run %s` to create it)", stylesPath, err, t.Name())
	}

	if string(wantPlain) != gotPlain {
		t.Errorf("plain output does not match golden %s\n--- want ---\n%s\n--- got ---\n%s\n(run `UPDATE=1 go test ./internal/tui -run %s` to update)",
			plainPath, wantPlain, gotPlain, t.Name())
	}
	if string(wantStyles) != gotStyles {
		t.Errorf("styled output does not match golden %s\n--- want ---\n%s\n--- got ---\n%s\n(run `UPDATE=1 go test ./internal/tui -run %s` to update)",
			stylesPath, wantStyles, gotStyles, t.Name())
	}
}

// renderGoldenPath resolves a path under internal/tui/testdata/render,
// relative to this source file rather than the process cwd (go test's cwd
// is the package directory, which happens to already be internal/tui, but
// resolving from runtime.Caller keeps this robust to how the test binary
// is invoked).
func renderGoldenPath(name string) string {
	return filepath.Join("testdata", "render", name)
}

// withRenderEnv forces the package's rendering globals to a known state
// for a golden test — colour on, plain mode off, a fixed render width —
// and restores the previous values with t.Cleanup. Plain mode is set
// first because SetPlainMode(false) recomputes colour from the real
// terminal state (colorEnabled()), which would clobber a color-forced
// setting if applied afterward.
func withRenderEnv(t *testing.T, width int) {
	t.Helper()
	prevEnabled := enabled
	prevPlain := plain
	prevWidth := renderWidth
	prevMargin := renderMargin

	SetPlainMode(false)
	SetColorEnabled(true)
	SetRenderWidth(width)
	// Golden tests assert exact-column strings against a fixed width, with
	// no margin baked into their expectations — reset renderMargin (which
	// Bridge.Commit reads centrally, bridge.go's own doc comment) to 0
	// rather than leaving it at whatever an earlier test's WindowSizeMsg
	// last left it, the same package-level-state hazard renderWidth itself
	// already had before this helper existed.
	renderMargin = 0

	t.Cleanup(func() {
		renderWidth = prevWidth
		renderMargin = prevMargin
		SetPlainMode(prevPlain)
		SetColorEnabled(prevEnabled)
	})
}

func TestRenderGolden_LabelRule(t *testing.T) {
	withRenderEnv(t, 80)

	lines := []string{
		labelRule("edit", KilnBlue, "math.js", 60),
		labelRule("you", KilnAmber, "", 60),
	}
	assertRenderGolden(t, "label-rule", lines)
}
