package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/agents"
	"github.com/andrepato/harness/internal/claude/permission"
	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/provider"
	fauxprovider "github.com/andrepato/harness/internal/provider/faux"
	"github.com/andrepato/harness/internal/router"
	"github.com/andrepato/harness/internal/session"
	tkfaux "github.com/andrepato/harness/internal/testkit/faux"
)

// autoRig is a registry with faux and kiln/auto planning on faux-1 and
// implementing on faux-2, and a parent session on kiln/auto.
type autoRig struct {
	srv     *tkfaux.Server
	reg     *provider.Registry
	routers router.Set
	targets router.Targets
	parent  *Started
	root    string
	cwd     string
}

func newAutoRig(t *testing.T, script string) autoRig {
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
	faux1, _ := reg.GetModel("faux", "faux-1")
	faux2, _ := reg.GetModel("faux", "faux-2")
	targets := router.Targets{Plan: faux1, PlanComplex: faux1, Implement: faux2, Direct: faux2}
	reg.Register(router.NewVirtualProvider(router.AutoModel(targets)))
	var routers router.Set
	routers.Add(router.AutoProvider, router.AutoModelID, &router.Auto{Targets: targets})
	resolved, err := reg.Resolve(router.AutoProvider, router.AutoModelID)
	if err != nil {
		t.Fatal(err)
	}
	cwd, root := t.TempDir(), t.TempDir()
	parent, err := Start(context.Background(), Options{Registry: reg, Resolved: resolved, Cwd: cwd, SessionsRoot: root, Routers: routers})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Harness.Close() })
	return autoRig{srv: srv, reg: reg, routers: routers, targets: targets, parent: parent, root: root, cwd: cwd}
}

func (r autoRig) dispatcher(gate *permission.Gate) *Dispatcher {
	return &Dispatcher{
		Registry: r.reg, Parent: r.parent, Gate: gate, Agents: []agents.Definition{GeneralPurpose},
		SessionsRoot: r.root, Env: execenv.New(r.cwd), Routers: r.routers,
		Approved: func(p, m string) bool {
			for _, t := range r.targets.All() {
				if t.Provider == p && t.ID == m {
					return true
				}
			}
			return false
		},
	}
}

func requestModels(srv *tkfaux.Server) []string {
	var out []string
	for _, r := range srv.Requests() {
		out = append(out, r.Model)
	}
	return out
}

// A task from a kiln/auto parent inherits kiln/auto and routes in its own
// session: its first request plans on faux-1, and its router state is
// stored in its own session file, not the parent's.
func TestDispatchFromKilnAutoInheritsTheVirtualModel(t *testing.T) {
	rig := newAutoRig(t, "models:\n  faux-1:\n    - text: \"sub report\"\n")
	var events []SubagentEvent
	d := rig.dispatcher(nil)
	d.OnEvent = func(ev SubagentEvent) { events = append(events, ev) }
	res, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "look"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "sub report" {
		t.Fatalf("Text = %q", res.Text)
	}
	if events[0].ProviderID != "kiln" || events[0].ModelID != "auto" {
		t.Errorf("subagent ran on %s/%s, want kiln/auto", events[0].ProviderID, events[0].ModelID)
	}
	if got := requestModels(rig.srv); len(got) != 1 || got[0] != "faux-1" {
		t.Errorf("requests went to %v, want [faux-1] (the plan slot)", got)
	}
	files, _ := filepath.Glob(filepath.Join(rig.root, "*", "*.jsonl"))
	var withState []string
	for _, f := range files {
		data, _ := os.ReadFile(f)
		if strings.Contains(string(data), harness.RouterStateEntry) {
			withState = append(withState, f)
		}
	}
	if len(files) != 2 || len(withState) != 1 || withState[0] == rig.parent.TranscriptPath {
		t.Errorf("router state in %v of %v, want only the subagent's file", withState, files)
	}
}

// An explicit role still wins over inheriting kiln/auto.
func TestDispatchFromKilnAutoExplicitRoleWins(t *testing.T) {
	rig := newAutoRig(t, "models:\n  faux-2:\n    - text: \"fast report\"\n")
	d := rig.dispatcher(nil)
	d.Roles = map[string]string{"fast": "faux/faux-2"}
	res, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "look", Model: "fast"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Model != "faux/faux-2" || res.Text != "fast report" {
		t.Fatalf("result = %+v, want faux-2's report", res)
	}
}

