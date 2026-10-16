package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/andrepato/harness/internal/auth"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/provider/ollama"
)

// fakeOllamaModel is one model a fakeOllama server advertises.
type fakeOllamaModel struct {
	name         string
	capabilities []string
}

// chatModel is a fakeOllamaModel with the capabilities every model
// DiscoverModels keeps needs: "completion" (or it's filtered out as an
// embedding model) and "tools" (RequireTools defaults true).
func chatModel(name string) fakeOllamaModel {
	return fakeOllamaModel{name: name, capabilities: []string{"completion", "tools"}}
}

// newFakeOllama starts an httptest Ollama stand-in behind which a real
// ollama.New provider is registered: /api/tags and /api/show (with the
// completion+tools capabilities DiscoverModels requires to keep a model)
// for discovery, and /api/ps (serving resident) for the one-model-per-
// host collapse's residency check. psHits counts /api/ps calls, so a
// test can assert it is queried at most once per setupModelRouter run,
// and not at all when there is nothing to collapse.
func newFakeOllama(t *testing.T, models []fakeOllamaModel, resident []string) (*httptest.Server, *int32) {
	t.Helper()
	psHits := new(int32)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) {
		var tags []map[string]string
		for _, m := range models {
			tags = append(tags, map[string]string{"name": m.name, "model": m.name})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"models": tags})
	})
	mux.HandleFunc("/api/show", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var caps []string
		for _, m := range models {
			if m.name == body.Model {
				caps = m.capabilities
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"capabilities": caps,
			"parameters":   "",
			"model_info":   map[string]any{},
		})
	})
	mux.HandleFunc("/api/ps", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(psHits, 1)
		var ms []map[string]string
		for _, id := range resident {
			ms = append(ms, map[string]string{"model": id})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"models": ms})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, psHits
}

// localTestRegistry is routerRegistry's local-collapse sibling: a
// registry with a real ollama.Provider pointed at srv (never the real
// OLLAMA_HOST -- t.Setenv below only satisfies Registry.CheckAuth's "is
// there a credential at all" check, which reads the env var's presence,
// not its value; every actual HTTP call this test makes goes to srv).
func localTestRegistry(t *testing.T, srv *httptest.Server) *provider.Registry {
	t.Helper()
	t.Setenv("OLLAMA_HOST", "set-for-checkauth-only")
	reg := provider.NewRegistry(auth.NewFileCredentialStore(filepath.Join(t.TempDir(), "auth.json")))
	reg.Register(ollama.New(ollama.Options{URL: srv.URL}))
	return reg
}

// mixedStackRoles is the scenario from the plan's own motivating bug:
// fast=qwen3.8, structured=qwen3:30b-a3b, heavy=gpt-oss:20b, all on one
// Ollama host. Defaults route plan->structured, planComplex->heavy,
// implement->fast, direct->fast (claudesettings.DefaultRouterRoles).
var mixedStackRoles = map[string]string{
	"fast":       "ollama/qwen3.8",
	"structured": "ollama/qwen3:30b-a3b",
	"heavy":      "ollama/gpt-oss:20b",
}

// TestModelRouterLocalCollapse_MixedStack: with nothing resident but
// qwen3.8 (as kiln/auto's own implement/direct/fast target), every slot
// collapses onto it; exactly one note is printed, and /api/ps is hit
// exactly once even though three slots and fast all live on this host.
func TestModelRouterLocalCollapse_MixedStack(t *testing.T) {
	srv, psHits := newFakeOllama(t, []fakeOllamaModel{
		chatModel("qwen3.8"), chatModel("qwen3:30b-a3b"), chatModel("gpt-oss:20b"),
	}, []string{"qwen3.8"})
	reg := localTestRegistry(t, srv)

	s, warnings := runSetup(t, reg, claudesettings.ModelRouterConfig{ModelRoles: mixedStackRoles})
	if !s.Registered {
		t.Fatalf("router did not register: %s", s.Reason)
	}
	if got := ids(s.Targets); got != "qwen3.8,qwen3.8,qwen3.8,qwen3.8" {
		t.Errorf("targets = %s, want every slot collapsed onto qwen3.8", got)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %q, want exactly 1", warnings)
	}
	note := warnings[0]
	for _, want := range []string{
		"the Ollama server", srv.Listener.Addr().String(), "qwen3.8 (already loaded)",
		"qwen3:30b-a3b", "gpt-oss:20b", "plan", "planComplex", "Keep all of them loaded",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("note = %q, want it to mention %q", note, want)
		}
	}
	if len(s.LocalCollapseNotes) != 1 || s.LocalCollapseNotes[0] != note {
		t.Errorf("LocalCollapseNotes = %q, want [note]", s.LocalCollapseNotes)
	}
	if got := atomic.LoadInt32(psHits); got != 1 {
		t.Errorf("/api/ps hits = %d, want exactly 1", got)
	}
}

