// Package tool defines the contract every tool implements: the built-ins
// (bash, read, edit, write), the harness-authored ones (tool_search, task,
// todo_write, exit_plan_mode, session_search, bash_background, bash_output,
// kill_shell) and MCP adapters. It mirrors pi-agent-core's AgentHarnessTool:
// a JSON Schema for parameters and an Execute that streams partial results
// through an update callback.
package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrepato/harness/internal/msg"
)

// Result is what a tool returns: content blocks the model sees, optional
// machine-readable details kept in the session but not sent to the model,
// and whether the call failed.
type Result struct {
	Content msg.Blocks
	Details json.RawMessage
	IsError bool
}

// Text builds a successful text result.
func Text(s string) Result { return Result{Content: msg.Blocks{msg.Text(s)}} }

// Errorf builds an error result with a formatted message.
func Errorf(format string, args ...any) Result {
	return Result{Content: msg.Blocks{msg.Text(fmt.Sprintf(format, args...))}, IsError: true}
}

// Update receives partial results while a long tool runs. The final result
// is returned from Execute, not sent through Update.
type Update func(partial Result)

// Invocation identifies the call being executed.
type Invocation struct {
	ToolCallID string
	ToolName   string
	// Cwd is the working directory the session runs in.
	Cwd string
}

// Tool is one callable tool. Parameters is a JSON Schema object as the
// provider APIs expect it. Execute receives the raw JSON arguments.
type Tool struct {
	Name        string
	Label       string
	Description string
	Parameters  json.RawMessage
	Execute     func(ctx context.Context, args json.RawMessage, onUpdate Update, inv Invocation) (Result, error)

	// Concurrent marks this tool safe to run in parallel with other
	// Concurrent calls from the same assistant message: its Execute must
	// not share mutable state with the session it was called from (read or
	// write anything the session's own turn loop reads or writes outside
	// of what it's explicitly given through Invocation). The `task` tool
	// qualifies because a dispatched subagent gets its own session and its
	// own storage. Tools that touch the calling session's files, branch or
	// process-wide state (bash, read, edit, write, ...) must leave this
	// false so the turn loop keeps running them one at a time.
	Concurrent bool

	// ServerTool, when non-nil, marks this as a provider server tool: a
	// tool the model calls whose execution happens on the provider's own
	// infrastructure (e.g. Anthropic's web_search), not through Execute.
	// It holds that provider's verbatim tool-declaration JSON (see
	// provider.ToolDef.ServerTool), copied into the request only by the
	// client for that provider; every other provider's client omits the
	// tool entirely. Execute is still required to satisfy the Tool
	// contract and as a defensive fallback, but must never legitimately
	// run: a provider that actually offered this tool always resolves the
	// call itself and never emits a client-side msg.ToolCall for it.
	ServerTool json.RawMessage
}

// Set is a name-indexed collection with stable order.
type Set struct {
	order  []string
	byName map[string]*Tool
}

// NewSet builds a set from tools; a later duplicate name replaces the earlier.
func NewSet(tools ...*Tool) *Set {
	s := &Set{byName: map[string]*Tool{}}
	for _, t := range tools {
		s.Add(t)
	}
	return s
}

// Add inserts or replaces a tool.
func (s *Set) Add(t *Tool) {
	if _, ok := s.byName[t.Name]; !ok {
		s.order = append(s.order, t.Name)
	}
	s.byName[t.Name] = t
}

// Get looks a tool up by name.
func (s *Set) Get(name string) (*Tool, bool) {
	t, ok := s.byName[name]
	return t, ok
}

// Names returns tool names in insertion order.
func (s *Set) Names() []string { return append([]string(nil), s.order...) }

// Select returns the tools whose names are in names, in the set's order.
// Unknown names are ignored.
func (s *Set) Select(names []string) []*Tool {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var out []*Tool
	for _, n := range s.order {
		if want[n] {
			out = append(out, s.byName[n])
		}
	}
	return out
}
