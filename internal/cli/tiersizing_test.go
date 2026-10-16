package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/budget"
	claudeagents "github.com/andrepato/harness/internal/claude/agents"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/claude/skills"
	mcpgate "github.com/andrepato/harness/internal/mcp"
	"github.com/andrepato/harness/internal/provider"
	fauxprovider "github.com/andrepato/harness/internal/provider/faux"
	tkfaux "github.com/andrepato/harness/internal/testkit/faux"
	"github.com/andrepato/harness/internal/tool"
	"github.com/andrepato/harness/internal/tools"
)

// longSkills are skills whose descriptions all fit faux-1's 128k skills
// listing (1,280 tokens) but not faux-2's 32k one (328): each description
// ends in a marker that only a full listing carries.
func longSkills() []skills.Skill {
	var out []skills.Skill
	for i := 0; i < 8; i++ {
		out = append(out, skills.Skill{
			Name:        fmt.Sprintf("skill-%d", i),
			Description: strings.Repeat("describes a long and careful procedure ", 9) + fmt.Sprintf("FULLDESC-%d", i),
			FilePath:    fmt.Sprintf("/skills/skill-%d/SKILL.md", i),
		})
	}
	return out
}

// longAgent's description fits the medium tier's 300-character limit in
// the task tool's text, but not the small tier's 120.
var longAgent = claudeagents.Definition{
	Name:        "reviewer",
	Description: strings.Repeat("reviews a change for bugs ", 8) + "AGENTTAIL",
	Prompt:      "review",
}

// sizingRig is a session on faux-1 wired the way chat.go wires it: a
// task tool, a sessionSizing as started.OnTierChanged, and an MCP session.
type sizingRig struct {
	srv     *tkfaux.Server
	reg     *provider.Registry
	started *agent.Started
	sizing  *sessionSizing
	mcpSess *mcpSession
	loads   []int
}

