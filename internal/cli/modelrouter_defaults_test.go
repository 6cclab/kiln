package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/auth"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/provider/catalog"
	"github.com/andrepato/harness/internal/router"
)

// TestDefaultRolesAreInTheCatalog: every model the zero-config table
// names exists in the built-in catalog, so a typo or a catalog update
// that drops one fails here rather than as a silently empty role.
func TestDefaultRolesAreInTheCatalog(t *testing.T) {
	for _, row := range router.DefaultRoles {
		for _, role := range router.DefaultRoleNames {
			id := row.Roles[role]
			if id == "" {
				t.Errorf("%s has no %s model", row.Provider, role)
				continue
			}
			if _, ok := catalog.Lookup(row.Provider, id); !ok {
				t.Errorf("%s %s = %s, not in the catalog", row.Provider, role, id)
			}
		}
	}
}

// defaultsRegistry is a registry holding stand-ins for the providers in
// router.DefaultRoles, named and stocked as the table expects: anthropic
// needs no credentials (so it counts as logged in) unless keyed, and
// openai-codex always lacks them.
func defaultsRegistry(t *testing.T, anthropicKeyed bool) *provider.Registry {
	t.Helper()
	reg := provider.NewRegistry(auth.NewFileCredentialStore(filepath.Join(t.TempDir(), "auth.json")))
	stock := func(id string, keyed bool) {
		var models []provider.Model
		for _, row := range router.DefaultRoles {
			if row.Provider != id {
				continue
			}
			for _, m := range row.Roles {
				models = append(models, provider.Model{Provider: id, ID: m, Api: provider.ApiAnthropicMessages, ContextWindow: 200000, MaxTokens: 8192, Input: []string{"text"}})
			}
		}
		reg.Register(stubProvider{id: id, keyed: keyed, models: models})
	}
	stock("anthropic", anthropicKeyed)
	stock("openai-codex", true)
	return reg
}

func anthropicRole(role string) string {
	return "anthropic/" + router.DefaultRoles[0].Roles[role]
}

func runSetupSession(t *testing.T, reg *provider.Registry, cfg claudesettings.ModelRouterConfig, session string) (modelRouterSetup, []string) {
	t.Helper()
	var warnings []string
	s := setupModelRouter(t.Context(), reg, cfg, session, func(w string) { warnings = append(warnings, w) }, nil, nil)
	return s, warnings
}

// TestModelRouterDefaults_NoModelRoles: with no modelRoles and a
// logged-in provider in the table, kiln/auto registers on that
// provider's three models instead of being unavailable.
func TestModelRouterDefaults_NoModelRoles(t *testing.T) {
	s, warnings := runSetupSession(t, defaultsRegistry(t, false), claudesettings.ModelRouterConfig{}, "anthropic/claude-haiku-4-5")
	if !s.Registered {
		t.Fatalf("not registered: %s", s.Reason)
	}
	want := router.DefaultRoles[0].Roles
	if got := ids(s.Targets); got != want["structured"]+","+want["heavy"]+","+want["fast"]+","+want["fast"] {
		t.Errorf("targets = %s", got)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %q, want none: derived roles are not problems", warnings)
	}
	if !s.Configured {
		t.Error("Configured = false, want doctor to show the derived router")
	}
	for _, role := range router.DefaultRoleNames {
		if !strings.Contains(s.DerivedNote, role+" "+anthropicRole(role)) {
			t.Errorf("note = %q, want it to name %s %s", s.DerivedNote, role, anthropicRole(role))
		}
	}
}

// TestModelRouterDefaults_NoLogin: with no logged-in provider in the
// table there is nothing to route between; kiln/auto stays off and says
// how to fix it.
func TestModelRouterDefaults_NoLogin(t *testing.T) {
	s, warnings := runSetupSession(t, defaultsRegistry(t, true), claudesettings.ModelRouterConfig{}, "")
	if s.Registered {
		t.Fatalf("registered with no login: %s", ids(s.Targets))
	}
	if !strings.Contains(s.Reason, "kiln login") || len(warnings) != 0 || s.Configured {
		t.Errorf("reason = %q, warnings = %q, configured = %v", s.Reason, warnings, s.Configured)
	}
}

// TestModelRouterDefaults_UserRoleWins: a role the user set is kept, and
// only the ones they left out are filled in.
func TestModelRouterDefaults_UserRoleWins(t *testing.T) {
	reg := defaultsRegistry(t, false)
	reg.Register(stubProvider{id: "faux", models: []provider.Model{{Provider: "faux", ID: "faux-1", Api: provider.ApiAnthropicMessages, ContextWindow: 128000, MaxTokens: 8192, Input: []string{"text"}}}})
	s, _ := runSetupSession(t, reg, claudesettings.ModelRouterConfig{ModelRoles: map[string]string{"heavy": "faux/faux-1"}}, "")
	if !s.Registered {
		t.Fatalf("not registered: %s", s.Reason)
	}
	if s.Targets.PlanComplex.ID != "faux-1" {
		t.Errorf("planComplex = %s, want the user's heavy faux-1", s.Targets.PlanComplex.ID)
	}
	if _, ok := s.DerivedRoles["heavy"]; ok || s.DerivedRoles["fast"] != anthropicRole("fast") {
		t.Errorf("derived = %v, want fast and structured only", s.DerivedRoles)
	}
}

// TestModelRouterDefaults_LocalSessionIsFast: a local session model is
// the fast role, and no other model on its server is added; the cloud
// table supplies structured and heavy.
func TestModelRouterDefaults_LocalSessionIsFast(t *testing.T) {
	srv, _ := newFakeOllama(t, []fakeOllamaModel{chatModel("qwen3.8"), chatModel("gpt-oss:20b")}, []string{"qwen3.8"})
	reg := localTestRegistry(t, srv)
	for _, p := range []string{"anthropic", "openai-codex"} {
		other := defaultsRegistry(t, false)
		prov, _ := other.Provider(p)
		reg.Register(prov)
	}
	s, _ := runSetupSession(t, reg, claudesettings.ModelRouterConfig{}, "ollama/qwen3.8")
	if !s.Registered {
		t.Fatalf("not registered: %s", s.Reason)
	}
	if s.Targets.Implement.ID != "qwen3.8" || s.Targets.Plan.ID != router.DefaultRoles[0].Roles["structured"] {
		t.Errorf("targets = %s, want implement qwen3.8 and plan from anthropic", ids(s.Targets))
	}
	if !strings.Contains(s.DerivedNote, "fast ollama/qwen3.8 (the session model)") {
		t.Errorf("note = %q", s.DerivedNote)
	}
}

// TestModelRouterDefaults_LocalOnly: a local session model with no cloud
// login is one model, so there is nothing to route between.
func TestModelRouterDefaults_LocalOnly(t *testing.T) {
	srv, _ := newFakeOllama(t, []fakeOllamaModel{chatModel("qwen3.8")}, []string{"qwen3.8"})
	s, _ := runSetupSession(t, localTestRegistry(t, srv), claudesettings.ModelRouterConfig{}, "ollama/qwen3.8")
	if s.Registered {
		t.Fatalf("registered on one local model: %s", ids(s.Targets))
	}
}