// TestModelRouterLocalCollapse_SingleModel: with only one local model in
// play, there is nothing to collapse and /api/ps is never queried.
func TestModelRouterLocalCollapse_SingleModel(t *testing.T) {
	srv, psHits := newFakeOllama(t, []fakeOllamaModel{chatModel("qwen3.8")}, nil)
	reg := localTestRegistry(t, srv)

	s, warnings := runSetup(t, reg, claudesettings.ModelRouterConfig{ModelRoles: map[string]string{"fast": "ollama/qwen3.8"}})
	if !s.Registered {
		t.Fatalf("router did not register: %s", s.Reason)
	}
	if got := ids(s.Targets); got != "qwen3.8,qwen3.8,qwen3.8,qwen3.8" {
		t.Errorf("targets = %s, want qwen3.8 everywhere (no collapse needed)", got)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %q, want none", warnings)
	}
	if len(s.LocalCollapseNotes) != 0 {
		t.Errorf("LocalCollapseNotes = %q, want none", s.LocalCollapseNotes)
	}
	if got := atomic.LoadInt32(psHits); got != 0 {
		t.Errorf("/api/ps hits = %d, want 0 (only one local candidate)", got)
	}
}

// TestModelRouterLocalCollapse_CloudSlotUntouched: a cloud slot
// (planComplex -> a paid provider) is left alone; only the two local
// candidates (implement/direct/fast's qwen3.8 and plan's qwen3:30b-a3b)
// collapse.
func TestModelRouterLocalCollapse_CloudSlotUntouched(t *testing.T) {
	srv, psHits := newFakeOllama(t, []fakeOllamaModel{chatModel("qwen3.8"), chatModel("qwen3:30b-a3b")}, nil)
	reg := localTestRegistry(t, srv)
	reg.Register(stubProvider{id: "paid", keyed: false, models: []provider.Model{
		{Provider: "paid", ID: "big", Api: provider.ApiAnthropicMessages, ContextWindow: 200000, MaxTokens: 8192, Input: []string{"text"}},
	}})

	cfg := claudesettings.ModelRouterConfig{ModelRoles: map[string]string{
		"fast": "ollama/qwen3.8", "structured": "ollama/qwen3:30b-a3b", "heavy": "paid/big",
	}}
	s, warnings := runSetup(t, reg, cfg)
	if !s.Registered {
		t.Fatalf("router did not register: %s", s.Reason)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %q, want exactly 1 (the local collapse note)", warnings)
	}
	if s.Targets.PlanComplex.Provider != "paid" || s.Targets.PlanComplex.ID != "big" {
		t.Errorf("PlanComplex = %+v, want the untouched cloud target", s.Targets.PlanComplex)
	}
	if s.Targets.Plan.Provider != "ollama" || s.Targets.Plan.ID != "qwen3.8" {
		t.Errorf("Plan = %+v, want it collapsed onto qwen3.8 (implement's model, nothing resident)", s.Targets.Plan)
	}
	if got := atomic.LoadInt32(psHits); got != 1 {
		t.Errorf("/api/ps hits = %d, want exactly 1", got)
	}
}

// TestModelRouterLocalCollapse_AllResidentUnchanged: when the host
// already holds every candidate, nothing collapses (the opt-out), though
// /api/ps is still queried once to find that out.
func TestModelRouterLocalCollapse_AllResidentUnchanged(t *testing.T) {
	srv, psHits := newFakeOllama(t, []fakeOllamaModel{
		chatModel("qwen3.8"), chatModel("qwen3:30b-a3b"), chatModel("gpt-oss:20b"),
	}, []string{"qwen3.8", "qwen3:30b-a3b", "gpt-oss:20b"})
	reg := localTestRegistry(t, srv)

	s, warnings := runSetup(t, reg, claudesettings.ModelRouterConfig{ModelRoles: mixedStackRoles})
	if !s.Registered {
		t.Fatalf("router did not register: %s", s.Reason)
	}
	if got := ids(s.Targets); got != "qwen3:30b-a3b,gpt-oss:20b,qwen3.8,qwen3.8" {
		t.Errorf("targets = %s, want unchanged (plan=structured, planComplex=heavy, implement/direct=fast)", got)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %q, want none (every candidate already resident)", warnings)
	}
	if got := atomic.LoadInt32(psHits); got != 1 {
		t.Errorf("/api/ps hits = %d, want exactly 1", got)
	}
}
