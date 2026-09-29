//go:build e2e

package e2e

// This file closes six behaviour gaps in the TUI regression suite
// (tui_test.go's own package comment / phase P2 brief) that no existing
// test covers: /cost's by-model breakdown, `/model roles`, the permission
// gate prompt for a `task` dispatch, --fullscreen with the subagents panel
// live, a resize mid-dispatch, and --ax-screen-reader's rendering of
// subagent start/done events. It drives the same real, PTY-attached
// harness binary as tui_test.go and reuses that file's helpers
// (startTUI, tuiFixture, waitReady, waitTurnSettled, waitQuiescent,
// submitSlashCommand, assertGoldenTail, assertFooterInvariant,
// loadFauxScript, turnSummaryPattern, fixBugScript) and task_test.go's
// writeModelRolesSettings rather than redefining them.
//
// Goldens are written under testdata/golden with a "tui-gap-" prefix, as
// scoped for this file.

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// tuiStartLinePattern matches bridge.go's SubagentSink "Start" commit line
// ("⏺ <agent> <description> on <model> [...]"). Now that CommitSynthetic
// (bridge.go) preserves it across a fullscreen/inline replay instead of
// silently dropping it, its position is subject to the exact same
// dispatch-order race tuiFinishedLinePattern's doc comment describes for
// the "Done" line and the panel rows below — two independent goroutines
// each committing their own Start line as soon as their dispatch begins.
var tuiStartLinePattern = regexp.MustCompile(`^\s*⏺ \S`)

// tuiFinishedLinePattern matches bridge.go's SubagentSink "Done" commit
// line ("  <agent> finished - N tool calls, N chars returned, N tokens").
var tuiFinishedLinePattern = regexp.MustCompile(`^\s+\S.* finished - \d+ tool calls`)

// tuiSubagentPanelRowPattern matches subagents.go's renderSubagentRow
// output for the two "general-purpose" rows task_concurrent_tui.yaml's
// script produces. Since assignDisplayNamesLocked (subagents.go) now gives
// each of several same-type rows a distinct "gen-N" name, the pattern
// matches either index; tuiNormalizeSubagentIndex below collapses it back
// to a fixed placeholder before the golden compare, since which physical
// dispatch gets "-1" vs "-2" is the same wall-clock race
// tuiSortSubagentFinishLines's doc comment already describes for row/Done
// order — the index is assigned in the order Start events are seen, not a
// property of the golden's expected content.
var tuiSubagentPanelRowPattern = regexp.MustCompile(`^\s*gen-(\d|N)  `)

// tuiSubagentIndexPattern matches the "gen-N" short name
// assignDisplayNamesLocked (internal/tui/subagents.go) gives each row when
// several dispatches share a type, so tuiNormalizeSubagentIndex can
// collapse it to a fixed placeholder ahead of a golden compare — see
// tuiSubagentPanelRowPattern's doc comment for why the index itself is
// racy.
var tuiSubagentIndexPattern = regexp.MustCompile(`gen-\d`)

// tuiNormalizeSubagentIndex replaces every "gen-N" name in rows with a
// fixed "gen-N" placeholder (literal "N", not a digit), in a copy of
// rows, so a golden compare does not depend on which concurrent dispatch
// happened to be seen first.
func tuiNormalizeSubagentIndex(rows []string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = tuiSubagentIndexPattern.ReplaceAllString(r, "gen-N")
	}
	return out
}

// tuiSubagentsBlockLabel matches the subagents panel's own label-rule row
// (subagents.go's labelRule("subagents", ...)), the fixed marker every
// committed panel snapshot starts with.
var tuiSubagentsBlockLabel = regexp.MustCompile(`^\s*subagents `)

