//go:build e2e

package e2e

// Permission wiring, driven through the real cmd/harness binary. The
// decision *table* itself (Decide's deny > bypass > allow > ask > mode
// order, MatchesRule) is unit-tested directly in
// internal/claude/settings and internal/claude/permission; these tests
// only prove the CLI wires settings.json, --permission-mode, --add-dir
// and the TUI's "allow always" prompt into that table correctly.
//
// Ground truth read for this file: internal/claude/settings/settings.go's
// Decide (deny -> bypassPermissions -> allow -> ask -> mode fallback;
// ModePlan denies anything not in settings.ReadOnly; ModeManual asks for
// anything not in settings.ReadOnly) and internal/claude/permission/
// permission.go's Gate.Check (an outside-workspace path always asks,
// even when the rule verdict is Allow, unless mode is bypassPermissions;
// with no prompter available — every print-mode run — an "ask" verdict
// or an outside-workspace question is refused, not silently allowed).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// permEditThenBashScript has the model edit src/math.js (a real,
// pre-existing oldText so the edit succeeds whenever the gate allows it)
// and then run a bash command that writes a file (harmless, in the scratch
// project), each gated by the permission table under test. It must write:
// plan mode allows read-only bash (settings.IsReadOnlyCommand), so a
// read-only command would not test plan mode's refusal.
const permEditThenBashScript = `model: faux-1
steps:
  - tool_call: {name: edit, args: {path: src/math.js, edits: [{oldText: "return a - b;", newText: "return a + b;"}]}, id: e1}
  - on_tool_result: e1
    then:
      - tool_call: {name: bash, args: {command: "echo hi > bash-ran.txt"}, id: b1}
  - on_tool_result: b1
    then:
      - text: "done"
`

// permWriteRules writes a scratch .claude/settings.json with the given
// permission rule lists and defaultMode, matching hooks_test.go's own
// write-settings helpers but scoped to this file (permission_behaviour_
// prefix per this suite's file-ownership rule) and to permissions rather
// than hooks.
func permWriteRules(t *testing.T, proj string, allow, deny, ask []string) {
	t.Helper()
	settings := map[string]any{
		"permissions": map[string]any{
			"allow": allow,
			"deny":  deny,
			"ask":   ask,
		},
	}
	permWriteJSON(t, proj, settings)
}