// The paid-provider gate lets a kiln/auto target through without asking:
// faux-2 is paid and on another provider than kiln, so without the
// approval a deny-everything gate refuses it.
func TestDispatchGateApprovesTheRoutersTargets(t *testing.T) {
	rig := newAutoRig(t, "models:\n  faux-2:\n    - text: \"fast report\"\n")
	deny := func() *permission.Gate {
		return permission.NewGate(permission.GateOptions{Permissions: settings.Permissions{Deny: []string{"task"}}, Mode: settings.ModeAuto})
	}
	d := rig.dispatcher(deny())
	d.Roles = map[string]string{"fast": "faux/faux-2"}
	res, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "look", Model: "fast"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "fast report" {
		t.Fatalf("Text = %q, want the approved target to run", res.Text)
	}

	d.Approved = nil
	res, err = d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "look", Model: "fast"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "not allowed") {
		t.Fatalf("Text = %q, want the gate's refusal without the approval", res.Text)
	}
}

// ResumedLastPhysical is the newest successful reply's model.
func TestResumedLastPhysical(t *testing.T) {
	reg := newFauxRegistry(t, "models:\n  faux-1:\n    - text: \"one\"\n  faux-2:\n    - text: \"two\"\n    - error: {status: 400, type: invalid_request_error, message: \"bad\"}\n")
	cwd, root := t.TempDir(), t.TempDir()
	if _, ok := ResumedLastPhysical(Options{Cwd: cwd, SessionsRoot: root, ResumeLatest: true}); ok {
		t.Fatal("found a model with no session")
	}
	started, err := Start(context.Background(), Options{Registry: reg, Resolved: mustResolve(t, reg), Cwd: cwd, SessionsRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Cwd: cwd, SessionsRoot: root, ResumeLatest: true}
	if _, ok := ResumedLastPhysical(opts); ok {
		t.Fatal("found a model before any reply")
	}
	if _, err := started.Lane.Prompt(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	if err := started.Lane.SetModel(session.ModelRef{Provider: "faux", ModelID: "faux-2"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := started.Lane.Prompt(context.Background(), "again", nil); err != nil {
		t.Fatal(err)
	}
	if got, ok := ResumedLastPhysical(opts); !ok || got.ModelID != "faux-2" {
		t.Fatalf("ResumedLastPhysical = %+v, %v; want faux-2", got, ok)
	}
	// A failed request and a later /model switch leave the answer as the
	// model that last replied, not the lane's configured model.
	_, _ = started.Lane.Prompt(context.Background(), "fail", nil)
	if err := started.Lane.SetModel(session.ModelRef{Provider: "faux", ModelID: "faux-1"}, ""); err != nil {
		t.Fatal(err)
	}
	if got, ok := ResumedLastPhysical(Options{Cwd: cwd, SessionsRoot: root, Resume: started.SessionID}); !ok || got.ModelID != "faux-2" {
		t.Fatalf("after a failed reply ResumedLastPhysical = %+v, %v; want faux-2 still", got, ok)
	}
}

// Switching a running session to kiln/auto (/model) routes its next
// request: the session was on faux-2, and kiln/auto plans on faux-1.
func TestSetModelToKilnAutoRoutes(t *testing.T) {
	rig := newAutoRig(t, "models:\n  faux-1:\n    - text: \"planned\"\n")
	faux2, err := rig.reg.Resolve("faux", "faux-2")
	if err != nil {
		t.Fatal(err)
	}
	if err := SetModel(context.Background(), rig.parent, faux2); err != nil {
		t.Fatal(err)
	}
	auto, err := rig.reg.Resolve(router.AutoProvider, router.AutoModelID)
	if err != nil {
		t.Fatal(err)
	}
	if err := SetModel(context.Background(), rig.parent, auto); err != nil {
		t.Fatal(err)
	}
	if res, err := rig.parent.Lane.Prompt(context.Background(), "hi", nil); err != nil || res.Status != harness.StatusCompleted {
		t.Fatalf("%+v, %v", res, err)
	}
	if got := requestModels(rig.srv); len(got) != 1 || got[0] != "faux-1" {
		t.Fatalf("requests went to %v, want [faux-1]", got)
	}
}
