// Package mods runs Claude Code mods: plugins whose hooks/hooks.json lists
// hooks modules ({"modules": ["./register.tsx"]}), TypeScript or JavaScript
// written against Claude Code's mod API (code.claude.com/docs/en/plugins/mods).
//
// kiln bundles each module with esbuild and runs it in goja, a JavaScript
// engine in Go, on one goroutine shared by every loaded mod, as Claude Code
// runs installed mods on one worker thread. Version 1 implements the part of
// the API Andre's mods (squad, savvy-progress) use; see docs/configuration.md
// "Mods" for the list.
//
// This file is the contract between the engine (engine.go and friends) and
// kiln: the element tree the TUI draws, and the host interface the engine
// calls for what only kiln can do (panes, toasts, live agents).
package mods

import "encoding/json"

// Element is one node of a tree a ui.render hook returned, decoded from
// the JavaScript value.
//
// Type is the element name ("Box", "Text", "Button", "Raster", "Image") or
// "" for a bare text node, whose content is Text. Props are the element's
// props with JSON-shaped values (string, float64, bool, []any,
// map[string]any); a function prop (Button's onPress) is replaced by a
// Callback. Children keep their order.
type Element struct {
	Type     string
	Key      string
	Props    map[string]any
	Children []Element
	Text     string
}

// Callback names a function a mod passed as a prop (Button onPress). The
// TUI hands it back to Engine.Press; it is only valid for the tree it came
// from, until the next render of the same site.
type Callback struct {
	ID int
}

// Site is a place a mod draws: a pane it opened ("Pane", with the pane's
// RequestID) or the shared band above the prompt ("AbovePrompt").
type Site struct {
	Component string
	RequestID string
	// BodyColumns and BodyRows are the space the site has, the
	// e.props.bodyColumns and e.props.scroll.bodyRows a hook reads.
	BodyColumns int
	BodyRows    int
	// Surface is always "terminal" in kiln.
	Surface string
	// ViewAgentID is the agent whose transcript is in view; kiln has no
	// per-agent view, so it is always "".
	ViewAgentID string
}

// Drawing is what one mod drew for one site: nil Tree means the mod did not
// draw there (it called next, or has no hook for the site).
type Drawing struct {
	Plugin string
	Tree   *Element
}

// PaneRequest is $.ui.open's argument.
type PaneRequest struct {
	ID            string
	Title         string
	Focus         bool
	CloseOnEscape bool
	Rows          int
	Columns       int
}

// PaneResult is what $.ui.open resolves to.
type PaneResult struct {
	IsPlaced bool
	Reason   string
}

// AgentInfo is one entry of $.agent.list().
type AgentInfo struct {
	ID          string
	Type        string
	Description string
	Status      string // pending, running, waiting, idle, completed, failed, killed
}

// Host is what the engine needs from kiln. Every method is called from the
// engine's goroutine and must not block on the engine (the TUI posts a
// message and returns).
type Host interface {
	// OpenPane, ClosePane and Panes back $.ui.open/close/panes; pane ids are
	// global across mods, as in Claude Code.
	OpenPane(plugin string, req PaneRequest) PaneResult
	ClosePane(id string) bool
	Panes() []string
	// Toast shows a short note ($.ui.toast).
	Toast(plugin, text string)
	// Invalidate asks for a redraw of every mod site ($.ui.invalidate, or a
	// $.state change a drawing read); the TUI coalesces calls.
	Invalidate()
	// Agents is $.agent.list(): the session's subagents.
	Agents() []AgentInfo
	// SessionUsage is $.session.usage(): the context window and what the
	// main conversation holds now, in tokens.
	SessionUsage() Usage
	// Settings is $.settings.read(): the merged settings JSON.
	Settings() json.RawMessage
}

// Usage is token usage as the mod API reports it.
type Usage struct {
	Input         int `json:"input"`
	Output        int `json:"output"`
	CacheRead     int `json:"cacheRead"`
	CacheWrite    int `json:"cacheWrite"`
	ContextWindow int `json:"contextWindow"`
	ContextTokens int `json:"contextTokens"`
}

// CommandSpec is a command a mod registered ($.command.register).
type CommandSpec struct {
	Plugin       string
	Name         string
	Description  string
	ArgumentHint string
}

// ToolSpec is a tool a mod registered ($.tool.register): Claude sees it as
// FullName, mcp__<plugin>__<name>.
type ToolSpec struct {
	Plugin      string
	FullName    string
	Description string
	InputSchema json.RawMessage
	IsDeferred  bool
}

// ToolCall is a tool.call event: a tool about to run, in the main
// conversation (AgentID "") or inside a subagent.
type ToolCall struct {
	ID      string
	Tool    string
	Args    json.RawMessage
	AgentID string
}

// ToolOutcome is what the tool.call chain decided. Exactly one of these
// holds: Deny is set (refused; the reason is the model's result), Result is
// set (answered without running the tool), or neither (run the tool, with
// Args, which a hook may have rewritten).
type ToolOutcome struct {
	Args    json.RawMessage
	Deny    string
	Result  json.RawMessage
	Handled bool
}

// Spawn is an agent.spawn event: a subagent about to start.
type Spawn struct {
	AgentID     string
	Type        string
	Description string
	Prompt      string
	Model       string
}

// SpawnOutcome is what the agent.spawn chain decided: Deny refuses the
// spawn; otherwise Spawn is the event as the hooks left it (a rewritten
// Prompt or Model).
type SpawnOutcome struct {
	Spawn Spawn
	Deny  string
}

// TurnStep is a turn.step event after the model answered: one request of a
// turn, in the main conversation (AgentID "") or a subagent.
type TurnStep struct {
	AgentID string
	TurnID  string
	Model   string
	Effort  string
	Usage   Usage
}

// TurnEnd is a turn.complete event.
type TurnEnd struct {
	AgentID    string
	TurnID     string
	IsAborted  bool
	IsFailed   bool
	DurationMs int64
	Usage      Usage
}
