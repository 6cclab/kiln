//go:build e2e

package e2e

// Phase P2: behaviour regression suite for subagent dispatch, driven
// through the real cmd/kiln binary. Ownership: this file, plus data under
// testdata/behaviour/subagents/ and testdata/faux/behaviour-*.yaml, is
// this file's alone - see harness_test.go and task_test.go for the shared
// helpers reused below (startFaux, runHarness, scratchHome, scratchProject,
// baseEnv, sessionFiles, assertGolden, goldenPath, loadFauxScript,
// writeModelRolesSettings).
//
// Every test here drives internal/agent/dispatch.go's Dispatch through a
// real `task` tool call, over the actual model-resolution and tool-
// allowlist machinery in internal/claude/agents and internal/agent -
// nothing here mocks or reimplements that logic.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	fauxprovider "github.com/andrepato/harness/internal/provider/faux"
)

// subagentWriteAgentDef copies a fixture agent definition from
// testdata/behaviour/subagents/<name>.md into proj/.claude/agents/<name>.md,
// which is where agents.LoadAgents(cwd) (internal/claude/agents/agents.go)
// looks for a project's own subagent definitions - cwd is the harness
// binary's working directory, which runHarness sets to proj.
func subagentWriteAgentDef(t *testing.T, proj, name string) {
	t.Helper()
	src := filepath.Join(repoTestdataDir(), "behaviour", "subagents", name+".md")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read agent fixture %s: %v", src, err)
	}
	dir := filepath.Join(proj, ".claude", "agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// subagentRunLog runs the harness binary exactly like runHarness, plus
// wires HARNESS_LOG_DIR to a fresh directory (diag.Dir, internal/diag/
// diag.go) so the run's diagnostics log - where Dispatch's "subagent model
// resolved" line (internal/agent/dispatch.go) lands - can be read back
// afterwards. It fails the test unless the run produced exactly one
// harness-*.log file (diag.Start's naming, internal/diag/diag.go), and
// returns that file's content alongside the run result.
func subagentRunLog(t *testing.T, proj string, env map[string]string, args ...string) (res runResult, log string) {
	t.Helper()
	logDir := t.TempDir()
	env = mergeEnv(env, map[string]string{"HARNESS_LOG_DIR": logDir})
	res = runHarness(t, proj, env, args...)
	entries, err := os.ReadDir(logDir)
	if err != nil {
		t.Fatalf("read log dir %s: %v", logDir, err)
	}
	var logFiles []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "harness-") && strings.HasSuffix(e.Name(), ".log") {
			logFiles = append(logFiles, e.Name())
		}
	}
	if len(logFiles) != 1 {
		t.Fatalf("got %d run logs under %s, want exactly 1: %v", len(logFiles), logDir, logFiles)
	}
	data, err := os.ReadFile(filepath.Join(logDir, logFiles[0]))
	if err != nil {
		t.Fatalf("read run log: %v", err)
	}
	return res, string(data)
}

// mergeEnv layers extra over base, returning a new map (neither input is
// mutated) - baseEnv's own map is a package-level helper other tests also
// call, so subagentRunLog must not write into it.
func mergeEnv(base, extra map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// subagentResolvedLine extracts the single "subagent model resolved" line
// (internal/agent/dispatch.go's diag.L().Info call) from a run log written
// by slog's TextHandler (internal/diag/diag.go's diag.Start). Fails the
// test unless there is exactly one.
func subagentResolvedLine(t *testing.T, log string) string {
	t.Helper()
	var lines []string
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, `msg="subagent model resolved"`) {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("got %d \"subagent model resolved\" log lines, want exactly 1:\n%s", len(lines), log)
	}
	return lines[0]
}

// subagentLogField extracts one slog TextHandler key=value field (e.g.
// "kind=role" from "kind", or "provider=faux" from "provider") from a log
// line, failing the test if the key is absent. TextHandler quotes a value
// only when it needs to (spaces, etc.) - none of the fields this suite
// reads ever do, so a plain (?:^|\s)key=(\S+) is exact, not an
// approximation.
func subagentLogField(t *testing.T, line, key string) string {
	t.Helper()
	re := regexp.MustCompile(`(?:^|\s)` + regexp.QuoteMeta(key) + `=(\S+)`)
	m := re.FindStringSubmatch(line)
	if m == nil {
		t.Fatalf("log line has no %q field:\n%s", key, line)
	}
	return m[1]
}

