package provider

import (
	"strings"

	"github.com/andrepato/harness/internal/msg"
)

// Reasoning suppression for local models.
//
// Why this module exists, and why it is not provider config:
//
// Some Qwen models keep reasoning no matter what the transport asks. They do
// not refuse -- they relocate the reasoning from the `thinking` field into
// `content`, then exhaust `max_tokens` without ever emitting a tool call. An
// agent loop driving such a model simply never acts.
//
// pi-ai models this properly and offers several transport-level fields for
// it (thinkingFormat: "qwen" | "qwen-chat-template", thinkingTokenBudgetField).
// Measured against Ollama 0.32.15 on 2026-09-22, none of them work. So
// suppression has to happen in the one channel Ollama cannot ignore -- the
// prompt itself.
//
// This is a verbatim port of harness/src/provider/reasoning.ts.

// NoThinkSuppression is how to stop a model reasoning past its token budget.
type NoThinkSuppression string

const (
	// SuppressionNone: nothing needed. The model honors think:false, or does
	// not reason.
	SuppressionNone NoThinkSuppression = "none"
	// SuppressionNoThinkSuffix appends Qwen's in-prompt switch to the last
	// user message.
	SuppressionNoThinkSuffix NoThinkSuppression = "no_think_suffix"
)

// NoThink is Qwen's in-prompt suffix that suppresses reasoning.
const NoThink = "/no_think"

// needsSuffix are models measured to need the suffix. Deliberately a list of
// verified ids rather than a regex over "qwen3": Qwen3.5 and qwen3.8 are both
// Qwen 3.x and both behave correctly, so a family-wide rule would suppress
// reasoning on models that were using it well.
var needsSuffix = map[string]bool{
	"qwen3-cc:latest": true,
	"qwen3:30b-a3b":   true,
	"qwen3:30b":       true,
}

// verifiedClean are models measured to be fine without it. Kept explicit so
// the list is auditable.
var verifiedClean = map[string]bool{
	"Qwen3.5:9b":     true,
	"Qwen3.5:latest": true,
	"qwen3.8:latest": true,
}

// promptSuppressedProviders are providers whose transport cannot control
// reasoning, so the prompt must.
//
// This gate is load-bearing. /no_think is a Qwen prompt convention, not a
// general switch: appending it to a Claude or GPT request injects a stray
// token into the user's message and suppresses nothing. Hosted providers
// already express reasoning properly through pi's thinkingLevelMap, which the
// transport honors, so the harness must leave them alone.
var promptSuppressedProviders = map[string]bool{
	"ollama": true,
}

// SuppressionOptions carries an operator override, e.g. from config. It wins
// over every heuristic.
type SuppressionOptions struct {
	Override NoThinkSuppression
}

// SuppressionForModel is the minimal model shape SuppressionFor needs.
type SuppressionForModel struct {
	ID        string
	Provider  string
	Reasoning bool
}

// suppressionDecide decides how to suppress reasoning for a model.
//
// Within a prompt-suppressed provider, unknown reasoning-capable models
// default to the suffix. That asymmetry is deliberate: an unnecessary
// /no_think costs a few tokens and a slightly shallower answer, while a
// missing one costs the entire turn. Outside those providers the default is
// "none" -- there, the expensive mistake runs the other way.
func suppressionDecide(model SuppressionForModel, opts SuppressionOptions) NoThinkSuppression {
	if opts.Override != "" {
		return opts.Override
	}
	if !model.Reasoning {
		return SuppressionNone
	}
	// A model reached over a transport that honors thinking config needs
	// nothing from us, whatever its family.
	if model.Provider != "" && !promptSuppressedProviders[model.Provider] {
		return SuppressionNone
	}
	if verifiedClean[model.ID] {
		return SuppressionNone
	}
	if needsSuffix[model.ID] {
		return SuppressionNoThinkSuffix
	}
	return SuppressionNoThinkSuffix
}

// SuppressionFor decides how to suppress reasoning for m, with no override.
// SuppressionFor(model) is Registry.Resolve's caller-facing entry point,
// converted into the registry's Suppression type.
func SuppressionFor(m Model) Suppression {
	mode := suppressionDecide(SuppressionForModel{ID: m.ID, Provider: m.Provider, Reasoning: m.Reasoning}, SuppressionOptions{})
	if mode == SuppressionNoThinkSuffix {
		return Suppression{Suffix: NoThink}
	}
	return Suppression{}
}

// ApplySuppression applies suppression to a transcript, returning a new
// slice. The suffix goes on the last user message rather than the system
// prompt, because that is where Qwen's template looks for it. Putting it in
// the system prompt looks tidier and does nothing.
//
// Only a UserMessage whose content is a single text block takes the suffix;
// multimodal content is left alone rather than guessed at. Never double
// appends: a message that already contains the suffix is left unchanged.
func ApplySuppression(messages []msg.Message, mode NoThinkSuppression) []msg.Message {
	out := make([]msg.Message, len(messages))
	copy(out, messages)
	if mode != SuppressionNoThinkSuffix {
		return out
	}

	lastUser := -1
	for i, m := range out {
		if m.MessageRole() == msg.RoleUser {
			lastUser = i
		}
	}
	if lastUser == -1 {
		return out
	}

	target, ok := out[lastUser].(msg.UserMessage)
	if !ok {
		return out
	}
	if len(target.Content) != 1 {
		return out
	}
	text, ok := target.Content[0].(msg.TextContent)
	if !ok {
		return out
	}
	if strings.Contains(text.Text, NoThink) {
		return out
	}

	text.Text = text.Text + " " + NoThink
	target.Content = msg.Blocks{text}
	out[lastUser] = target
	return out
}
