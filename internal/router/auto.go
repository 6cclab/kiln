package router

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// kiln/auto is kiln's built-in virtual model. Its router, Auto, is a port
// of pi's example router (packages/coding-agent/examples/extensions/
// jev-router.ts): it plans on a strong or mid model, hands the work to a
// cheap one after the first successful edit, and sends requests outside
// the turn loop to the cheap one. jev's three models become slots that
// settings.json's modelRouter maps to modelRoles: Sol is PlanComplex,
// Terra is Plan, and Luna is both Implement and Direct.
const (
	AutoProvider = "kiln"
	AutoModelID  = "auto"
	AutoName     = "Auto (kiln)"
)

// editTools are the tools whose successful result means implementation
// has started (jev's EDIT_TOOLS). kiln's file tools are edit and write
// (internal/tools); it has no multi-edit or notebook tool.
var editTools = map[string]bool{"edit": true, "write": true}

// classifierPromptChars caps the user text a classifier reads, as jev's
// lastUserText(...).slice(0, 16_000).
const classifierPromptChars = 16_000

// Phases of Auto's state, as jev's JevState.phase.
const (
	PhasePlanning       = "planning"
	PhaseImplementation = "implementation"
)

// Targets are the physical models kiln/auto routes to, one per slot.
type Targets struct {
	Plan        provider.Model
	PlanComplex provider.Model
	Implement   provider.Model
	Direct      provider.Model
}

// All lists the targets, slot order, duplicates included.
func (t Targets) All() []provider.Model {
	return []provider.Model{t.Plan, t.PlanComplex, t.Implement, t.Direct}
}

// Describe is kiln/auto's role summary for /model and "kiln models":
// "plan→<id> · complex→<id> · implement→<id>" — the three slots a
// request actually plans or implements on (Direct, compaction's own, is
// not shown: it is rarely distinct from Implement and would just add
// noise to an already-dense row).
func (t Targets) Describe() string {
	return fmt.Sprintf("plan→%s · complex→%s · implement→%s", t.Plan.ID, t.PlanComplex.ID, t.Implement.ID)
}

// Complexity is a classifier's rating of a request (jev's criteria).
type Complexity string

const (
	// Standard is "Ordinary features, fixes, reviews, or questions".
	Standard Complexity = "standard"
	// Complex is "Subtle design, cross-cutting changes, or hard
	// debugging".
	Complex Complexity = "complex"
)

// Classifier rates how demanding the request in prompt is. An error, or
// an answer that is neither Standard nor Complex, counts as no answer.
type Classifier interface {
	Classify(ctx context.Context, prompt string) (Complexity, error)
}

// Auto is kiln/auto's router. Classifier nil means no classifier is
// available, as jev's findOfType returning nothing; Fallback nil means
// the planning model is Plan whenever the classifier gives no answer (jev
// picks Terra).
type Auto struct {
	Targets    Targets
	Classifier Classifier
	// Fallback rates prompt when the classifier is nil or gives no
	// answer. Nil rates every request Standard.
	Fallback func(prompt string) Complexity
}

// AutoState is Auto's router state (jev's JevState): the phase, and the
// model it routes that phase to, as "provider/model".
type AutoState struct {
	Phase string `json:"phase"`
	Model string `json:"model"`
}

func modelKey(m provider.Model) string { return m.Provider + "/" + m.ID }

// routeTo is jev's routeTo: the chosen model at the requested thinking
// level (Set.Resolve clamps it to the model), with the state to store.
func routeTo(req Request, m provider.Model, state *AutoState) (Route, error) {
	out := Route{Model: m, ThinkingLevel: req.ThinkingLevel}
	if state != nil {
		raw, err := json.Marshal(state)
		if err != nil {
			return Route{}, err
		}
		out.State = raw
	}
	return out, nil
}

// Route is jev's route, line for line.
func (a *Auto) Route(ctx context.Context, req Request) (Route, error) {
	if req.Reason == ReasonDirect {
		return routeTo(req, a.Targets.Direct, nil)
	}
	state, ok := parseAutoState(req.State)
	if !ok {
		m, err := a.choosePlanningModel(ctx, req)
		if err != nil {
			return Route{}, err
		}
		return routeTo(req, m, &AutoState{Phase: PhasePlanning, Model: modelKey(m)})
	}
	// The planning model made the first edit: hand the rest of the work
	// to the implementation model.
	if state.Phase == PhasePlanning && editedThisTurn(req.Messages) {
		return routeTo(req, a.Targets.Implement, &AutoState{Phase: PhaseImplementation, Model: modelKey(a.Targets.Implement)})
	}
	m, current := a.stateModel(state)
	if !current {
		// The stored model is no longer the phase's (modelRoles or
		// modelRouter changed since it was stored): re-derive it from the
		// phase, and store that. jev's models are constants, so it never
		// meets this.
		return routeTo(req, m, &AutoState{Phase: state.Phase, Model: modelKey(m)})
	}
	return routeTo(req, m, nil)
}

// parseAutoState reads a stored state; ok is false when there is none or
// it is not one Auto wrote.
func parseAutoState(raw json.RawMessage) (AutoState, bool) {
	if len(raw) == 0 {
		return AutoState{}, false
	}
	var s AutoState
	if err := json.Unmarshal(raw, &s); err != nil {
		return AutoState{}, false
	}
	if s.Phase != PhasePlanning && s.Phase != PhaseImplementation {
		return AutoState{}, false
	}
	return s, true
}

