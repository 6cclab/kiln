//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/session"
	tkfaux "github.com/andrepato/harness/internal/testkit/faux"
)

// The built-in model router (kiln/auto), driven through the real binary
// against the faux server. Its settings live in the scratch HOME's user
// settings; the router reads modelRoles and modelRouter from there only.

// routerRoles is the user modelRoles every test here starts from: kiln/auto
// plans on structured (faux-1) and implements on fast (faux-2), which is
// also its direct slot. The classifier is turned off explicitly (P3 turns
// it on by default whenever fast resolves, which it does here): these
// tests are P1/P2's and exercise the plan/implement switch, compaction and
// resume mechanics, not the classifier, so their fixtures script no
// classifier call. TestRouter_Classifier* below cover the classifier
// itself, with its own settings.
const routerRoles = `{"modelRoles": {"structured": "faux/faux-1", "fast": "faux/faux-2"}, "modelRouter": {"classifier": false}}`

// writeUserSettings writes the scratch HOME's ~/.claude/settings.json.
func writeUserSettings(t *testing.T, home, body string) {
	t.Helper()
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// requestModels lists the model of every request the faux server got.
func requestModels(srv *tkfaux.Server) []string {
	var out []string
	for _, r := range srv.Requests() {
		out = append(out, r.Model)
	}
	return out
}

func sameModels(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestRouter_PlanThenImplement: faux-1 plans and makes the first edit; the
// request after the edit goes to faux-2, and a resumed turn stays there
// (the phase is router state on the session branch). Each switch is in
// the session as a kiln.router.state entry.
func TestRouter_PlanThenImplement(t *testing.T) {
	addr, srv := startFaux(t, loadFauxScript(t, "router-plan-implement"))
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writeUserSettings(t, home, routerRoles)
	env := baseEnv(home, sessDir, addr)

	res := runHarness(t, proj, env, "--model", "kiln/auto", "-p", "fix the bug in src/math.js", "--output-format", "text", "--permission-mode", "bypassPermissions")
	if res.Code != 0 {
		t.Fatalf("exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "Fixed.") {
		t.Errorf("stdout = %q, want faux-2's reply", res.Stdout)
	}
	if got, want := requestModels(srv), []string{"faux-1", "faux-1", "faux-2"}; !sameModels(got, want) {
		t.Fatalf("requests went to %v, want %v (plan on faux-1, implement on faux-2 after the edit)", got, want)
	}
	data, err := os.ReadFile(filepath.Join(proj, "src", "math.js"))
	if err != nil || !strings.Contains(string(data), "return a + b;") {
		t.Errorf("math.js = %q, %v: the edit did not land", data, err)
	}

	// Resumed: the session continues on kiln/auto, in the implementation
	// phase, so a new message goes to faux-2 without re-planning.
	res = runHarness(t, proj, env, "-c", "-p", "anything else?", "--output-format", "text")
	if res.Code != 0 {
		t.Fatalf("resume: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if got, want := requestModels(srv), []string{"faux-1", "faux-1", "faux-2", "faux-2"}; !sameModels(got, want) {
		t.Fatalf("requests went to %v, want %v (the resumed turn stays on faux-2)", got, want)
	}

	var phases []string
	for _, e := range compactionEntries(t, sessionFile(t, sessDir, proj)) {
		if e.Type == session.EntryCustom && e.CustomType == harness.RouterStateEntry {
			phases = append(phases, string(e.Data))
		}
	}
	if len(phases) != 2 || !strings.Contains(phases[0], `"phase":"planning","model":"faux/faux-1"`) || !strings.Contains(phases[1], `"phase":"implementation","model":"faux/faux-2"`) {
		t.Errorf("router states on the branch = %q, want planning on faux-1 then implementation on faux-2", phases)
	}
}

// TestRouter_SwitchToSmallerWindowCompactsFirst: the conversation fits
// faux-1's 128k window but not faux-2's 32k threshold, so the request
// after the edit compacts before it goes to faux-2. The summary is written
// by the direct slot (faux-2) and the request faux-2 then answers is
// smaller than faux-1's last.
func TestRouter_SwitchToSmallerWindowCompactsFirst(t *testing.T) {
	addr, srv := startFaux(t, loadFauxScript(t, "router-small-window"))
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writeUserSettings(t, home, routerRoles)
	env := baseEnv(home, sessDir, addr)

	res := runHarness(t, proj, env, "--model", "kiln/auto", "-p", "look around, then fix the bug", "--output-format", "text", "--permission-mode", "bypassPermissions")
	if res.Code != 0 {
		t.Fatalf("exit %d, stderr=%s", res.Code, res.Stderr)
	}
	reqs := srv.Requests()
	models := requestModels(srv)
	if len(reqs) < 7 || !sameModels(models[:5], []string{"faux-1", "faux-1", "faux-1", "faux-1", "faux-1"}) {
		t.Fatalf("requests went to %v, want five on faux-1 before the switch", models)
	}
	sumIdx := -1
	for i, r := range reqs {
		if isSummaryRequest(r) {
			sumIdx = i
			break
		}
	}
	if sumIdx != 5 || reqs[sumIdx].Model != "faux-2" {
		t.Fatalf("summary request at %d of %v, want the sixth request, on faux-2", sumIdx, models)
	}
	var answer *tkfaux.Request
	for i := sumIdx + 1; i < len(reqs); i++ {
		if !isSummaryRequest(reqs[i]) {
			answer = &reqs[i]
			break
		}
	}
	if answer == nil || answer.Model != "faux-2" {
		t.Fatalf("no conversation request on faux-2 after the summary: %v", models)
	}
	if lastPlan := reqs[4]; len(answer.Messages) >= len(lastPlan.Messages) {
		t.Errorf("faux-2's request (%d bytes) is not smaller than faux-1's last (%d bytes): not compacted", len(answer.Messages), len(lastPlan.Messages))
	}
	if !strings.Contains(string(answer.Messages), "ROUTER-SUMMARY-MARKER") {
		t.Error("faux-2's request lacks the compaction summary")
	}
	if compactionFindEntry(compactionEntries(t, sessionFile(t, sessDir, proj)), session.EntryCompaction) == nil {
		t.Error("no compaction entry in the session")
	}
}

// isSummaryRequest reports a compaction summary request. It reuses the
// conversation's own system prompt and prefix (so a provider's cache
// applies), and ends with the summary instructions, whose template has a
// "## Key Decisions" section.
func isSummaryRequest(r tkfaux.Request) bool {
	return strings.Contains(r.System, "context summarization assistant") || strings.Contains(string(r.Messages), "## Key Decisions")
}

// TestRouter_SubagentRoutesOnItsOwn: the parent on kiln/auto has made an
// edit and runs on faux-2 when it dispatches a task. The subagent
// inherits kiln/auto, not the parent's routed faux-2, and routes with its
// own state: its first request plans on faux-1.
func TestRouter_SubagentRoutesOnItsOwn(t *testing.T) {
	addr, srv := startFaux(t, `
models:
  faux-1:
    - tool_call:
        name: edit
        args:
          path: src/math.js
          edits:
            - oldText: "return a - b;"
              newText: "return a + b;"
        id: tc1
    - text: "subagent report ROUTER_SUB_51"
      end_turn: true
  faux-2:
    - on_tool_result: tc1
      then:
        - tool_call:
            name: task
            args: {subagent_type: "general-purpose", description: "check", prompt: "check the fix"}
            id: tc2
    - on_tool_result: tc2
      then:
        - text: "Done."
          end_turn: true
`)
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writeUserSettings(t, home, routerRoles)
	env := baseEnv(home, sessDir, addr)

	res, log := subagentRunLog(t, proj, env, "--model", "kiln/auto", "-p", "fix it and check", "--output-format", "text", "--permission-mode", "bypassPermissions")
	if res.Code != 0 {
		t.Fatalf("exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if got, want := requestModels(srv), []string{"faux-1", "faux-2", "faux-1", "faux-2"}; !sameModels(got, want) {
		t.Fatalf("requests went to %v, want %v (the subagent plans on faux-1)", got, want)
	}
	line := subagentResolvedLine(t, log)
	if p, m := subagentLogField(t, line, "provider"), subagentLogField(t, line, "model"); p != "kiln" || m != "auto" {
		t.Errorf("subagent resolved to %s/%s, want kiln/auto\nline: %s", p, m, line)
	}
	for _, r := range srv.Requests() {
		if r.Model == "faux-2" && strings.Contains(string(r.Messages), "ROUTER_SUB_51") {
			return
		}
	}
	t.Error("the subagent's report never reached the parent's faux-2 request")
}

// TestRouter_ProjectModelRouterIgnored: a repository's modelRouter cannot
// choose the models; kiln warns and routes by the user's settings (plan
// on faux-1), not the project's (plan on faux-2).
func TestRouter_ProjectModelRouterIgnored(t *testing.T) {
	addr, srv := startFaux(t, "models:\n  faux-1:\n    - text: \"planned\"\n      end_turn: true\n  faux-2:\n    - text: \"wrong model\"\n      end_turn: true\n")
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writeUserSettings(t, home, routerRoles)
	if err := os.MkdirAll(filepath.Join(proj, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".claude", "settings.json"), []byte(`{"modelRouter": {"roles": {"plan": "fast", "implement": "fast"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	env := baseEnv(home, sessDir, addr)

	res := runHarness(t, proj, env, "--model", "kiln/auto", "-p", "hello", "--output-format", "text")
	if res.Code != 0 {
		t.Fatalf("exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "modelRouter in .claude/settings.json is ignored") {
		t.Errorf("stderr = %q, want the ignored-modelRouter warning", res.Stderr)
	}
	if got := requestModels(srv); !sameModels(got, []string{"faux-1"}) {
		t.Errorf("requests went to %v, want [faux-1] (the user's plan role)", got)
	}
}

// TestRouter_ResumeFallsBackToTheLastPhysicalModel: a session on kiln/auto
// is resumed after the router's settings are gone. It continues on the
// model that answered last (faux-1), as pi does, with a note, not on the
// configured default (faux-2 here).
func TestRouter_ResumeFallsBackToTheLastPhysicalModel(t *testing.T) {
	addr, srv := startFaux(t, "models:\n  faux-1:\n    - text: \"one\"\n      end_turn: true\n    - text: \"two\"\n      end_turn: true\n  faux-2:\n    - text: \"configured default\"\n      end_turn: true\n")
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writeUserSettings(t, home, routerRoles)
	env := baseEnv(home, sessDir, addr)

	if res := runHarness(t, proj, env, "--model", "kiln/auto", "-p", "hello", "--output-format", "text"); res.Code != 0 {
		t.Fatalf("first run: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	writeUserSettings(t, home, `{}`)
	env["HARNESS_MODEL"] = "faux/faux-2"
	res := runHarness(t, proj, env, "-c", "-p", "again", "--output-format", "text")
	if res.Code != 0 {
		t.Fatalf("resume: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "Could not resume on this session's model kiln/auto") || !strings.Contains(res.Stderr, "using faux/faux-1, the model that answered last") {
		t.Errorf("stderr = %q, want the fallback note", res.Stderr)
	}
	if got := requestModels(srv); !sameModels(got, []string{"faux-1", "faux-1"}) {
		t.Errorf("requests went to %v, want [faux-1 faux-1] (resumed on the last physical model)", got)
	}
}

// TestRouter_FallbackNote: modelRoles.heavy names a real anthropic model
// but this scratch HOME has no anthropic credentials, so planComplex
// (heavy's default slot) falls back to plan. kiln/auto still registers
// (implement/plan/direct all resolve on faux) and prints exactly one
// combined note on stderr naming the fallback, with no second,
// vaguer "Model role heavy: ..." line from the subagent-role validator —
// the router note already covers "heavy" and says subagents asking for
// it run on the current model instead.
func TestRouter_FallbackNote(t *testing.T) {
	addr, _ := startFaux(t, "models:\n  faux-1:\n    - text: \"planned\"\n      end_turn: true\n")
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writeUserSettings(t, home, `{"modelRoles": {"structured": "faux/faux-1", "fast": "faux/faux-1", "heavy": "anthropic/claude-opus-5"}, "modelRouter": {"classifier": false}}`)
	env := baseEnv(home, sessDir, addr)
	// No ANTHROPIC_* credentials anywhere in this scratch HOME/env: the
	// fallback must come from "no credentials", not a real API call.
	for _, k := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		delete(env, k)
	}

	res := runHarness(t, proj, env, "--model", "kiln/auto", "-p", "hello", "--output-format", "text")
	if res.Code != 0 {
		t.Fatalf("exit %d, stderr=%s", res.Code, res.Stderr)
	}
	noteCount := strings.Count(res.Stderr, "kiln/auto: anthropic has no credentials")
	if noteCount != 1 {
		t.Fatalf("stderr = %q, want exactly one fallback note", res.Stderr)
	}
	if !strings.Contains(res.Stderr, "kiln login anthropic") || !strings.Contains(res.Stderr, "planComplex") {
		t.Errorf("stderr = %q, want the login hint and the affected slot", res.Stderr)
	}
	if strings.Contains(res.Stderr, "Model role heavy:") {
		t.Errorf("stderr = %q, want no separate ValidateRoles line for heavy (the router note already covers it)", res.Stderr)
	}
}

// classifierRoles is the user settings for the classifier tests below:
// faux-3 is modelRoles.fast, the classifier's own model, never a routing
// target itself — modelRouter.roles sends implement and direct to
// "structured" instead of the default "fast", so faux-3's only job is
// answering the classifier's question. Every slot is named explicitly:
// once modelRouter.roles is set at all, ModelRouterConfig.RoleFor no
// longer defaults an unlisted slot to DefaultRouterRoles, so leaving plan
// or planComplex out here would fall them both back to implement
// (structured/faux-1), losing the planComplex/faux-2 split these tests
// depend on.
const classifierRolesBase = `{"modelRoles": {"structured": "faux/faux-1", "heavy": "faux/faux-2", "fast": "faux/faux-3"}, "modelRouter": {"roles": {"plan": "structured", "planComplex": "heavy", "implement": "structured", "direct": "structured"}`

func classifierRoles(extra string) string {
	if extra == "" {
		return classifierRolesBase + "}}"
	}
	return classifierRolesBase + ", " + extra + "}}"
}

// TestRouter_ClassifierComplexGoesToPlanComplex: the classifier (faux-3,
// modelRoles.fast) answers "complex" for the session's first request, so
// it skips the plan model (faux-1) entirely and lands on planComplex
// (faux-2).
func TestRouter_ClassifierComplexGoesToPlanComplex(t *testing.T) {
	addr, srv := startFaux(t, loadFauxScript(t, "router-classifier-complex"))
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writeUserSettings(t, home, classifierRoles(""))
	env := baseEnv(home, sessDir, addr)
	env["HARNESS_FAUX_EXTRA_MODELS"] = "faux-3"

	res := runHarness(t, proj, env, "--model", "kiln/auto", "-p", "design a new subsystem", "--output-format", "text", "--permission-mode", "bypassPermissions")
	if res.Code != 0 {
		t.Fatalf("exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "Planned on the complex target.") {
		t.Errorf("stdout = %q, want planComplex's (faux-2's) reply", res.Stdout)
	}
	if got, want := requestModels(srv), []string{"faux-3", "faux-2"}; !sameModels(got, want) {
		t.Fatalf("requests went to %v, want %v (the classifier, then planComplex directly)", got, want)
	}
}

// TestRouter_ClassifierUnreadableFallsBackToPlan: the classifier's model
// answers something that is not the expected JSON. The call itself
// succeeds (a clean stop), so Auto.rate's fallback runs: with
// modelRouter.fallback left at its "plan" default, that is Standard, so
// the request goes to plan (faux-1), never planComplex.
func TestRouter_ClassifierUnreadableFallsBackToPlan(t *testing.T) {
	addr, srv := startFaux(t, loadFauxScript(t, "router-classifier-fallback"))
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writeUserSettings(t, home, classifierRoles(""))
	env := baseEnv(home, sessDir, addr)
	env["HARNESS_FAUX_EXTRA_MODELS"] = "faux-3"

	res := runHarness(t, proj, env, "--model", "kiln/auto", "-p", "fix the typo", "--output-format", "text", "--permission-mode", "bypassPermissions")
	if res.Code != 0 {
		t.Fatalf("exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "Planned on plan (the classifier's fallback).") {
		t.Errorf("stdout = %q, want plan's (faux-1's) reply", res.Stdout)
	}
	if got, want := requestModels(srv), []string{"faux-3", "faux-1"}; !sameModels(got, want) {
		t.Fatalf("requests went to %v, want %v (the classifier, then plan)", got, want)
	}
}

// TestRouter_ClassifierUnreadableWithHeuristicFallback: the same
// unreadable classifier answer, but with modelRouter.fallback set to
// "heuristic" and a long (>=1,200 character) typed prompt, which the
// heuristic rates Complex: the request goes to planComplex (faux-2)
// instead of plan.
func TestRouter_ClassifierUnreadableWithHeuristicFallback(t *testing.T) {
	addr, srv := startFaux(t, loadFauxScript(t, "router-classifier-fallback"))
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writeUserSettings(t, home, classifierRoles(`"fallback": "heuristic"`))
	env := baseEnv(home, sessDir, addr)
	env["HARNESS_FAUX_EXTRA_MODELS"] = "faux-3"

	long := "please help: " + strings.Repeat("this request is long enough to trip the heuristic's character count. ", 20)
	res := runHarness(t, proj, env, "--model", "kiln/auto", "-p", long, "--output-format", "text", "--permission-mode", "bypassPermissions")
	if res.Code != 0 {
		t.Fatalf("exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "Planned on planComplex (the heuristic fallback).") {
		t.Errorf("stdout = %q, want planComplex's (faux-2's) reply", res.Stdout)
	}
	if got, want := requestModels(srv), []string{"faux-3", "faux-2"}; !sameModels(got, want) {
		t.Fatalf("requests went to %v, want %v (the classifier, then the heuristic's planComplex)", got, want)
	}
}

// TestRouter_ClassifierUsageSurvivesResume: the model router classifier's
// own call (faux-3) is recorded as a side usage row (Harness.RecordSideUsage,
// labelled "model router classifier · faux/faux-3"), and that row is still
// in the session's cost after a resume, exactly as auto mode's classifier's
// is (precedent: qa/findings/20261004T204953Z-classifier-cost-not-in-
// footer.json in the harness repo).
func TestRouter_ClassifierUsageSurvivesResume(t *testing.T) {
	addr, _ := startFaux(t, loadFauxScript(t, "router-classifier-complex"))
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	writeUserSettings(t, home, classifierRoles(""))
	env := baseEnv(home, sessDir, addr)
	env["HARNESS_FAUX_EXTRA_MODELS"] = "faux-3"

	if res := runHarness(t, proj, env, "--model", "kiln/auto", "-p", "design a new subsystem", "--output-format", "text", "--permission-mode", "bypassPermissions"); res.Code != 0 {
		t.Fatalf("first run: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	res := runHarness(t, proj, env, "-c", "-p", "/cost", "--output-format", "text")
	if res.Code != 0 {
		t.Fatalf("/cost: exit %d, stderr=%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "by model:") || !strings.Contains(res.Stdout, "model router classifier · faux/faux-3") {
		t.Errorf("/cost after -c is missing the classifier's row:\n%s", res.Stdout)
	}
}
