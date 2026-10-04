package commands

import (
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/budget"
)

// TestSwitchFit covers the switch warning and which model /compact uses:
// a conversation that fits says nothing; one over the new window warns
// with the numbers and offers the outgoing model when that one holds it;
// /compact uses the outgoing model only while the conversation is still
// too large for the current one.
func TestSwitchFit(t *testing.T) {
	big := budget.TierForWindow(200000)
	small := budget.TierForWindow(49152)

	sw := &switchFit{}
	if w := sw.switched("anthropic/opus", big.ContextWindow, "ollama/qwen", small, 20000); w != "" {
		t.Errorf("a 20k conversation fits 49k, but the switch warned: %q", w)
	}

	w := sw.switched("anthropic/opus", big.ContextWindow, "ollama/qwen", small, 107000)
	for _, want := range []string{"~107k tokens", "more than qwen takes in one request (44.2k of its 49.2k window", "next message compacts it with qwen", "/compact now to summarise it with anthropic/opus"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning %q lacks %q", w, want)
		}
	}
	if got := sw.summariserFor(107000, small); got != "anthropic/opus" {
		t.Errorf("summariserFor(107k) = %q, want the outgoing model", got)
	}
	if got := sw.summariserFor(20000, small); got != "" {
		t.Errorf("summariserFor(20k) = %q, want the current model once it fits", got)
	}

	// Under the window but over what one request may carry: the warning
	// compares against that limit, never saying a smaller number is over a
	// larger one, and ends as a sentence.
	sw = &switchFit{}
	w = sw.switched("anthropic/opus", big.ContextWindow, "ollama/qwen", small, 46100)
	if !strings.Contains(w, "~46.1k tokens with the system prompt and tools) is more than qwen takes in one request (44.2k of its 49.2k window") || strings.Contains(w, "is over") || !strings.HasSuffix(w, ".") {
		t.Errorf("warning for 46.1k against a 44.2k limit in a 49.2k window: %q", w)
	}

	// Too big for the outgoing model too: no offer.
	sw = &switchFit{}
	w = sw.switched("ollama/a", 32768, "ollama/b", budget.TierForWindow(16384), 40000)
	if strings.Contains(w, "/compact now") {
		t.Errorf("offered a model that cannot hold the conversation: %q", w)
	}
	if got := sw.summariserFor(40000, budget.TierForWindow(16384)); got != "" {
		t.Errorf("summariserFor = %q, want none", got)
	}
}