// stateModel is the target state names for its phase. When it names none
// of the phase's current targets, it is the phase's default target (Plan
// for planning, Implement for implementation) and current is false.
func (a *Auto) stateModel(s AutoState) (m provider.Model, current bool) {
	if s.Phase == PhaseImplementation {
		return a.Targets.Implement, s.Model == modelKey(a.Targets.Implement)
	}
	switch s.Model {
	case modelKey(a.Targets.PlanComplex):
		return a.Targets.PlanComplex, true
	case modelKey(a.Targets.Plan):
		return a.Targets.Plan, true
	}
	return a.Targets.Plan, false
}

// choosePlanningModel is jev's choosePlanningModel: PlanComplex for
// complex work, Plan otherwise or when no classifier answers. An error
// means the caller's ctx was cancelled (Esc) during classification: the
// turn must abort, not fall back.
func (a *Auto) choosePlanningModel(ctx context.Context, req Request) (provider.Model, error) {
	// Keep a planning model the session already uses, so switching to
	// kiln/auto costs no cache miss.
	if p := req.Previous; p != nil {
		switch modelKey(p.Model) {
		case modelKey(a.Targets.PlanComplex):
			return a.Targets.PlanComplex, nil
		case modelKey(a.Targets.Plan):
			return a.Targets.Plan, nil
		}
	}
	c, err := a.rate(ctx, lastUserText(req.Messages))
	if err != nil {
		return provider.Model{}, err
	}
	if c == Complex {
		return a.Targets.PlanComplex, nil
	}
	return a.Targets.Plan, nil
}

// rate asks the classifier, then the fallback, how demanding prompt is.
// An error is returned only when ctx itself was cancelled while the
// classifier was running: that must abort the turn, never fall back, so
// Esc during classification does not quietly pick a model instead of
// stopping.
func (a *Auto) rate(ctx context.Context, prompt string) (Complexity, error) {
	if runes := []rune(prompt); len(runes) > classifierPromptChars {
		prompt = string(runes[:classifierPromptChars])
	}
	if a.Classifier != nil {
		c, err := a.Classifier.Classify(ctx, prompt)
		if err == nil && (c == Standard || c == Complex) {
			return c, nil
		}
		if err != nil && ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	if a.Fallback != nil {
		return a.Fallback(prompt), nil
	}
	return Standard, nil
}

// lastUserText is jev's lastUserText, reading what the user actually
// typed (msg.UserMessage.KilnTyped) rather than the content kiln built
// around it, so @file expansions, hook context and command output never
// reach the classifier or the heuristic fallback. A message kiln wrote
// before KilnTyped existed, or one no user typed, falls back to its
// content.
func lastUserText(messages []msg.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if um, ok := messages[i].(msg.UserMessage); ok {
			if typed := strings.TrimSpace(um.KilnTyped); typed != "" {
				return um.KilnTyped
			}
			return msg.TextOf(um.Content)
		}
	}
	return ""
}

// editedThisTurn is jev's editedThisTurn: whether a tool call since the
// last user message edited a file successfully.
func editedThisTurn(messages []msg.Message) bool {
	lastUser := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if _, ok := messages[i].(msg.UserMessage); ok {
			lastUser = i
			break
		}
	}
	for _, m := range messages[lastUser+1:] {
		if tr, ok := m.(msg.ToolResultMessage); ok && editTools[tr.ToolName] && !tr.IsError {
			return true
		}
	}
	return false
}

// AutoModel is kiln/auto's catalog entry for targets. Its window and
// output cap are the Plan target's: a session starts sized for the model
// its first request goes to (plan, or planComplex on a complex prompt),
// and each later request re-sizes it for the model it is routed to (the
// harness's OnRoute hook, agent.Retier), as pi's limits follow the
// physical model that answered. Its inputs are those every target takes.
// It reasons if any target does; Set.Resolve clamps each request's level
// to the model it goes to.
func AutoModel(t Targets) provider.Model {
	m := provider.Model{ID: AutoModelID, Name: AutoName, Api: provider.ApiVirtual, Provider: AutoProvider,
		ContextWindow: t.Plan.ContextWindow, MaxTokens: t.Plan.MaxTokens}
	for i, target := range t.All() {
		if i == 0 {
			m.Input = append([]string(nil), target.Input...)
		} else {
			m.Input = intersect(m.Input, target.Input)
		}
		m.Reasoning = m.Reasoning || target.Reasoning
	}
	return m
}

func intersect(a, b []string) []string {
	in := map[string]bool{}
	for _, s := range b {
		in[s] = true
	}
	out := []string{}
	for _, s := range a {
		if in[s] {
			out = append(out, s)
		}
	}
	return out
}

// VirtualProvider is the "kiln" provider: it lists kiln's virtual models
// and never streams one. Every request on a virtual lane is routed to a
// physical model first (internal/harness), so a Stream call here is a
// bug, and fails rather than reaching any endpoint.
type VirtualProvider struct {
	models []provider.Model
}

// NewVirtualProvider lists models, which must be virtual.
func NewVirtualProvider(models ...provider.Model) *VirtualProvider {
	return &VirtualProvider{models: models}
}

func (p *VirtualProvider) ID() string   { return AutoProvider }
func (p *VirtualProvider) Name() string { return "kiln" }
func (p *VirtualProvider) Auth() provider.AuthSpec {
	return provider.AuthSpec{Kind: provider.AuthKindNone}
}
func (p *VirtualProvider) Models() []provider.Model                { return p.models }
func (p *VirtualProvider) RefreshModels(ctx context.Context) error { return nil }

func (p *VirtualProvider) Stream(_ context.Context, m provider.Model, _ []msg.Message, _ provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	ch := make(chan msg.StreamEvent)
	close(ch)
	return ch, func() (*msg.AssistantMessage, error) {
		return nil, fmt.Errorf("virtual model %s/%s must be routed before streaming", m.Provider, m.ID)
	}
}