func newSizingRig(t *testing.T, script string) *sizingRig {
	t.Helper()
	srv, err := tkfaux.New(tkfaux.Options{ScriptYAML: script})
	if err != nil {
		t.Fatal(err)
	}
	addr, err := srv.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	t.Setenv("HARNESS_FAUX_ADDR", addr)
	t.Setenv("HARNESS_FAUX_API", "anthropic-messages")
	fp, _ := fauxprovider.New()
	reg := provider.NewRegistry(nil)
	reg.Register(fp)
	resolved, err := reg.Resolve(fauxprovider.ProviderID, "faux-1")
	if err != nil {
		t.Fatal(err)
	}

	rig := &sizingRig{srv: srv, reg: reg}
	list := longSkills()
	// Each load is numbered, as if the files it reads had changed since
	// the last one: a prompt rebuilt from a fresh load would differ.
	memo := newTierPromptMemo(func(tier budget.Tier) tierPrompt {
		rig.loads = append(rig.loads, tier.ContextWindow)
		return tierPrompt{Memory: fmt.Sprintf("memory load %d", len(rig.loads)), Skills: formatSkillsIndex(list, tier.SkillsListingTokens)}
	})
	memo.seed(resolved.Tier, tierPrompt{Memory: "memory at startup", Skills: formatSkillsIndex(list, resolved.Tier.SkillsListingTokens)})
	dispatch := func(ctx context.Context, req tools.TaskRequest) (tools.TaskDispatchResult, error) {
		return tools.TaskDispatchResult{}, nil
	}
	agentsList := []claudeagents.Definition{longAgent}
	rig.mcpSess = &mcpSession{tier: resolved.Tier, resident: []string{"task"}, state: mcpgate.NewGateState()}
	rig.sizing = &sessionSizing{
		mcpSess: rig.mcpSess,
		memo:    memo,
		build: func(tp tierPrompt, scratchpad, mcpIndex string) string {
			return strings.Join(nonEmpty([]string{"base", scratchpad, tp.Memory, tp.AutoMemory, tp.Skills, mcpIndex}), "\n\n")
		},
		mcpIndex: "mcp index",
		taskTool: func(tier budget.Tier) *tool.Tool { return tools.TaskTool(dispatch, agentsList, nil, tier) },
	}
	started, err := agent.Start(context.Background(), agent.Options{
		Registry: reg, Resolved: resolved, Cwd: t.TempDir(), SessionsRoot: t.TempDir(),
		SystemPrompt:    rig.sizing.prompt(resolved.Tier),
		ExtraTools:      []*tool.Tool{rig.sizing.taskTool(resolved.Tier)},
		ActiveToolNames: []string{"task"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = started.Harness.Close() })
	rig.mcpSess.lane = started.Lane
	rig.sizing.started = started
	started.OnTierChanged = rig.sizing.onTierChanged
	rig.started = started
	return rig
}

// lastBody is the raw body of the last request sent to model.
func (r *sizingRig) lastBody(t *testing.T, model string) string {
	t.Helper()
	var body string
	for _, req := range r.srv.Requests() {
		if req.Model == model {
			body = string(req.Body)
		}
	}
	if body == "" {
		t.Fatalf("no request to %s", model)
	}
	return body
}

// Going A→B→A gives A the byte-identical prompt it had: each window's
// tier-sized part is loaded once and reused, so A's prompt cache can
// still hit after a detour through B.
func TestTierPromptMemoIsDeterministicAcrossSwitches(t *testing.T) {
	rig := newSizingRig(t, "model: faux-1\nsteps:\n  - text: \"hi\"\n")
	ctx := context.Background()
	a := rig.started.CurrentTier()
	promptA := rig.started.Harness.SystemPrompt()

	agent.Retier(ctx, rig.started, budget.TierForWindow(32_768))
	promptB := rig.started.Harness.SystemPrompt()
	agent.Retier(ctx, rig.started, a)
	if got := rig.started.Harness.SystemPrompt(); got != promptA {
		t.Errorf("A's prompt after A→B→A differs:\nbefore: %q\nafter:  %q", promptA, got)
	}
	agent.Retier(ctx, rig.started, budget.TierForWindow(32_768))
	if got := rig.started.Harness.SystemPrompt(); got != promptB {
		t.Errorf("B's prompt on the second visit differs")
	}
	if promptA == promptB {
		t.Error("A's and B's prompts are the same: the skills listing was not re-sized")
	}
	if len(rig.loads) != 1 || rig.loads[0] != 32_768 {
		t.Errorf("loads = %v, want B's window once (A was seeded)", rig.loads)
	}
}

// Regression: /model from faux-1 (128k, medium) to faux-2 (32k, small)
// re-sizes the system prompt's skills listing and the task tool's text.
// Before, agent.SetModel moved only compaction and the MCP tier, so
// faux-2 was sent faux-1's full listing and untruncated agent text.
func TestSwitchModelResizesThePromptAndTaskTool(t *testing.T) {
	rig := newSizingRig(t, `
models:
  faux-1:
    - text: "one"
  faux-2:
    - text: "two"
`)
	ctx := context.Background()
	if _, err := rig.started.Lane.Prompt(ctx, "first", nil); err != nil {
		t.Fatal(err)
	}
	before := rig.started.Harness.SystemPrompt()
	if !strings.Contains(before, "FULLDESC-7") {
		t.Fatalf("faux-1's prompt lacks the full skills listing:\n%s", before)
	}
	if body := rig.lastBody(t, "faux-1"); !strings.Contains(body, "AGENTTAIL") {
		t.Fatalf("faux-1's task tool lacks the full agent description:\n%s", body)
	}

	tier, err := switchModel(ctx, rig.reg, rig.started, rig.mcpSess, fauxprovider.ProviderID, "faux-2")
	if err != nil {
		t.Fatal(err)
	}
	if tier.Name != "small" {
		t.Fatalf("faux-2 tier = %s, want small", tier.Name)
	}
	after := rig.started.Harness.SystemPrompt()
	if after == before || strings.Contains(after, "FULLDESC-7") {
		t.Errorf("the system prompt was not re-sized for faux-2:\n%s", after)
	}
	if after != rig.sizing.prompt(tier) {
		t.Errorf("the system prompt is not faux-2's sized prompt")
	}
	if _, err := rig.started.Lane.Prompt(ctx, "second", nil); err != nil {
		t.Fatal(err)
	}
	body := rig.lastBody(t, "faux-2")
	if strings.Contains(body, "AGENTTAIL") || strings.Contains(body, "FULLDESC-7") {
		t.Errorf("faux-2 was sent faux-1's task tool text or skills listing:\n%s", body)
	}
	if !strings.Contains(body, "reviewer") {
		t.Errorf("faux-2's request lost the task tool:\n%s", body)
	}
	rig.mcpSess.mu.Lock()
	mcpTier := rig.mcpSess.tier
	rig.mcpSess.mu.Unlock()
	if mcpTier.Name != "small" {
		t.Errorf("mcp session tier = %s, want small", mcpTier.Name)
	}
}

// A planComplex target whose window is too small to run a session falls
// back like any other unusable target, with a note, instead of making
// kiln/auto unselectable (it used to be sized to its smallest target, and
// Registry.Resolve refused that window).
func TestRouterTooSmallTargetFallsBack(t *testing.T) {
	reg := routerRegistry(t)
	reg.Register(stubProvider{id: "tiny", models: []provider.Model{{Provider: "tiny", ID: "t4k", Api: provider.ApiAnthropicMessages, ContextWindow: 4096, MaxTokens: 1024, Input: []string{"text"}}}})
	s, warnings := runSetup(t, reg, claudesettings.ModelRouterConfig{ModelRoles: map[string]string{
		"fast": "faux/faux-2", "structured": "faux/faux-1", "heavy": "tiny/t4k",
	}})
	if !s.Registered {
		t.Fatalf("kiln/auto not registered: %s", s.Reason)
	}
	if _, err := reg.Resolve("kiln", "auto"); err != nil {
		t.Fatalf("Resolve(kiln/auto): %v", err)
	}
	if got := ids(s.Targets); got != "faux-1,faux-1,faux-2,faux-2" {
		t.Errorf("targets = %s, want planComplex on plan's faux-1", got)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %q, want one note", warnings)
	}
	w := warnings[0]
	for _, want := range []string{"tiny/t4k's 4,096-token window is too small to run", "planComplex", "faux/faux-1"} {
		if !strings.Contains(w, want) {
			t.Errorf("note %q lacks %q", w, want)
		}
	}
	if len(s.FallbackReasons) != 1 || !strings.Contains(s.FallbackReasons[0], "too small") {
		t.Errorf("fallback reasons = %q", s.FallbackReasons)
	}
}

func TestGroupThousands(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 4096: "4,096", 8192: "8,192", 1000000: "1,000,000", -1234: "-1,234"} {
		if got := groupThousands(n); got != want {
			t.Errorf("groupThousands(%d) = %q, want %q", n, got, want)
		}
	}
}
