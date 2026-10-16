package cli

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/auth"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/router"
)

// stubProvider lists fixed models; keyed providers need an API key that
// the tests never store.
type stubProvider struct {
	id     string
	keyed  bool
	models []provider.Model
}

func (p stubProvider) ID() string   { return p.id }
func (p stubProvider) Name() string { return p.id }
func (p stubProvider) Auth() provider.AuthSpec {
	if p.keyed {
		return provider.AuthSpec{Kind: provider.AuthKindAPIKey, EnvVars: []string{"KILN_TEST_NO_SUCH_KEY"}}
	}
	return provider.AuthSpec{Kind: provider.AuthKindNone}
}
func (p stubProvider) Models() []provider.Model                { return p.models }
func (p stubProvider) RefreshModels(ctx context.Context) error { return nil }
func (p stubProvider) Stream(context.Context, provider.Model, []msg.Message, provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	panic("not streamed")
}

func routerRegistry(t *testing.T) *provider.Registry {
	t.Helper()
	reg := provider.NewRegistry(auth.NewFileCredentialStore(filepath.Join(t.TempDir(), "auth.json")))
	mk := func(p, id string, window int) provider.Model {
		return provider.Model{Provider: p, ID: id, Api: provider.ApiAnthropicMessages, ContextWindow: window, MaxTokens: 8192, Input: []string{"text"}}
	}
	reg.Register(stubProvider{id: "faux", models: []provider.Model{mk("faux", "faux-1", 128000), mk("faux", "faux-2", 32768), mk("faux", "faux-3", 128000)}})
	reg.Register(stubProvider{id: "paid", keyed: true, models: []provider.Model{mk("paid", "big", 200000), mk("paid", "zeta", 200000)}})
	reg.Register(stubProvider{id: "ollama", models: []provider.Model{mk("ollama", "qwen", 65536)}})
	return reg
}

func runSetup(t *testing.T, reg *provider.Registry, cfg claudesettings.ModelRouterConfig) (modelRouterSetup, []string) {
	t.Helper()
	var warnings []string
	s := setupModelRouter(context.Background(), reg, cfg, "", func(w string) { warnings = append(warnings, w) }, nil, nil)
	return s, warnings
}

func ids(t router.Targets) string {
	var out []string
	for _, m := range t.All() {
		out = append(out, m.ID)
	}
	return strings.Join(out, ",")
}

