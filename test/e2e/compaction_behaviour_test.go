//go:build e2e

// Package e2e: compaction behaviour tests, driving auto-compaction (see
// internal/harness/compaction.go's autoCompact, called at the end of every
// turn that made tool calls) through the real kiln binary against faux-2
// (32k window, small tier: ReserveTokens ~3,277, so ShouldCompact fires
// once estimated context tokens exceed ~29,491 -- see
// internal/compaction/estimate.go's ShouldCompact/estimateContextTokens).
//
// Triggering compaction deterministically requires understanding how
// estimateContextTokens actually works, which took real experimentation to
// pin down (recorded here rather than left implicit, since it is load-
// bearing for every test in this file):
//
//   - It does NOT sum every message's estimated size. It finds the most
//     recent assistant message with a non-zero reported Usage and uses that
//     usage's total (input+output+cache) as the running total, plus a
//     char/4 estimate of whatever comes after it (typically that message's
//     own tool_result). This mirrors a real provider's cumulative
//     prompt-token reporting, so the faux script must set a growing
//     `usage: {input: N, ...}` on each round -- faux's own default usage
//     (100/50 flat, see anthropic.go's resolveUsage) never grows and so
//     never trips ShouldCompact on its own.
//   - Once threshold is crossed, autoCompact runs BEFORE the next
//     conversation turn is requested -- it makes its own model call(s)
//     (compaction.Compact -> generateSummary and/or
//     generateTurnPrefixSummary, both via the SAME model/provider,
//     identified by System == compaction.SummarizationSystemPrompt) to a
//     lane whose transcript looks like a "split turn" (IsSplitTurn), which
//     in practice means only generateTurnPrefixSummary fires (one extra
//     request) when there's no material to hand generateSummary --
//     confirmed empirically, not assumed: a scripted run logging every
//     recorded request's System showed exactly one
//     "you are a context summarization assistant" request between the last
//     conversation round and the post-compaction continuation.
//   - Because that extra request consumes the NEXT unconsumed script step
//     like any other, whatever step is scripted there becomes the
//     compaction summary's text (via TextOf on the returned assistant
//     message) -- so the round count in the tool_call chain below is tuned
//     (calibrated against faux-2's actual threshold, not guessed) so the
//     compaction call lands on a plain `text:` step carrying a known
//     marker, not on another tool_call (which would make the summary
//     empty, silently, since msg.TextOf on a tool_use content list is "").
//
// A real, load-bearing finding from that same experimentation, confirmed
// with a throwaway instrumented test (not assumed from reading code alone):
// compaction commits a "compaction" session entry with a real summary, but
// it does NOT shrink what the harness sends the model on the next turn, and
// the summary text never reaches the model as conversation content either.
// Two independent causes, both in code this task does not own and must not
// edit:
//
//   - internal/harness/lane.go's turn loop calls
//     `l.h.opts.Storage.ScanBranch(session.BranchScan{Start: tip, Order:
//     "oldestFirst"})` with no StopAtType, so it always walks the full
//     branch from root to tip -- unlike
//     internal/compaction/estimate.go's buildContextEntries, which
//     deliberately keeps only the last compaction entry onward. The
//     estimator and the actual transcript builder disagree about what
//     "the session so far" means.
//   - internal/harness/lane.go's entriesToTranscript only turns
//     session.EntryMessage entries into messages; an EntryCompaction entry
//     produces nothing at all, unlike
//     internal/compaction/estimate.go's sessionEntryToContextMessages
//     (used only for the estimate), which wraps it via
//     wrapCompactionSummary into a real message.
//
// Net effect, measured against this file's own 7-round script: the request
// right before compaction was 73,467 bytes of messages; the request right
// after was 85,700 bytes -- bigger, not smaller -- and contained zero
// occurrences of the compaction summary's marker text, in that request or
// any later one. TestCompaction_TriggersAndShrinksNextRequest and
// TestCompaction_SummaryReachesModel below assert this actual, current
// behaviour (with the "desired" behaviour stated in each test's own doc
// comment) rather than the plan's assumption that compaction shrinks and
// forwards -- see this task's instructions on documenting a real gap
// without silently patching non-test code to make it look fixed.
package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/session/jsonl"
	tkfaux "github.com/andrepato/harness/internal/testkit/faux"
)

// compactionSummaryMarker is the sentinel compactionScript's post-round
// text step carries, so a test can confirm it reached both the compaction
// entry's summary and (test 6) the following request's messages.
const compactionSummaryMarker = "SUMMARY-MARKER-9f3ac21"