// subagentDefaultEnv builds baseEnv plus the "fast" -> faux-2 model role
// used by several subtests, writing settings.json into proj first.
func subagentDefaultEnv(t *testing.T, proj, home, sessDir, addr string, roles map[string]string) map[string]string {
	t.Helper()
	if roles != nil {
		writeModelRolesSettings(t, proj, roles)
	}
	return baseEnv(home, sessDir, addr)
}

// ---------------------------------------------------------------------
// 1. TestSubagent_RoleRoutingKinds
// ---------------------------------------------------------------------

// TestSubagent_RoleRoutingKinds drives a real `task` dispatch through the
// built binary for each of the four agents.ResolveKind outcomes
// (internal/claude/agents/agents.go's ResolveModel), confirmed against
// that function's actual source rather than assumed:
//
//   - kind=role: requested names a configured modelRoles key.
//   - kind=inherited: requested is "" or "inherit".
//   - kind=fallback: requested is a bare alias ("sonnet") with no
//     modelRoles configured at all, so builtinAliasRoles["sonnet"] =
//     "structured" has no roles["structured"] to resolve against
//     (agents.go's ResolveModel, the `if roleValue, ok := roles[role]; ok`
//     branch under the bare-alias case) and the same-provider substring
//     fallback then finds nothing named "sonnet" among faux's candidates
//     (faux-1, faux-2), so ResolveModel returns (parent, ResolveFallback).
//   - kind=alias: same "sonnet" request, but WITH modelRoles
//     {"structured": "faux/faux-2"} configured - agents.go's
//     builtinAliasRoles map (`"sonnet": "structured"`) means this resolves
//     via the alias path (ResolveAlias), landing on faux-2. This matches
//     the plan's original hypothesis exactly; no deviation to report here.
func TestSubagent_RoleRoutingKinds(t *testing.T) {
	t.Run("role", func(t *testing.T) {
		script := loadFauxScript(t, "behaviour-role")
		addr, srv := startFaux(t, script)
		home, sessDir := scratchHome(t)
		proj := scratchProject(t)
		env := subagentDefaultEnv(t, proj, home, sessDir, addr, map[string]string{"fast": fauxprovider.ProviderID + "/" + fauxprovider.ModelID2})

		res, log := subagentRunLog(t, proj, env,
			"-p", "dispatch a fast task",
			"--output-format", "stream-json",
			"--permission-mode", "bypassPermissions",
		)
		if res.Code != 0 {
			t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
		}

		line := subagentResolvedLine(t, log)
		if kind := subagentLogField(t, line, "kind"); kind != "role" {
			t.Errorf("kind = %q, want role\nline: %s", kind, line)
		}
		if provider := subagentLogField(t, line, "provider"); provider != "faux" {
			t.Errorf("provider = %q, want faux", provider)
		}
		if model := subagentLogField(t, line, "model"); model != fauxprovider.ModelID2 {
			t.Errorf("model = %q, want %s", model, fauxprovider.ModelID2)
		}

		// faux-2's script step actually answered: a request reached it.
		var faux2Requests int
		for _, r := range srv.Requests() {
			if r.Model == fauxprovider.ModelID2 {
				faux2Requests++
			}
		}
		if faux2Requests == 0 {
			t.Fatal("no request reached faux-2 (the fast role)")
		}

		// The parent's final reply reflects the subagent's report: the
		// parent's post-dispatch request carries the subagent's canned
		// report text as tool_result content - not merely a matching
		// canned final string, which would prove nothing about the
		// plumbing.
		var sawReportInParentContext bool
		for _, r := range srv.Requests() {
			if r.Model == fauxprovider.ModelID && strings.Contains(string(r.Messages), "fast subagent report BEHAVIOUR_ROLE_9c31") {
				sawReportInParentContext = true
			}
		}
		if !sawReportInParentContext {
			t.Error("the subagent's report never appeared in a parent-model (faux-1) request - the dispatch result was not plumbed back as the tool_result")
		}
	})

	t.Run("inherited", func(t *testing.T) {
		script := loadFauxScript(t, "behaviour-inherit")
		addr, _ := startFaux(t, script)
		home, sessDir := scratchHome(t)
		proj := scratchProject(t)
		env := baseEnv(home, sessDir, addr)

		res, log := subagentRunLog(t, proj, env,
			"-p", "dispatch an inherited task",
			"--output-format", "stream-json",
			"--permission-mode", "bypassPermissions",
		)
		if res.Code != 0 {
			t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
		}

		line := subagentResolvedLine(t, log)
		if kind := subagentLogField(t, line, "kind"); kind != "inherited" {
			t.Errorf("kind = %q, want inherited\nline: %s", kind, line)
		}
		if model := subagentLogField(t, line, "model"); model != fauxprovider.ModelID {
			t.Errorf("model = %q, want %s", model, fauxprovider.ModelID)
		}
	})

	t.Run("fallback", func(t *testing.T) {
		script := loadFauxScript(t, "behaviour-fallback")
		addr, _ := startFaux(t, script)
		home, sessDir := scratchHome(t)
		proj := scratchProject(t)
		subagentWriteAgentDef(t, proj, "sonnetagent")
		env := baseEnv(home, sessDir, addr) // no modelRoles configured at all

		res, log := subagentRunLog(t, proj, env,
			"-p", "dispatch to sonnetagent",
			"--output-format", "stream-json",
			"--permission-mode", "bypassPermissions",
		)
		if res.Code != 0 {
			t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
		}

		line := subagentResolvedLine(t, log)
		if requested := subagentLogField(t, line, "requested"); requested != "sonnet" {
			t.Errorf("requested = %q, want sonnet (the definition's model: field)", requested)
		}
		if kind := subagentLogField(t, line, "kind"); kind != "fallback" {
			t.Errorf("kind = %q, want fallback (see internal/claude/agents/agents.go's ResolveModel: a bare alias with no matching role and no same-provider substring match falls back)\nline: %s", kind, line)
		}
		if model := subagentLogField(t, line, "model"); model != fauxprovider.ModelID {
			t.Errorf("model = %q, want %s (fallback keeps the parent's model)", model, fauxprovider.ModelID)
		}
	})

	t.Run("alias", func(t *testing.T) {
		script := loadFauxScript(t, "behaviour-alias")
		addr, _ := startFaux(t, script)
		home, sessDir := scratchHome(t)
		proj := scratchProject(t)
		subagentWriteAgentDef(t, proj, "sonnetagent")
		env := subagentDefaultEnv(t, proj, home, sessDir, addr, map[string]string{"structured": fauxprovider.ProviderID + "/" + fauxprovider.ModelID2})

		res, log := subagentRunLog(t, proj, env,
			"-p", "dispatch to sonnetagent",
			"--output-format", "stream-json",
			"--permission-mode", "bypassPermissions",
		)
		if res.Code != 0 {
			t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
		}

		line := subagentResolvedLine(t, log)
		if requested := subagentLogField(t, line, "requested"); requested != "sonnet" {
			t.Errorf("requested = %q, want sonnet", requested)
		}
		if kind := subagentLogField(t, line, "kind"); kind != "alias" {
			t.Errorf("kind = %q, want alias (builtinAliasRoles[\"sonnet\"] = \"structured\", and \"structured\" is configured here) - see internal/claude/agents/agents.go\nline: %s", kind, line)
		}
		if model := subagentLogField(t, line, "model"); model != fauxprovider.ModelID2 {
			t.Errorf("model = %q, want %s", model, fauxprovider.ModelID2)
		}
	})
}