// dropIntermediateSubagentsBlocks removes every committed subagents-panel
// block except the last, given a copy of rows.
//
// Under the "live while last" freeze rule (internal/tui/live_freeze.go),
// the panel can now commit more than once during a single turn — once
// ahead of each task call's own tool-result commit, while the panel is
// still "running" (frozen with whatever partial-completion state it
// happened to be in at that instant), and again at finishTurn with the
// final state. Two concurrent dispatches racing on their own goroutines
// (the same race tuiSortSubagentFinishLines's own doc comment documents
// for the panel's row order) means an intermediate snapshot's actual
// content — not just its rows' order — is genuinely non-deterministic
// (which dispatch, if any, has finished by the time the other one's tool
// result commits). This test's own invariant (the panel survives a
// mid-dispatch fullscreen toggle) does not depend on which intermediate
// snapshot happened to land, only on the final one, so this drops every
// earlier one before the golden compare rather than trying to make their
// content deterministic.
func dropIntermediateSubagentsBlocks(rows []string) []string {
	var labelIdx []int
	for i, r := range rows {
		if tuiSubagentsBlockLabel.MatchString(r) {
			labelIdx = append(labelIdx, i)
		}
	}
	out := append([]string(nil), rows...)
	if len(labelIdx) < 2 {
		return out
	}
	// Remove every block but the last, from the last-to-remove backward,
	// so earlier (not-yet-processed) indices stay valid.
	for k := len(labelIdx) - 2; k >= 0; k-- {
		start := labelIdx[k]
		if start > 0 && out[start-1] == "" {
			start-- // the block's own leading blank separator row.
		}
		end := labelIdx[k] + 1
		for end < len(out) && out[end] != "" {
			end++
		}
		out = append(out[:start], out[end:]...)
	}
	return out
}

// tuiSortSubagentFinishLines sorts two independent contiguous blocks of
// rows — the transcript's "finished" lines and the subagents panel's own
// rows — each among themselves, in a copy of rows, before a golden
// compare.
//
// Two real, independent races found while writing
// TestTUI_FullscreenWithSubagentsPanel, not routed around silently:
// task_concurrent_tui.yaml's two subagents (tc1 on faux-1, tc2 on faux-2)
// carry the identical 400ms scripted delay and are genuinely dispatched
// concurrently (internal/harness runs concurrent `task` tool calls in
// parallel, each its own goroutine), and neither race is something
// HARNESS_TEST_CLOCK freezes (it only freezes the footer's/model's own
// "now" — see tui_test.go's testClock doc comment):
//
//  1. Which one's agent.SubagentEventDone reaches bridge.go's SubagentSink
//     and gets committed to the transcript first is a real wall-clock race
//     between two independent goroutines.
//  2. An earlier version of this helper assumed the subagents panel's own
//     row order was pinned by SubagentPanelState.order (subagents.go),
//     fixed at each dispatch's Start event in "the assistant message's
//     fixed tool_calls order" — that assumption was wrong, disproved by
//     `go test -tags e2e -run TestTUI_FullscreenWithSubagentsPanel
//     -count=12` on this machine, which also showed the two panel rows
//     swapped in ~1 run in 4: the two Start events themselves are each
//     sent from their own dispatch goroutine (internal/agent's
//     dispatcher), so the order SubagentPanelState.Apply first sees them
//     in is itself a race, not just their Done order.
//
// Both blocks are sorted independently since they have unrelated formats
// and unrelated (if correlated) underlying races.
func tuiSortSubagentFinishLines(rows []string) []string {
	out := dropIntermediateSubagentsBlocks(rows)
	out = tuiNormalizeSubagentIndex(out)
	tuiSortBlock(out, tuiStartLinePattern)
	tuiSortBlock(out, tuiFinishedLinePattern)
	tuiSortPanelPairs(out, tuiSubagentPanelRowPattern)
	return out
}

// tuiSortBlock finds the first contiguous run of rows matching pattern and
// sorts that run in place.
func tuiSortBlock(rows []string, pattern *regexp.Regexp) {
	start := -1
	for i, r := range rows {
		if pattern.MatchString(r) {
			start = i
			break
		}
	}
	if start == -1 {
		return
	}
	end := start
	for end < len(rows) && pattern.MatchString(rows[end]) {
		end++
	}
	block := append([]string{}, rows[start:end]...)
	sort.Strings(block)
	copy(rows[start:end], block)
}