func TestSetupModelRouter(t *testing.T) {
	roles := map[string]string{"fast": "faux/faux-2", "structured": "faux/faux-1", "heavy": "faux/faux-3"}
	cases := []struct {
		name     string
		cfg      claudesettings.ModelRouterConfig
		want     string // plan,planComplex,implement,direct; "" for not registered
		warnings int
	}{
		{"defaults from modelRoles",
			claudesettings.ModelRouterConfig{ModelRoles: roles}, "faux-1,faux-3,faux-2,faux-2", 0},
		{"defaults, only fast: everything on fast",
			claudesettings.ModelRouterConfig{ModelRoles: map[string]string{"fast": "faux/faux-2"}}, "faux-2,faux-2,faux-2,faux-2", 0},
		{"defaults, no fast: no kiln/auto, quietly",
			claudesettings.ModelRouterConfig{ModelRoles: map[string]string{"structured": "faux/faux-1"}}, "", 0},
		{"no modelRoles: no kiln/auto",
			claudesettings.ModelRouterConfig{}, "", 0},
		{"disabled",
			claudesettings.ModelRouterConfig{Set: true, Disabled: true, ModelRoles: roles}, "", 0},
		{"explicit roles; the unlisted slots use their own defaults, not a fallback chain",
			claudesettings.ModelRouterConfig{Set: true, ModelRoles: roles, Roles: map[string]string{"plan": "heavy", "implement": "structured"}},
			// planComplex is unlisted, so it keeps its own default role
			// ("heavy" = faux-3, which happens to equal plan here); direct
			// is unlisted too and keeps its default ("fast" = faux-2),
			// which resolves on its own rather than falling back to
			// implement (faux-1).
			"faux-3,faux-3,faux-1,faux-2", 0},
		{"explicit roles; the unlisted plan keeps its own default (structured), not implement's fallback",
			claudesettings.ModelRouterConfig{Set: true, ModelRoles: roles, Roles: map[string]string{"implement": "fast", "planComplex": "heavy"}},
			"faux-1,faux-3,faux-2,faux-2", 0},
		{"explicit role missing from modelRoles: warned, falls back; the unlisted planComplex/direct still resolve on their own defaults",
			claudesettings.ModelRouterConfig{Set: true, ModelRoles: roles, Roles: map[string]string{"plan": "ghost", "implement": "fast"}},
			"faux-2,faux-3,faux-2,faux-2", 1},
		{"explicit implement unresolved: warned, no kiln/auto",
			claudesettings.ModelRouterConfig{Set: true, ModelRoles: roles, Roles: map[string]string{"plan": "structured", "implement": "ghost"}},
			"", 2},
		{"a target whose provider has no credentials is not used",
			claudesettings.ModelRouterConfig{Set: true, ModelRoles: map[string]string{"fast": "faux/faux-2", "structured": "faux/faux-1", "heavy": "paid/big"},
				Roles: map[string]string{"planComplex": "heavy", "implement": "fast"}},
			// plan is unlisted and defaults to "structured" (faux-1, which
			// resolves cleanly); only planComplex's explicit "heavy" hits
			// the credential-less provider and falls back to plan.
			"faux-1,faux-1,faux-2,faux-2", 1},
		{"a default slot whose role modelRoles does not define falls back quietly",
			claudesettings.ModelRouterConfig{Set: true, ModelRoles: map[string]string{"fast": "faux/faux-2", "structured": "faux/faux-1"}},
			// planComplex defaults to "heavy", which is not defined: it
			// falls back to plan without a warning (qa finding
			// router-default-slot-warns).
			"faux-1,faux-1,faux-2,faux-2", 0},
		{"problems are passed on",
			claudesettings.ModelRouterConfig{ModelRoles: roles, Problems: []string{"bad fallback"}}, "faux-1,faux-3,faux-2,faux-2", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reg := routerRegistry(t)
			s, warnings := runSetup(t, reg, c.cfg)
			if len(warnings) != c.warnings {
				t.Errorf("warnings = %q, want %d", warnings, c.warnings)
			}
			_, inRegistry := reg.GetModel("kiln", "auto")
			if c.want == "" {
				if s.Registered || inRegistry || s.Reason == "" {
					t.Fatalf("setup = %+v (in registry %v), want no kiln/auto with a reason", s, inRegistry)
				}
				return
			}
			if !s.Registered || !inRegistry || !s.Routers.Has("kiln", "auto") {
				t.Fatalf("setup = %+v (in registry %v), want kiln/auto", s, inRegistry)
			}
			if got := ids(s.Targets); got != c.want {
				t.Errorf("targets = %s, want %s", got, c.want)
			}
		})
	}
}

// kiln/auto in the registry is sized to its Plan target (faux-1 here): the
// first request goes there, and later ones re-size on each route.
func TestSetupModelRouterRegistersTheVirtualModel(t *testing.T) {
	reg := routerRegistry(t)
	s, _ := runSetup(t, reg, claudesettings.ModelRouterConfig{ModelRoles: map[string]string{"fast": "faux/faux-2", "structured": "faux/faux-1"}})
	if !s.Registered {
		t.Fatal(s.Reason)
	}
	resolved, err := reg.Resolve("kiln", "auto")
	if err != nil {
		t.Fatal(err)
	}
	if !provider.IsVirtual(resolved.Model) || resolved.Model.ContextWindow != 128000 {
		t.Errorf("kiln/auto = %+v, want a virtual model with plan's (faux-1's) 128k window", resolved.Model)
	}
	if !s.approved("faux", "faux-1") || s.approved("faux", "faux-3") {
		t.Error("approved does not match the targets")
	}
	if m, ok := s.direct(); !ok || m.ID != "faux-2" {
		t.Errorf("direct = %s, %v", m.ID, ok)
	}
}

