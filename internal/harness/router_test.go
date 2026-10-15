package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/andrepato/harness/internal/compaction"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	fauxprovider "github.com/andrepato/harness/internal/provider/faux"
	"github.com/andrepato/harness/internal/router"
	"github.com/andrepato/harness/internal/session"
)

// virtualProvider lists one virtual model, kiln/auto, and refuses to
// stream it: a request that reaches it was never routed.
type virtualProvider struct{ window int }

func (p virtualProvider) ID() string   { return "kiln" }
func (p virtualProvider) Name() string { return "kiln" }
func (p virtualProvider) Auth() provider.AuthSpec {
	return provider.AuthSpec{Kind: provider.AuthKindNone}
}
func (p virtualProvider) RefreshModels(ctx context.Context) error { return nil }
func (p virtualProvider) Models() []provider.Model {
	return []provider.Model{{ID: "auto", Name: "auto", Api: provider.ApiVirtual, Provider: "kiln", Reasoning: true,
		ContextWindow: p.window, MaxTokens: 16384}}
}
func (p virtualProvider) Stream(ctx context.Context, m provider.Model, _ []msg.Message, _ provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	ch := make(chan msg.StreamEvent)
	close(ch)
	return ch, func() (*msg.AssistantMessage, error) {
		return nil, fmt.Errorf("virtual model %s/%s must be routed before streaming", m.Provider, m.ID)
	}
}

// scriptedRouter answers each request with pick's model id (a faux model)
// and counts its state up by one; it records every request it was asked.
type scriptedRouter struct {
	mu   sync.Mutex
	pick func(req router.Request) string
	reqs []router.Request
}

type counterState struct {
	N int `json:"n"`
}

func (r *scriptedRouter) Route(_ context.Context, req router.Request) (router.Route, error) {
	r.mu.Lock()
	r.reqs = append(r.reqs, req)
	r.mu.Unlock()
	var st counterState
	_ = json.Unmarshal(req.State, &st)
	next, _ := json.Marshal(counterState{N: st.N + 1})
	return router.Route{
		Model:         provider.Model{Provider: fauxprovider.ProviderID, ID: r.pick(req)},
		ThinkingLevel: req.ThinkingLevel,
		State:         next,
	}, nil
}

func (r *scriptedRouter) reasons() []router.Reason {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]router.Reason, len(r.reqs))
	for i, q := range r.reqs {
		out[i] = q.Reason
	}
	return out
}

func (r *scriptedRouter) states() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.reqs))
	for i, q := range r.reqs {
		out[i] = string(q.State)
	}
	return out
}

func always(id string) func(router.Request) string { return func(router.Request) string { return id } }

// newRoutedRig is a test rig whose lane names kiln/auto, routed by r.
// window is the virtual model's own context window.
func newRoutedRig(t *testing.T, script string, r router.Router, window int) *testRig {
	t.Helper()
	return newTestRigWith(t, script, []string{"bash"}, nil, func(opts *Options, reg *provider.Registry) {
		reg.Register(virtualProvider{window: window})
		opts.Model = session.ModelRef{Provider: "kiln", ModelID: "auto"}
		if r != nil {
			opts.Routers.Add("kiln", "auto", r)
		}
	})
}

// routerStates are the states stored on the lane's branch, oldest first.
func routerStates(t *testing.T, l *Lane) []string {
	t.Helper()
	entries, _ := l.FindEntries(context.Background())
	var out []string
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Type == session.EntryCustom && e.CustomType == RouterStateEntry {
			var d RouterStateData
			if err := json.Unmarshal(e.Data, &d); err != nil {
				t.Fatal(err)
			}
			if d.Provider != "kiln" || d.ModelID != "auto" {
				t.Errorf("state stored for %s/%s, want kiln/auto", d.Provider, d.ModelID)
			}
			out = append(out, string(d.State))
		}
	}
	return out
}

