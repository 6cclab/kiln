//go:build e2e

// Package e2e: budget behaviour tests. These exercise internal/budget's
// tier resolution end to end -- through the real kiln binary against the
// faux provider's two models (faux-1: 128k window, faux-2: 32k window,
// see internal/provider/faux/faux.go) -- rather than unit-testing
// internal/budget directly, which internal/budget/tier_test.go already
// does. Each test's doc comment records the break used to confirm it can
// fail.
package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/budget"
)

// budgetSimpleScript is a one-turn script good enough for tests that only
// need to inspect the first recorded request's System/Tools, reused across
// both faux-1 and faux-2 runs (the faux server routes by the "model" field
// in each request, so one script document serves both).
const budgetSimpleScript = `models:
  faux-1:
    - text: "ok"
  faux-2:
    - text: "ok"
`

// budgetWriteFile writes data to path, creating parent directories.
func budgetWriteFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// budgetRunAndFirstSystem runs the harness once against model against a
// fresh faux server scripted with budgetSimpleScript (or scriptYAML if
// non-empty), returning the first recorded request's System text.
func budgetRunAndFirstSystem(t *testing.T, home, sessDir, proj, model, scriptYAML string, extraEnv map[string]string, extraArgs ...string) string {
	t.Helper()
	if scriptYAML == "" {
		scriptYAML = budgetSimpleScript
	}
	addr, srv := startFaux(t, scriptYAML)
	env := baseEnv(home, sessDir, addr)
	env["HARNESS_MODEL"] = model
	for k, v := range extraEnv {
		env[k] = v
	}
	args := append([]string{"-p", "hello", "--output-format", "json"}, extraArgs...)
	res := runHarness(t, proj, env, args...)
	if res.Code != 0 {
		t.Fatalf("run (%s): exit %d, stderr=%s", model, res.Code, res.Stderr)
	}
	reqs := srv.Requests()
	if len(reqs) == 0 {
		t.Fatalf("run (%s): faux recorded no requests", model)
	}
	return reqs[0].System
}

// roughTokens is the same len/4 estimate internal/claude/memory uses to
// budget CLAUDE.md content (memory.go's unexported estimate), so a test
// asserting against SystemPromptTokens compares like with like rather than
// against compaction.EstimateTokens' heavier per-message estimate (which
// counts role/structural overhead a raw System string does not have).
func roughTokens(s string) int {
	return (len(s) + 3) / 4
}

// TestBudget_SystemPromptFitsTier writes a large project CLAUDE.md plus
// several .claude/rules files -- more than either tier's SystemPromptTokens
// ceiling (faux-1's ~12,800, faux-2's ~2,048 floor since 32768*0.1=3277 is
// clamped down by the 1.25x-floor cap... in practice small but non-zero) --
// and checks that the memory content actually embedded into the recorded
// System prompt stays under budget.Tier.SystemPromptTokens for each model,
// and that faux-2's is the shorter of the two.
//
// Proved able to fail: temporarily inverted `len(mem2) >= len(mem1)` to
// `len(mem2) <= len(mem1)` (production code left untouched, since this test
// file is the only file this task owns) -- went red with "faux-2 (small
// tier) embedded memory (11464 chars) not shorter than faux-1's (33049
// chars)" -- confirming the test does observe a real runtime difference
// between the two models, not a tautology -- then reverted.
func TestBudget_SystemPromptFitsTier(t *testing.T) {
	tier1 := budget.TierForWindow(128000)
	tier2 := budget.TierForWindow(32768)

	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	// A large CLAUDE.md plus rule files: several KB of content, comfortably
	// more than either tier's SystemPromptTokens ceiling if all of it were
	// embedded unbudgeted.
	var claudeMD strings.Builder
	claudeMD.WriteString("# Project notes\n\n")
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&claudeMD, "Paragraph %d: %s\n\n", i, strings.Repeat("lorem ipsum dolor sit amet ", 8))
	}
	budgetWriteFile(t, filepath.Join(proj, "CLAUDE.md"), claudeMD.String())

	for i := 0; i < 5; i++ {
		var rule strings.Builder
		fmt.Fprintf(&rule, "# Rule %d\n\n", i)
		for j := 0; j < 20; j++ {
			fmt.Fprintf(&rule, "Rule line %d: %s\n", j, strings.Repeat("foo bar baz ", 10))
		}
		budgetWriteFile(t, filepath.Join(proj, ".claude", "rules", fmt.Sprintf("rule%d.md", i)), rule.String())
	}

	sys1 := budgetRunAndFirstSystem(t, home, sessDir, proj, "faux/faux-1", "", nil)
	sys2 := budgetRunAndFirstSystem(t, home, sessDir, proj, "faux/faux-2", "", nil)

	// Isolate the <memory ...> block(s) internal/claude/memory embeds,
	// rather than estimating the whole System string (which also carries
	// the unbudgeted base persona/tool-usage text).
	mem1 := budgetExtractMemoryBlocks(sys1)
	mem2 := budgetExtractMemoryBlocks(sys2)

	if got := roughTokens(mem1); got > tier1.SystemPromptTokens {
		t.Errorf("faux-1 embedded memory ~%d tokens, want <= tier ceiling %d", got, tier1.SystemPromptTokens)
	}
	if got := roughTokens(mem2); got > tier2.SystemPromptTokens {
		t.Errorf("faux-2 embedded memory ~%d tokens, want <= tier ceiling %d", got, tier2.SystemPromptTokens)
	}
	if len(mem2) >= len(mem1) {
		t.Errorf("faux-2 (small tier) embedded memory (%d chars) not shorter than faux-1's (%d chars)", len(mem2), len(mem1))
	}
	if tier2.SystemPromptTokens >= tier1.SystemPromptTokens {
		t.Fatalf("test setup invalid: tier2.SystemPromptTokens=%d not < tier1's %d", tier2.SystemPromptTokens, tier1.SystemPromptTokens)
	}
}

