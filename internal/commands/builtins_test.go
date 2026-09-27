package commands

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/auth"
	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// fakeProvider is a minimal provider.Provider for tests: no network, no
// auth required, a fixed model list.
type fakeProvider struct {
	id     string
	models []provider.Model
}

func (f fakeProvider) ID() string                              { return f.id }
func (f fakeProvider) Name() string                            { return f.id }
func (f fakeProvider) Auth() provider.AuthSpec                 { return provider.AuthSpec{Kind: provider.AuthKindNone} }
func (f fakeProvider) Models() []provider.Model                { return f.models }
func (f fakeProvider) RefreshModels(ctx context.Context) error { return nil }
func (f fakeProvider) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	ch := make(chan msg.StreamEvent)
	close(ch)
	return ch, func() (*msg.AssistantMessage, error) { return nil, nil }
}

func testRegistry(t *testing.T) *provider.Registry {
	t.Helper()
	reg := provider.NewRegistry(auth.NewFileCredentialStore(t.TempDir()))
	reg.Register(fakeProvider{
		id: "ollama",
		models: []provider.Model{
			{ID: "qwen3.8:latest", Provider: "ollama", ContextWindow: 49_152},
			{ID: "qwen3-cc:latest", Provider: "ollama", ContextWindow: 32_768},
		},
	})
	reg.Register(fakeProvider{
		id: "anthropic",
		models: []provider.Model{
			{ID: "claude-opus-5", Provider: "anthropic", ContextWindow: 1_000_000},
		},
	})
	return reg
}

func modelCompletions(t *testing.T, prefix string) []Completion {
	t.Helper()
	source := BuiltinCommands(BuiltinDeps{
		Registry:     testRegistry(t),
		CurrentModel: func() (string, string) { return "", "" },
		CurrentTier:  func() budget.Tier { return budget.Tier{} },
	})
	cmds, err := source.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cmds {
		if c.Name == "model" {
			if c.ArgumentCompletions == nil {
				t.Fatal("no completions on /model")
			}
			return c.ArgumentCompletions(prefix)
		}
	}
	t.Fatal("no /model command")
	return nil
}

func TestModelCompletionsOffersEveryReachableModel(t *testing.T) {
	if got := len(modelCompletions(t, "")); got != 3 {
		t.Fatalf("got %d completions, want 3", got)
	}
}

func TestModelCompletionsFiltersOnAnyPartOfID(t *testing.T) {
	items := modelCompletions(t, "qwen3.8")
	if len(items) != 1 {
		t.Fatalf("got %d, want 1", len(items))
	}
	if items[0].Value != "ollama/qwen3.8:latest" {
		t.Fatalf("got %q", items[0].Value)
	}
}

func TestModelCompletionsFiltersByProvider(t *testing.T) {
	if got := len(modelCompletions(t, "anthropic")); got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
}

func TestModelCompletionsCaseInsensitive(t *testing.T) {
	if got := len(modelCompletions(t, "QWEN3.8")); got != 1 {
		t.Fatalf("got %d, want 1", got)
	}
}

func TestModelCompletionsValueIsProviderSlashModel(t *testing.T) {
	for _, item := range modelCompletions(t, "") {
		if !strings.Contains(item.Value, "/") {
			t.Fatalf("%q is not provider/model", item.Value)
		}
	}
}

func TestModelCompletionsDescriptionSaysWhatDiffers(t *testing.T) {
	items := modelCompletions(t, "qwen3.8")
	if len(items) != 1 {
		t.Fatal("expected exactly one match")
	}
	d := items[0].Description
	if !strings.Contains(d, "49.2k") || !strings.Contains(d, "medium") || !strings.Contains(d, "usable") {
		t.Fatalf("got %q", d)
	}
}