func eq[T comparable](a, b []T) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The first request after a prompt is routed as user, a tool follow-up as
// continuation; each request reads the state the previous one stored on
// the branch, and gets the previous answer's physical model. The state
// entries never reach the provider.
func TestRouterReasonSequenceAndState(t *testing.T) {
	r := &scriptedRouter{pick: always("faux-1")}
	rig := newRoutedRig(t, `
model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "echo hi"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
  - text: "second"
`, r, 128000)
	var routes []Event
	rig.H.Events().On(EventRoute, func(ev Event) { routes = append(routes, ev) })
	var usageModels []string
	rig.H.Events().On(EventUsage, func(ev Event) {
		if ev.UsageModel != nil {
			usageModels = append(usageModels, ev.UsageModel.Provider+"/"+ev.UsageModel.ModelID)
		}
	})
	lane := rig.mustLane("main")
	for _, p := range []string{"first", "next"} {
		if res, err := lane.Prompt(context.Background(), p, nil); err != nil || res.Status != StatusCompleted {
			t.Fatalf("prompt %q: %+v, %v", p, res, err)
		}
	}

	if got, want := r.reasons(), []router.Reason{router.ReasonUser, router.ReasonContinuation, router.ReasonUser}; !eq(got, want) {
		t.Errorf("reasons = %v, want %v", got, want)
	}
	if got, want := r.states(), []string{"", `{"n":1}`, `{"n":2}`}; !eq(got, want) {
		t.Errorf("states read = %q, want %q", got, want)
	}
	if got, want := routerStates(t, lane), []string{`{"n":1}`, `{"n":2}`, `{"n":3}`}; !eq(got, want) {
		t.Errorf("states stored = %q, want %q", got, want)
	}
	if r.reqs[0].Previous != nil || r.reqs[2].Previous == nil || r.reqs[2].Previous.Model.ID != "faux-1" {
		t.Errorf("previous = %+v then %+v, want none then faux-1", r.reqs[0].Previous, r.reqs[2].Previous)
	}
	if len(routes) != 3 || routes[0].RouteReason != "user" || routes[0].RouteModel.ModelID != "faux-1" || routes[0].RouteVirtual.ModelID != "auto" {
		t.Errorf("route events = %+v", routes)
	}
	if !eq(usageModels, []string{"faux/faux-1", "faux/faux-1", "faux/faux-1"}) {
		t.Errorf("usage models = %v", usageModels)
	}
	reqs := rig.Faux.Requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests, want 3", len(reqs))
	}
	if strings.Contains(string(reqs[2].Messages), RouterStateEntry) || strings.Contains(string(reqs[2].Messages), `"n"`) {
		t.Errorf("router state reached the provider: %s", reqs[2].Messages)
	}

	// Each state entry sits right before the reply it decided.
	entries, _ := lane.FindEntries(context.Background())
	for i, e := range entries {
		if e.Type == session.EntryCustom && (i == 0 || entries[i-1].Message == nil || entries[i-1].Message.MessageRole() != msg.RoleAssistant) {
			t.Errorf("state entry %d is not followed by a reply", i)
		}
	}
}

// A Stop hook's message keeps the turn going, but no user wrote it: the
// request after it is a continuation.
func TestRouterStopHookContinuationIsContinuation(t *testing.T) {
	r := &scriptedRouter{pick: always("faux-1")}
	rig := newRoutedRig(t, `
model: faux-1
steps:
  - text: "first reply"
    end_turn: true
  - text: "second reply"
    end_turn: true
`, r, 128000)
	rig.H.Hooks().OnBeforeStop(func(ctx context.Context, info StopInfo) (StopVerdict, error) {
		if info.StopHookActive {
			return StopVerdict{}, nil
		}
		return StopVerdict{Continue: true, Message: "run the tests", Source: "Stop"}, nil
	})
	lane := rig.mustLane("main")
	if res, err := lane.Prompt(context.Background(), "do it", nil); err != nil || res.Status != StatusCompleted {
		t.Fatalf("%+v, %v", res, err)
	}
	if got, want := r.reasons(), []router.Reason{router.ReasonUser, router.ReasonContinuation}; !eq(got, want) {
		t.Errorf("reasons = %v, want %v", got, want)
	}
}

