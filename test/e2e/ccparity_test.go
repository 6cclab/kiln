//go:build e2e

// Package e2e: ccparity_test.go holds the harness against the reference
// screens captured from the real `claude` binary under
// testdata/reference/claude-code/ (see docs/claude-code-reference.md and
// that directory's README). Every subtest here drives the real harness
// TUI through internal/testkit/screen the same way tui_test.go does,
// normalises both sides with normalizeScreen (ccparity_normalize.go), and
// prints a unified diff of the normalised rows on failure so a colour
// failure, a layout failure and a wording failure each read distinctly.
//
// This suite is allowed to be red: internal/tui is being rewritten by
// other agents while this suite is being built (S1 shell, S2 transcript,
// S3 dialogs). Its job is to make every remaining difference from Claude
// Code visible and precise, not to pass today.
package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aymanbagabas/go-udiff"

	"github.com/andrepato/harness/internal/testkit/screen"
)

// ccparityRefDir is testdata/reference/claude-code, resolved from this
// source file the same way repoTestdataDir is.
func ccparityRefDir() string {
	return filepath.Join(repoTestdataDir(), "reference", "claude-code")
}

// loadReferenceScreen reads testdata/reference/claude-code/<name>.txt and
// strips harness-drive's own trailing "-- cursor: ... --" summary line
// (screen.go/harness-drive's SCREEN dump convention), which is driver
// metadata, not screen content, and was never meant to be compared.
func loadReferenceScreen(t *testing.T, name string) []string {
	t.Helper()
	path := filepath.Join(ccparityRefDir(), name+".txt")
	data, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		t.Fatalf("ccparity: read reference %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "-- cursor:") {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// compareNormalized normalises both refRows and gotRows (see
// ccparity_normalize.go), and on a mismatch fails t with a unified diff
// of the normalised text plus a "columns differ" note when every
// differing line pair is equal except for leading whitespace — that
// distinguishes an indentation/column bug from a content bug at a
// glance, per this suite's brief.
func compareNormalized(t *testing.T, label string, refRows, gotRows []string, cwd string) {
	t.Helper()
	ref := normalizeScreen(refRows, cwd)
	got := normalizeScreen(gotRows, cwd)
	refText := strings.Join(ref, "\n") + "\n"
	gotText := strings.Join(got, "\n") + "\n"
	if refText == gotText {
		return
	}
	d := udiff.Unified("claude-code/"+label, "harness/"+label, refText, gotText)
	t.Errorf("ccparity: %s mismatch (normalised rows differ)\n%s%s", label, d, columnsOnlyNote(ref, got))
}

// columnsOnlyNote reports, for lines at the same index in both slices
// whose trimmed content matches but whose leading whitespace does not,
// that the difference is a column/indentation shift rather than a wording
// or element difference.
func columnsOnlyNote(ref, got []string) string {
	var notes []string
	n := len(ref)
	if len(got) < n {
		n = len(got)
	}
	for i := 0; i < n; i++ {
		if ref[i] == got[i] {
			continue
		}
		if strings.TrimLeft(ref[i], " ") == strings.TrimLeft(got[i], " ") {
			notes = append(notes, fmt.Sprintf("  row %d: columns differ only (leading whitespace): %q vs %q", i, ref[i], got[i]))
		}
	}
	if len(notes) == 0 {
		return ""
	}
	return "\ncolumns-only differences:\n" + strings.Join(notes, "\n") + "\n"
}

// rowsWithLeftColumnIn returns the subset of rows whose left column
// (everything before two-or-more spaces, i.e. the label a two-column
// legend row uses) matches some row in other, for scenarios (like
// shortcuts.txt) where the reference may list entries the harness hasn't
// implemented yet: comparing only rows whose left column exists on both
// sides avoids failing the whole scenario on a not-yet-built feature
// while still catching a wording/column difference on rows both sides do
// have. Returns the matched reference rows and the reference rows that
// had no match (the "omitted" report).
func rowsWithLeftColumnIn(ref, harness []string) (matched, omitted []string) {
	leftCol := func(s string) string {
		s = strings.TrimRight(s, " ")
		if i := strings.Index(s, "  "); i > 0 {
			return strings.TrimSpace(s[:i])
		}
		return strings.TrimSpace(s)
	}
	harnessLeft := map[string]bool{}
	for _, h := range harness {
		if l := leftCol(h); l != "" {
			harnessLeft[l] = true
		}
	}
	for _, r := range ref {
		l := leftCol(r)
		if l == "" {
			continue
		}
		if harnessLeft[l] {
			matched = append(matched, r)
		} else {
			omitted = append(omitted, r)
		}
	}
	return matched, omitted
}

// --- Scenario 1: startup -----------------------------------------------

func TestCCParity_Startup(t *testing.T) {
	proj := scratchProject(t)
	home, sessDir := scratchHome(t)
	addr, _ := startFaux(t, fixBugScript)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--strict-mcp-config", "--mcp-config", "/dev/null",
	)
	waitReady(t, s)

	ref := loadReferenceScreen(t, "startup-default-home")
	compareNormalized(t, "startup", ref, s.Rows(), proj)
}

// --- Scenario 2: "/mod" autocomplete popup ------------------------------

func TestCCParity_SlashAutocomplete(t *testing.T) {
	proj := scratchProject(t)
	home, sessDir := scratchHome(t)
	addr, _ := startFaux(t, fixBugScript)

	s := startTUI(t, 100, 40, proj, home, sessDir, addr,
		"--strict-mcp-config", "--mcp-config", "/dev/null",
	)
	waitReady(t, s)

	s.Send("/mod")
	if err := s.WaitFor("/mod", 2*time.Second); err != nil {
		t.Fatal(err)
	}

	ref := loadReferenceScreen(t, "autocomplete-slash")
	compareNormalized(t, "autocomplete-slash", ref, s.Rows(), proj)
}

// --- Scenario 3: "/model" dialog ----------------------------------------

func TestCCParity_ModelDialog(t *testing.T) {
	proj := scratchProject(t)
	home, sessDir := scratchHome(t)
	addr, _ := startFaux(t, fixBugScript)

	s := startTUI(t, 100, 40, proj, home, sessDir, addr,
		"--strict-mcp-config", "--mcp-config", "/dev/null",
	)
	waitReady(t, s)

	submitSlashCommand(s, "model")
	if err := s.WaitFor("model", 3*time.Second); err != nil {
		t.Fatal(err)
	}

	ref := loadReferenceScreen(t, "dialog-model")
	compareNormalized(t, "dialog-model", ref, s.Rows(), proj)
}

// --- Scenario 4: "@ma" file-path autocomplete ---------------------------

func TestCCParity_AtAutocomplete(t *testing.T) {
	proj := scratchProject(t)
	home, sessDir := scratchHome(t)
	addr, _ := startFaux(t, fixBugScript)

	s := startTUI(t, 100, 40, proj, home, sessDir, addr,
		"--strict-mcp-config", "--mcp-config", "/dev/null",
	)
	waitReady(t, s)

	s.Send("@ma")
	if err := s.WaitFor("@ma", 2*time.Second); err != nil {
		t.Fatal(err)
	}

	ref := loadReferenceScreen(t, "autocomplete-at")
	compareNormalized(t, "autocomplete-at", ref, s.Rows(), proj)
}

// --- Scenario 5: shift+tab mode cycle ------------------------------------

func TestCCParity_ModeCycle(t *testing.T) {
	proj := scratchProject(t)
	home, sessDir := scratchHome(t)
	addr, _ := startFaux(t, fixBugScript)

	s := startTUI(t, 100, 40, proj, home, sessDir, addr,
		"--strict-mcp-config", "--mcp-config", "/dev/null",
	)
	waitReady(t, s)

	ref := loadReferenceScreen(t, "mode-cycle")
	var got []string
	for i := 0; i < 3; i++ {
		s.SendKey("shift+tab")
		if err := waitQuiescent(s, 100*time.Millisecond, 2*time.Second); err != nil {
			t.Fatal(err)
		}
		rows := s.Rows()
		if len(rows) == 0 {
			t.Fatal("ccparity: no rows after shift+tab")
		}
		got = append(got, rows[len(rows)-1])
	}
	compareNormalized(t, "mode-cycle", ref, got, proj)
}

// --- Scenario 6: an edit turn in manual mode -----------------------------
// permission prompt vs permission-edit.txt, transcript vs turn-edit.txt,
// ctrl+o (verbose) vs verbose-ctrl-o.txt.

func TestCCParity_EditTurn(t *testing.T) {
	proj := scratchProject(t)
	home, sessDir := scratchHome(t)
	addr, _ := startFaux(t, ccparityEditScript)

	s := startTUI(t, 100, 40, proj, home, sessDir, addr,
		"--strict-mcp-config", "--mcp-config", "/dev/null",
		"--permission-mode", "manual",
	)
	waitReady(t, s)

	s.Send("Use the Edit tool to change math.js so add returns a + b. Do not explain.")
	s.SendKey("enter")

	if err := s.WaitFor("Do you want", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	t.Run("Permission", func(t *testing.T) {
		ref := loadReferenceScreen(t, "permission-edit")
		compareNormalized(t, "permission-edit", ref, s.Rows(), proj)
	})

	s.SendKey("enter")
	if err := s.WaitFor("done", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := waitQuiescent(s, 250*time.Millisecond, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	t.Run("Transcript", func(t *testing.T) {
		ref := loadReferenceScreen(t, "turn-edit")
		compareNormalized(t, "turn-edit", ref, s.Rows(), proj)
	})

	s.SendKey("ctrl+o")
	if err := waitQuiescent(s, 150*time.Millisecond, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	t.Run("VerboseCtrlO", func(t *testing.T) {
		ref := loadReferenceScreen(t, "verbose-ctrl-o")
		compareNormalized(t, "verbose-ctrl-o", ref, s.Rows(), proj)
	})
}

// ccparityEditScript mirrors fixBugScript's read-then-edit shape but ends
// with "Done." (no explanatory text), matching capture-manual.txt's
// prompt ("Use the Edit tool to change math.js so add returns a + b. Do
// not explain.") and turn-edit.txt's transcript ("⏺ Done.").
const ccparityEditScript = `model: faux-1
steps:
  - tool_call: {name: read, args: {path: math.js}, id: tc1}
  - on_tool_result: tc1
    then:
      - tool_call:
          name: edit
          args:
            path: math.js
            edits:
              - oldText: "return a - b;"
                newText: "return a + b;"
          id: tc2
  - on_tool_result: tc2
    then:
      - text: "Done."
        usage: {input: 812, output: 34}
`

// --- Scenario 7: a bash turn in bypassPermissions mode -------------------
// (the reference's "auto mode"; the harness has no --permission-mode auto,
// so this uses bypassPermissions, the closest analogue — see this test's
// report for that gap.)

func TestCCParity_BashTurn(t *testing.T) {
	proj := scratchProject(t)
	home, sessDir := scratchHome(t)
	addr, _ := startFaux(t, ccparityBashScript)

	s := startTUI(t, 100, 40, proj, home, sessDir, addr,
		"--strict-mcp-config", "--mcp-config", "/dev/null",
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("run this shell command and report its output: echo parity-check")
	s.SendKey("enter")
	if err := s.WaitFor("Output: parity-check", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := waitQuiescent(s, 250*time.Millisecond, 3*time.Second); err != nil {
		t.Fatal(err)
	}

	ref := loadReferenceScreen(t, "turn-bash-auto")
	compareNormalized(t, "turn-bash-auto", ref, s.Rows(), proj)
}

const ccparityBashScript = `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "echo parity-check"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "Output: parity-check"
        usage: {input: 100, output: 10}
`

// --- Scenario 8: plan mode -----------------------------------------------

func TestCCParity_PlanMode(t *testing.T) {
	proj := scratchProject(t)
	home, sessDir := scratchHome(t)
	addr, _ := startFaux(t, ccparityPlanScript)

	s := startTUI(t, 100, 40, proj, home, sessDir, addr,
		"--strict-mcp-config", "--mcp-config", "/dev/null",
		"--permission-mode", "plan",
	)
	waitReady(t, s)

	s.Send("Make a two-step plan to rename math.js to calc.js.")
	s.SendKey("enter")
	if err := s.WaitFor("proceed", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	t.Run("Approval", func(t *testing.T) {
		ref := loadReferenceScreen(t, "plan-approval")
		compareNormalized(t, "plan-approval", ref, s.Rows(), proj)
	})

	s.SendKey("3")
	if err := waitQuiescent(s, 250*time.Millisecond, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	t.Run("KeepPlanning", func(t *testing.T) {
		ref := loadReferenceScreen(t, "plan-keep-planning")
		compareNormalized(t, "plan-keep-planning", ref, s.Rows(), proj)
	})
}

const ccparityPlanScript = `model: faux-1
steps:
  - text: "Here is my plan."
  - tool_call: {name: exit_plan_mode, args: {plan: "1. Rename math.js to calc.js\n2. Verify no dangling references"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "Plan handled."
        usage: {input: 200, output: 20}
`

// --- Scenario 9: "?" shortcuts on empty input ----------------------------

func TestCCParity_Shortcuts(t *testing.T) {
	proj := scratchProject(t)
	home, sessDir := scratchHome(t)
	addr, _ := startFaux(t, fixBugScript)

	s := startTUI(t, 100, 40, proj, home, sessDir, addr,
		"--strict-mcp-config", "--mcp-config", "/dev/null",
	)
	waitReady(t, s)

	s.Send("?")
	if err := waitQuiescent(s, 150*time.Millisecond, 2*time.Second); err != nil {
		t.Fatal(err)
	}

	refFull := loadReferenceScreen(t, "shortcuts")
	got := s.Rows()
	matched, omitted := rowsWithLeftColumnIn(refFull, got)
	compareNormalized(t, "shortcuts (rows present on both sides)", matched, got, proj)
	if len(omitted) > 0 {
		t.Logf("ccparity: shortcuts.txt entries the harness has no matching row for (not failed, reported): %s",
			strings.Join(omitted, " | "))
	}
}

// --- Scenario 10: esc esc rewind dialog -----------------------------------

func TestCCParity_RewindDialog(t *testing.T) {
	proj := scratchProject(t)
	home, sessDir := scratchHome(t)
	addr, _ := startFaux(t, ccparityBashScript)

	s := startTUI(t, 100, 40, proj, home, sessDir, addr,
		"--strict-mcp-config", "--mcp-config", "/dev/null",
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("run this shell command and report its output: echo parity-check")
	s.SendKey("enter")
	if err := s.WaitFor("Output: parity-check", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := waitQuiescent(s, 250*time.Millisecond, 3*time.Second); err != nil {
		t.Fatal(err)
	}

	s.SendKey("esc")
	s.SendKey("esc")
	if err := waitQuiescent(s, 150*time.Millisecond, 2*time.Second); err != nil {
		t.Fatal(err)
	}

	ref := loadReferenceScreen(t, "dialog-rewind")
	compareNormalized(t, "dialog-rewind", ref, s.Rows(), proj)
}

// --- Scenario 11 (styles): startup, dialog-model, turn-edit --------------
// Separate subtests per screen so a colour failure reads apart from a
// layout failure (this suite's brief, item 5).

func TestCCParity_Styles(t *testing.T) {
	t.Run("Startup", func(t *testing.T) {
		proj := scratchProject(t)
		home, sessDir := scratchHome(t)
		addr, _ := startFaux(t, fixBugScript)
		s := startTUI(t, 100, 40, proj, home, sessDir, addr,
			"--strict-mcp-config", "--mcp-config", "/dev/null",
		)
		waitReady(t, s)
		compareStyles(t, "startup", s)
	})

	t.Run("DialogModel", func(t *testing.T) {
		proj := scratchProject(t)
		home, sessDir := scratchHome(t)
		addr, _ := startFaux(t, fixBugScript)
		s := startTUI(t, 100, 40, proj, home, sessDir, addr,
			"--strict-mcp-config", "--mcp-config", "/dev/null",
		)
		waitReady(t, s)
		submitSlashCommand(s, "model")
		if err := s.WaitFor("model", 3*time.Second); err != nil {
			t.Fatal(err)
		}
		compareStyles(t, "dialog-model", s)
	})

	t.Run("TurnEdit", func(t *testing.T) {
		proj := scratchProject(t)
		home, sessDir := scratchHome(t)
		addr, _ := startFaux(t, ccparityEditScript)
		s := startTUI(t, 100, 40, proj, home, sessDir, addr,
			"--strict-mcp-config", "--mcp-config", "/dev/null",
			"--permission-mode", "bypassPermissions",
		)
		waitReady(t, s)
		s.Send("Use the Edit tool to change math.js so add returns a + b. Do not explain.")
		s.SendKey("enter")
		if err := s.WaitFor("done", 10*time.Second); err != nil {
			t.Fatal(err)
		}
		if err := waitQuiescent(s, 250*time.Millisecond, 3*time.Second); err != nil {
			t.Fatal(err)
		}
		compareStyles(t, "turn-edit", s)
	})
}

// compareStyles diffs the harness's own styled encoding
// (screen.EncodeStyledScreen, the same encoding SCREEN --styles and
// vtreplay use) against testdata/reference/claude-code/<name>.styles.txt.
func compareStyles(t *testing.T, name string, s *screen.Screen) {
	t.Helper()
	path := filepath.Join(ccparityRefDir(), name+".styles.txt")
	wantBytes, err := os.ReadFile(path) //nolint:gosec
	if err != nil {
		t.Fatalf("ccparity: read reference styles %s: %v", path, err)
	}
	want := string(wantBytes)
	got := screen.EncodeStyledScreen(s.Viewport(), s.Styles())
	if strings.TrimRight(want, "\n") == got {
		return
	}
	d := udiff.Unified("claude-code/"+name+".styles", "harness/"+name+".styles", want, got+"\n")
	t.Errorf("ccparity: %s styles mismatch\n%s", name, d)
}
