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
// ModePlan refuses edits, asks about bash that is not read-only and denies
// any other tool not in settings.ReadOnly; ModeManual asks for
// anything not in settings.ReadOnly) and internal/claude/permission/
// permission.go's Gate.Check (an outside-workspace path always asks,
// even when the rule verdict is Allow, unless mode is bypassPermissions;
// with no prompter available — every print-mode run — an "ask" verdict
// or an outside-workspace question is refused, not silently allowed).

import (
	"encoding/json"
	"fmt"
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
		// auto: the edit is inside the workspace and skips the classifier;
		// the bash write goes to the classifier, which here is the session
		// model (no "fast" role) answering from a script written for the
		// conversation, not with a verdict. An unreadable verdict fails
		// closed, and print mode refuses. automode_behaviour_test.go covers
		// real allow and block verdicts.
		{mode: "auto", wantEditBlocked: false, wantBashBlocked: true, blockReasonHas: "Auto mode could not check"},
		// dontAsk (Claude Code's meaning): nothing prompts; what would have
		// prompted is refused. Edit and a non-read-only bash both would.
		{mode: "dontAsk", wantEditBlocked: true, wantBashBlocked: true, blockReasonHas: "don't-ask mode refuses"},
		// bypassPermissions: allow (only an explicit deny rule would stop it).
		{mode: "bypassPermissions", wantEditBlocked: false, wantBashBlocked: false},
		// plan: edits refused outright with a plan-specific reason; a bash
		// command that is not read-only takes the regular flow, so with no
		// rule it asks, which print mode refuses for want of a prompter.
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

// TestPermission_AllowAlwaysSavesTheRule drives the TUI in manual mode:
// the first "echo hi | tee hi.txt" call prompts, and answering "2" ("Yes,
// and don't ask again for: tee hi.txt") saves Bash(tee hi.txt) to the
// project's .kiln/settings.local.json (as Claude Code persists it, "Permanently
// per repository and command"). A second, identical call in the same run
// does not prompt, and neither does one in a fresh process, which reads
// the rule back from that file.
//
// Proved able to fail: asserting the second call *does* show the prompt
// (inverting the no-second-prompt check) turned this red because the
// prompt text never reappeared within the wait window; reverted.
func TestPermission_AllowAlwaysSavesTheRule(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, permBashPromptScript)

	s := startTUI(t, 100, 30, proj, home, sessDir, addr, "--permission-mode", "manual")
	waitReady(t, s)

	s.Send("run a command")
	s.SendKey("enter")
	if err := s.WaitFor("Allow kiln to run this command", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := s.WaitFor("don’t ask again for: tee hi.txt", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	s.SendKey("2") // "Yes, and don't ask again for: tee hi.txt"
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
	assertLocalAllow(t, proj, []string{"Bash(tee hi.txt)"})

	// A fresh process reads the saved rule: no prompt. It needs its own
	// faux server: the first one's script cursor is exhausted.
	addr2, _ := startFaux(t, permBashPromptScript)
	s2 := startTUI(t, 100, 30, proj, home, sessDir, addr2, "--permission-mode", "manual")
	waitReady(t, s2)
	s2.Send("run a command")
	s2.SendKey("enter")
	if err := s2.WaitFor("first done", 5*time.Second); err != nil {
		t.Fatalf("fresh process did not run the saved command unasked: %v", err)
	}
}

// assertLocalAllow checks the allow list in proj's .kiln/settings.local.json.
func assertLocalAllow(t *testing.T, proj string, want []string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(proj, ".kiln", "settings.local.json"))
	if err != nil {
		t.Fatalf("settings.local.json: %v", err)
	}
	var local struct {
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &local); err != nil {
		t.Fatalf("settings.local.json: %v\n%s", err, data)
	}
	if fmt.Sprint(local.Permissions.Allow) != fmt.Sprint(want) {
		t.Errorf("settings.local.json allow = %q, want %q", local.Permissions.Allow, want)
	}
}

// permCompoundScript runs a compound line, then one of its commands on its
// own. The commands do not exist (they fail harmlessly): the test is about
// the prompt, not what runs.
const permCompoundScript = `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "git status && kilnfakea test && kilnfakeb build"}, id: c1}
  - on_tool_result: c1
    then:
      - text: "compound done"
  - tool_call: {name: bash, args: {command: "kilnfakea test -- upload"}, id: c2}
  - on_tool_result: c2
    then:
      - text: "follow-up done"
`

// TestPermission_DontAskSavesARulePerSubcommand: "don't ask again" on
// "git status && kilnfakea test && kilnfakeb build" names and saves one
// rule per command that needed approval (Claude Code's "Compound
// commands"), not git status (read-only) and not the whole line; a later
// "kilnfakea test -- upload" then runs without a prompt, in the same
// process and in a fresh one. The rules go to .kiln/settings.local.json
// (with a .kiln/.gitignore); Claude Code's ~/.claude and <proj>/.claude,
// which kiln reads, are left byte-identical with no file added.
func TestPermission_DontAskSavesARulePerSubcommand(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, permCompoundScript)
	ccFiles := map[string]string{
		filepath.Join(home, ".claude", "settings.json"):       `{"permissions":{"allow":["Bash(echo *)"]}}`,
		filepath.Join(proj, ".claude", "settings.json"):       `{"permissions":{"deny":["Bash(rm -rf *)"]}}`,
		filepath.Join(proj, ".claude", "settings.local.json"): `{"permissions":{"allow":["Bash(true)"]}}`,
	}
	for path, body := range ccFiles {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	s := startTUI(t, 100, 30, proj, home, sessDir, addr, "--permission-mode", "manual")
	waitReady(t, s)

	s.Send("run the compound line")
	s.SendKey("enter")
	if err := s.WaitFor("don’t ask again for: kilnfakea test *, kilnfakeb build *", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	s.SendKey("2")
	if err := s.WaitFor("compound done", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	assertLocalAllow(t, proj, []string{"Bash(kilnfakea test *)", "Bash(kilnfakeb build *)"})

	s.Send("now just the tests")
	s.SendKey("enter")
	if err := s.WaitFor("follow-up done", 5*time.Second); err != nil {
		t.Fatalf("the follow-up command was not approved by the saved rule: %v", err)
	}

	// A fresh process reads the rules back from .kiln: no prompt.
	addr2, _ := startFaux(t, `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "kilnfakeb build"}, id: f1}
  - on_tool_result: f1
    then:
      - text: "fresh done"
`)
	s2 := startTUI(t, 100, 30, proj, home, sessDir, addr2, "--permission-mode", "manual")
	waitReady(t, s2)
	s2.Send("build it")
	s2.SendKey("enter")
	if err := s2.WaitFor("fresh done", 5*time.Second); err != nil {
		t.Fatalf("a fresh process did not run the saved command unasked: %v", err)
	}

	for path, body := range ccFiles {
		if data, err := os.ReadFile(path); err != nil || string(data) != body {
			t.Errorf("%s changed: %q (%v)", path, data, err)
		}
	}
	for dir, want := range map[string]int{filepath.Join(home, ".claude"): 1, filepath.Join(proj, ".claude"): 2} {
		if entries, _ := os.ReadDir(dir); len(entries) != want {
			t.Errorf("%s holds %d entries, want %d: kiln wrote into it", dir, len(entries), want)
		}
	}
	if data, err := os.ReadFile(filepath.Join(proj, ".kiln", ".gitignore")); err != nil || string(data) != "*\n" {
		t.Errorf(".kiln/.gitignore = %q (%v)", data, err)
	}
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
// that only reads and asks about one that writes
// (qa/findings *plan-mode-denies-read-only-bash). It used to refuse both,
// so a model planning a change could not even cat the spec. Print mode has
// nobody to ask, so the mutating one is refused as any ask is. An allow
// rule for it lets it run: shell commands take the regular permission flow
// while planning (Claude Code's permissions doc).
func TestPermission_PlanModeAllowsReadOnlyBash(t *testing.T) {
	for _, allow := range [][]string{nil, {"Bash(mkdir *)"}} {
		t.Run(fmt.Sprintf("allow=%v", allow), func(t *testing.T) {
			addr, _ := startFaux(t, permPlanReadThenWriteScript)
			home, sessDir := scratchHome(t)
			proj := scratchProject(t)
			if allow != nil {
				permWriteRules(t, proj, allow, nil, nil)
			}
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
			_, statErr := os.Stat(filepath.Join(proj, "build"))
			if allow != nil {
				if strings.Contains(joined, "mkdir build") || statErr != nil {
					t.Errorf("mkdir under Bash(mkdir *) did not run in plan mode: blocked=%v stat=%v", res.Blocked, statErr)
				}
				return
			}
			if !strings.Contains(joined, "mkdir build") || !strings.Contains(joined, "requires confirmation") {
				t.Errorf("mutating bash was not put to the user (refused for want of a prompter) in plan mode: %v", res.Blocked)
			}
			if statErr == nil {
				t.Errorf("plan mode let mkdir run")
			}
		})
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