// State lives on the branch: after a rewind (NavigateTree) the router
// reads the state of the branch it is on, not the newest it ever wrote.
func TestRouterStateFollowsTheBranch(t *testing.T) {
	r := &scriptedRouter{pick: always("faux-1")}
	rig := newRoutedRig(t, `
model: faux-1
steps:
  - text: "a"
  - text: "b"
  - text: "c"
  - text: "d"
`, r, 128000)
	lane := rig.mustLane("main")
	if _, err := lane.Prompt(context.Background(), "one", nil); err != nil {
		t.Fatal(err)
	}
	afterOne := mustTip(t, lane)
	if _, err := lane.Prompt(context.Background(), "two", nil); err != nil {
		t.Fatal(err)
	}
	// Rewind to just after the first reply, as /rewind to "two" does.
	if err := lane.NavigateTree(context.Background(), &afterOne); err != nil {
		t.Fatal(err)
	}
	if _, err := lane.Prompt(context.Background(), "two again", nil); err != nil {
		t.Fatal(err)
	}
	// And to the root: no state at all.
	if err := lane.NavigateTree(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := lane.Prompt(context.Background(), "fresh", nil); err != nil {
		t.Fatal(err)
	}
	if got, want := r.states(), []string{"", `{"n":1}`, `{"n":1}`, ""}; !eq(got, want) {
		t.Errorf("states read = %q, want %q", got, want)
	}
}

// A retry is routed again with reason retry and the failed request, and
// may go elsewhere: here the overloaded faux-1 hands over to faux-2, the
// reply and its usage are faux-2's, and the retry's state is stored.
func TestRouterRetryCarriesTheFailure(t *testing.T) {
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
      usage: {input: 1000, output: 100}
`, r, 128000)
	rig.H.opts.Retry = RetryPolicy{BaseDelayMs: 1, MaxAgentDelayMs: 1, MaxAttempts: 2}
	var usage []Event
	rig.H.Events().On(EventUsage, func(ev Event) { usage = append(usage, ev) })
	lane := rig.mustLane("main")
	if res, err := lane.Prompt(context.Background(), "hi", nil); err != nil || res.Status != StatusCompleted {
		t.Fatalf("%+v, %v", res, err)
	}
	if got, want := r.reasons(), []router.Reason{router.ReasonUser, router.ReasonRetry}; !eq(got, want) {
		t.Fatalf("reasons = %v, want %v", got, want)
	}
	f := r.reqs[1].Failed
	if f == nil || f.Model.ID != "faux-1" || f.Err == nil || !strings.Contains(f.Err.Error(), "Overloaded") {
		t.Errorf("failed = %+v, want faux-1's overloaded error", f)
	}
	if string(r.reqs[1].State) != `{"n":1}` {
		t.Errorf("retry read state %s, want the first route's", r.reqs[1].State)
	}
	if got := requestsFor(rig.Faux, "faux-2"); len(got) != 1 {
		t.Errorf("%d requests to faux-2, want the retry", len(got))
	}
	if len(usage) != 1 || usage[0].UsageModel == nil || usage[0].UsageModel.ModelID != "faux-2" || usage[0].UsageRow.Cost.Total <= 0 {
		t.Errorf("usage events = %+v, want one priced on faux-2", usage)
	}
	if got, want := routerStates(t, lane), []string{`{"n":1}`, `{"n":2}`}; !eq(got, want) {
		t.Errorf("states stored = %q, want %q", got, want)
	}
}

// After a provider refuses a request as too long, the conversation is
// compacted by the direct route and the resend is routed as a retry.
func TestRouterOverflowResendIsARetry(t *testing.T) {
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
      end_turn: true
    - text: "second reply"
      end_turn: true
    - error: {status: 400, type: invalid_request_error, message: "prompt is too long: 140000 tokens > 128000 maximum"}
    - text: "third reply"
  faux-2:
    - text: "a summary"
      end_turn: true
`, r, 128000)
	rig.H.SetCompactionSettings(compaction.Settings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 1})
	lane := rig.mustLane("main")
	for _, p := range []string{"one", "two", "three"} {
		if res, err := lane.Prompt(context.Background(), p, nil); err != nil || res.Status != StatusCompleted {
			t.Fatalf("prompt %q: %+v, %v", p, res, err)
		}
	}
	want := []router.Reason{router.ReasonUser, router.ReasonUser, router.ReasonUser, router.ReasonDirect, router.ReasonRetry}
	if got := r.reasons(); !eq(got, want) {
		t.Fatalf("reasons = %v, want %v", got, want)
	}
	if f := r.reqs[4].Failed; f == nil || !isContextOverflow(f.Err) {
		t.Errorf("resend's failed = %+v, want the overflow", f)
	}
	if got := requestsFor(rig.Faux, "faux-2"); len(got) != 1 {
		t.Errorf("%d summary requests to faux-2, want 1", len(got))
	}
	reqs := requestsFor(rig.Faux, "faux-1")
	if last := string(reqs[len(reqs)-1].Messages); !strings.Contains(last, "a summary") {
		t.Errorf("the resend was not sent the compacted conversation: %s", last)
	}
}