// ---------------------------------------------------------------------
// 2. TestSubagent_ToolAllowlist
// ---------------------------------------------------------------------

// TestSubagent_ToolAllowlist dispatches to "reader"
// (testdata/behaviour/subagents/reader.md, tools: Read), whose script then
// attempts a bash call.
//
// The allowlist is enforced twice: AllowedToolNames (internal/claude/agents)
// narrows the schema the model is offered (assertion a), and
// internal/harness/turn.go's beginTool refuses a call whose name is not in
// the lane's active tool set (assertion b), so a model that guesses a tool
// name it was not offered gets an error result instead of a real
// execution. Break to verify: drop the toolActive check in beginTool; the
// bash probe's output then reaches a later request and (b) goes red.
func TestSubagent_ToolAllowlist(t *testing.T) {
	script := loadFauxScript(t, "behaviour-toolallowlist")
	addr, srv := startFaux(t, script)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	subagentWriteAgentDef(t, proj, "reader")
	env := baseEnv(home, sessDir, addr)

	res := runHarness(t, proj, env,
		"-p", "dispatch to reader",
		"--output-format", "stream-json",
		"--permission-mode", "bypassPermissions",
	)
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	// (a) The subagent's own request(s) - identified by System containing
	// its definition's prompt body, not by Model (both parent and
	// subagent run on faux-1 here) - expose only "read".
	var subagentRequests int
	for _, r := range srv.Requests() {
		if !strings.Contains(r.System, "You are a test subagent restricted to reading files.") {
			continue
		}
		subagentRequests++
		var names []string
		for _, tl := range r.Tools {
			names = append(names, strings.ToLower(tl.Name))
		}
		if len(names) != 1 || names[0] != "read" {
			t.Errorf("subagent request tools = %v, want exactly [read]", names)
		}
	}
	if subagentRequests == 0 {
		t.Fatal("no request carried the reader subagent's system prompt")
	}

	// (b) The bash call was refused at execution: its output never reached
	// a request, and the refusal text did. The marker is the command's
	// OUTPUT (built from two printf halves), never its argument text,
	// because the argument is replayed as tool_use input in every later
	// request and would match whether or not the command ran.
	var sawProbeOutput, sawRefusal bool
	for _, r := range srv.Requests() {
		if strings.Contains(string(r.Messages), "ALLOWLIST_OUT_c3d2") {
			sawProbeOutput = true
		}
		if strings.Contains(string(r.Messages), "not available to this agent") {
			sawRefusal = true
		}
	}
	if sawProbeOutput {
		t.Error("the disallowed bash call executed: its output reached a later request")
	}
	if !sawRefusal {
		t.Error("no request carried the refusal for the disallowed bash call")
	}
}

