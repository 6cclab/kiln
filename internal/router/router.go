// Package router routes a virtual model's requests to physical models, as
// pi's virtual models do (packages/coding-agent/src/core/virtual-models.ts).
//
// A lane may name a virtual model (provider.ApiVirtual). Before every
// request the harness asks that model's Router which physical model and
// thinking level to use; nothing below the routing step ever sees the
// virtual model. Set.Resolve checks every answer the way pi's
// ModelRuntime.resolveModel does. The package knows nothing of the
// harness: the harness supplies the reason, the conversation and the state
// it stored on the session branch, and stores the state a route returns.
package router

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// Reason says why a request is being routed (pi's ModelRouteReason).
type Reason string

const (
	// ReasonUser is the first request after a message the user wrote (a
	// prompt or a queued follow-up).
	ReasonUser Reason = "user"
	// ReasonContinuation is any other request in the turn loop: after tool
	// results, a hook's message, or a paused turn.
	ReasonContinuation Reason = "continuation"
	// ReasonRetry is an automatic retry after a failed request, including
	// the resend after compacting for a context overflow.
	ReasonRetry Reason = "retry"
	// ReasonDirect is a request outside the turn loop, such as a
	// compaction summary. It neither reads nor writes router state.
	ReasonDirect Reason = "direct"
)

// Choice is a physical model and the thinking level it ran at.
type Choice struct {
	Model         provider.Model
	ThinkingLevel provider.ThinkingLevel
}

// Failure is the request a retry follows: the physical model it went to,
// its thinking level, and why it failed. Messages no longer contain it.
type Failure struct {
	Model         provider.Model
	ThinkingLevel provider.ThinkingLevel
	Err           error
}

// Request is what a Router decides from (pi's ModelRouteRequest).
type Request struct {
	// Virtual is the selected virtual model.
	Virtual provider.Model
	// ThinkingLevel is the selected thinking level; its meaning is up to
	// the router. "" means the model decides.
	ThinkingLevel provider.ThinkingLevel
	Reason        Reason
	// Previous is the physical model of the latest successful response in
	// Messages, nil when there is none. Set.Resolve fills it.
	Previous *Choice
	// Failed is the failed request a ReasonRetry follows, nil otherwise.
	Failed *Failure
	// State is the router state last stored on this session branch, nil
	// before the first and for ReasonDirect.
	State json.RawMessage
	// Messages is the conversation this request sends.
	Messages []msg.Message
}

// Route is a router's answer for one request (pi's ModelRoute).
type Route struct {
	Model         provider.Model
	ThinkingLevel provider.ThinkingLevel
	// State is the new router state, stored on the session branch unless
	// it is nil or equal to Request.State. Ignored for ReasonDirect.
	State json.RawMessage
}

// Router picks the physical model and thinking level for one request.
// The model it returns must be a physical model whose provider has
// credentials.
type Router interface {
	Route(ctx context.Context, req Request) (Route, error)
}

// Catalog is what Set.Resolve checks a route against: the model registry
// and its credential check. *provider.Registry satisfies it.
type Catalog interface {
	GetModel(providerID, modelID string) (provider.Model, bool)
	CheckAuth(ctx context.Context, providerID string) (bool, error)
}

// Set holds the routers of the registered virtual models, by provider and
// model id. The zero Set has none.
type Set struct {
	routers map[string]Router
}

func key(providerID, modelID string) string { return providerID + "/" + modelID }

// Add registers r as the router of the virtual model providerID/modelID.
func (s *Set) Add(providerID, modelID string, r Router) {
	if s.routers == nil {
		s.routers = map[string]Router{}
	}
	s.routers[key(providerID, modelID)] = r
}

// Has reports whether providerID/modelID has a router.
func (s *Set) Has(providerID, modelID string) bool {
	_, ok := s.routers[key(providerID, modelID)]
	return ok
}

// Get returns providerID/modelID's router, for inspecting what
// setupModelRouter built (its Classifier, its Fallback) in a test.
func (s *Set) Get(providerID, modelID string) (Router, bool) {
	r, ok := s.routers[key(providerID, modelID)]
	return r, ok
}

// Resolve asks req.Virtual's router for one request's model and checks the
// answer as pi's ModelRuntime.resolveModel does, with its messages less
// the trailing period (Go error style): the virtual model must be
// registered, the target must be a physical model in catalog, and its
// provider must have credentials. The thinking level is clamped to the
// target. Resolve fills req.Previous from req.Messages, and resolves
// req.Failed's model to its catalog entry.
func (s *Set) Resolve(ctx context.Context, catalog Catalog, req Request) (Route, error) {
	name := "Virtual model " + key(req.Virtual.Provider, req.Virtual.ID)
	r, ok := s.routers[key(req.Virtual.Provider, req.Virtual.ID)]
	if !ok {
		return Route{}, fmt.Errorf("%s is not registered", name)
	}
	if latest := latestResponse(req.Messages); latest != nil {
		if m, ok := physical(catalog, latest.Provider, latest.Model); ok {
			req.Previous = &Choice{Model: m}
		}
	}
	if req.Failed != nil {
		// A failure that names no physical model (the routing itself
		// failed) has no request to report, as in pi.
		if m, ok := physical(catalog, req.Failed.Model.Provider, req.Failed.Model.ID); ok {
			f := *req.Failed
			f.Model = m
			req.Failed = &f
		} else {
			req.Failed = nil
		}
	}
	if req.Reason == ReasonDirect {
		req.State = nil
	}
	route, err := r.Route(ctx, req)
	if err != nil {
		return Route{}, err
	}
	routed := fmt.Sprintf("%s routed to %s", name, key(route.Model.Provider, route.Model.ID))
	target, ok := physical(catalog, route.Model.Provider, route.Model.ID)
	if !ok {
		return Route{}, fmt.Errorf("%s, which is not a physical model", routed)
	}
	if hasAuth, err := catalog.CheckAuth(ctx, target.Provider); err != nil || !hasAuth {
		return Route{}, fmt.Errorf("%s, which has no credentials", routed)
	}
	out := Route{Model: target, ThinkingLevel: provider.ClampThinkingLevel(target, route.ThinkingLevel), State: route.State}
	if req.Reason == ReasonDirect {
		out.State = nil
	}
	return out, nil
}

// physical is a catalog model that is not virtual (pi's getPhysicalModel).
func physical(catalog Catalog, providerID, modelID string) (provider.Model, bool) {
	m, ok := catalog.GetModel(providerID, modelID)
	if !ok || provider.IsVirtual(m) {
		return provider.Model{}, false
	}
	return m, true
}

// latestResponse is the latest successful response in messages: failed and
// aborted requests are skipped (pi's findLatestResponse).
func latestResponse(messages []msg.Message) *msg.AssistantMessage {
	for i := len(messages) - 1; i >= 0; i-- {
		am, ok := messages[i].(msg.AssistantMessage)
		if ok && am.StopReason != msg.StopError && am.StopReason != msg.StopAborted {
			return &am
		}
	}
	return nil
}