// compactionRounds is the number of large tool_call rounds
// compactionScript scripts before the marker text step. Calibrated
// experimentally against faux-2's tier (window 32768, ReserveTokens
// ~3,277, threshold ~29,491 estimated tokens) with usage input growing by
// 4,000 tokens/round: round 7 (cumulative usage input 28,000, output 20)
// plus that round's own ~3,000-token tool_result trailing estimate crosses
// the threshold, so autoCompact fires immediately after round 7, and the
// very next script step -- the marker text below -- is what the
// compaction call consumes. If internal/budget/tier.go's shares or
// internal/compaction/estimate.go's estimator ever change, this constant
// (and the two below) may need recalibrating; a wrong value shows up as
// "no compaction entry found" in TestCompaction_TriggersAndShrinksNextRequest,
// not a silent pass.
const compactionRounds = 7

// compactionUsageStep is the per-round input-token growth scripted via
// each round's `usage: {input: ...}`, simulating a real provider's
// cumulative prompt-token reporting (see this file's package doc comment).
const compactionUsageStep = 4000

// compactionBuildScript builds a faux-2 script: compactionRounds bash
// tool_call rounds (each printing ~9KB of base64 so the tool_result itself
// contributes real trailing tokens, and each carrying a growing scripted
// usage), then a `text:` step carrying compactionSummaryMarker (consumed
// by the compaction call once threshold crosses), then extraFinalSteps
// more plain text steps as a safety margin -- compaction may cost one or
// two extra model requests depending on whether generateSummary also
// fires (see this file's doc comment), so a test that cares about the
// run's own final reply should not assume which trailing step lands on
// it, only that the run completes without error.
func compactionBuildScript(extraFinalSteps int) string {
	var b strings.Builder
	b.WriteString("model: faux-2\nsteps:\n")
	for i := 0; i < compactionRounds; i++ {
		id := "tc" + strconv.Itoa(i)
		fmt.Fprintf(&b, "  - tool_call: {name: bash, args: {command: \"head -c 9000 /dev/urandom | base64\"}, id: %s}\n    usage: {input: %d, output: 20}\n", id, compactionUsageStep*(i+1))
	}
	fmt.Fprintf(&b, "  - text: %q\n", compactionSummaryMarker)
	for i := 0; i < extraFinalSteps; i++ {
		fmt.Fprintf(&b, "  - text: %q\n", fmt.Sprintf("final reply %d", i))
	}
	return b.String()
}

// compactionRunToTrigger runs the harness once against a fresh faux-2
// server scripted by compactionBuildScript, driving enough rounds to
// trigger auto-compaction, and returns the session file path plus the
// faux server (so a test can inspect srv.Requests() alongside the
// session).
func compactionRunToTrigger(t *testing.T, home, sessDir, proj string, extraFinalSteps int, extraArgs ...string) (sessPath, addr string, srv *tkfaux.Server) {
	t.Helper()
	scriptAddr, s := startFaux(t, compactionBuildScript(extraFinalSteps))
	env := baseEnv(home, sessDir, scriptAddr)
	env["HARNESS_MODEL"] = "faux/faux-2"
	args := append([]string{"-p", "do a long task", "--output-format", "text", "--permission-mode", "bypassPermissions"}, extraArgs...)
	res := runHarness(t, proj, env, args...)
	if res.Code != 0 {
		t.Fatalf("run: exit %d, stdout=%q stderr=%q", res.Code, res.Stdout, res.Stderr)
	}
	sess := sessionFile(t, sessDir, proj)
	return sess, scriptAddr, s
}

// compactionEntries opens the session file at path and returns its full
// list of committed entries on the "main" branch, oldest first.
func compactionEntries(t *testing.T, path string) []session.Entry {
	t.Helper()
	st, err := jsonl.Open(path, nil)
	if err != nil {
		t.Fatalf("open session %s: %v", path, err)
	}
	tipAddr := session.BranchTip("main")
	tipRaw, _, ok := st.GetValue(tipAddr.Namespace, tipAddr.Key)
	if !ok {
		t.Fatalf("session %s: no branch tip for main", path)
	}
	var tip *string
	if err := json.Unmarshal(tipRaw, &tip); err != nil {
		t.Fatalf("session %s: parse branch tip: %v", path, err)
	}
	if tip == nil {
		t.Fatalf("session %s: branch tip is nil (empty branch)", path)
	}
	entries, err := st.ScanBranch(session.BranchScan{Start: *tip, Order: "oldestFirst"})
	if err != nil {
		t.Fatalf("session %s: scan branch: %v", path, err)
	}
	return entries
}

// compactionFindEntry returns the first entry of the given type, or nil.
func compactionFindEntry(entries []session.Entry, typ session.EntryType) *session.Entry {
	for i := range entries {
		if entries[i].Type == typ {
			return &entries[i]
		}
	}
	return nil
}

