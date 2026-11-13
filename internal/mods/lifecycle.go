package mods

import (
	"context"
	"time"
)

// The session's own events: its end, a compaction about to run, a row about
// to be kept. Each builds the payload claude-code.d.ts declares and reads
// back what the hooks decided.

// sessionEndBound is how long session.end's whole chain may take: exits stay
// fast whatever is loaded (claude-code.d.ts SessionEndInput).
const sessionEndBound = 2 * time.Second

// SessionEnd raises session.end ({ reason, sessionId, resume: { id } }) and
// waits for its hooks, all of them within one short bound. reason is a
// SessionEnd settings-hook reason ("prompt_input_exit" leaving the
// interactive UI, "other" when a -p run finishes); resumeID is the session
// --resume named, "" when there was none.
func (e *Engine) SessionEnd(reason, sessionID, resumeID string) {
	if e == nil || e.closed.Load() {
		return
	}
	ev := map[string]any{"reason": reason, "sessionId": sessionID, "resume": map[string]any{"id": resumeID}}
	_, err := e.raiseBounded("session.end", ev, OpResult(func(map[string]any) (map[string]any, error) {
		return map[string]any{"sessionId": sessionID}, nil
	}), sessionEndBound)
	if err != nil && err != ErrClosed {
		e.log.Warn("mods: session.end did not settle", "err", err)
	}
}

// CompactDecision is what the session.compact chain decided about a
// compaction kiln is about to run.
type CompactDecision struct {
	// Skip is a hook's reason to leave the conversation as it is ("" to go on).
	Skip string
	// Instructions is the summary's instructions as the hooks left them;
	// Rewritten is false when none changed them.
	Instructions string
	Rewritten    bool
}

// SessionCompact raises session.compact ({ trigger, instructions?, messages,
// agentId? }) before kiln summarises. trigger is manual, auto or plugin;
// messages is the conversation as $.session.messages() lists it. A $
// call that already raised the event (MarkRaised) is not raised again. A hook
// that rewrites messages, or answers { messages } of its own, is not applied:
// kiln summarises the conversation it holds.
func (e *Engine) SessionCompact(ctx context.Context, agentID, trigger, instructions string, messages []any) CompactDecision {
	out := CompactDecision{Instructions: instructions}
	if e == nil || IsRaised(ctx, "session.compact") || !e.Has("session.compact") {
		return out
	}
	ev := map[string]any{"trigger": trigger, "messages": messages}
	if agentID != "" {
		ev["agentId"] = agentID
	}
	if instructions != "" {
		ev["instructions"] = instructions
	}
	ran := false
	core := func(in map[string]any, done func(map[string]any, error)) {
		ran = true
		if s, ok := in["instructions"].(string); ok && s != instructions {
			out.Instructions, out.Rewritten = s, true
		}
		done(map[string]any{"messages": in["messages"]}, nil)
	}
	res, err := e.Raise(ctx, "session.compact", ev, core)
	if err != nil {
		e.log.Warn("mods: session.compact chain failed; compacting as asked", "err", err)
		return CompactDecision{Instructions: instructions}
	}
	if skip, ok := res["skip"].(string); ok && skip != "" {
		out.Skip = skip
	} else if !ran {
		out.Rewritten = false
	}
	return out
}

// AppendRow is a row a conversation of the session kept: the session.append
// event's payload.
type AppendRow struct {
	// Type is user, assistant, attachment or system; Role the API role.
	Type    string
	Role    string
	Door    string
	UUID    string
	AgentID string
	// Content is the row's API content blocks (JSON-shaped).
	Content []any
	// Origin is the row's SessionAppendOrigin; nil is { kind: "engine" }.
	Origin map[string]any
}

// SessionAppend raises session.append ({ message, door, origin, uuid,
// agentId? }) for one row. In Claude Code the event runs before the row is
// stored and a hook may rewrite its content; kiln raises it right after the
// row is committed, so hooks observe every row and a rewrite is not applied
// (logged at debug).
func (e *Engine) SessionAppend(row AppendRow) {
	if e == nil || !e.Has("session.append") {
		return
	}
	msg := map[string]any{"type": row.Type, "content": row.Content}
	if row.Role != "" {
		msg["role"] = row.Role
	}
	origin := row.Origin
	if origin == nil {
		origin = map[string]any{"kind": "engine"}
	}
	ev := map[string]any{"message": msg, "door": row.Door, "origin": origin, "uuid": row.UUID}
	if row.AgentID != "" {
		ev["agentId"] = row.AgentID
	}
	e.raise("session.append", ev, ev)
}
