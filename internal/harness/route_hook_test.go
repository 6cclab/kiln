package harness

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/router"
)

// routeRecorder is an OnRoute handler that records every call and
// re-sizes the harness for the routed model, the way agent.Retier does:
// the system prompt names the model, which proves the request the route
// decided was built after the handler ran.
type routeRecorder struct {
	mu    sync.Mutex
	infos []RouteInfo
}

func (r *routeRecorder) register(h *Harness) {
	h.Hooks().OnRoute(func(ctx context.Context, info RouteInfo) error {
		r.mu.Lock()
		r.infos = append(r.infos, info)
		r.mu.Unlock()
		// Each of these setters emits an event: called inside an emit,
		// they would deadlock on the bus's invoke lock.
		h.SetSystemPrompt("sized for " + info.Model.ID)
		h.SetCompactionSettings(h.opts.Compaction)
		h.AddTools()
		return nil
	})
}

func (r *routeRecorder) reasons() []router.Reason {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]router.Reason, len(r.infos))
	for i, info := range r.infos {
		out[i] = info.Reason
	}
	return out
}

// promptWithin runs one prompt and fails the test if it does not finish
// within a few seconds, which is how a handler deadlocked on the event
// bus would show up.
func promptWithin(t *testing.T, lane *Lane, text string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		res, err := lane.Prompt(context.Background(), text, nil)
		if err == nil && res.Status != StatusCompleted {
			err = context.DeadlineExceeded
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("prompt %q: %v", text, err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("prompt %q did not finish: an OnRoute handler deadlocked", text)
	}
}

// An OnRoute handler runs before the request it routed is built: the
// system prompt it sets is the one that same request carries, for the
// first request and for a tool follow-up.
func TestOnRouteResizesTheRequestItRouted(t *testing.T) {
	r := &scriptedRouter{pick: always("faux-1")}
	rig := newRoutedRig(t, `
model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "echo hi"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`, r, 128000)
	rec := &routeRecorder{}
	rec.register(rig.H)
	promptWithin(t, rig.mustLane("main"), "hi")

	reqs := rig.Faux.Requests()
	if len(reqs) != 2 {
		t.Fatalf("%d requests, want 2", len(reqs))
	}
	for i, req := range reqs {
		if req.System != "sized for faux-1" {
			t.Errorf("request %d system prompt = %q, want the one the route hook set", i, req.System)
		}
	}
	if got, want := rec.reasons(), []router.Reason{router.ReasonUser, router.ReasonContinuation}; !eq(got, want) {
		t.Errorf("hook reasons = %v, want %v", got, want)
	}
	if len(rec.infos) == 0 {
		t.Fatal("the route hook never ran")
	}
	if info := rec.infos[0]; info.Virtual.Provider != "kiln" || info.Virtual.ID != "auto" || info.Model.ID != "faux-1" {
		t.Errorf("route info = %+v", info)
	}
}

// A retry routed to another model is re-sized for that model before the
// retry is sent.
func TestOnRouteResizesARetry(t *testing.T) {
	r := &scriptedRouter{pick: func(req router.Request) string {
		if req.Failed != nil {
			return "faux-2"
		}
		return "faux-1"
	}}
	rig := newRoutedRig(t, `
models:
  faux-1:
    - error: {status: 529, type: overloaded_error, message: "Overloaded"}
  faux-2:
    - text: "from faux-2"
`, r, 128000)
	rig.H.opts.Retry = RetryPolicy{BaseDelayMs: 1, MaxAgentDelayMs: 1, MaxAttempts: 2}
	rec := &routeRecorder{}
	rec.register(rig.H)
	promptWithin(t, rig.mustLane("main"), "hi")

	got := requestsFor(rig.Faux, "faux-2")
	if len(got) != 1 || got[0].System != "sized for faux-2" {
		t.Fatalf("faux-2 requests = %+v, want one sized for faux-2", got)
	}
	if first := requestsFor(rig.Faux, "faux-1"); len(first) != 1 || first[0].System != "sized for faux-1" {
		t.Errorf("faux-1 requests = %+v, want one sized for faux-1", first)
	}
	if got, want := rec.reasons(), []router.Reason{router.ReasonUser, router.ReasonRetry}; !eq(got, want) {
		t.Errorf("hook reasons = %v, want %v", got, want)
	}
}

// A direct route (a compaction summary) does not re-size the session: the
// conversation stays on the model the turn loop routed to.
func TestOnRouteNotCalledForDirectRoutes(t *testing.T) {
	r := &scriptedRouter{pick: func(req router.Request) string {
		if req.Reason == router.ReasonDirect {
			return "faux-2"
		}
		return "faux-1"
	}}
	rig := newRoutedRig(t, `
models:
  faux-1:
    - text: "first reply"
  faux-2:
    - text: "a summary"
      end_turn: true
`, r, 128000)
	rec := &routeRecorder{}
	rec.register(rig.H)
	lane := rig.mustLane("main")
	promptWithin(t, lane, "one")
	if err := lane.Compact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := r.reasons(); len(got) != 2 || got[1] != router.ReasonDirect {
		t.Fatalf("router reasons = %v, want user then direct", got)
	}
	if got, want := rec.reasons(), []router.Reason{router.ReasonUser}; !eq(got, want) {
		t.Errorf("hook reasons = %v, want %v (none for the direct route)", got, want)
	}
	if got := rig.H.SystemPrompt(); got != "sized for faux-1" {
		t.Errorf("system prompt after compaction = %q, want faux-1's", got)
	}
}

// A physical lane never routes, so the hook never fires and its requests
// carry the harness's own system prompt.
func TestOnRouteNotCalledOnPhysicalLanes(t *testing.T) {
	rig := newTestRig(t, `
model: faux-1
steps:
  - text: "hello"
`, []string{"bash"})
	rec := &routeRecorder{}
	rec.register(rig.H)
	promptWithin(t, rig.mustLane("main"), "hi")
	if got := rec.reasons(); len(got) != 0 {
		t.Errorf("hook called %d times on a physical lane", len(got))
	}
	if reqs := rig.Faux.Requests(); len(reqs) != 1 || reqs[0].System != "test harness" {
		t.Errorf("requests = %+v, want one with the harness's own prompt", reqs)
	}
}
