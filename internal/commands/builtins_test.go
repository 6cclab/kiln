package commands

import (
	"context"
	"strings"
	"testing"

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