// tuiSortPanelPairs sorts the subagents panel's two-line rows — a header
// row matching headerPattern (subagents.go's renderSubagentRow: name/task/
// meter/tokens) immediately followed by its own indented status
// continuation row ("→ ..." or "✓ finished") — as whole two-line units,
// keyed by the header row's text.
//
// tuiSortBlock cannot do this: it only sorts the single lines matching
// pattern within one contiguous run, and here every other line (the
// continuation row) does NOT match headerPattern, so a naive contiguous
// scan sees a "block" of length one per pair and never actually reorders
// anything — confirmed by driving this test `-count=12` with only
// tuiSortBlock wired in, which still showed the two panel rows swapped in
// roughly 1 run in 4, silently unfixed. This walks header/continuation
// pairs explicitly instead.
//
// It sorts EVERY such contiguous block it finds in rows, not just the
// first: the "live while last" freeze rule (internal/tui/live_freeze.go)
// can commit the subagents panel more than once in a single turn — once
// mid-turn (frozen ahead of a task call's own tool-result commit, while
// still "running") and again at finishTurn (the final "finished" state)
// — and each commit is its own independent instance of the same
// goroutine race, so each needs sorting on its own.
func tuiSortPanelPairs(rows []string, headerPattern *regexp.Regexp) {
	for i := 0; i+1 < len(rows); {
		if !headerPattern.MatchString(rows[i]) {
			i++
			continue
		}
		start := i
		end := start
		for end+1 < len(rows) && headerPattern.MatchString(rows[end]) {
			end += 2
		}
		n := (end - start) / 2
		if n >= 2 {
			type pair struct{ header, cont string }
			pairs := make([]pair, n)
			for j := 0; j < n; j++ {
				pairs[j] = pair{rows[start+2*j], rows[start+2*j+1]}
			}
			sort.Slice(pairs, func(a, b int) bool { return pairs[a].header < pairs[b].header })
			for j, p := range pairs {
				rows[start+2*j] = p.header
				rows[start+2*j+1] = p.cont
			}
		}
		i = end
	}
}

// --- 10. /cost by-model breakdown ------------------------------------------

// TestTUI_CostByModel drives testdata/faux/task_concurrent_tui.yaml (the
// same script test/e2e/tui_test.go's TestTUI_SubagentsPanel_TwoLiveThenCleared
// uses): the parent dispatches one task inherited on faux-1 and one routed
// by the "fast" modelRole to faux-2, so the session's accumulated usage
// legitimately spans two models. /cost's "by model" table
// (internal/commands/builtins.go's cost command, gated on
// deps.UsageByModel returning more than one entry) is otherwise
// unreachable with a single model, confirmed by reading that file: the
// table is only appended when len(byModel) > 0, and a session that never
// dispatches to a second model has exactly one key in it.
func TestTUI_CostByModel(t *testing.T) {
	script, err := os.ReadFile("../../testdata/faux/task_concurrent_tui.yaml")
	if err != nil {
		t.Fatal(err)
	}
	proj, home, sessDir, addr, _ := tuiFixture(t, string(script))
	writeModelRolesSettings(t, proj, map[string]string{"fast": "faux/faux-2"})

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("dispatch two tasks")
	s.SendKey("enter")
	waitTurnSettled(t, s)

	submitSlashCommand(s, "cost")
	if err := s.WaitFor("by model:", 3*time.Second); err != nil {
		t.Fatalf("/cost never showed a by-model breakdown: %v", err)
	}
	if err := waitQuiescent(s, 150*time.Millisecond, 2*time.Second); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(s.Rows(), "\n")
	// UsageByModel's keys are "provider/model" (internal/cli/chat.go's
	// addUsage: key := providerID + "/" + modelID), so both rows read
	// "faux/faux-1" and "faux/faux-2", not bare model ids.
	for _, want := range []string{"faux/faux-1", "faux/faux-2"} {
		if !strings.Contains(joined, want) {
			t.Errorf("/cost by-model table missing %q:\n%s", want, joined)
		}
	}

	assertGoldenTail(t, s, "tui-gap-cost-by-model", "by model:")
}