// Under HARNESS_OFFLINE=1 a remote target is not used.
func TestSetupModelRouterOffline(t *testing.T) {
	t.Setenv("HARNESS_OFFLINE", "1")
	t.Setenv("KILN_TEST_NO_SUCH_KEY", "set")
	reg := routerRegistry(t)
	s, warnings := runSetup(t, reg, claudesettings.ModelRouterConfig{Set: true,
		ModelRoles: map[string]string{"fast": "ollama/qwen", "heavy": "paid/big"},
		Roles:      map[string]string{"plan": "heavy", "implement": "fast"}})
	if !s.Registered || ids(s.Targets) != "qwen,qwen,qwen,qwen" {
		t.Fatalf("targets = %s, want only the local model", ids(s.Targets))
	}
	// One combined warning: the explicit plan=heavy and the unlisted
	// planComplex's own default (also heavy) both name the remote
	// paid/big, which HARNESS_OFFLINE refuses; both fall back to the
	// local qwen target, grouped into one sentence rather than one
	// warning per slot (Design 3's "one note per run").
	if len(warnings) != 1 || !strings.Contains(warnings[0], "HARNESS_OFFLINE") || !strings.Contains(warnings[0], "plan") || !strings.Contains(warnings[0], "planComplex") {
		t.Errorf("warnings = %q", warnings)
	}
}

// Auto mode's classifier never runs on a virtual model: with no fast role
// it takes the router's direct target.
func TestClassifierSessionModel(t *testing.T) {
	auto := provider.Model{Provider: "kiln", ID: "auto", Api: provider.ApiVirtual}
	physical := provider.Model{Provider: "faux", ID: "faux-1"}
	direct := func() (provider.Model, bool) { return provider.Model{Provider: "faux", ID: "faux-2"}, true }
	if got := classifierSessionModel(auto, direct); got.ID != "faux-2" {
		t.Errorf("virtual session: classifier on %s, want the direct target faux-2", got.ID)
	}
	if got := classifierSessionModel(physical, direct); got.ID != "faux-1" {
		t.Errorf("physical session: classifier on %s, want the session's faux-1", got.ID)
	}
	if got := classifierSessionModel(auto, nil); got.ID != "auto" {
		t.Errorf("no router: %s", got.ID)
	}
}