// ---------------------------------------------------------------------
// 3. TestSubagent_ContextIsolation
// ---------------------------------------------------------------------

// TestSubagent_ContextIsolation dispatches a task routed (via modelRoles)
// to faux-2, whose script reads a marker string via a bash tool result
// that must stay inside its own session; only its final report (which
// deliberately withholds the marker) crosses back to the parent. Routing
// the subagent to a DIFFERENT model than the parent's is what makes
// filtering srv.Requests() by Model actually distinguish "the parent's own
// requests" from "the subagent's own requests" - inheriting the parent's
// model would put both on faux-1 and make a Model-based filter vacuous.
//
// Per the task's ground rules, this test's sensitivity was verified by
// running it once with a deliberately broken (too-broad) filter and
// observing it fail, then restoring the correct one - see this function's
// t.Run subtests below and the report handed back to the caller for the
// literal red/green command output.
func TestSubagent_ContextIsolation(t *testing.T) {
	script := loadFauxScript(t, "behaviour-contextisolation")
	addr, srv := startFaux(t, script)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	env := subagentDefaultEnv(t, proj, home, sessDir, addr, map[string]string{"fast": fauxprovider.ProviderID + "/" + fauxprovider.ModelID2})

	res := runHarness(t, proj, env,
		"-p", "dispatch an isolated task",
		"--output-format", "stream-json",
		"--permission-mode", "bypassPermissions",
	)
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	const marker = "MARKER_9f3a2b1c"

	// Sanity check: the marker really was exercised somewhere (the
	// subagent's own faux-2 requests), so an all-clear on the parent's
	// requests below cannot be a vacuous pass over a script that never
	// produced the marker in the first place.
	var sawMarkerOnSubagentModel bool
	for _, r := range srv.Requests() {
		if r.Model == fauxprovider.ModelID2 && strings.Contains(string(r.Messages), marker) {
			sawMarkerOnSubagentModel = true
		}
	}
	if !sawMarkerOnSubagentModel {
		t.Fatal("marker never appeared in any faux-2 (subagent) request - the script fixture is broken, not the isolation guarantee")
	}

	// The actual guarantee: no PARENT-model (faux-1) request ever carries
	// the marker.
	for _, r := range srv.Requests() {
		if r.Model != fauxprovider.ModelID {
			continue
		}
		if strings.Contains(string(r.Messages), marker) {
			t.Errorf("marker %q leaked into a parent-model (faux-1) request (seq %d): subagent context isolation violated", marker, r.Seq)
		}
	}
}