// --- 11. /model roles -------------------------------------------------------

// TestTUI_ModelRolesView drives the real `/model roles` command (verified
// by reading internal/commands/builtins.go: the "model" command's Run
// checks strings.TrimSpace(args) == "roles" and returns
// modelRolesTable(deps.ModelRoles, models) — there is no separate "/roles"
// command) against a settings.json with two roles configured, and checks
// the rendered table names both roles and both provider/model values.
func TestTUI_ModelRolesView(t *testing.T) {
	proj, home, sessDir, addr, _ := tuiFixture(t, fixBugScript)
	writeModelRolesSettings(t, proj, map[string]string{
		"fast":  "faux/faux-2",
		"heavy": "faux/faux-1",
	})

	s := startTUI(t, 100, 30, proj, home, sessDir, addr)
	waitReady(t, s)

	submitSlashCommand(s, "model roles")
	if err := s.WaitFor("provider/model", 3*time.Second); err != nil {
		t.Fatalf("/model roles never rendered the roles table header: %v", err)
	}
	if err := waitQuiescent(s, 150*time.Millisecond, 2*time.Second); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(s.Rows(), "\n")
	for _, want := range []string{"fast", "faux/faux-2", "heavy", "faux/faux-1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("/model roles table missing %q:\n%s", want, joined)
		}
	}

	assertGoldenTail(t, s, "tui-gap-model-roles", "role")
}

// --- 12. task dispatch permission gate prompt --------------------------