// compactionFindSummarizerIndex returns the index of the first recorded
// request whose System is the compaction summarization prompt, or -1.
func compactionFindSummarizerIndex(reqs []tkfaux.Request) int {
	for i, r := range reqs {
		if strings.Contains(r.System, "context summarization assistant") {
			return i
		}
	}
	return -1
}

// TestCompaction_TriggersAndShrinksNextRequest drives faux-2 through
// compactionRounds large tool_call rounds -- enough to cross the tier's
// ShouldCompact threshold (see this file's doc comment for how that
// threshold and round count were derived) -- and asserts a "compaction"
// entry lands in the session.
//
// It documents ACTUAL behaviour (confirmed with an instrumented run, not
// assumed) for the "shrinks next request" half of its name: today it does
// NOT. Deterministic, reproduced numbers from this exact script: the
// request right before the summarization call carried 73,467 bytes of
// messages; the request right after it carried 85,700 -- bigger. See this
// file's package doc comment for the two root causes in
// internal/harness/lane.go (ScanBranch has no StopAtType: EntryCompaction,
// and entriesToTranscript drops EntryCompaction entries entirely). Desired
// behaviour: the post-compaction request should be smaller than the
// pre-compaction one, per this task's plan.
//
// Proved able to fail: temporarily inverted the growth assertion to
// `if len(after.Messages) < len(before.Messages) { t.Errorf(...) }` (i.e.
// required a real shrink, the plan's original assumption) -- went red with
// the exact 73,467-vs-85,700 numbers above -- then reverted to the
// assertion matching reality (no shrink).
func TestCompaction_TriggersAndShrinksNextRequest(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	sess, _, srv := compactionRunToTrigger(t, home, sessDir, proj, 2)

	entries := compactionEntries(t, sess)
	compactionEntry := compactionFindEntry(entries, session.EntryCompaction)
	if compactionEntry == nil {
		var types []string
		for _, e := range entries {
			types = append(types, string(e.Type))
		}
		t.Fatalf("no compaction entry found in session; entry types were: %v", types)
	}

	reqs := srv.Requests()
	sumIdx := compactionFindSummarizerIndex(reqs)
	if sumIdx <= 0 || sumIdx >= len(reqs)-1 {
		t.Fatalf("expected a summarization request strictly between two conversation requests, got sumIdx=%d of %d requests", sumIdx, len(reqs))
	}
	before := reqs[sumIdx-1]
	after := reqs[sumIdx+1]
	// The request after compaction must be smaller: the turn loop builds the
	// transcript from the last compaction entry onward
	// (compaction.ContextMessages via entriesToTranscript). Break to
	// verify: make entriesToTranscript project every entry again.
	if len(after.Messages) >= len(before.Messages) {
		t.Errorf("post-compaction request (%d bytes) is not smaller than pre-compaction (%d bytes)", len(after.Messages), len(before.Messages))
	}
}

// TestCompaction_SummaryReachesModel checks whether the compaction entry's
// summary text (which carries compactionSummaryMarker -- see this file's
// doc comment on why the marker lands there deterministically) shows up in
// any later conversation request's messages.
//
// Documents ACTUAL behaviour: it does not, in any request, ever. Root
// cause: internal/harness/lane.go's entriesToTranscript only converts
// session.EntryMessage entries to messages; a session.EntryCompaction entry
// (which is what the summary is stored as) produces nothing, so the model
// never receives it as conversation content -- it only exists on disk.
// Desired behaviour, per this task's plan ("assert the compaction entry's
// summary text appears in the following request's messages"): a compaction
// entry should be turned into a summary message the same way
// internal/compaction/estimate.go's sessionEntryToContextMessages already
// does (via wrapCompactionSummary) for its own token-estimate purposes.
//
// Proved able to fail: temporarily required the marker to be PRESENT in
// the request right after compaction (the plan's original assumption,
// `if !strings.Contains(...) { t.Errorf(...) }`) -- went red (it is
// absent) -- then reverted to the assertion matching reality.
func TestCompaction_SummaryReachesModel(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	sess, _, srv := compactionRunToTrigger(t, home, sessDir, proj, 2)

	entries := compactionEntries(t, sess)
	compactionEntry := compactionFindEntry(entries, session.EntryCompaction)
	if compactionEntry == nil {
		t.Fatal("no compaction entry found in session")
	}
	if !strings.Contains(compactionEntry.Summary, compactionSummaryMarker) {
		t.Fatalf("compaction entry summary missing marker %q; summary=%q", compactionSummaryMarker, compactionEntry.Summary)
	}

	reqs := srv.Requests()
	sumIdx := compactionFindSummarizerIndex(reqs)
	if sumIdx < 0 {
		t.Fatalf("no summarization request found (of %d requests)", len(reqs))
	}
	// The summary must reach the model in the very next conversation
	// request after the summarizer call: the transcript is built from the
	// compaction entry onward, with its summary in front. Break to verify:
	// make entriesToTranscript project every entry again (the marker then
	// appears in no later request).
	if sumIdx+1 >= len(reqs) {
		t.Fatalf("no conversation request followed the summarization request (%d requests)", len(reqs))
	}
	if !strings.Contains(string(reqs[sumIdx+1].Messages), compactionSummaryMarker) {
		t.Errorf("request %d (the first after compaction) does not carry the summary marker", sumIdx+1)
	}
}