func permWriteJSON(t *testing.T, proj string, settings map[string]any) {
	t.Helper()
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(proj, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// permRunResult runs permEditThenBashScript under mode and returns the
// parsed --output-format json result.
func permRunResult(t *testing.T, proj, home, sessDir, addr, mode string) (code int, res struct {
	OK      bool     `json:"ok"`
	Blocked []string `json:"blocked"`
}) {
	t.Helper()
	args := []string{"-p", "fix the bug and run a command", "--output-format", "json"}
	if mode != "" {
		args = append(args, "--permission-mode", mode)
	}
	run := runHarness(t, proj, baseEnv(home, sessDir, addr), args...)
	if err := json.Unmarshal([]byte(run.Stdout), &res); err != nil {
		t.Fatalf("parse --output-format json: %v\nstdout=%s\nstderr=%s", err, run.Stdout, run.Stderr)
	}
	return run.Code, res
}

func permBlockedFor(blocked []string, tool string) bool {
	for _, b := range blocked {
		if len(b) >= len(tool) && b[:len(tool)] == tool {
			return true
		}
	}
	return false
}

// TestPermission_ModesAgainstEditAndBash tables every PermissionMode
// against a script that edits then runs bash, in print mode (which has no
// prompter, so any "ask" verdict is refused rather than interactively
// resolved — Gate.Check's "headless with no way to ask" branch).
//
// Proved able to fail: flipping the acceptEdits case's wantEditBlocked
// from false to true (claiming acceptEdits also blocks edits) turned this
// red with "acceptEdits: edit blocked = false, want true"; reverted.
func TestPermission_ModesAgainstEditAndBash(t *testing.T) {
	cases := []struct {
		mode            string
		wantEditBlocked bool
		wantBashBlocked bool
		blockReasonHas  string // substring expected in the matching blocked entries, "" to skip
	}{
		// manual: edit and bash are both non-read-only, so both are "ask" —
		// and print mode has no prompter, so both are refused.
		{mode: "manual", wantEditBlocked: true, wantBashBlocked: true, blockReasonHas: "requires confirmation"},
		// acceptEdits: edit/write are auto-allowed; everything else still asks.
		{mode: "acceptEdits", wantEditBlocked: false, wantBashBlocked: true, blockReasonHas: "requires confirmation"},
		// auto: blanket allow (still subject to deny rules, none set here).
		{mode: "auto", wantEditBlocked: false, wantBashBlocked: false},
		// dontAsk (Claude Code's meaning): nothing prompts; what would have
		// prompted is refused. Edit and a non-read-only bash both would.
		{mode: "dontAsk", wantEditBlocked: true, wantBashBlocked: true, blockReasonHas: "don't-ask mode refuses"},
		// bypassPermissions: allow (only an explicit deny rule would stop it).
		{mode: "bypassPermissions", wantEditBlocked: false, wantBashBlocked: false},
		// plan: read-only tools allowed, everything else refused outright
		// (not asked) with a plan-specific reason.
		{mode: "plan", wantEditBlocked: true, wantBashBlocked: true, blockReasonHas: "plan mode is read-only"},
	}

	for _, c := range cases {
		t.Run(c.mode, func(t *testing.T) {
			addr, _ := startFaux(t, permEditThenBashScript)
			home, sessDir := scratchHome(t)
			proj := scratchProject(t)

			code, res := permRunResult(t, proj, home, sessDir, addr, c.mode)
			if code != 0 {
				t.Fatalf("exit code %d, want 0 (a block is feedback to the model, not a run failure); result=%+v", code, res)
			}

			gotEditBlocked := permBlockedFor(res.Blocked, "edit(")
			gotBashBlocked := permBlockedFor(res.Blocked, "bash(")
			if gotEditBlocked != c.wantEditBlocked {
				t.Errorf("%s: edit blocked = %v, want %v; blocked=%v", c.mode, gotEditBlocked, c.wantEditBlocked, res.Blocked)
			}
			if gotBashBlocked != c.wantBashBlocked {
				t.Errorf("%s: bash blocked = %v, want %v; blocked=%v", c.mode, gotBashBlocked, c.wantBashBlocked, res.Blocked)
			}
			if c.blockReasonHas != "" {
				var found bool
				for _, b := range res.Blocked {
					if containsSub(b, c.blockReasonHas) {
						found = true
					}
				}
				if !found {
					t.Errorf("%s: blocked=%v, want an entry containing %q", c.mode, res.Blocked, c.blockReasonHas)
				}
			}

			if !c.wantEditBlocked {
				fixed, err := os.ReadFile(filepath.Join(proj, "src", "math.js"))
				if err != nil {
					t.Fatal(err)
				}
				if !containsSub(string(fixed), "return a + b;") {
					t.Errorf("%s: edit was allowed but math.js was not actually changed:\n%s", c.mode, fixed)
				}
			}
		})
	}
}

func containsSub(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return sub == ""
}

const permReadScript = `model: faux-1
steps:
  - tool_call: {name: read, args: {path: %s}, id: r1}
  - on_tool_result: r1
    then:
      - text: "done"
`

// TestPermission_OutsideWorkspaceAsksEvenWhenAllowed writes settings that
// unconditionally allow Read, then reads a path under the scratch HOME
// (outside the project workspace roots). The allow rule alone is not
// enough — Gate.Check's OutsideWorkspace branch asks regardless of the
// rule verdict — and with no prompter (print mode) that becomes a block.
// --add-dir on the containing directory then makes the same read succeed.
//
// Proved able to fail: temporarily changing the assertion to expect the
// read to succeed without --add-dir turned this red ("read was not
// blocked despite being outside every workspace root"); reverted.
func TestPermission_OutsideWorkspaceAsksEvenWhenAllowed(t *testing.T) {
	proj := scratchProject(t)
	home, sessDir := scratchHome(t)
	permWriteRules(t, proj, []string{"Read"}, nil, nil)

	outside := filepath.Join(home, "secret.txt")
	if err := os.WriteFile(outside, []byte("top secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	scriptFmt := permReadScript
	// sprintf the literal path into the YAML (paths in this test have no
	// special YAML characters, so a plain %s substitution is safe).
	script := sprintfScript(scriptFmt, outside)

	t.Run("blocked without add-dir", func(t *testing.T) {
		addr, _ := startFaux(t, script)
		run := runHarness(t, proj, baseEnv(home, sessDir, addr),
			"-p", "read the file", "--output-format", "json")
		if run.Code != 0 {
			t.Fatalf("exit code %d, stderr=%s", run.Code, run.Stderr)
		}
		var res struct {
			Blocked []string `json:"blocked"`
		}
		if err := json.Unmarshal([]byte(run.Stdout), &res); err != nil {
			t.Fatalf("parse json: %v\n%s", err, run.Stdout)
		}
		var found bool
		for _, b := range res.Blocked {
			if containsSub(b, "outside the workspace") {
				found = true
			}
		}
		if !found {
			t.Errorf("blocked=%v, want an entry naming the outside-workspace reason", res.Blocked)
		}
	})

	t.Run("allowed with add-dir", func(t *testing.T) {
		addr, _ := startFaux(t, script)
		run := runHarness(t, proj, baseEnv(home, sessDir, addr),
			"-p", "read the file", "--output-format", "json",
			"--add-dir", home)
		if run.Code != 0 {
			t.Fatalf("exit code %d, stderr=%s", run.Code, run.Stderr)
		}
		var res struct {
			Blocked []string `json:"blocked"`
		}
		if err := json.Unmarshal([]byte(run.Stdout), &res); err != nil {
			t.Fatalf("parse json: %v\n%s", err, run.Stdout)
		}
		if len(res.Blocked) != 0 {
			t.Errorf("blocked=%v, want none once --add-dir %s widens the workspace", res.Blocked, home)
		}
	})
}

func sprintfScript(format, path string) string {
	// Minimal stand-in for fmt.Sprintf kept local so this file's only
	// import list stays exactly what it needs.
	out := make([]byte, 0, len(format)+len(path))
	for i := 0; i < len(format); i++ {
		if i+1 < len(format) && format[i] == '%' && format[i+1] == 's' {
			out = append(out, path...)
			i++
			continue
		}
		out = append(out, format[i])
	}
	return string(out)
}

const permBashPromptScript = `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "echo hi | tee hi.txt"}, id: b1}
  - on_tool_result: b1
    then:
      - text: "first done"
  - tool_call: {name: bash, args: {command: "echo hi | tee hi.txt"}, id: b2}
  - on_tool_result: b2
    then:
      - text: "second done"
`

// TestPermission_AllowAlwaysWithinSessionNotAcross drives the TUI in
// manual mode: the first "echo hi" bash call prompts, answering "2"
// ("Yes, and don't ask again for: ...") grants it for the rest of the
// session; a second, identical bash call in the same run does not prompt
// again; a fresh process (a brand-new Gate, since sessionAllows is
// in-memory only per permission.go's own doc comment) prompts again for
// the identical command.
//
// Proved able to fail: asserting the second call *does* show the prompt
// (inverting the no-second-prompt check) turned this red because the
// prompt text never reappeared within the wait window; reverted.
func TestPermission_AllowAlwaysWithinSessionNotAcross(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, permBashPromptScript)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr, "--permission-mode", "manual")
	waitReady(t, s)

	s.Send("run a command")
	s.SendKey("enter")
	if err := s.WaitFor("Allow kiln to run this command", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	s.SendKey("2") // "Yes, and don't ask again for: ..."
	// Wait for the first turn's own distinctive reply text, not the
	// generic turn-summary pattern: that pattern is already satisfied by
	// this same turn's own summary line and would still be sitting in
	// scrollback during the second turn, making a bare
	// WaitFor(turnSummaryPattern) after the second send pass instantly
	// whether or not a second prompt actually blocked it — a false pass
	// this test's own break-and-revert caught (see doc comment above).
	if err := s.WaitFor("first done", 5*time.Second); err != nil {
		t.Fatal(err)
	}

	s.Send("run it again")
	s.SendKey("enter")
	// If the grant did not carry over, the run stalls on a second
	// "Allow kiln to run this command" prompt (no key is sent for it),
	// and this wait times out rather than ever seeing "second done".
	if err := s.WaitFor("second done", 5*time.Second); err != nil {
		t.Fatalf("second identical bash call did not complete without a fresh prompt: %v", err)
	}

	// A fresh process (new Gate) must prompt again for the same command.
	// It needs its own faux server too: the first process's script cursor
	// is already exhausted (both steps consumed), so a fresh cursor is
	// wired up rather than trying to Reset the first server mid-test.
	addr2, _ := startFaux(t, permBashPromptScript)
	s2 := startTUI(t, 100, 30, proj, home, sessDir, addr2, "--permission-mode", "manual")
	waitReady(t, s2)
	s2.Send("run a command")
	s2.SendKey("enter")
	if err := s2.WaitFor("Allow kiln to run this command", 5*time.Second); err != nil {
		t.Fatalf("fresh process never prompted for the same command: %v", err)
	}
	s2.SendKey("2")
	waitTurnSettled(t, s2)
}

// TestPermission_RulePrecedence checks settings.Decide's precedence order
// end-to-end through settings.json: a deny rule beats a matching allow
// rule for the same tool, and an ask rule beats a mode that would
// otherwise auto-allow (auto's blanket allow).
//
// Proved able to fail: swapping the deny-beats-allow case's mode from
// "auto" (blanket allow) to a mode where bash would already be blocked
// made the assertion pass vacuously; using auto (which by itself allows
// everything) is what actually exercises deny > mode.
func TestPermission_RulePrecedence(t *testing.T) {
	const bashOnlyScript = `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "echo hi"}, id: b1}
  - on_tool_result: b1
    then:
      - text: "done"
`
	t.Run("deny beats allow for the same rule", func(t *testing.T) {
		addr, _ := startFaux(t, bashOnlyScript)
		home, sessDir := scratchHome(t)
		proj := scratchProject(t)
		permWriteRules(t, proj, []string{"Bash"}, []string{"Bash"}, nil)

		run := runHarness(t, proj, baseEnv(home, sessDir, addr),
			"-p", "run a command", "--output-format", "json", "--permission-mode", "auto")
		if run.Code != 0 {
			t.Fatalf("exit code %d, stderr=%s", run.Code, run.Stderr)
		}
		var res struct {
			Blocked []string `json:"blocked"`
		}
		if err := json.Unmarshal([]byte(run.Stdout), &res); err != nil {
			t.Fatalf("parse json: %v\n%s", err, run.Stdout)
		}
		if !permBlockedFor(res.Blocked, "bash(") {
			t.Errorf("blocked=%v, want bash blocked (deny beats allow, and auto alone would have allowed it)", res.Blocked)
		}
	})

	t.Run("ask rule beats mode auto", func(t *testing.T) {
		addr, _ := startFaux(t, bashOnlyScript)
		home, sessDir := scratchHome(t)
		proj := scratchProject(t)
		permWriteRules(t, proj, nil, nil, []string{"Bash"})

		run := runHarness(t, proj, baseEnv(home, sessDir, addr),
			"-p", "run a command", "--output-format", "json", "--permission-mode", "auto")
		if run.Code != 0 {
			t.Fatalf("exit code %d, stderr=%s", run.Code, run.Stderr)
		}
		var res struct {
			Blocked []string `json:"blocked"`
		}
		if err := json.Unmarshal([]byte(run.Stdout), &res); err != nil {
			t.Fatalf("parse json: %v\n%s", err, run.Stdout)
		}
		if !permBlockedFor(res.Blocked, "bash(") {
			t.Errorf("blocked=%v, want bash blocked (an ask rule beats mode auto's blanket allow, and with no prompter ask becomes a block)", res.Blocked)
		}
	})
}

// permPlanReadThenWriteScript runs a read-only bash command, then a
// mutating one, then answers.
const permPlanReadThenWriteScript = `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "cat src/math.js && ls -la"}, id: r1}
  - on_tool_result: r1
    then:
      - tool_call: {name: bash, args: {command: "mkdir build"}, id: w1}
  - on_tool_result: w1
    then:
      - text: "planned"
`

// TestPermission_PlanModeAllowsReadOnlyBash: plan mode runs a bash command
// that only reads and still refuses one that writes
// (qa/findings *plan-mode-denies-read-only-bash). It used to refuse both,
// so a model planning a change could not even cat the spec.
func TestPermission_PlanModeAllowsReadOnlyBash(t *testing.T) {
	addr, _ := startFaux(t, permPlanReadThenWriteScript)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	run := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "plan it", "--output-format", "json", "--permission-mode", "plan")
	var res struct {
		Blocked []string `json:"blocked"`
	}
	if err := json.Unmarshal([]byte(run.Stdout), &res); err != nil {
		t.Fatalf("parse --output-format json: %v\nstdout=%s\nstderr=%s", err, run.Stdout, run.Stderr)
	}
	joined := strings.Join(res.Blocked, "\n")
	if strings.Contains(joined, "cat src/math.js") {
		t.Errorf("read-only bash was blocked in plan mode: %v", res.Blocked)
	}
	if !strings.Contains(joined, "mkdir build") || !strings.Contains(joined, "plan mode is read-only") {
		t.Errorf("mutating bash was not refused in plan mode: %v", res.Blocked)
	}
	if _, err := os.Stat(filepath.Join(proj, "build")); err == nil {
		t.Errorf("plan mode let mkdir run")
	}
}

// TestPermission_ReadOnlyBashInWorkspaceRunsWithoutPrompt: in manual mode a
// command that only reads project files runs straight away, as the read
// tool does; one that reads outside the workspace still asks.
func TestPermission_ReadOnlyBashInWorkspaceRunsWithoutPrompt(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "cat src/math.js | head -2"}, id: r1}
  - on_tool_result: r1
    then:
      - tool_call: {name: bash, args: {command: "cat /etc/hosts"}, id: r2}
  - on_tool_result: r2
    then:
      - text: "read both"
`)
	s := startTUI(t, 100, 40, proj, home, sessDir, addr, "--permission-mode", "manual")
	defer s.Close()
	waitReady(t, s)
	s.Send("look around")
	s.SendKey("enter")
	if err := s.WaitFor("Allow kiln to run this command", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	screen := strings.Join(s.Rows(), "\n")
	if !strings.Contains(screen, "function add") {
		t.Errorf("the in-workspace cat did not run before the prompt:\n%s", screen)
	}
	if !strings.Contains(screen, "cat /etc/hosts") {
		t.Errorf("the prompt is not for the outside-workspace read:\n%s", screen)
	}
}
