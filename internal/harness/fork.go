package harness

import (
	"errors"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/session"
)

// lastRequest is what the lane last sent and where its answer was stored.
type lastRequest struct {
	model      provider.Model
	transcript []msg.Message
	opts       provider.StreamOptions
	entryID    string
}

func (l *Lane) noteRequest(m provider.Model, transcript []msg.Message, opts provider.StreamOptions, entryID string) {
	if entryID == "" {
		return
	}
	l.mu.Lock()
	l.lastReq = &lastRequest{model: m, transcript: transcript, opts: opts, entryID: entryID}
	l.mu.Unlock()
}

// SideRequest continues the lane's last request: the same model, system
// prompt, tools and thinking level, and the same messages followed by its
// answer. A side request built on it (the learning review) shares every
// byte of the prefix the provider just processed, so a prompt cache (an
// Anthropic cache breakpoint, llama.cpp's or Ollama's KV cache) serves
// all of it and only the new tail is read. Hermes-agent forks its review
// for the same reason (NousResearch, MIT).
type SideRequest struct {
	Provider provider.Provider
	Model    provider.Model
	Messages []msg.Message
	Options  provider.StreamOptions
}

// ErrNoSideRequest is ForkLastRequest's error when there is nothing to
// continue: no answered request yet, the conversation moved past the last
// answer (a tool step, a rewind, a compaction), or that answer's tool
// calls never ran.
var ErrNoSideRequest = errors.New("no answered request at the conversation's tip")

// ForkLastRequest returns the side request that continues the lane's last
// request, when that request's answer is still the conversation's tip.
// Messages and Options are the caller's to extend; the lane's are not
// shared.
func (l *Lane) ForkLastRequest() (SideRequest, error) {
	l.mu.Lock()
	last := l.lastReq
	l.mu.Unlock()
	if last == nil {
		return SideRequest{}, ErrNoSideRequest
	}
	if tip, ok := l.GetTipID(); !ok || tip != last.entryID {
		return SideRequest{}, ErrNoSideRequest
	}
	entries, err := l.h.opts.Storage.ScanBranch(session.BranchScan{Start: last.entryID, Order: "newestFirst", Limit: 1})
	if err != nil {
		return SideRequest{}, err
	}
	answer := entriesToTranscript(entries)
	if len(answer) != 1 {
		return SideRequest{}, ErrNoSideRequest
	}
	am, ok := answer[0].(msg.AssistantMessage)
	if !ok {
		return SideRequest{}, ErrNoSideRequest
	}
	for _, c := range am.Content {
		// An answer whose tool calls never ran (the run was stopped)
		// cannot be continued: the provider wants their results next.
		if _, isCall := c.(msg.ToolCall); isCall {
			return SideRequest{}, ErrNoSideRequest
		}
	}
	p, ok := l.h.opts.Registry.Provider(last.model.Provider)
	if !ok {
		return SideRequest{}, errors.New("harness: unknown provider " + last.model.Provider)
	}
	messages := make([]msg.Message, 0, len(last.transcript)+2)
	messages = append(append(messages, last.transcript...), answer[0])
	opts := last.opts
	opts.Tools = append([]provider.ToolDef(nil), last.opts.Tools...)
	return SideRequest{Provider: p, Model: last.model, Messages: messages, Options: opts}, nil
}