// TestCompaction_ResumeKeepsCompactedHistory runs the same trigger-compaction
// flow, then starts a second harness process with `--resume <id>` against a
// fresh faux-2 script, and inspects the FIRST request the resumed process
// makes.
//
// Documents ACTUAL behaviour, confirmed deterministic across repeated runs
// (85,845 bytes every time): --resume replays the FULL, uncompacted
// history, the same as a live continuing session does (see this file's
// package doc comment on entriesToTranscript/ScanBranch not respecting
// compaction boundaries) -- it does not even improve on the live case.
// Desired behaviour, per this task's plan ("assert the first request of
// the resumed process is the compacted shape"): resuming should load from
// the branch tip and stop at the last compaction entry, same as
// internal/compaction/estimate.go's buildContextEntries already does for
// its own estimate.
//
// Proved able to fail: temporarily required the request to be small
// (`if len(first.Messages) > 2000 { t.Errorf(...) }`, the plan's original
// assumption) -- went red with "resumed run's first request is 85845
// bytes" -- then reverted to the assertion matching reality (full replay).
func TestCompaction_ResumeKeepsCompactedHistory(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	sess, _, _ := compactionRunToTrigger(t, home, sessDir, proj, 2)
	// The file name's own id fragment is URL-escaped and not reliably what
	// --resume expects to match against; read the JSONL header's own "id"
	// field instead (same approach test/e2e/session_test.go's own resume
	// tests use, via its sessionHeaderID helper -- reused here rather than
	// duplicated, since it is read-only and this task's file only needs to
	// call it, not own it).
	sessionID := sessionHeaderID(t, sess)

	addr2, srv2 := startFaux(t, `model: faux-2
steps:
  - text: "resumed reply"
`)
	env2 := baseEnv(home, sessDir, addr2)
	env2["HARNESS_MODEL"] = "faux/faux-2"
	res2 := runHarness(t, proj, env2, "-p", "continue", "--output-format", "text",
		"--permission-mode", "bypassPermissions", "--resume", sessionID)
	if res2.Code != 0 {
		t.Fatalf("resume run: exit %d, stdout=%q stderr=%q", res2.Code, res2.Stdout, res2.Stderr)
	}

	reqs2 := srv2.Requests()
	if len(reqs2) == 0 {
		t.Fatal("resumed run made no requests")
	}
	first := reqs2[0]
	// A resumed session builds its transcript the same compaction-aware
	// way, so its first request carries the summary and is the small,
	// compacted shape (the uncompacted replay measured 85,845 bytes).
	if !strings.Contains(string(first.Messages), compactionSummaryMarker) {
		t.Errorf("resumed run's first request does not carry the summary marker")
	}
	if len(first.Messages) > 50_000 {
		t.Errorf("resumed run's first request is %d bytes: the full uncompacted history, not the compacted shape", len(first.Messages))
	}
}

// TestCompaction_PreCompactHookFires wires testdata/hooks/record-payload.sh
// on PreCompact and drives the same compaction trigger as the tests above.
// The recorded payload must name the event and carry trigger "auto"
// (internal/cli/chat.go:700 fires it from the harness's before-compaction
// hook). Break to verify: remove the PreCompact wiring in chat.go; the
// payload file is then never written.
func TestCompaction_PreCompactHookFires(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	payload := filepath.Join(t.TempDir(), "precompact.json")
	script := hookScript(t, "record-payload.sh")
	writeHookSettings(t, proj, "PreCompact", "", fmt.Sprintf("HARNESS_TEST_PAYLOAD_FILE=%s %s", payload, script))

	compactionRunToTrigger(t, home, sessDir, proj, 0)

	raw, err := os.ReadFile(payload)
	if err != nil {
		t.Fatalf("PreCompact hook never wrote a payload: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("parse PreCompact payload: %v\n%s", err, raw)
	}
	if got["hook_event_name"] != "PreCompact" {
		t.Errorf("hook_event_name = %v, want PreCompact", got["hook_event_name"])
	}
	if got["trigger"] != "auto" {
		t.Errorf("trigger = %v, want auto", got["trigger"])
	}
}
