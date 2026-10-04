package commands

import (
	"fmt"
	"strings"
	"sync"

	"github.com/andrepato/harness/internal/budget"
	"github.com/andrepato/harness/internal/harness"
)

// switchFit is what /model and /compact share about a switch to a model
// whose window is smaller than the conversation.
//
// At the switch, /model says so with the numbers and what happens next:
// the next message compacts the conversation with the new model first, in
// parts that each fit its window (internal/compaction/fit.go), so nothing
// is ever sent that the new model cannot hold. That is the default because
// it works with any model, local ones included, and spends nothing on a
// model the user just moved away from.
//
// It also offers the alternative: the outgoing model can still read the
// whole conversation in one request, which is faster and summarises
// better. /compact, run while the conversation is still too large for the
// current model, summarises with that outgoing model. The user asks for it
// by running /compact after being told which model it will use.
type switchFit struct {
	mu sync.Mutex
	// outgoing is "provider/id" of the last model whose window held the
	// conversation, recorded at a switch away from it; "" when none.
	outgoing       string
	outgoingWindow int
}

// conversationSize is what the next request carries, system prompt and
// tools included: the lane's one context estimate
// (harness.Lane.ContextTokens), which the footer and /context also show.
// Read after a switch it is the estimate the harness checks the new
// model's requests with.
func conversationSize(deps BuiltinDeps) int {
	if deps.Lane == nil {
		return 0
	}
	n, _ := deps.Lane.ContextTokens()
	return n
}

// shortModel drops the provider from "provider/id" for prose.
func shortModel(label string) string {
	if i := strings.Index(label, "/"); i >= 0 {
		return label[i+1:]
	}
	return label
}

// switched records a switch from outgoing (whose window was
// outgoingWindow) to label on tier, with a conversation of size tokens,
// and returns the warning to show, or "" when the conversation fits.
func (s *switchFit) switched(outgoing string, outgoingWindow int, label string, tier budget.Tier, size int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := harness.RequestLimit(tier.ContextWindow, tier.Compaction.ReserveTokens)
	if limit <= 0 || size <= limit {
		return ""
	}
	// Remember the outgoing model if it holds the conversation; a chain of
	// switches between small models keeps the last one that did.
	if outgoing != "" && outgoing != label && size <= harness.RequestLimit(outgoingWindow, 0) {
		s.outgoing, s.outgoingWindow = outgoing, outgoingWindow
	}
	if s.outgoing == label {
		s.outgoing = ""
	}
	warning := fmt.Sprintf("The conversation (~%s tokens with the system prompt and tools) is more than %s takes in one request (%s of its %s window; the rest is room for the reply): the next message compacts it with %s first, in parts that fit.",
		formatTokens(size), shortModel(label), formatTokens(limit), formatTokens(tier.ContextWindow), shortModel(label))
	if s.outgoing != "" {
		warning += fmt.Sprintf(" Or /compact now to summarise it with %s, which holds all of it.", s.outgoing)
	}
	return warning
}

// summariserFor is the model /compact should summarise with: the
// remembered outgoing model while the conversation (size tokens) is still
// too large for the current tier, otherwise "" (the current model).
func (s *switchFit) summariserFor(size int, tier budget.Tier) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outgoing == "" {
		return ""
	}
	limit := harness.RequestLimit(tier.ContextWindow, tier.Compaction.ReserveTokens)
	if limit <= 0 || size <= limit || size > harness.RequestLimit(s.outgoingWindow, 0) {
		return ""
	}
	return s.outgoing
}