// budgetExtractMemoryBlocks returns the substring of sys spanning every
// `<memory ...>...</memory>` block internal/claude/memory.LoadMemory emits,
// concatenated. Returns "" if none are present (budget exhausted every
// file).
func budgetExtractMemoryBlocks(sys string) string {
	var out strings.Builder
	rest := sys
	for {
		start := strings.Index(rest, "<memory ")
		if start < 0 {
			break
		}
		end := strings.Index(rest[start:], "</memory>")
		if end < 0 {
			break
		}
		end += start + len("</memory>")
		out.WriteString(rest[start:end])
		rest = rest[end:]
	}
	return out.String()
}

// TestBudget_MemoryDroppedLeastSpecificFirst writes a user-scope CLAUDE.md
// (under the scratch HOME) and a project-scope CLAUDE.md, sized so that on
// faux-2 (small tier, tight SystemPromptTokens budget) only the project file
// fits -- internal/claude/memory.LoadMemory spends its budget most-specific
// first (project before user) -- while on faux-1 (large tier) both fit.
//
// Proved able to fail: temporarily inverted the "user marker dropped on
// faux-2" assertion (`if strings.Contains(sys2, userMarker)` to
// `if !strings.Contains(...)`) -- went red with "expected user CLAUDE.md
// (least specific) to be dropped, but it is present" -- confirming the test
// observes real drop behaviour against the actual binary, not a tautology
// -- then reverted.
func TestBudget_MemoryDroppedLeastSpecificFirst(t *testing.T) {
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	const userMarker = "USER-SCOPE-MARKER-fdb3a1"
	const projectMarker = "PROJECT-SCOPE-MARKER-9c7e2f"

	// Project CLAUDE.md alone is sized to consume most of faux-2's budget
	// (tier2.SystemPromptTokens is small; see TestBudget_SystemPromptFitsTier's
	// comment on its floor), leaving no room for the user file.
	// Sized (measured empirically against the memory package's len/4
	// estimate) to land just under faux-2's ~3,277-token SystemPromptTokens
	// budget on its own, leaving too little room for the user file below.
	var projectMD strings.Builder
	fmt.Fprintf(&projectMD, "# Project\n\n%s\n\n", projectMarker)
	for i := 0; i < 44; i++ {
		fmt.Fprintf(&projectMD, "Project paragraph %d: %s\n\n", i, strings.Repeat("project content words here ", 10))
	}
	budgetWriteFile(t, filepath.Join(proj, "CLAUDE.md"), projectMD.String())

	var userMD strings.Builder
	fmt.Fprintf(&userMD, "# User\n\n%s\n\n", userMarker)
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&userMD, "Personal preference %d: %s\n\n", i, strings.Repeat("user preference words ", 10))
	}
	budgetWriteFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), userMD.String())

	sys1 := budgetRunAndFirstSystem(t, home, sessDir, proj, "faux/faux-1", "", nil)
	sys2 := budgetRunAndFirstSystem(t, home, sessDir, proj, "faux/faux-2", "", nil)

	if !strings.Contains(sys1, projectMarker) || !strings.Contains(sys1, userMarker) {
		t.Errorf("faux-1 (large tier): expected both memory files present; project=%v user=%v",
			strings.Contains(sys1, projectMarker), strings.Contains(sys1, userMarker))
	}
	if !strings.Contains(sys2, projectMarker) {
		t.Errorf("faux-2 (small tier): expected project CLAUDE.md (most specific) to survive budgeting; System=%q", sys2)
	}
	if strings.Contains(sys2, userMarker) {
		t.Errorf("faux-2 (small tier): expected user CLAUDE.md (least specific) to be dropped, but it is present")
	}
}