// The model router's own classifier is on by default once user-scope
// modelRoles.fast resolves: setupModelRouter wires it into the registered
// Auto router, unless classifier is false.
func TestSetupModelRouter_Classifier(t *testing.T) {
	roles := map[string]string{"fast": "faux/faux-2", "structured": "faux/faux-1", "heavy": "faux/faux-3"}
	t.Run("on by default when fast resolves", func(t *testing.T) {
		reg := routerRegistry(t)
		s, warnings := runSetup(t, reg, claudesettings.ModelRouterConfig{Set: true, Classifier: true, ModelRoles: roles})
		if len(warnings) != 0 {
			t.Fatalf("warnings = %q", warnings)
		}
		if s.classifier == nil {
			t.Fatal("classifier not wired, want it on by default")
		}
	})
	t.Run("classifier: false turns it off", func(t *testing.T) {
		reg := routerRegistry(t)
		s, warnings := runSetup(t, reg, claudesettings.ModelRouterConfig{Set: true, Classifier: false, ModelRoles: roles})
		if len(warnings) != 0 {
			t.Fatalf("warnings = %q", warnings)
		}
		if s.classifier != nil {
			t.Fatal("classifier wired, want it off")
		}
	})
	t.Run("classifier: true explicit but fast does not resolve: warned", func(t *testing.T) {
		reg := routerRegistry(t)
		cfg := claudesettings.ModelRouterConfig{Set: true, Classifier: true,
			// heavy is given so the unlisted planComplex's own default
			// resolves quietly; only "fast" (the unlisted direct's
			// default, and the classifier's own role) is unresolvable,
			// so it warns twice: once for direct's fallback, once for
			// the classifier itself.
			ModelRoles: map[string]string{"structured": "faux/faux-1", "heavy": "faux/faux-3", "fast": "ghost/nope"},
			Roles:      map[string]string{"implement": "structured"}}
		s, warnings := runSetup(t, reg, cfg)
		if !s.Registered {
			t.Fatalf("router did not register: %s", s.Reason)
		}
		if s.classifier != nil {
			t.Error("classifier wired despite fast not resolving")
		}
		if len(warnings) != 2 {
			t.Fatalf("warnings = %q, want 2 (direct's grouped fallback note and the classifier)", warnings)
		}
		if !strings.Contains(warnings[0], "direct") || !strings.Contains(warnings[0], "ghost") || !strings.Contains(warnings[1], "modelRouter.classifier") {
			t.Errorf("warnings = %q, want direct's fallback note then classifier", warnings)
		}
	})
	t.Run("defaults (modelRouter absent) with no fast: quiet", func(t *testing.T) {
		reg := routerRegistry(t)
		cfg := claudesettings.ModelRouterConfig{Classifier: true,
			ModelRoles: map[string]string{"structured": "faux/faux-1", "fast": "faux/faux-2"}}
		// cfg.Set is false here (as LoadModelRouter leaves it with no
		// modelRouter key): fast does resolve, though, so this exercises
		// the quiet *success* path, not the warning. A separate case below
		// covers the quiet failure.
		s, warnings := runSetup(t, reg, cfg)
		if len(warnings) != 0 {
			t.Fatalf("warnings = %q", warnings)
		}
		if s.classifier == nil {
			t.Error("classifier not wired even though fast resolves")
		}
	})
	t.Run("defaults, fast unresolved: fails quietly", func(t *testing.T) {
		reg := routerRegistry(t)
		cfg := claudesettings.ModelRouterConfig{Classifier: true,
			ModelRoles: map[string]string{"structured": "faux/faux-1", "implement": "ghost/nope"}}
		// cfg.Set is false: no modelRouter key at all, so a missing fast
		// role must not warn, matching the existing "defaults fail
		// quietly" rule for the router itself.
		cfg.ModelRoles["fast"] = ""
		s, warnings := runSetup(t, reg, cfg)
		if s.Registered {
			t.Fatalf("router registered: %+v", s.Targets)
		}
		if len(warnings) != 0 {
			t.Errorf("warnings = %q, want none (defaults fail quietly)", warnings)
		}
	})
	t.Run("fallback: heuristic wires the heuristic fallback", func(t *testing.T) {
		reg := routerRegistry(t)
		cfg := claudesettings.ModelRouterConfig{Set: true, Classifier: false, Fallback: claudesettings.RouterFallbackHeuristic, ModelRoles: roles}
		s, _ := runSetup(t, reg, cfg)
		if !s.Registered {
			t.Fatal("router did not register")
		}
		r, ok := s.Routers.Get("kiln", "auto")
		if !ok {
			t.Fatal("kiln/auto has no router")
		}
		auto, ok := r.(*router.Auto)
		if !ok || auto.Fallback == nil {
			t.Fatal("heuristic fallback not wired")
		}
		if got := auto.Fallback(strings.Repeat("a", 1300)); got != router.Complex {
			t.Errorf("fallback(long prompt) = %q, want complex", got)
		}
	})
}

// TestSetupModelRouter_FallbackNote: a defined role that cannot be used
// (here, no credentials) warns even with no modelRouter key at all
// (Set: false) — before this phase it stayed silent, since the old gate
// required cfg.Set. The c88cecb case (a default slot whose role
// modelRoles never mentions) must stay quiet; that is
// TestSetupModelRouter's own "falls back quietly" case, unchanged.
func TestSetupModelRouter_FallbackNote(t *testing.T) {
	reg := routerRegistry(t)
	cfg := claudesettings.ModelRouterConfig{ModelRoles: map[string]string{"fast": "faux/faux-2", "heavy": "paid/big"}}
	s, warnings := runSetup(t, reg, cfg)
	if !s.Registered {
		t.Fatalf("router did not register: %s", s.Reason)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %q, want exactly 1 (today it gives 0)", warnings)
	}
	note := warnings[0]
	for _, want := range []string{"paid", "no credentials", "planComplex", "kiln login paid"} {
		if !strings.Contains(note, want) {
			t.Errorf("note = %q, want it to mention %q", note, want)
		}
	}
	if !s.CoveredRoles["heavy"] {
		t.Errorf("CoveredRoles = %v, want \"heavy\" covered", s.CoveredRoles)
	}
	if len(s.FallbackReasons) != 1 || !strings.Contains(s.FallbackReasons[0], "no credentials") {
		t.Errorf("FallbackReasons = %q", s.FallbackReasons)
	}
}