// TestSubagent_ContextIsolation_FilterSensitivity is not a regression
// guard on production code; it is the recorded proof, required by this
// phase's ground rules, that TestSubagent_ContextIsolation's assertion is
// not vacuously true. It reruns the same scenario and applies the
// INVERTED (deliberately over-broad) filter the real test does not use -
// checking ALL requests, not just faux-1 ones, for the marker - which
// must find it (it is legitimately present in the subagent's own faux-2
// requests) and therefore fail if asserted against as "must be marker-
// free". This proves the strings.Contains/Model-filter machinery actually
// detects a leak when one is present, rather than the correct test above
// merely happening to find nothing.
func TestSubagent_ContextIsolation_FilterSensitivity(t *testing.T) {
	script := loadFauxScript(t, "behaviour-contextisolation")
	addr, srv := startFaux(t, script)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	env := subagentDefaultEnv(t, proj, home, sessDir, addr, map[string]string{"fast": fauxprovider.ProviderID + "/" + fauxprovider.ModelID2})

	res := runHarness(t, proj, env,
		"-p", "dispatch an isolated task",
		"--output-format", "stream-json",
		"--permission-mode", "bypassPermissions",
	)
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}

	const marker = "MARKER_9f3a2b1c"
	var sawMarkerAnywhere bool
	for _, r := range srv.Requests() { // deliberately unfiltered by Model
		if strings.Contains(string(r.Messages), marker) {
			sawMarkerAnywhere = true
		}
	}
	if !sawMarkerAnywhere {
		t.Fatal("the over-broad, all-requests filter found no marker at all - the fixture is broken, not proving anything about isolation")
	}
	t.Log("filter sensitivity confirmed: the unfiltered (all-requests) check does find the marker, so the scoped (parent-model-only) check in TestSubagent_ContextIsolation is a real assertion, not a vacuous one")
}

// ---------------------------------------------------------------------
// 4. TestSubagent_RoleFallbackWarnsInLog
// ---------------------------------------------------------------------

// TestSubagent_RoleFallbackWarnsInLog covers budget item 4. The plan's
// original TestSubagent_PaidGateRefusedInPrintMode needs a subagent
// dispatch that crosses to a DIFFERENT, priced provider (see
// internal/agent/dispatch.go's Dispatch: the permission.Gate check is
// scoped to `choice.ProviderID != parentModel.ProviderID &&
// resolved.Model.Cost.ModelCostRates.Input > 0`). faux registers exactly
// one provider ("faux", internal/provider/faux/faux.go's ProviderID) for
// both faux-1 and faux-2, so a faux-only e2e run can never make
// choice.ProviderID differ from parentModel.ProviderID - the cross-
// provider paid gate cannot trigger through this harness's e2e faux setup
// at all. That gate IS covered - by
// internal/agent/dispatch_test.go's existing unit test(s), which construct
// distinct providers directly - but not by this e2e suite. See this
// function's report to the caller for the exact citation.
//
// In its place, this test dispatches with an unresolvable role name (not a
// key of the configured modelRoles, and not the built-in "sonnet" alias
// either) and asserts the run log reports kind=fallback and that the
// dispatch still completes rather than erroring.
func TestSubagent_RoleFallbackWarnsInLog(t *testing.T) {
	script := loadFauxScript(t, "behaviour-rolefallback")
	addr, _ := startFaux(t, script)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	env := subagentDefaultEnv(t, proj, home, sessDir, addr, map[string]string{"fast": fauxprovider.ProviderID + "/" + fauxprovider.ModelID2})

	res, log := subagentRunLog(t, proj, env,
		"-p", "dispatch with an unresolvable role",
		"--output-format", "stream-json",
		"--permission-mode", "bypassPermissions",
	)
	if res.Code != 0 {
		t.Fatalf("exit code %d, stderr=%s", res.Code, res.Stderr)
	}
	if strings.Contains(res.Stdout, `"isError":true`) {
		t.Errorf("a tool_end reported an error - fallback should still let the dispatch complete:\n%s", res.Stdout)
	}
	if !strings.Contains(res.Stdout, `"text":"Done rolefallback."`) {
		t.Errorf("final assistant text was not \"Done rolefallback.\" - the dispatch did not complete:\n%s", res.Stdout)
	}

	line := subagentResolvedLine(t, log)
	if requested := subagentLogField(t, line, "requested"); requested != "ghost" {
		t.Errorf("requested = %q, want ghost", requested)
	}
	if kind := subagentLogField(t, line, "kind"); kind != "fallback" {
		t.Errorf("kind = %q, want fallback\nline: %s", kind, line)
	}
	if model := subagentLogField(t, line, "model"); model != fauxprovider.ModelID {
		t.Errorf("model = %q, want %s (fallback keeps the parent's model)", model, fauxprovider.ModelID)
	}
}