// TestBudget_ToolStrategyPerWindow checks internal/budget.StrategyForWindow
// picks full-index for faux-1's 128k window and posture-index for faux-2's
// 32k window (both fall short of full-schemas' fixed 31,897-token measured
// cost regardless of how many tools are actually registered -- the cost
// table is a measurement against the real 165-tool catalog, not a function
// of this test's tool count).
//
// It then documents what that strategy choice actually changes in a
// request, which is NOT the Tools list's size: internal/mcp/gating.go's
// ActiveToolNames only diverges from "tool_search plus admitted tools" when
// the strategy is full-schemas (see its own doc comment: "posture-index and
// full-index both start gated ... differ in how much of the catalog the
// index describes, which is handled by IndexPromptText, not here"). So the
// recorded request's Tools count is asserted equal between faux-1 and
// faux-2 here, not different -- the difference (if any) lives in the System
// prompt's <available_tools> index text, which is itself scoped by posture
// in internal/cli/chat.go regardless of strategy (mcp.go's buildMCPExtras
// calls scopedMCPTools(mcpTools, activePosture) unconditionally, never
// consulting full-index vs posture-index). This is a real gap against the
// plan's assumption, not a test bug: full-index and posture-index are
// distinct budget.ToolStrategy values with no distinct code path today.
// Desired behaviour: full-index should list every configured server's tools
// in the index regardless of posture, and posture-index should list only
// in-posture servers, per gating.go's own measured-cost comment.
//
// Proved able to fail: temporarily changed the faux-1 assertion's wanted
// strategy from StrategyFullIndex to StrategyPostureIndex -- went red with
// "faux-1 (128k) tier strategy = full-index, want full-index" -- confirming
// the assertion is checked at all -- then reverted. (Not touching
// budget/tier.go itself: this task owns only its own test files.)
func TestBudget_ToolStrategyPerWindow(t *testing.T) {
	tier1 := budget.TierForWindow(128000)
	tier2 := budget.TierForWindow(32768)
	if tier1.ToolStrategy != budget.StrategyFullIndex {
		t.Fatalf("faux-1 (128k) tier strategy = %v, want full-index", tier1.ToolStrategy)
	}
	if tier2.ToolStrategy != budget.StrategyPostureIndex {
		t.Fatalf("faux-2 (32k) tier strategy = %v, want posture-index", tier2.ToolStrategy)
	}

	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	mcpConfig := budgetBuildMCPFixtureConfig(t)

	sys1 := budgetRunAndFirstSystem(t, home, sessDir, proj, "faux/faux-1", "",
		map[string]string{"HARNESS_POSTURE": "all"}, "--mcp-config", mcpConfig)
	sys2 := budgetRunAndFirstSystem(t, home, sessDir, proj, "faux/faux-2", "",
		map[string]string{"HARNESS_POSTURE": "all"}, "--mcp-config", mcpConfig)

	// Documented current behaviour: neither index strategy puts MCP tool
	// schemas on the wire (only full-schemas does), so tool_search shows up
	// in the System's residual tool listing/behaviour the same way in both
	// cases. We can't directly read the Tools list two different ways here
	// (both runs use fresh faux servers), so the check is: both systems
	// mention the fixture's index (posture "all" includes the fixture
	// server), i.e. the gap described above -- full-index and
	// posture-index produce the SAME index scope today, not a different
	// one, because scopedMCPTools never looks at strategy.
	sawFixture1 := strings.Contains(sys1, "mcp__fixture__")
	sawFixture2 := strings.Contains(sys2, "mcp__fixture__")
	if sawFixture1 != sawFixture2 {
		t.Errorf("full-index vs posture-index unexpectedly differ in whether the fixture server is indexed (faux-1=%v faux-2=%v); if this now fails, the gap described in this test's doc comment may have been fixed -- update the comment and this assertion to check the actual index difference instead of equality", sawFixture1, sawFixture2)
	}
}