// The note pairs each slot with the model it had: plan (structured =
// paid/zeta) then planComplex (heavy = paid/big), in slot order, not
// alphabetical order. Found live: "plan and planComplex use qwen3.8
// instead of claude-opus and claude-sonnet" named them the wrong way round.
func TestSetupModelRouter_FallbackNoteKeepsSlotOrder(t *testing.T) {
	reg := routerRegistry(t)
	cfg := claudesettings.ModelRouterConfig{ModelRoles: map[string]string{"fast": "faux/faux-2", "structured": "paid/zeta", "heavy": "paid/big"}}
	_, warnings := runSetup(t, reg, cfg)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %q, want 1", warnings)
	}
	if want := "so plan and planComplex use faux/faux-2 instead of paid/zeta and paid/big"; !strings.Contains(warnings[0], want) {
		t.Errorf("note = %q, want it to contain %q", warnings[0], want)
	}
}

// unreachableProvider is routerRegistry's "ollama" stand-in for a host
// that is down: RefreshModels always fails, so it never has any models to
// resolve a role against. It implements the same optional BaseURL()
// interface internal/provider/ollama.Provider does, so routerTarget can
// name the host in its "unreachable" problem.
type unreachableProvider struct {
	stubProvider
	url string
	err error
}

func (p unreachableProvider) RefreshModels(ctx context.Context) error { return p.err }
func (p unreachableProvider) BaseURL() string                         { return p.url }

// TestSetupModelRouter_Unreachable: a role on a provider whose
// RefreshModels fails (an unreachable Ollama host, modelled here by a
// stub rather than a real HTTP server, since routerTarget only needs the
// refresh error and the provider's BaseURL()) is reported as
// "unreachable", not the generic "not an available model" — and the
// host's URL is in the note.
func TestSetupModelRouter_Unreachable(t *testing.T) {
	reg := routerRegistry(t)
	downErr := errors.New("dial tcp 127.0.0.1:11434: connect: connection refused")
	reg.Register(unreachableProvider{
		stubProvider: stubProvider{id: "down-ollama"},
		url:          "http://127.0.0.1:11434",
		err:          downErr,
	})
	cfg := claudesettings.ModelRouterConfig{ModelRoles: map[string]string{
		"fast": "faux/faux-2", "structured": "faux/faux-1", "heavy": "down-ollama/qwen3.8",
	}}
	s, warnings := runSetup(t, reg, cfg)
	if !s.Registered {
		t.Fatalf("router did not register: %s", s.Reason)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %q, want exactly 1", warnings)
	}
	note := warnings[0]
	for _, want := range []string{"down-ollama", "unreachable", "http://127.0.0.1:11434", downErr.Error(), "planComplex"} {
		if !strings.Contains(note, want) {
			t.Errorf("note = %q, want it to mention %q", note, want)
		}
	}
}

// TestModelRouterSetup_ShortestFallbackReason checks shortestFallbackReason
// picks the shortest of several reasons (the one /model's describe row has
// room for) and that chat.go's own suffix construction ("Targets.Describe()
// + \" · fallback: \" + shortestFallbackReason()") reads as intended.
func TestModelRouterSetup_ShortestFallbackReason(t *testing.T) {
	s := modelRouterSetup{FallbackReasons: []string{
		"ollama at http://192.168.3.168:11434 unreachable: dial tcp: i/o timeout",
		"paid has no credentials (kiln login paid)",
	}}
	if got := s.shortestFallbackReason(); got != "paid has no credentials (kiln login paid)" {
		t.Errorf("shortestFallbackReason() = %q, want the shorter of the two", got)
	}
	s.Targets = router.Targets{
		Plan:        provider.Model{Provider: "faux", ID: "faux-1"},
		PlanComplex: provider.Model{Provider: "faux", ID: "faux-1"},
		Implement:   provider.Model{Provider: "faux", ID: "faux-2"},
	}
	describe := s.Targets.Describe() + " · fallback: " + s.shortestFallbackReason()
	want := "plan→faux-1 · complex→faux-1 · implement→faux-2 · fallback: paid has no credentials (kiln login paid)"
	if describe != want {
		t.Errorf("describe = %q, want %q", describe, want)
	}
	if got := (modelRouterSetup{}).shortestFallbackReason(); got != "" {
		t.Errorf("shortestFallbackReason() with none = %q, want \"\"", got)
	}
}