// TestTUI_TaskDispatchGatePrompt asserts the permission prompt shown for
// an ordinary `task` tool call in manual (gate-prompted) permission mode.
//
// The work item's plan called for a role-heavy prompt reached via a
// literal "$ role:<name>" argument on a cross-provider, priced model — but
// faux (internal/provider/faux) registers exactly one provider ("faux")
// with two models that share the same cost shape, so no request this
// suite can drive ever produces that argument text; RenderPermissionPrompt
// (internal/tui/permission_render.go) only ever shows what SummarizeArg
// derives from req.PrimaryArg/Args, and the `task` tool's own args
// (subagent_type/description/prompt/model) never assemble into a
// "role:<name>" token. That variant is NOT reachable with faux and is not
// tested here. What IS real and reachable: any `task` dispatch in
// permission-mode "manual" (the default gate; not bypassPermissions,
// which the rest of this suite mostly uses) falls through
// RenderPermissionPrompt's generic path (permission_render.go line ~65:
// `Allow kiln to use %s?` with req.ToolName), since the task tool has no
// dedicated command/edit rendering — this test drives exactly that.
func TestTUI_TaskDispatchGatePrompt(t *testing.T) {
	script, err := os.ReadFile("../../testdata/faux/task.yaml")
	if err != nil {
		t.Fatal(err)
	}
	proj, home, sessDir, addr, _ := tuiFixture(t, string(script))

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "manual",
	)
	waitReady(t, s)

	s.Send("look something up for me")
	s.SendKey("enter")

	if err := s.WaitFor("Allow kiln to use task?", 5*time.Second); err != nil {
		t.Fatalf("task dispatch never showed the generic gate prompt: %v", err)
	}
	assertGoldenNormalizedSpinner(t, s, "tui-gap-task-gate-prompt")

	// Answer yes so the turn (and this test) finishes cleanly.
	s.SendKey("y")
	if err := s.WaitFor(turnSummaryPattern, 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

// --- 13. --fullscreen with the subagents panel live -------------------------

// TestTUI_FullscreenWithSubagentsPanel dispatches two concurrent subagents
// (task_concurrent_tui.yaml, same script/pattern as
// TestTUI_SubagentsPanel_TwoLiveThenCleared) starting in the default
// inline layout, then presses Ctrl+F to enter --fullscreen
// (internal/tui/app.go's toggleFullscreen) while the panel still shows a
// live row, and checks the panel survives the alt-screen switch; then
// toggles back to inline and checks it is still there. toggleFullscreen
// rebuilds the transcript from the session log (the same replay Ctrl+O
// uses) but the subagents panel is separate, turn-scoped Model state
// (m.subagents, reset only at the next turn's beginTurn) untouched by that
// replay, which is exactly what this test is checking actually holds.
func TestTUI_FullscreenWithSubagentsPanel(t *testing.T) {
	script, err := os.ReadFile("../../testdata/faux/task_concurrent_tui.yaml")
	if err != nil {
		t.Fatal(err)
	}
	proj, home, sessDir, addr, _ := tuiFixture(t, string(script))
	writeModelRolesSettings(t, proj, map[string]string{"fast": "faux/faux-2"})

	// Start inline (no --fullscreen yet).
	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("dispatch two tasks")
	s.SendKey("enter")

	if err := s.WaitFor(regexp.MustCompile(`2 subagents running in parallel`), 3*time.Second); err != nil {
		t.Fatalf("subagents panel never showed two live rows before entering fullscreen: %v", err)
	}

	// Enter fullscreen while the panel is (or, worst case, just finished
	// being) live.
	s.SendKey("ctrl+f")
	subagentsAnyState := regexp.MustCompile(`subagents running in parallel|subagents finished`)
	if err := s.WaitFor(subagentsAnyState, 3*time.Second); err != nil {
		t.Fatalf("subagents panel not visible after Ctrl+F entered fullscreen mid-dispatch: %v", err)
	}
	rows := s.Viewport()
	if len(rows) != 30 {
		t.Fatalf("Viewport() returned %d rows after entering fullscreen, want 30", len(rows))
	}

	waitTurnSettled(t, s)
	if err := s.WaitFor(regexp.MustCompile(`2 subagents finished`), 3*time.Second); err != nil {
		t.Fatalf("subagents panel did not settle to two done rows in fullscreen: %v", err)
	}
	got := strings.Join(tuiSortSubagentFinishLines(normalizeBannerCwdRow(s.Rows())), "\n") + "\n"
	assertGolden(t, goldenPath("tui-gap-fullscreen-subagents.txt"), got)

	// Toggle back to inline. Going fullscreen -> inline rebuilds the
	// transcript from the session log (replayTranscript/
	// RenderTranscriptEntries, replay.go), which reconstructs blocks
	// backed by a session.Entry (user/assistant/tool messages) directly —
	// except the task calls the panel covered (Bridge.CoveredCalls) — and
	// every other committed block via Bridge.CommitSynthetic's recorded
	// splice list (bridge.go's SyntheticCommit) — including the
	// subagents PANEL itself (the aggregate name/task/tokens table,
	// finishTurn's commit in app.go, now routed through CommitSynthetic
	// precisely so this survives). The panel must be on screen once after
	// the round trip, with no task block beside it.
	s.SendKey("ctrl+f")
	if err := s.WaitFor(tuiUserMark, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := waitQuiescent(s, 150*time.Millisecond, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(s.Rows(), "\n")
	// The panel is the one record of each dispatch, live and replayed: no
	// separate "task ──" block per subagent beside it.
	if regexp.MustCompile(`(?m)^\s*task ─`).MatchString(joined) {
		t.Errorf("task blocks replayed beside the subagents panel after toggling back to inline:\n%s", joined)
	}
	if n := strings.Count(joined, "2 subagents finished"); n != 1 {
		t.Errorf("subagents panel shown %d times after toggling back to inline, want 1:\n%s", n, joined)
	}
}

// --- 14. Resize while a subagent row is live --------------------------

// TestTUI_ResizeDuringLiveSubagentRow resizes the terminal while
// task_concurrent_tui.yaml's two subagents are still running (their 400ms
// delay holds them open), and checks: the screen does not crash (Rows()
// itself enforces the per-row width invariant — see screen.Screen.Rows'
// checkWidth — so simply calling it after the resize is part of the
// assertion), the footer invariant still holds, the in-flight turn still
// finishes normally, and the process still answers a follow-up command
// afterward. The follow-up is a `!` bang command (never reaches the
// scripted model — see TestTUI_BangCommand) rather than a real prompt, so
// this does not depend on the faux script having any steps left over
// after the dispatch turn's own gated continuation.
func TestTUI_ResizeDuringLiveSubagentRow(t *testing.T) {
	script, err := os.ReadFile("../../testdata/faux/task_concurrent_tui.yaml")
	if err != nil {
		t.Fatal(err)
	}
	proj, home, sessDir, addr, _ := tuiFixture(t, string(script))
	writeModelRolesSettings(t, proj, map[string]string{"fast": "faux/faux-2"})

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--permission-mode", "bypassPermissions",
	)
	waitReady(t, s)

	s.Send("dispatch two tasks")
	s.SendKey("enter")

	if err := s.WaitFor(regexp.MustCompile(`2 subagents running in parallel`), 3*time.Second); err != nil {
		t.Fatalf("subagents panel never showed two live rows before resize: %v", err)
	}

	s.Resize(60, 24)
	if err := waitQuiescent(s, 150*time.Millisecond, 2*time.Second); err != nil {
		t.Fatalf("screen never settled after resize to 60x24: %v", err)
	}

	rows := s.Rows()
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "subagents") {
		t.Errorf("subagents panel missing after resize to 60x24:\n%s", joined)
	}
	assertFooterInvariant(t, s)

	// The turn that was live during the resize still finishes normally.
	waitTurnSettled(t, s)
	assertFooterInvariant(t, s)

	// The process still answers a follow-up command after the resize.
	s.Send("!echo still alive")
	s.SendKey("enter")
	if err := s.WaitFor("still alive", 3*time.Second); err != nil {
		t.Fatalf("process unresponsive after a resize during a live subagent row: %v", err)
	}
}

// --- 15. --ax-screen-reader subagent events -----------------------------

// TestTUI_AxScreenReaderSubagentEvents drives testdata/faux/task.yaml
// under --ax-screen-reader and checks the subagent dispatch's start/done
// events appear as plain committed transcript lines, not styled/boxed TUI
// chrome. Reading internal/tui/bridge.go's SubagentSink shows these two
// lines are committed to the transcript unconditionally (they are not
// gated on fullscreen/plain mode at all): the only thing that changes
// under --ax-screen-reader is which glyph set theme.go's G() returns
// (ASCIIGlyphs' Call is "*", not kiln's "⏺" — matching
// TestTUI_AxScreenReader_FixBug's own glyph check elsewhere in this
// package), so the events read as ordinary ASCII text lines rather than
// Unicode decorative markers.
func TestTUI_AxScreenReaderSubagentEvents(t *testing.T) {
	script, err := os.ReadFile("../../testdata/faux/task.yaml")
	if err != nil {
		t.Fatal(err)
	}
	proj, home, sessDir, addr, _ := tuiFixture(t, string(script))

	s := startTUI(t, 100, 30, proj, home, sessDir, addr,
		"--ax-screen-reader",
		"--permission-mode", "bypassPermissions",
	)
	if err := s.WaitFor(">", 5*time.Second); err != nil {
		t.Fatal(err)
	}

	s.Send("look something up for me")
	s.SendKey("enter")
	waitTurnSettled(t, s)

	joined := strings.Join(s.Rows(), "\n")
	// The subagents panel (subagents.go) is the one transcript record of
	// a dispatch: its name, its description and its finished state, in
	// ASCII under --ax-screen-reader.
	if !strings.Contains(joined, "general-purpose") {
		t.Errorf("ax-screen-reader transcript missing the subagent name:\n%s", joined)
	}
	if !strings.Contains(joined, "look something up") {
		t.Errorf("ax-screen-reader transcript missing the dispatch's description:\n%s", joined)
	}
	if !regexp.MustCompile(`subagents? finished`).MatchString(joined) {
		t.Errorf("ax-screen-reader transcript missing the finished panel header:\n%s", joined)
	}
	for _, glyph := range []string{"⏺", "›", "✻", "∴"} {
		if strings.Contains(joined, glyph) {
			t.Errorf("--ax-screen-reader subagent output still contains decorative glyph %q:\n%s", glyph, joined)
		}
	}
}