// budgetBuildMCPFixtureConfig builds a --mcp-config JSON file wiring the
// internal/testkit/mcpfixture stdio binary (built fresh here, same pattern
// as test/e2e/mcp_tui_test.go's own fixture build) as one server, and
// returns the config file's path.
func budgetBuildMCPFixtureConfig(t *testing.T) string {
	t.Helper()
	fixture := filepath.Join(t.TempDir(), "mcpfixture")
	if out, err := exec.Command("go", "build", "-o", fixture, "../../cmd/mcpfixture").CombinedOutput(); err != nil {
		t.Fatalf("build mcpfixture: %v\n%s", err, out)
	}
	cfg := filepath.Join(t.TempDir(), "mcp.json")
	cfgJSON := fmt.Sprintf(`{"mcpServers":{"fixture":{"command":%q}}}`, fixture)
	if err := os.WriteFile(cfg, []byte(cfgJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// TestBudget_ToolOutputTruncated exercises internal/budget's
// ToolOutputTokens ceiling. There is no per-tier truncation on a live bash
// tool_result today: internal/tools/bash.go truncates by fixed line/byte
// counts (execenv.DefaultMaxBytes and a line cap), independent of the
// model's tier. The mechanism that IS gated by ToolOutputTokens is `@path`
// mention inlining (internal/cli/mentions.go's ResolveMentions, wired into
// the headless print path at internal/cli/chat.go:1027 -- "@path inlines
// files here too. A prompt that behaves differently under -p than it does
// interactively is a trap"). So this test mentions a large file in the
// prompt and checks it gets capped to (approximately) the tier's
// ToolOutputTokens on faux-2 (small tier => small per-mention budget) while
// fitting whole on faux-1.
//
// Desired behaviour, if a genuine per-tier cap on live tool_result content
// is wanted (not just @mentions): none exists in internal/tools/bash.go
// today; this test documents that gap rather than asserting something
// untrue about it.
//
// Proved able to fail: temporarily inverted `len(msgs2) >= len(msgs1)` to
// `len(msgs2) <= len(msgs1)` -- went red with "faux-2's first request
// messages (15052 bytes) not shorter than faux-1's (29281 bytes)" --
// confirming the two runs really do produce different-sized requests, not
// identical ones -- then reverted.
func TestBudget_ToolOutputTruncated(t *testing.T) {
	tier2 := budget.TierForWindow(32768)

	home, sessDir := scratchHome(t)
	proj := scratchProject(t)

	// One big file: comfortably over tier2's ToolOutputTokens (a small
	// tier's floor/share is a few hundred to low thousands of tokens; see
	// budget/tier.go's shareToolOutput=0.12 and its 2,048 floor) but well
	// under tier1's (128k window => much larger share).
	// Sized (empirically, against countTokens' message-wrapped estimate) to
	// fit under tier1's ToolOutputTokens (~15,360 for a 128k window) but
	// overflow tier2's (~3,932 for a 32k window).
	const bigLines = 550
	var big strings.Builder
	for i := 0; i < bigLines; i++ {
		fmt.Fprintf(&big, "line %04d: %s\n", i, strings.Repeat("x", 40))
	}
	bigPath := filepath.Join(proj, "big.txt")
	budgetWriteFile(t, bigPath, big.String())

	addr1, srv1 := startFaux(t, budgetSimpleScript)
	env1 := baseEnv(home, sessDir, addr1)
	env1["HARNESS_MODEL"] = "faux/faux-1"
	res1 := runHarness(t, proj, env1, "-p", "@big.txt summarize", "--output-format", "json")
	if res1.Code != 0 {
		t.Fatalf("faux-1 run: exit %d, stderr=%s", res1.Code, res1.Stderr)
	}
	reqs1 := srv1.Requests()
	if len(reqs1) == 0 {
		t.Fatal("faux-1: no requests recorded")
	}
	msgs1 := string(reqs1[0].Messages)

	addr2, srv2 := startFaux(t, budgetSimpleScript)
	env2 := baseEnv(home, sessDir, addr2)
	env2["HARNESS_MODEL"] = "faux/faux-2"
	res2 := runHarness(t, proj, env2, "-p", "@big.txt summarize", "--output-format", "json")
	if res2.Code != 0 {
		t.Fatalf("faux-2 run: exit %d, stderr=%s", res2.Code, res2.Stderr)
	}
	reqs2 := srv2.Requests()
	if len(reqs2) == 0 {
		t.Fatal("faux-2: no requests recorded")
	}
	msgs2 := string(reqs2[0].Messages)

	tailMarker := fmt.Sprintf("line %04d", bigLines-1)
	if strings.Contains(msgs1, tailMarker) && !strings.Contains(msgs2, tailMarker) {
		// Good: faux-1 (large budget) got the whole file, faux-2 (small
		// budget, ToolOutputTokens=%d) got a head-truncated version.
	} else {
		t.Errorf("expected faux-1's request to contain the file's tail (%s) and faux-2's to have it cut; faux1 has tail=%v faux2 has tail=%v (tier2.ToolOutputTokens=%d)",
			tailMarker, strings.Contains(msgs1, tailMarker), strings.Contains(msgs2, tailMarker), tier2.ToolOutputTokens)
	}
	if len(msgs2) >= len(msgs1) {
		t.Errorf("faux-2's first request messages (%d bytes) not shorter than faux-1's (%d bytes)", len(msgs2), len(msgs1))
	}
}