func TestModelCompletionsEmptyForNoMatch(t *testing.T) {
	if got := modelCompletions(t, "nonexistent-model"); got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

func TestFormatTokens(t *testing.T) {
	cases := map[int]string{
		1_000_000: "1.0m",
		200_000:   "200.0k",
		49_152:    "49.2k",
		999:       "999",
	}
	for n, want := range cases {
		if got := formatTokens(n); got != want {
			t.Fatalf("formatTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

// TestModelSwitchMovesTheTier is the Go analogue of completions.test.ts's
// "/model switching moves the whole posture": SwitchModel is the seam
// SetModel+re-gating happens behind, and this only checks that builtins.go
// calls it and reports the tier it returns -- the actual tier move is
// agent.SetModel's job, exercised in internal/agent's own tests.
func TestModelSwitchMovesTheTier(t *testing.T) {
	var switched struct {
		provider, model string
	}
	var changed struct {
		label string
		tier  budget.Tier
	}
	largeTier := budget.Tier{Name: "large", ContextWindow: 1_000_000}
	reg := testRegistry(t)

	source := BuiltinCommands(BuiltinDeps{
		Registry:     reg,
		CurrentModel: func() (string, string) { return "ollama", "qwen3.8:latest" },
		CurrentTier:  func() budget.Tier { return budget.Tier{Name: "medium", ContextWindow: 49_152} },
		SwitchModel: func(ctx context.Context, providerID, modelID string) (budget.Tier, error) {
			switched.provider, switched.model = providerID, modelID
			return largeTier, nil
		},
		OnModelChanged: func(label string, tier budget.Tier) {
			changed.label, changed.tier = label, tier
		},
	})
	cmds, err := source.Load()
	if err != nil {
		t.Fatal(err)
	}
	var model Command
	for _, c := range cmds {
		if c.Name == "model" {
			model = c
		}
	}
	if _, err := model.Run(context.Background(), "anthropic/claude-opus-5"); err != nil {
		t.Fatal(err)
	}
	if switched.provider != "anthropic" || switched.model != "claude-opus-5" {
		t.Fatalf("got %+v", switched)
	}
	if changed.label != "anthropic/claude-opus-5" || changed.tier.Name != "large" {
		t.Fatalf("got %+v", changed)
	}
}

func TestModelSwitchRejectsNonProviderModel(t *testing.T) {
	source := BuiltinCommands(BuiltinDeps{
		Registry:     testRegistry(t),
		CurrentModel: func() (string, string) { return "", "" },
		CurrentTier:  func() budget.Tier { return budget.Tier{} },
		SwitchModel: func(ctx context.Context, providerID, modelID string) (budget.Tier, error) {
			return budget.Tier{}, nil
		},
	})
	cmds, err := source.Load()
	if err != nil {
		t.Fatal(err)
	}
	var model Command
	for _, c := range cmds {
		if c.Name == "model" {
			model = c
		}
	}
	if _, err := model.Run(context.Background(), "nonsense"); err == nil {
		t.Fatal("expected an error for a target that is not provider/model")
	}
}

func modelCommand(t *testing.T, deps BuiltinDeps) Command {
	t.Helper()
	source := BuiltinCommands(deps)
	cmds, err := source.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cmds {
		if c.Name == "model" {
			return c
		}
	}
	t.Fatal("no /model command")
	return Command{}
}

func TestModelRolesPrintsAMessageWhenNoneConfigured(t *testing.T) {
	model := modelCommand(t, BuiltinDeps{
		Registry:     testRegistry(t),
		CurrentModel: func() (string, string) { return "", "" },
		CurrentTier:  func() budget.Tier { return budget.Tier{} },
	})
	res, err := model.Run(context.Background(), "roles")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Output) != 1 || !strings.Contains(res.Output[0], "no modelRoles configured") {
		t.Fatalf("got %v", res.Output)
	}
}

func TestModelRolesTableReportsResolvedAndUnresolvedRoles(t *testing.T) {
	model := modelCommand(t, BuiltinDeps{
		Registry:     testRegistry(t),
		CurrentModel: func() (string, string) { return "", "" },
		CurrentTier:  func() budget.Tier { return budget.Tier{} },
		ModelRoles: map[string]string{
			"heavy": "anthropic/claude-opus-5",
			"fast":  "ollama/does-not-exist",
		},
	})
	res, err := model.Run(context.Background(), "roles")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Output, "\n")
	if !strings.Contains(joined, "heavy") || !strings.Contains(joined, "anthropic/claude-opus-5") {
		t.Fatalf("missing resolved role row: %v", res.Output)
	}
	if !strings.Contains(joined, "fast") || !strings.Contains(joined, "unresolved") {
		t.Fatalf("missing unresolved role row: %v", res.Output)
	}
}

func TestCostCommandShowsByModelTable(t *testing.T) {
	reg := testRegistry(t)
	source := BuiltinCommands(BuiltinDeps{
		Registry:     reg,
		CurrentModel: func() (string, string) { return "ollama", "qwen3.8:latest" },
		CurrentTier:  func() budget.Tier { return budget.Tier{} },
		UsageByModel: func() map[string]msg.Usage {
			return map[string]msg.Usage{
				"ollama/qwen3.8:latest": {Input: 100, Output: 50, TotalTokens: 150},
				"anthropic/claude-opus-5": {
					Input: 10, Output: 5, TotalTokens: 15,
					Cost: msg.Cost{Total: 0.42},
				},
			}
		},
	})
	cmds, err := source.Load()
	if err != nil {
		t.Fatal(err)
	}
	var cost *Command
	for i := range cmds {
		if cmds[i].Name == "cost" {
			cost = &cmds[i]
		}
	}
	if cost == nil {
		t.Fatal("no cost command registered")
	}
	res, err := cost.Run(context.Background(), "")
	if err != nil {
		t.Fatalf("cost.Run: %v", err)
	}
	if len(res.Output) == 0 || !strings.HasPrefix(res.Output[0], "Session: $0.42") {
		t.Fatalf("output[0] = %v, want the design summary line leading with the total spend", res.Output)
	}
	out := strings.Join(res.Output, "\n")
	if !strings.Contains(out, "by model:") {
		t.Fatalf("output = %q, want a by-model section", out)
	}
	if !strings.Contains(out, "anthropic/claude-opus-5") || !strings.Contains(out, "$0.4200") {
		t.Fatalf("output = %q, want the priced model's cost", out)
	}
	if !strings.Contains(out, "ollama/qwen3.8:latest") || !strings.Contains(out, "cost -") {
		t.Fatalf("output = %q, want the free model's cost shown as \"-\"", out)
	}
}

// TestCostCommand_OneLineNoteForSingleModel pins the fix for
// *qa/findings/20260927T014721Z-cost-not-one-line-note.json*: the design
// (Terminal.dc.html:320) renders /cost as a single system note — "Session:
// $0.27 · 66k tokens in context · 71s" — not a rate table. With a single
// model's usage tracked (the common case), /cost's Output is exactly that
// one line (which tui/app.go's `len(Output) == 1` case renders as the
// design's note), with no "by model:" block appended, since a lone
// model's breakdown would only repeat the summary's own total.
func TestCostCommand_OneLineNoteForSingleModel(t *testing.T) {
	reg := testRegistry(t)
	source := BuiltinCommands(BuiltinDeps{
		Registry:     reg,
		CurrentModel: func() (string, string) { return "ollama", "qwen3.8:latest" },
		CurrentTier:  func() budget.Tier { return budget.Tier{} },
		UsageByModel: func() map[string]msg.Usage {
			return map[string]msg.Usage{
				"ollama/qwen3.8:latest": {Input: 100, Output: 50, TotalTokens: 150, Cost: msg.Cost{Total: 0.27}},
			}
		},
		ContextUsed: func() (int, bool) { return 66_000, true },
		SessionElapsed: func() time.Duration {
			return 71 * time.Second
		},
	})
	cmds, err := source.Load()
	if err != nil {
		t.Fatal(err)
	}
	var cost *Command
	for i := range cmds {
		if cmds[i].Name == "cost" {
			cost = &cmds[i]
		}
	}
	if cost == nil {
		t.Fatal("no cost command registered")
	}
	res, err := cost.Run(context.Background(), "")
	if err != nil {
		t.Fatalf("cost.Run: %v", err)
	}
	if len(res.Output) != 1 {
		t.Fatalf("Output = %v, want exactly one line (renders as the design's note) with a single model tracked", res.Output)
	}
	want := "Session: $0.27 · 66.0k tokens in context · 71s"
	if res.Output[0] != want {
		t.Errorf("Output[0] = %q, want %q", res.Output[0], want)
	}
}

// TestCostCommand_OmitsElapsedWhenUnknown checks the elapsed segment is
// left off rather than printing a false "0s" when SessionElapsed is nil
// or reports zero (BuiltinDeps.SessionElapsed's own doc comment).
func TestCostCommand_OmitsElapsedWhenUnknown(t *testing.T) {
	reg := testRegistry(t)
	source := BuiltinCommands(BuiltinDeps{
		Registry:     reg,
		CurrentModel: func() (string, string) { return "ollama", "qwen3.8:latest" },
		CurrentTier:  func() budget.Tier { return budget.Tier{} },
	})
	cmds, err := source.Load()
	if err != nil {
		t.Fatal(err)
	}
	var cost *Command
	for i := range cmds {
		if cmds[i].Name == "cost" {
			cost = &cmds[i]
		}
	}
	res, err := cost.Run(context.Background(), "")
	if err != nil {
		t.Fatalf("cost.Run: %v", err)
	}
	want := "Session: $0.00 · 0 tokens in context"
	if len(res.Output) != 1 || res.Output[0] != want {
		t.Errorf("Output = %v, want a single line %q with no elapsed segment", res.Output, want)
	}
}

// TestContextCommand_BuildsBreakdown checks /context's Result carries a
// ContextBreakdown (registry.go) built from the tier's fixed budgets and
// the live usage total, for the kiln TUI's RenderContext
// (internal/tui/context.go) — not just the plain-text Output rows -p mode
// still uses.
func TestContextCommand_BuildsBreakdown(t *testing.T) {
	tier := budget.Tier{
		Name:               "medium",
		ContextWindow:      200_000,
		SystemPromptTokens: 8_000,
		ToolStrategy:       budget.StrategyFullSchemas,
	}
	source := BuiltinCommands(BuiltinDeps{
		Registry:     testRegistry(t),
		CurrentModel: func() (string, string) { return "anthropic", "claude-opus-5" },
		CurrentTier:  func() budget.Tier { return tier },
		ModelLabel:   func() string { return "kiln-large" },
		ContextUsed:  func() (int, bool) { return 76_000, true },
	})
	cmds, err := source.Load()
	if err != nil {
		t.Fatal(err)
	}
	var ctxCmd *Command
	for i := range cmds {
		if cmds[i].Name == "context" {
			ctxCmd = &cmds[i]
		}
	}
	if ctxCmd == nil {
		t.Fatal("no context command registered")
	}
	res, err := ctxCmd.Run(context.Background(), "")
	if err != nil {
		t.Fatalf("context.Run: %v", err)
	}
	if res.Context == nil {
		t.Fatal("Result.Context is nil, want a ContextBreakdown")
	}
	if res.Context.ModelLabel != "kiln-large" {
		t.Errorf("ModelLabel = %q, want kiln-large", res.Context.ModelLabel)
	}
	if res.Context.Used != 76_000 || res.Context.Window != 200_000 {
		t.Errorf("got Used=%d Window=%d, want 76000/200000", res.Context.Used, res.Context.Window)
	}
	var conversation, free int
	for _, seg := range res.Context.Segments {
		switch seg.Label {
		case "Conversation":
			conversation = seg.Tokens
		case "Free":
			free = seg.Tokens
		}
	}
	toolsCost := budget.ToolStrategyCost[tier.ToolStrategy]
	wantConversation := 76_000 - 8_000 - toolsCost
	if conversation != wantConversation {
		t.Errorf("Conversation segment = %d, want %d", conversation, wantConversation)
	}
	if free != 200_000-76_000 {
		t.Errorf("Free segment = %d, want %d", free, 200_000-76_000)
	}
}

// TestContextCommand_SelfConsistentWhenUsedIsSmall covers finding 4's
// actual repro: a session that has barely used any tokens yet, well under
// the tier's fixed System+Tools budgets. Before the fix, the header
// showed the real (small) ContextUsed while the legend's System/Tools
// rows showed the tier's big fixed budgets and Free was computed
// independently as window-minus-used, so the four segments summed to
// well over Window and Conversation clamped to a lying zero. The fixed
// version must have all four segments sum to exactly Window, and the
// header must equal System+Tools+Conversation (not the raw ContextUsed).
func TestContextCommand_SelfConsistentWhenUsedIsSmall(t *testing.T) {
	tier := budget.Tier{
		Name:               "small",
		ContextWindow:      128_000,
		SystemPromptTokens: 12_800,
		ToolStrategy:       budget.StrategyFullIndex, // ToolStrategyCost = 7,597
	}
	source := BuiltinCommands(BuiltinDeps{
		Registry:     testRegistry(t),
		CurrentModel: func() (string, string) { return "faux", "faux-1" },
		CurrentTier:  func() budget.Tier { return tier },
		ContextUsed:  func() (int, bool) { return 5_000, true },
	})
	cmds, err := source.Load()
	if err != nil {
		t.Fatal(err)
	}
	var ctxCmd *Command
	for i := range cmds {
		if cmds[i].Name == "context" {
			ctxCmd = &cmds[i]
		}
	}
	if ctxCmd == nil {
		t.Fatal("no context command registered")
	}
	res, err := ctxCmd.Run(context.Background(), "")
	if err != nil {
		t.Fatalf("context.Run: %v", err)
	}
	if res.Context == nil {
		t.Fatal("Result.Context is nil")
	}

	sum := 0
	segByLabel := map[string]int{}
	for _, seg := range res.Context.Segments {
		sum += seg.Tokens
		segByLabel[seg.Label] = seg.Tokens
	}
	if sum != tier.ContextWindow {
		t.Errorf("segments sum to %d, want exactly Window %d", sum, tier.ContextWindow)
	}
	wantUsed := segByLabel["System prompt"] + segByLabel["Tools"] + segByLabel["Conversation"]
	if res.Context.Used != wantUsed {
		t.Errorf("Used = %d, want System+Tools+Conversation = %d", res.Context.Used, wantUsed)
	}
	// The real scenario this test pins: used(5000) < system+tools(20397),
	// so Conversation clamps to 0 and System+Tools themselves must clamp
	// so the total still comes out to exactly Window.
	if segByLabel["Conversation"] != 0 {
		t.Errorf("Conversation = %d, want 0 (used is well under System+Tools)", segByLabel["Conversation"])
	}
}

// TestContextCommand_FilesReadSegment covers defect 2:
// docs/kiln-design-handoff/Terminal.dc.html line 227 specifies a fifth
// "Files read" segment that buildContextBreakdown never produced. With
// FileReadTokens wired, the segment must appear, must be carved OUT of
// Conversation (not double-counted on top of it), and the five segments
// must still sum to exactly Window with the header equal to their sum.
func TestContextCommand_FilesReadSegment(t *testing.T) {
	tier := budget.Tier{
		Name:               "medium",
		ContextWindow:      200_000,
		SystemPromptTokens: 8_000,
		ToolStrategy:       budget.StrategyFullSchemas,
	}
	source := BuiltinCommands(BuiltinDeps{
		Registry:       testRegistry(t),
		CurrentModel:   func() (string, string) { return "anthropic", "claude-opus-5" },
		CurrentTier:    func() budget.Tier { return tier },
		ModelLabel:     func() string { return "kiln-large" },
		ContextUsed:    func() (int, bool) { return 76_000, true },
		FileReadTokens: func() (int, bool) { return 20_000, true },
	})
	cmds, err := source.Load()
	if err != nil {
		t.Fatal(err)
	}
	var ctxCmd *Command
	for i := range cmds {
		if cmds[i].Name == "context" {
			ctxCmd = &cmds[i]
		}
	}
	if ctxCmd == nil {
		t.Fatal("no context command registered")
	}
	res, err := ctxCmd.Run(context.Background(), "")
	if err != nil {
		t.Fatalf("context.Run: %v", err)
	}
	if res.Context == nil {
		t.Fatal("Result.Context is nil")
	}

	sum := 0
	segByLabel := map[string]int{}
	for _, seg := range res.Context.Segments {
		sum += seg.Tokens
		segByLabel[seg.Label] = seg.Tokens
	}
	if sum != tier.ContextWindow {
		t.Errorf("segments sum to %d, want exactly Window %d", sum, tier.ContextWindow)
	}
	if segByLabel["Files read"] != 20_000 {
		t.Errorf("Files read = %d, want 20000", segByLabel["Files read"])
	}
	toolsCost := budget.ToolStrategyCost[tier.ToolStrategy]
	wantConversation := 76_000 - 8_000 - toolsCost - 20_000
	if segByLabel["Conversation"] != wantConversation {
		t.Errorf("Conversation = %d, want %d (used minus system, tools AND files read — not double-counted)", segByLabel["Conversation"], wantConversation)
	}
	wantUsed := segByLabel["System prompt"] + segByLabel["Tools"] + segByLabel["Files read"] + segByLabel["Conversation"]
	if res.Context.Used != wantUsed {
		t.Errorf("Used = %d, want System+Tools+FilesRead+Conversation = %d", res.Context.Used, wantUsed)
	}
}

// TestContextCommand_NoFileReadTokensOmitsSegmentValue covers the "don't
// fabricate" requirement: a nil FileReadTokens (no caller has wired real
// data) must leave the segment at zero rather than showing a guessed
// number, while every other segment behaves exactly as before this
// change.
func TestContextCommand_NoFileReadTokensOmitsSegmentValue(t *testing.T) {
	tier := budget.Tier{
		Name:               "medium",
		ContextWindow:      200_000,
		SystemPromptTokens: 8_000,
		ToolStrategy:       budget.StrategyFullSchemas,
	}
	source := BuiltinCommands(BuiltinDeps{
		Registry:     testRegistry(t),
		CurrentModel: func() (string, string) { return "anthropic", "claude-opus-5" },
		CurrentTier:  func() budget.Tier { return tier },
		ModelLabel:   func() string { return "kiln-large" },
		ContextUsed:  func() (int, bool) { return 76_000, true },
	})
	cmds, err := source.Load()
	if err != nil {
		t.Fatal(err)
	}
	var ctxCmd *Command
	for i := range cmds {
		if cmds[i].Name == "context" {
			ctxCmd = &cmds[i]
		}
	}
	if ctxCmd == nil {
		t.Fatal("no context command registered")
	}
	res, err := ctxCmd.Run(context.Background(), "")
	if err != nil {
		t.Fatalf("context.Run: %v", err)
	}
	for _, seg := range res.Context.Segments {
		if seg.Label == "Files read" && seg.Tokens != 0 {
			t.Errorf("Files read = %d with no FileReadTokens source, want 0 (never fabricated)", seg.Tokens)
		}
	}
}

// TestContextCommand_MeasuredSystemPromptOverridesTierBudget covers
// deps.SystemPromptTokens taking priority over the tier's fixed ceiling.
func TestContextCommand_MeasuredSystemPromptOverridesTierBudget(t *testing.T) {
	tier := budget.Tier{
		Name:               "small",
		ContextWindow:      128_000,
		SystemPromptTokens: 12_800,
		ToolStrategy:       budget.StrategyFullIndex,
	}
	source := BuiltinCommands(BuiltinDeps{
		Registry:           testRegistry(t),
		CurrentModel:       func() (string, string) { return "faux", "faux-1" },
		CurrentTier:        func() budget.Tier { return tier },
		ContextUsed:        func() (int, bool) { return 20_000, true },
		SystemPromptTokens: func() (int, bool) { return 900, true },
	})
	cmds, err := source.Load()
	if err != nil {
		t.Fatal(err)
	}
	var ctxCmd *Command
	for i := range cmds {
		if cmds[i].Name == "context" {
			ctxCmd = &cmds[i]
		}
	}
	res, err := ctxCmd.Run(context.Background(), "")
	if err != nil {
		t.Fatalf("context.Run: %v", err)
	}
	for _, seg := range res.Context.Segments {
		if seg.Label == "System prompt" && seg.Tokens != 900 {
			t.Errorf("System prompt = %d, want the measured 900, not the tier's 12800 budget", seg.Tokens)
		}
	}
}

// TestContextBreakdown_NeverExceedsMeasuredUsed pins the invariant that
// /context cannot claim more context occupancy than was actually
// measured. The static per-strategy Tools estimate can overshoot what a
// provider really bills: a real ollama session measured 10,677 tokens
// while the estimates alone summed to 16.3k, which drove Conversation to
// zero and made the header contradict the pinned status meter.
func TestContextBreakdown_NeverExceedsMeasuredUsed(t *testing.T) {
	const used = 10677
	deps := BuiltinDeps{
		CurrentModel:       func() (string, string) { return "ollama", "qwen3.8" },
		ContextUsed:        func() (int, bool) { return used, true },
		SystemPromptTokens: func() (int, bool) { return 8500, true },
		FileReadTokens:     func() (int, bool) { return 173, true },
	}
	tier := budget.Tier{ContextWindow: 49000, ToolStrategy: budget.StrategyFullSchemas}

	b := buildContextBreakdown(deps, tier)
	if b == nil {
		t.Fatal("buildContextBreakdown returned nil")
	}
	if b.Used != used {
		t.Errorf("Used = %d, want the measured figure %d (the status meter reports this)", b.Used, used)
	}

	sum := 0
	var free int
	for _, seg := range b.Segments {
		if seg.Label == "Free" {
			free = seg.Tokens
			continue
		}
		sum += seg.Tokens
		if seg.Tokens < 0 {
			t.Errorf("segment %q has negative tokens %d", seg.Label, seg.Tokens)
		}
	}
	if sum != used {
		t.Errorf("occupied segments sum to %d, want %d", sum, used)
	}
	if sum+free != tier.ContextWindow {
		t.Errorf("segments sum to %d, want the full window %d", sum+free, tier.ContextWindow)
	}
}
