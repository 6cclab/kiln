package cli

import (
	"context"
	"sync"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/tool"
)

// tierPrompt is the part of the system prompt sized by the tier: CLAUDE.md
// memory, auto memory and the skills index, each budgeted against the
// tier's shares (SystemPromptTokens, SkillsListingTokens).
type tierPrompt struct {
	Memory     string
	AutoMemory string
	Skills     string
}

// tierPromptMemo holds one tierPrompt per context window. kiln/auto's
// windows are all loaded at startup (prime), so a route that re-sizes the
// session reads no files on the lane's goroutine; a window first seen
// later (/model to another model) is loaded on demand. Each window is
// loaded once, so a session that goes A→B→A sends A the byte-identical
// prompt it sent before, and A's prompt cache can still hit.
type tierPromptMemo struct {
	load func(tier budget.Tier) tierPrompt

	mu       sync.Mutex
	byWindow map[int]tierPrompt
}

func newTierPromptMemo(load func(tier budget.Tier) tierPrompt) *tierPromptMemo {
	return &tierPromptMemo{load: load, byWindow: map[int]tierPrompt{}}
}

// seed records p as tier's prompt without loading it: the startup tier's,
// which the session already loaded (with its warnings) on its own.
func (m *tierPromptMemo) seed(tier budget.Tier, p tierPrompt) {
	m.mu.Lock()
	m.byWindow[tier.ContextWindow] = p
	m.mu.Unlock()
}

// prime loads the prompt for every window not yet held.
func (m *tierPromptMemo) prime(windows ...int) {
	for _, w := range windows {
		if w > 0 {
			m.get(budget.TierForWindow(w))
		}
	}
}

// get is tier's prompt, loading it the first time its window is asked for.
func (m *tierPromptMemo) get(tier budget.Tier) tierPrompt {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.byWindow[tier.ContextWindow]; ok {
		return p
	}
	p := m.load(tier)
	m.byWindow[tier.ContextWindow] = p
	return p
}

// sessionSizing re-sizes what the CLI built from the tier — the system
// prompt, the task tool's description and the MCP session's tier — when
// the session's tier moves: on /model (agent.SetModel) and, on kiln/auto,
// on every route to a model with another window. Its onTierChanged is the
// session's agent.Started.OnTierChanged.
type sessionSizing struct {
	started *agent.Started
	mcpSess *mcpSession
	memo    *tierPromptMemo
	// build assembles the system prompt from the tier's part, the
	// scratchpad instructions and the MCP index.
	build func(p tierPrompt, scratchpad, mcpIndex string) string
	// taskTool builds the task tool, whose agent and role descriptions
	// are truncated per tier.
	taskTool func(tier budget.Tier) *tool.Tool

	// mu serialises every prompt rebuild, so a route's rebuild and the
	// background MCP connect's never land out of order; it also guards
	// the prompt's other inputs.
	mu         sync.Mutex
	scratchpad string
	mcpIndex   string
}

// prompt is the system prompt for tier with the inputs held now.
func (s *sessionSizing) prompt(tier budget.Tier) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.build(s.memo.get(tier), s.scratchpad, s.mcpIndex)
}

// refresh rebuilds the system prompt for the session's current tier and
// sets it. update, if non-nil, changes the prompt's inputs first.
func (s *sessionSizing) refresh(update func(s *sessionSizing)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if update != nil {
		update(s)
	}
	s.started.Harness.SetSystemPrompt(s.build(s.memo.get(s.started.CurrentTier()), s.scratchpad, s.mcpIndex))
}

// setMCPIndex records the MCP posture index and rebuilds the prompt.
func (s *sessionSizing) setMCPIndex(idx string) {
	s.refresh(func(s *sessionSizing) { s.mcpIndex = idx })
}

// setScratchpad records the scratchpad instructions and rebuilds the
// prompt.
func (s *sessionSizing) setScratchpad(text string) {
	s.refresh(func(s *sessionSizing) { s.scratchpad = text })
}

// onTierChanged is agent.Started.OnTierChanged. On kiln/auto it runs on
// the lane's goroutine before the routed request is built: the memo holds
// every target's prompt, so it reads no files. It does not re-gate tools:
// every tier's strategy is posture-index (budget.StrategyForWindow), and
// /model re-gates on its own (switchModel).
func (s *sessionSizing) onTierChanged(ctx context.Context, tier budget.Tier) {
	if s.mcpSess != nil {
		s.mcpSess.mu.Lock()
		s.mcpSess.tier = tier
		s.mcpSess.mu.Unlock()
	}
	if s.taskTool != nil {
		// AddTools replaces a tool of the same name.
		s.started.Harness.AddTools(s.taskTool(tier))
	}
	s.refresh(nil)
}
