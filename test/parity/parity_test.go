//go:build parity

// Package parity drives the TypeScript harness TUI and the Go harness TUI
// through the same scripted model run and diffs what lands on screen.
//
// The Go side is the real cmd/harness binary, driven through a real PTY by
// internal/testkit/screen (the same package cmd/harness-drive and
// test/e2e/tui_test.go use) and fed by the real Go faux HTTP server
// (internal/testkit/faux), matching the anthropic-messages wire shape.
//
// The TS side is the real `runApp` (src/tui/app.ts) driven in-process
// against @xterm/headless by test/parity/run-ts.mts, fed by pi-ai's own
// in-process fauxProvider(). See run-ts.mts's header comment for exactly how
// a faux YAML script is translated into that provider's response queue, and
// for the two known-divergent mechanisms (usage numbers, on_tool_result
// gating) that this test does NOT try to paper over.
//
// Both sides are driven by the exact same action script (test/parity/scripts/
// *.actions), a tiny line language (SEND/KEY/WAIT/SLEEP/SCREEN [--json]/
// RESIZE/EXIT) that cmd/harness-drive already speaks on stdin; this test
// interprets the same language directly against internal/testkit/screen for
// the Go side, and run-ts.mts interprets it in TS for the TS side.
package parity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/testkit/faux"
	"github.com/andrepato/harness/internal/testkit/screen"

	"github.com/aymanbagabas/go-udiff"
)

// repoRoot is this file's directory's grandparent (test/parity -> repo root).
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// test/parity -> repo root
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

type testCase struct {
	Name           string   `json:"name"`
	Faux           string   `json:"faux"`
	Actions        string   `json:"actions"`
	Cols           int      `json:"cols"`
	Rows           int      `json:"rows"`
	PermissionMode string   `json:"permissionMode"`
	AllowedTools   []string `json:"allowedTools"`
	Fixture        string   `json:"fixture"`
}

// screenDump matches cmd/harness-drive/main.go's cmdScreen JSON payload
// exactly, so both sides' SCREEN --json output decode into the same struct.
type screenDump struct {
	Rows           []string `json:"rows"`
	CursorRow      int      `json:"cursorRow"`
	OccupiedHeight int      `json:"occupiedHeight"`
	Cols           int      `json:"cols"`
	RowsCount      int      `json:"rowsCount"`
}

// testClock freezes the footer's "now" (and, on the Go side, the model's
// StartedAt) so the elapsed-time segment does not depend on wall-clock skew
// between the two sides. The TS side has no equivalent env var (grepped
// src/ and test/support/ - see the parity report), so its clock segment is
// normalized away below instead of frozen.
const testClock = "2026-09-23T12:00:00Z"

func loadCase(t *testing.T, path string) testCase {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read case %s: %v", path, err)
	}
	var tc testCase
	if err := json.Unmarshal(data, &tc); err != nil {
		t.Fatalf("parse case %s: %v", path, err)
	}
	return tc
}

// setupFixture writes whatever files a case's script expects to find on
// disk into dir. "mathjs" matches testdata/faux/fix-bug.yaml's edit target.
func setupFixture(t *testing.T, dir, fixture string) {
	t.Helper()
	switch fixture {
	case "", "none":
		return
	case "mathjs":
		if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
		mathJS := "function add(a, b) {\n  return a - b;\n}\n\nfunction mul(a, b) {\n  return a * b;\n}\n"
		if err := os.WriteFile(filepath.Join(dir, "src", "math.js"), []byte(mathJS), 0o644); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown fixture %q", fixture)
	}
}