// /compact on a virtual lane is a direct request: the router picks the
// summariser, without reading or writing state.
func TestRouterManualCompactionIsDirect(t *testing.T) {
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
    - text: "second reply"
  faux-2:
    - text: "a summary"
      end_turn: true
`, r, 128000)
	lane := rig.mustLane("main")
	for _, p := range []string{"one", "two"} {
		if _, err := lane.Prompt(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := lane.Compact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := r.reasons(); len(got) != 3 || got[2] != router.ReasonDirect {
		t.Fatalf("reasons = %v, want a direct one last", got)
	}
	if r.reqs[2].State != nil {
		t.Errorf("direct request read state %s", r.reqs[2].State)
	}
	if got := requestsFor(rig.Faux, "faux-2"); len(got) != 1 {
		t.Errorf("%d requests to faux-2, want the summary", len(got))
	}
	if got := routerStates(t, lane); len(got) != 2 {
		t.Errorf("states stored = %q, want the two turns' only", got)
	}
}

// The compaction threshold is the routed model's window: a lane whose
// router moves to the 32k faux-2 compacts before sending it a
// conversation that faux-1's 128k (and the virtual model's own window)
// would hold. The summary is written by the direct route.
func TestRouterCompactionThresholdUsesTheRoutedWindow(t *testing.T) {
	for _, tc := range []struct {
		name      string
		second    string
		compacted bool
	}{
		{"routed to the small model", "faux-2", true},
		{"routed to the large model", "faux-1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &scriptedRouter{}
			r.pick = func(req router.Request) string {
				switch {
				case req.Reason == router.ReasonDirect:
					return "faux-3"
				case req.State == nil:
					return "faux-1"
				}
				return tc.second
			}
			t.Setenv("HARNESS_FAUX_EXTRA_MODELS", "faux-3")
			rig := newRoutedRig(t, `
models:
  faux-1:
    - text: "first reply"
      usage: {input: 20000, output: 10}
    - text: "second reply"
  faux-2:
    - text: "second reply"
  faux-3:
    - text: "a summary"
      end_turn: true
`, r, 128000)
			rig.H.SetCompactionSettings(compaction.Settings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 1})
			lane := rig.mustLane("main")
			for _, p := range []string{longText(19000), "go on"} {
				if res, err := lane.Prompt(context.Background(), p, nil); err != nil || res.Status != StatusCompleted {
					t.Fatalf("%+v, %v", res, err)
				}
			}
			compacted := false
			entries, _ := lane.FindEntries(context.Background())
			for _, e := range entries {
				compacted = compacted || e.Type == session.EntryCompaction
			}
			if compacted != tc.compacted {
				t.Fatalf("compacted = %v, want %v", compacted, tc.compacted)
			}
			if summaries := len(requestsFor(rig.Faux, "faux-3")); (summaries > 0) != tc.compacted {
				t.Errorf("%d summary requests to the direct route's faux-3", summaries)
			}
			if got := requestsFor(rig.Faux, tc.second); len(got) == 0 {
				t.Errorf("no request reached %s", tc.second)
			}
		})
	}
}

// Routing failures fail the run before anything is sent.
func TestRouterUnregisteredVirtualModelFailsTheRun(t *testing.T) {
	rig := newRoutedRig(t, "model: faux-1\nsteps:\n  - text: \"never\"\n", nil, 128000)
	lane := rig.mustLane("main")
	res, _ := lane.Prompt(context.Background(), "hi", nil)
	if res.Status != StatusFailed || res.Error == nil || res.Error.Error() != "Virtual model kiln/auto is not registered" {
		t.Fatalf("result = %+v, want the unregistered error", res)
	}
	if n := len(rig.Faux.Requests()); n != 0 {
		t.Errorf("%d requests sent", n)
	}
}

func TestRouterTargetVirtualFailsTheRun(t *testing.T) {
	r := routerFunc(func(router.Request) router.Route {
		return router.Route{Model: provider.Model{Provider: "kiln", ID: "auto"}}
	})
	rig := newRoutedRig(t, "model: faux-1\nsteps:\n  - text: \"never\"\n", r, 128000)
	res, _ := rig.mustLane("main").Prompt(context.Background(), "hi", nil)
	if res.Status != StatusFailed || res.Error == nil || res.Error.Error() != "Virtual model kiln/auto routed to kiln/auto, which is not a physical model" {
		t.Fatalf("result = %+v, want the not-physical error", res)
	}
}

type routerFunc func(router.Request) router.Route

func (f routerFunc) Route(_ context.Context, req router.Request) (router.Route, error) {
	return f(req), nil
}

// An interrupted reply is priced at the routed model's rates. Priced at
// the lane's virtual model, which has none, it cost nothing.
func TestRouterInterruptedReplyPricedOnTheRoutedModel(t *testing.T) {
	r := &scriptedRouter{pick: always("faux-2")}
	rig := newRoutedRig(t, `
model: faux-2
steps:
  - chunk_delay: 30ms
    text: "`+strings.Repeat("The design has three parts. ", 12)+`"
    end_turn: true
`, r, 128000)
	lane := rig.mustLane("main")
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	updates := 0
	rig.H.Events().On(EventMessageUpdate, func(ev Event) {
		mu.Lock()
		defer mu.Unlock()
		if ev.StreamEvent != nil && ev.StreamEvent.Type == msg.EventTextDelta {
			if updates++; updates == 5 {
				cancel()
			}
		}
	})
	var usage *Event
	rig.H.Events().On(EventUsage, func(ev Event) { usage = &ev })
	if res, _ := lane.Prompt(ctx, "write it", nil); res.Status != StatusAborted {
		t.Fatalf("status %q, want aborted", res.Status)
	}
	if usage == nil || usage.UsageRow.Cost.Total <= 0 || usage.UsageModel == nil || usage.UsageModel.ModelID != "faux-2" {
		t.Fatalf("usage event = %+v, want the partial reply priced on faux-2", usage)
	}
}

// ContextTokens reads the measured size of a request that went to the
// model the lane was last routed to; the lane's own virtual model never
// answers.
func TestRouterContextTokensUsesTheRoutedModel(t *testing.T) {
	r := &scriptedRouter{pick: always("faux-1")}
	rig := newRoutedRig(t, `
model: faux-1
steps:
  - text: "a reply"
    usage: {input: 90000, output: 6000}
`, r, 128000)
	lane := rig.mustLane("main")
	if _, err := lane.Prompt(context.Background(), "hello", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := lane.ContextTokens(); got != 96000 {
		t.Fatalf("ContextTokens = %d, want the measured 96000", got)
	}
}
