package router

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// catalog is a Catalog over a fixed model list; authed names the
// providers with credentials.
type catalog struct {
	models  []provider.Model
	authed  map[string]bool
	authErr error
}

func (c catalog) GetModel(p, id string) (provider.Model, bool) {
	for _, m := range c.models {
		if m.Provider == p && m.ID == id {
			return m, true
		}
	}
	return provider.Model{}, false
}

func (c catalog) CheckAuth(_ context.Context, p string) (bool, error) {
	return c.authed[p], c.authErr
}

// fixed answers every request with route, and records what it was asked.
type fixed struct {
	route Route
	err   error
	got   []Request
}

func (f *fixed) Route(_ context.Context, req Request) (Route, error) {
	f.got = append(f.got, req)
	return f.route, f.err
}

var (
	auto    = provider.Model{Provider: "kiln", ID: "auto", Api: provider.ApiVirtual}
	other   = provider.Model{Provider: "kiln", ID: "other", Api: provider.ApiVirtual}
	big     = provider.Model{Provider: "cloud", ID: "big", Api: provider.ApiAnthropicMessages, Reasoning: true}
	small   = provider.Model{Provider: "cloud", ID: "small", Api: provider.ApiAnthropicMessages}
	locked  = provider.Model{Provider: "paid", ID: "locked", Api: provider.ApiAnthropicMessages}
	testCat = catalog{models: []provider.Model{auto, other, big, small, locked}, authed: map[string]bool{"cloud": true, "kiln": true}}
)

func TestResolveValidates(t *testing.T) {
	cases := []struct {
		name    string
		virtual provider.Model
		route   Route
		cat     catalog
		want    string
	}{
		{"unregistered virtual model", other, Route{Model: big}, testCat,
			"Virtual model kiln/other is not registered"},
		{"target not in the catalog", auto, Route{Model: provider.Model{Provider: "cloud", ID: "gone"}}, testCat,
			"Virtual model kiln/auto routed to cloud/gone, which is not a physical model"},
		{"target is virtual", auto, Route{Model: other}, testCat,
			"Virtual model kiln/auto routed to kiln/other, which is not a physical model"},
		{"target has no credentials", auto, Route{Model: locked}, testCat,
			"Virtual model kiln/auto routed to paid/locked, which has no credentials"},
		{"credential check fails", auto, Route{Model: big},
			catalog{models: testCat.models, authed: testCat.authed, authErr: errors.New("keychain locked")},
			"Virtual model kiln/auto routed to cloud/big, which has no credentials"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var s Set
			s.Add("kiln", "auto", &fixed{route: c.route})
			_, err := s.Resolve(context.Background(), c.cat, Request{Virtual: c.virtual, Reason: ReasonUser})
			if err == nil || err.Error() != c.want {
				t.Fatalf("Resolve error = %v, want %q", err, c.want)
			}
		})
	}
}

func TestResolveRouterErrorPassesThrough(t *testing.T) {
	var s Set
	boom := errors.New("classifier exploded")
	s.Add("kiln", "auto", &fixed{err: boom})
	if _, err := s.Resolve(context.Background(), testCat, Request{Virtual: auto}); !errors.Is(err, boom) {
		t.Fatalf("Resolve error = %v, want the router's own", err)
	}
}

// The route's thinking level is clamped to the target, and the target is
// the catalog's entry, not the router's copy.
func TestResolveClampsAndUsesCatalogModel(t *testing.T) {
	var s Set
	s.Add("kiln", "auto", &fixed{route: Route{Model: provider.Model{Provider: "cloud", ID: "small"}, ThinkingLevel: provider.ThinkingHigh}})
	got, err := s.Resolve(context.Background(), testCat, Request{Virtual: auto})
	if err != nil {
		t.Fatal(err)
	}
	if got.Model.Api != small.Api || got.ThinkingLevel != provider.ThinkingOff {
		t.Fatalf("route = %+v, want the catalog's cloud/small at off", got)
	}
}

// Previous is the latest successful physical response; failed and aborted
// replies are skipped. Failed is resolved against the catalog.
func TestResolveFillsPreviousAndFailed(t *testing.T) {
	var s Set
	r := &fixed{route: Route{Model: big}}
	s.Add("kiln", "auto", r)
	messages := []msg.Message{
		msg.UserMessage{Role: msg.RoleUser},
		msg.AssistantMessage{Role: msg.RoleAssistant, Provider: "cloud", Model: "small", StopReason: msg.StopStop},
		msg.AssistantMessage{Role: msg.RoleAssistant, Provider: "cloud", Model: "big", StopReason: msg.StopError},
		msg.AssistantMessage{Role: msg.RoleAssistant, Provider: "cloud", Model: "big", StopReason: msg.StopAborted},
	}
	cause := errors.New("529 overloaded")
	_, err := s.Resolve(context.Background(), testCat, Request{Virtual: auto, Reason: ReasonRetry, Messages: messages,
		Failed: &Failure{Model: provider.Model{Provider: "cloud", ID: "big"}, Err: cause}})
	if err != nil {
		t.Fatal(err)
	}
	req := r.got[0]
	if req.Previous == nil || req.Previous.Model.ID != "small" {
		t.Errorf("Previous = %+v, want cloud/small", req.Previous)
	}
	if req.Failed == nil || req.Failed.Model.Api != big.Api || !errors.Is(req.Failed.Err, cause) {
		t.Errorf("Failed = %+v, want the catalog's cloud/big with the error", req.Failed)
	}
}

// A direct request neither reads nor writes state.
func TestResolveDirectIgnoresState(t *testing.T) {
	var s Set
	r := &fixed{route: Route{Model: big, State: json.RawMessage(`{"n":2}`)}}
	s.Add("kiln", "auto", r)
	got, err := s.Resolve(context.Background(), testCat, Request{Virtual: auto, Reason: ReasonDirect, State: json.RawMessage(`{"n":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if r.got[0].State != nil {
		t.Errorf("router saw state %s on a direct request", r.got[0].State)
	}
	if got.State != nil {
		t.Errorf("direct route kept state %s", got.State)
	}
	// Any other reason passes state both ways.
	got, _ = s.Resolve(context.Background(), testCat, Request{Virtual: auto, Reason: ReasonContinuation, State: json.RawMessage(`{"n":1}`)})
	if string(r.got[1].State) != `{"n":1}` || string(got.State) != `{"n":2}` {
		t.Errorf("continuation: router saw %s, returned %s", r.got[1].State, got.State)
	}
}