// --- recent-sessions banner excludes subagent sessions ----------------
//
// TestTUI_RecentSessions_ExcludesSubagents drives the real TUI (not
// runHarness's print mode, unlike this file's other tests) through one
// full `task` dispatch, then starts kiln again in the very same project
// and $HOME and checks the startup banner's "Recent sessions" block: it
// must list the first run's own prompt and must not list the subagent's
// dispatch description, proving internal/cli/tui.go's
// buildRecentSessionRows actually excludes the dispatched session rather
// than just having a filter that never fires — it only fires now that
// internal/agent/dispatch.go's Dispatch sets Options.ParentSessionID to
// the dispatching session's own SessionID, which agent.Start threads into
// the header repo.Create writes (internal/agent/session.go).
//
// buildRecentSessionRows keys off $HOME (jsonl.NewRepo("")'s own default,
// deliberately not HARNESS_SESSIONS_DIR — see internal/cli/banner_golden_test.go's
// own doc comment on this), so sessDir here is pinned to exactly
// $HOME/.harness/sessions rather than an unrelated scratchHome temp dir:
// the CLI's own session repo (chat.go, HARNESS_SESSIONS_DIR-driven) and
// the banner's recent-sessions repo (tui.go, $HOME-driven) must agree on
// where the session files live, or the second run would never see the
// first run's sessions at all, task-dispatch history or not.
func TestTUI_RecentSessions_ExcludesSubagents(t *testing.T) {
	script := loadFauxScript(t, "task")
	addr, _ := startFaux(t, script)
	proj := scratchProject(t)
	home := t.TempDir()
	sessDir := filepath.Join(home, ".harness", "sessions")

	const mainPrompt = "look something up for me"
	// subagentPrompt is task.yaml's own dispatch prompt (`prompt: "find
	// X"`) — the text firstUserMessageTitle (internal/cli/tui.go) would
	// read as the subagent session's own title, were it not excluded.
	// Checked instead of the dispatch's `description` ("look something
	// up"), which is a substring of mainPrompt itself and so cannot tell
	// "excluded" apart from "coincidentally not shown".
	const subagentPrompt = "find X"

	s := startTUI(t, 100, 30, proj, home, sessDir, addr, "--permission-mode", "bypassPermissions")
	waitReady(t, s)
	s.Send(mainPrompt)
	s.SendKey("enter")
	waitTurnSettled(t, s)

	s.SendKey("ctrl+c")
	if err := s.WaitFor("Press Ctrl-C again to exit", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	s.SendKey("ctrl+c")
	if _, err := s.Exit(); err != nil {
		t.Fatal(err)
	}

	// A second, freshly started process, same project and $HOME, same
	// faux server (still needed so the model resolves at startup even
	// though nothing is submitted — HARNESS_MODEL is set unconditionally
	// by startTUI, and the faux provider is dynamic: without
	// HARNESS_FAUX_ADDR its model list is never populated, and startup
	// fails with "unknown model ... run a refresh first").
	s2 := startTUI(t, 100, 30, proj, home, sessDir, addr)
	waitReady(t, s2)

	joined := strings.Join(s2.Rows(), "\n")
	if !strings.Contains(joined, "Recent sessions") {
		t.Fatalf("banner has no \"Recent sessions\" block on the second run:\n%s", joined)
	}
	if !strings.Contains(joined, mainPrompt) {
		t.Errorf("banner's recent sessions do not list the first run's own prompt %q:\n%s", mainPrompt, joined)
	}
	if strings.Contains(joined, subagentPrompt) {
		t.Errorf("banner's recent sessions list the subagent's own prompt %q — the subagent session was not excluded:\n%s", subagentPrompt, joined)
	}
	if got := strings.Count(joined, "just now"); got != 1 {
		t.Errorf("banner's recent-sessions block has %d row(s) (\"just now\"), want exactly 1 — parent only, subagent excluded:\n%s", got, joined)
	}
}
