package cli

import (
	"context"
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
	reg.Register(stubProvider{id: "paid", keyed: true, models: []provider.Model{mk("paid", "big", 200000)}})
	reg.Register(stubProvider{id: "ollama", models: []provider.Model{mk("ollama", "qwen", 65536)}})
	return reg
}

func runSetup(t *testing.T, reg *provider.Registry, cfg claudesettings.ModelRouterConfig) (modelRouterSetup, []string) {
	t.Helper()
	var warnings []string
	s := setupModelRouter(context.Background(), reg, cfg, func(w string) { warnings = append(warnings, w) }, nil)
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
		{"explicit roles; planComplex falls back to plan, direct to implement",
			claudesettings.ModelRouterConfig{Set: true, ModelRoles: roles, Roles: map[string]string{"plan": "heavy", "implement": "structured"}},
			"faux-3,faux-3,faux-1,faux-1", 0},
		{"explicit roles; no plan: plan falls back to implement",
			claudesettings.ModelRouterConfig{Set: true, ModelRoles: roles, Roles: map[string]string{"implement": "fast", "planComplex": "heavy"}},
			"faux-2,faux-3,faux-2,faux-2", 0},
		{"explicit role missing from modelRoles: warned, falls back",
			claudesettings.ModelRouterConfig{Set: true, ModelRoles: roles, Roles: map[string]string{"plan": "ghost", "implement": "fast"}},
			"faux-2,faux-2,faux-2,faux-2", 1},
		{"explicit implement unresolved: warned, no kiln/auto",
			claudesettings.ModelRouterConfig{Set: true, ModelRoles: roles, Roles: map[string]string{"plan": "structured", "implement": "ghost"}},
			"", 2},
		{"a target whose provider has no credentials is not used",
			claudesettings.ModelRouterConfig{Set: true, ModelRoles: map[string]string{"fast": "faux/faux-2", "heavy": "paid/big"},
				Roles: map[string]string{"planComplex": "heavy", "implement": "fast"}},
			"faux-2,faux-2,faux-2,faux-2", 1},
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

// kiln/auto in the registry is sized to its smallest target.
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
	if !provider.IsVirtual(resolved.Model) || resolved.Model.ContextWindow != 32768 {
		t.Errorf("kiln/auto = %+v, want a virtual model with faux-2's 32k window", resolved.Model)
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
	if len(warnings) != 1 || !strings.Contains(warnings[0], "HARNESS_OFFLINE") {
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