func TestParity(t *testing.T) {
	root := repoRoot(t)
	harnessBin := filepath.Join(root, "bin", "harness")
	if _, err := os.Stat(harnessBin); err != nil {
		t.Fatalf("bin/harness missing (run `make build` first): %v", err)
	}

	caseFiles, err := filepath.Glob(filepath.Join(root, "test", "parity", "cases", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(caseFiles) == 0 {
		t.Fatal("no cases found under test/parity/cases")
	}

	for _, cf := range caseFiles {
		cf := cf
		tc := loadCase(t, cf)
		t.Run(tc.Name, func(t *testing.T) {
			runCase(t, root, harnessBin, tc)
		})
	}
}

func runCase(t *testing.T, root, harnessBin string, tc testCase) {
	actionsPath := filepath.Join(root, tc.Actions)
	fauxPath := filepath.Join(root, tc.Faux)

	goScreens := runGoSide(t, root, harnessBin, tc, fauxPath, actionsPath)
	tsScreens := runTSSide(t, root, tc, fauxPath, actionsPath)

	if len(goScreens) != len(tsScreens) {
		t.Fatalf("SCREEN step count differs: go=%d ts=%d (one side likely errored/timed out before reaching every SCREEN action - see the actions script %s and each side's stderr above)",
			len(goScreens), len(tsScreens), tc.Actions)
	}

	anyDiff := false
	for i := range goScreens {
		goText := normalize(strings.Join(goScreens[i].Rows, "\n"))
		tsText := normalize(strings.Join(tsScreens[i].Rows, "\n"))
		if goText == tsText {
			continue
		}
		anyDiff = true
		label := fmt.Sprintf("%s: screen step %d", tc.Name, i+1)
		diff := udiff.Unified("go/"+label, "ts/"+label, goText+"\n", tsText+"\n")
		t.Errorf("%s: screens differ\n%s", label, diff)
	}
	if anyDiff {
		t.Logf("go cursorRow/occupiedHeight last step: %d/%d ; ts: %d/%d",
			goScreens[len(goScreens)-1].CursorRow, goScreens[len(goScreens)-1].OccupiedHeight,
			tsScreens[len(tsScreens)-1].CursorRow, tsScreens[len(tsScreens)-1].OccupiedHeight)
	}
}

// normalize strips the diff noise this oracle has decided is not a real
// difference: elapsed seconds ("Worked for 3s" -> "Worked for Ns"), the wall
// clock in the footer ("11:39pm" -> "HH:MMxm"), and trailing whitespace.
// Token counts are normalised too, by a known mechanism difference rather
// than a rendering one: the Go faux server returns the usage the script
// states, while pi-ai's fauxProvider ignores scripted usage and
// re-estimates it from content length (providers/faux.js
// withUsageEstimate). The spinner's token count, the turn summary's and
// the footer's context meter all derive from it, so their digits are
// folded to N; the surrounding glyphs, separators and layout still diff.
var (
	elapsedRe = regexp.MustCompile(`Worked for \d+s`)
	clockRe   = regexp.MustCompile(`\d{1,2}:\d{2}(am|pm)`)
	tokensRe  = regexp.MustCompile(`[\d.]+k? tokens`)
	// The spinner's animation frame depends on how many ticks elapsed
	// before the dump; both sides use the same six glyphs.
	spinnerRe = regexp.MustCompile(`^[·✢✳✶✻✽] `)
	meterRe   = regexp.MustCompile(`[░▓█]+ [\d.]+k?/[\d.]+k \d+%`)
)

func normalize(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		l = elapsedRe.ReplaceAllString(l, "Worked for Ns")
		l = clockRe.ReplaceAllString(l, "HH:MMxm")
		l = tokensRe.ReplaceAllString(l, "N tokens")
		l = spinnerRe.ReplaceAllString(l, "S ")
		l = meterRe.ReplaceAllString(l, "METER N/Nk N%")
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}

// --- Go side ---------------------------------------------------------------

func runGoSide(t *testing.T, root, harnessBin string, tc testCase, fauxPath, actionsPath string) []screenDump {
	t.Helper()

	srv, err := faux.New(faux.Options{ScriptPath: fauxPath})
	if err != nil {
		t.Fatalf("faux.New: %v", err)
	}
	addr, err := srv.Start()
	if err != nil {
		t.Fatalf("faux.Start: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	cwd := t.TempDir()
	setupFixture(t, cwd, tc.Fixture)
	home := t.TempDir()
	sessDir := t.TempDir()

	args := []string{"--permission-mode", tc.PermissionMode}
	for _, tool := range tc.AllowedTools {
		args = append(args, "--allowed-tools", tool)
	}

	opts := []screen.Option{
		screen.WithEnv("HOME", home),
		screen.WithEnv("HARNESS_SESSIONS_DIR", sessDir),
		screen.WithEnv("HARNESS_MODEL", "faux/faux-1"),
		screen.WithEnv("HARNESS_TEST_CLOCK", testClock),
		screen.WithEnv("HARNESS_FAUX_ADDR", addr),
		screen.WithEnv("HARNESS_FAUX_API", "anthropic-messages"),
	}

	// screen.Start/StartDetached has no per-process cwd option (only
	// WithEnv/WithUnsetEnv/WithTimeout/WithRecord - see internal/testkit/
	// screen/screen.go's Option list); test/e2e/tui_test.go works around this
	// the same way, chdir'ing the *test process* around the fork+exec. This
	// package never runs cases in parallel, so the race that workaround
	// warns about does not apply here either.
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	s, startErr := screen.StartDetached(harnessBin, args, tc.Cols, tc.Rows, opts...)
	if chdirErr := os.Chdir(orig); chdirErr != nil {
		t.Fatal(chdirErr)
	}
	if startErr != nil {
		t.Fatalf("screen.StartDetached: %v", startErr)
	}
	t.Cleanup(func() { s.Close() })

	return runActionsGo(t, s, actionsPath)
}

func runActionsGo(t *testing.T, s *screen.Screen, actionsPath string) []screenDump {
	t.Helper()
	data, err := os.ReadFile(actionsPath)
	if err != nil {
		t.Fatal(err)
	}

	var dumps []screenDump
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		cmd := strings.ToUpper(fields[0])
		rest := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))

		switch cmd {
		case "SEND":
			s.Send(unescapeAction(rest))
		case "KEY":
			names := strings.Fields(rest)
			s.SendKey(names...)
		case "WAIT":
			timeout := screen.DefaultTimeout
			pattern := rest
			if wf := strings.Fields(rest); len(wf) > 1 {
				if d, perr := time.ParseDuration(wf[0]); perr == nil {
					timeout = d
					pattern = strings.TrimSpace(strings.TrimPrefix(rest, wf[0]))
				}
			}
			var pat any = pattern
			if strings.HasPrefix(pattern, "/") && strings.HasSuffix(pattern, "/") && len(pattern) >= 2 {
				re, rerr := regexp.Compile(pattern[1 : len(pattern)-1])
				if rerr != nil {
					t.Fatalf("bad WAIT regexp %q: %v", pattern, rerr)
				}
				pat = re
			}
			if err := s.WaitFor(pat, timeout); err != nil {
				t.Fatalf("go side WAIT %q: %v\n--- screen ---\n%s", pattern, err, strings.Join(s.Rows(), "\n"))
			}
		case "SLEEP":
			d, perr := time.ParseDuration(rest)
			if perr != nil {
				t.Fatalf("bad SLEEP %q: %v", rest, perr)
			}
			time.Sleep(d)
		case "SCREEN":
			dumps = append(dumps, screenDump{
				Rows:           s.Viewport(),
				CursorRow:      s.CursorRow(),
				OccupiedHeight: s.OccupiedHeight(),
			})
		case "RESIZE":
			parts := strings.Fields(rest)
			w, _ := strconv.Atoi(parts[0])
			h, _ := strconv.Atoi(parts[1])
			s.Resize(w, h)
		case "EXIT":
			return dumps
		default:
			t.Fatalf("unknown action %q", cmd)
		}
	}
	return dumps
}

func unescapeAction(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 'r':
				b.WriteByte('\r')
				i++
				continue
			case 'n':
				b.WriteByte('\n')
				i++
				continue
			case 't':
				b.WriteByte('\t')
				i++
				continue
			case 'e':
				b.WriteByte('\x1b')
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// --- TS side -----------------------------------------------------------

func runTSSide(t *testing.T, root string, tc testCase, fauxPath, actionsPath string) []screenDump {
	t.Helper()

	cwd := t.TempDir()
	setupFixture(t, cwd, tc.Fixture)
	home := t.TempDir()
	sessDir := t.TempDir()

	args := []string{
		"--experimental-strip-types", "--no-warnings",
		filepath.Join(root, "test", "parity", "run-ts.mts"),
		"--faux", fauxPath,
		"--actions", actionsPath,
		"--cols", strconv.Itoa(tc.Cols),
		"--rows", strconv.Itoa(tc.Rows),
		"--cwd", cwd,
		"--home", home,
		"--sessions-dir", sessDir,
		"--permission-mode", tc.PermissionMode,
	}
	if len(tc.AllowedTools) > 0 {
		args = append(args, "--allowed-tools", strings.Join(tc.AllowedTools, ","))
	}

	cmd := exec.Command("node", args...)
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	ctxErr := cmd.Run()

	var dumps []screenDump
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var d screenDump
		if err := json.Unmarshal([]byte(line), &d); err != nil {
			t.Fatalf("ts side: could not parse stdout line as SCREEN JSON: %v\nline: %s\nfull stdout:\n%s\nstderr:\n%s",
				err, line, stdout.String(), stderr.String())
		}
		dumps = append(dumps, d)
	}

	if ctxErr != nil {
		t.Fatalf("ts side run-ts.mts exited with error: %v\nstderr:\n%s\nstdout:\n%s", ctxErr, stderr.String(), stdout.String())
	}

	return dumps
}
