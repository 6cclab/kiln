package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/claude/agents"
	claudeplugins "github.com/andrepato/harness/internal/claude/plugins"
	slashcommands "github.com/andrepato/harness/internal/commands"
	"github.com/andrepato/harness/internal/crash"
	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/mods"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/tool"
	"github.com/andrepato/harness/internal/tui"
)

// claudeToolNames maps kiln's tool names to Claude Code's, which mods
// compare against (squad sorts a subagent's activity by "Read", "Bash",
// ...; savvy-progress watches "Agent"). Tools not listed (MCP tools, a
// mod's own) keep their names.
var claudeToolNames = map[string]string{
	"bash":              "Bash",
	"bash_background":   "Bash",
	"bash_output":       "BashOutput",
	"kill_shell":        "KillShell",
	"read":              "Read",
	"edit":              "Edit",
	"write":             "Write",
	"task":              "Agent",
	"web_fetch":         "WebFetch",
	"web_search":        "WebSearch",
	"todo_write":        "TodoWrite",
	"task_state_write":  "TaskStateWrite",
	"ask_user_question": "AskUserQuestion",
	"exit_plan_mode":    "ExitPlanMode",
	"skill":             "Skill",
	"tool_search":       "ToolSearch",
}

func claudeToolName(name string) string {
	if cc, ok := claudeToolNames[name]; ok {
		return cc
	}
	return name
}

// gitRemoteURL is the origin's push URL for the repo at root, "" when there is
// no origin (or git is unavailable). Claude Code's session.repo prefers the push
// URL; kiln reads it the same way, falling back to the fetch URL.
func gitRemoteURL(ctx context.Context, root string) string {
	run := func(args ...string) string {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = root
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	if u := run("remote", "get-url", "--push", "origin"); u != "" {
		return u
	}
	return run("remote", "get-url", "origin")
}

// modsRuntime connects the mod engine to a session: the main conversation
// and every subagent (tool.call, turn.*, agent.spawn), kiln's commands and
// tools, and the TUI's panes. Every method is a no-op until start has run,
// so the session can wire it before knowing whether any mod will load (an
// untrusted folder starts mods only once trusted).
type modsRuntime struct {
	modules []mods.Module
	model   string
	window  int

	engine  atomic.Pointer[mods.Engine]
	host    *modsHost
	driver  atomic.Pointer[tui.ModsDriver]
	toolSet atomic.Pointer[[]mods.ToolSpec]
	// registry is the slash-command registry, set once it is built.
	registry atomic.Pointer[slashcommands.Registry]
	// agentDefs lists the subagent types the dispatcher knows (agent.offer
	// states each one's description and source).
	agentDefs func() []agents.Definition

	mu      sync.Mutex
	pending map[string]mods.ToolHandle // agentID + "\x00" + tool call id
	agents  map[string]*mods.AgentInfo
	order   []string
	turnSeq atomic.Int64

	// mainTurn is the id of the main loop's running turn ("" between turns);
	// $.turn.abort checks it. lane aborts it. stops end the per-conversation
	// watchers attach started.
	mainTurn  atomic.Value // string
	lane      atomic.Pointer[harness.Lane]
	stops     []func()
	sessionID string
	// endReason is session.end's reason: "prompt_input_exit" leaving the
	// interactive UI, "other" when a -p run finishes.
	endReason string

	// print is set for a -p run, which draws no prompt box.
	print bool
	// modelFn is the live model id, for the system prompt's facts.
	modelFn func() string
	// toolsFn lists the tools the model has (Services.Tools).
	toolsFn func() []mods.ToolInfo
	// lastSections is the system prompt's sections before the hooks shaped
	// them, for $.prompt.compose (read without taking the sizing lock).
	lastSections atomic.Pointer[[]mods.PromptSection]
}

// newModsRuntime collects the hooks modules of the active plugins; nil
// when none has any.
func newModsRuntime(plugins []claudeplugins.Plugin, model string, window int) *modsRuntime {
	configs := pluginConfigOptions()
	var modules []mods.Module
	for _, p := range plugins {
		for _, path := range claudeplugins.Modules(p) {
			opts, err := mods.OptionsFor(p.Root, configs[p.Key])
			if err != nil {
				diag.L().Warn("mods: plugin options unreadable", "plugin", p.Key, "error", err)
				continue
			}
			modules = append(modules, mods.Module{Plugin: p.Name, Root: p.Root, Path: path, Options: opts})
		}
	}
	if len(modules) == 0 {
		return nil
	}
	r := &modsRuntime{
		modules: modules,
		model:   model,
		window:  window,
		pending: map[string]mods.ToolHandle{},
		agents:  map[string]*mods.AgentInfo{},

		endReason: "prompt_input_exit",
	}
	r.host = &modsHost{r: r}
	return r
}

// pluginConfigOptions reads pluginConfigs.<id>.options from the user's
// settings, the values `/plugin configure` saves (non-sensitive ones).
func userClaudeSettingsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "settings.json")
}

func pluginConfigOptions() map[string]map[string]any {
	data, err := os.ReadFile(userClaudeSettingsPath())
	if err != nil {
		return nil
	}
	var s struct {
		PluginConfigs map[string]struct {
			Options map[string]any `json:"options"`
		} `json:"pluginConfigs"`
	}
	if json.Unmarshal(data, &s) != nil {
		return nil
	}
	out := map[string]map[string]any{}
	for k, v := range s.PluginConfigs {
		out[k] = v.Options
	}
	return out
}

// egressDeps are the per-session capabilities the mod engine reaches out
// through: the repo root, the live model id, an MCP tool call, a one-shot
// model classification, the build version, the working copy's repository, the
// user's turn count, and one-shot model completion and fork.
type egressDeps struct {
	root     string
	model    func() string
	mcpCall  func(ctx context.Context, server, tool string, args map[string]any) (json.RawMessage, error)
	classify func(ctx context.Context, text string, labels []string) (string, error)
	version  string
	repo     func(ctx context.Context) (map[string]any, error)
	turns    func(ctx context.Context) (int, error)
	complete func(ctx context.Context, req mods.ModelRequest) (map[string]any, error)
	fork     func(ctx context.Context, prompt string) (map[string]any, error)
	// services are the conversation-reaching $ methods (compaction, append,
	// messages); start adds the ones that need the runtime itself.
	services mods.Services
}

// start loads every module and runs session.start; then the mods' tools
// and commands exist. Errors are logged and returned for a startup note.
func (r *modsRuntime) start(ctx context.Context, cwd, sessionID string, dep egressDeps) []error {
	if r == nil || r.engine.Load() != nil {
		return nil
	}
	home, _ := os.UserHomeDir()
	cache := filepath.Join(home, ".harness", "cache", "mods")
	e := mods.New(r.host, mods.Options{
		CacheDir: cache, Cwd: cwd, SessionID: sessionID, Logger: diag.L(),
		SessionRoot: dep.root, Model: dep.model,
		McpCall: dep.mcpCall, Classify: dep.classify,
		Version: dep.version, Repo: dep.repo, Turns: dep.turns,
		Complete: dep.complete, Fork: dep.fork,
		Services: r.services(dep.services),
	})
	r.sessionID = sessionID
	r.modelFn = dep.model
	errs := e.Load(r.modules)
	for _, err := range errs {
		diag.L().Warn("mods: module failed to load", "error", err)
	}
	r.engine.Store(e)
	e.SessionStart(ctx)
	specs := e.Tools()
	r.toolSet.Store(&specs)
	return errs
}

func (r *modsRuntime) get() *mods.Engine {
	if r == nil {
		return nil
	}
	return r.engine.Load()
}

func (r *modsRuntime) close() {
	if e := r.get(); e != nil {
		e.SessionEnd(r.endReason, r.sessionID, "")
		for _, stop := range r.stops {
			stop()
		}
		e.Close()
	}
	if r != nil {
		if d := r.driver.Load(); d != nil {
			d.Close()
		}
	}
}

func (r *modsRuntime) isModTool(name string) bool {
	if r == nil {
		return false
	}
	specs := r.toolSet.Load()
	if specs == nil {
		return false
	}
	for _, s := range *specs {
		if s.FullName == name {
			return true
		}
	}
	return false
}

// toolNames are the full names of the mods' tools, for the gate.
func (r *modsRuntime) toolNames() []string {
	var out []string
	if r == nil {
		return out
	}
	if specs := r.toolSet.Load(); specs != nil {
		for _, s := range *specs {
			out = append(out, s.FullName)
		}
	}
	return out
}

// tools are the mods' tools as kiln tools, for the conversation agentID
// runs in ("" for the main one). Claude Code answers a mod tool through
// the tool.call chain alone; kiln runs nothing for it.
func (r *modsRuntime) tools(agentID string) []*tool.Tool {
	if r == nil {
		return nil
	}
	specs := r.toolSet.Load()
	if specs == nil {
		return nil
	}
	out := make([]*tool.Tool, 0, len(*specs))
	for _, spec := range *specs {
		spec := spec
		params := spec.InputSchema
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, &tool.Tool{
			Name:        spec.FullName,
			Label:       spec.FullName,
			Description: spec.Description,
			Parameters:  params,
			Concurrent:  true,
			Execute: func(ctx context.Context, raw json.RawMessage, _ tool.Update, inv tool.Invocation) (tool.Result, error) {
				e := r.get()
				if e == nil {
					return tool.Errorf("%s is not available: its mod is not loaded.", spec.FullName), nil
				}
				outcome, handle := e.BeginTool(ctx, mods.ToolCall{ID: inv.ToolCallID, Tool: spec.FullName, Args: raw, AgentID: agentID})
				handle.Finish(nil, true)
				switch {
				case outcome.Deny != "":
					return tool.Errorf("%s", outcome.Deny), nil
				case outcome.Result != nil:
					return tool.Text(resultText(outcome.Result)), nil
				}
				return tool.Errorf("%s did not answer.", spec.FullName), nil
			},
		})
	}
	return out
}

// resultText is the text a mod's {result} carries: a string as is, an
// object's "text", or the JSON itself.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Text != nil {
		return *obj.Text
	}
	return string(raw)
}

// commands are the mods' slash commands.
func (r *modsRuntime) commands() []slashcommands.Command {
	e := r.get()
	if e == nil {
		return nil
	}
	var out []slashcommands.Command
	for _, spec := range e.Commands() {
		name := spec.Name
		out = append(out, slashcommands.Command{
			Name:         name,
			Description:  spec.Description,
			ArgumentHint: spec.ArgumentHint,
			Origin:       slashcommands.OriginPlugin,
			Run: func(ctx context.Context, args string) (slashcommands.Result, error) {
				text, ok, err := e.RunCommand(ctx, name, args)
				if err != nil {
					return slashcommands.Result{}, err
				}
				if !ok || text == "" {
					return slashcommands.Result{}, nil
				}
				return slashcommands.Result{Output: strings.Split(strings.TrimRight(text, "\n"), "\n")}, nil
			},
		})
	}
	return out
}

// attach registers the session's hooks on one conversation's harness:
// the main one (agentID "") or a subagent's.
func (r *modsRuntime) attach(agentID, model string, h *harness.Harness) {
	if r == nil {
		return
	}
	hooks := h.Hooks()
	var (
		mu      sync.Mutex
		turnID  string
		started time.Time
		index   int
		step    mods.StepHandle
		answer  string
		usage   mods.Usage
	)
	finishStep := func(ts mods.TurnStep) {
		mu.Lock()
		s := step
		step = nil
		mu.Unlock()
		if s != nil {
			s.Finish(ts)
		}
	}
	hooks.OnBeforeRun(func(ctx context.Context) error {
		e := r.get()
		if e == nil {
			return nil
		}
		mu.Lock()
		turnID = fmt.Sprintf("t%d", r.turnSeq.Add(1))
		started, index, answer, usage = time.Now(), 0, "", mods.Usage{}
		id := turnID
		mu.Unlock()
		if agentID == "" {
			r.mainTurn.Store(id)
		}
		e.TurnStart(agentID, id)
		return nil
	})
	hooks.OnBeforeRequest(func(ctx context.Context) error {
		e := r.get()
		if e == nil {
			return nil
		}
		finishStep(mods.TurnStep{AgentID: agentID})
		mu.Lock()
		id, i := turnID, index
		index++
		mu.Unlock()
		s := e.BeginStep(ctx, mods.StepStart{AgentID: agentID, TurnID: id, Index: i, Model: model})
		mu.Lock()
		step = s
		mu.Unlock()
		return nil
	})
	hooks.OnAfterResponse(func(ctx context.Context, m *msg.AssistantMessage) error {
		if r.get() == nil || m == nil {
			return nil
		}
		u := modsUsage(m.Usage, r.window)
		ts := mods.TurnStep{AgentID: agentID, Model: m.Model, Usage: u, Answer: msg.TextOf(m.Content), StopReason: string(m.StopReason)}
		for _, c := range msg.ToolCallsOf(m.Content) {
			args, _ := json.Marshal(c.Arguments)
			ts.ToolUses = append(ts.ToolUses, mods.ToolUse{Name: claudeToolName(c.Name), Input: args})
		}
		mu.Lock()
		ts.TurnID = turnID
		if ts.Answer != "" {
			answer = ts.Answer
		}
		usage = addUsage(usage, u)
		mu.Unlock()
		finishStep(ts)
		if agentID == "" {
			// The response reported the context's fill: the measurement
			// session.measure carries (the figure /context and the footer read).
			r.get().SessionMeasure(ctx, u)
		}
		return nil
	})
	hooks.OnBeforeRunEnd(func(ctx context.Context, status string) error {
		e := r.get()
		if e == nil {
			return nil
		}
		finishStep(mods.TurnStep{AgentID: agentID})
		mu.Lock()
		end := mods.TurnEnd{AgentID: agentID, TurnID: turnID, DurationMs: time.Since(started).Milliseconds(),
			Usage: usage, Answer: answer, Model: model,
			IsAborted: status == harness.StatusAborted, IsFailed: status == harness.StatusFailed}
		mu.Unlock()
		if agentID == "" {
			r.mainTurn.Store("")
		}
		e.TurnComplete(end)
		return nil
	})
	// Chunks of the response stream to the hooks of the step it belongs to.
	h.Events().On(harness.EventMessageUpdate, func(ev harness.Event) {
		if ev.StreamEvent == nil {
			return
		}
		chunk := stepChunk(*ev.StreamEvent)
		if chunk == nil {
			return
		}
		mu.Lock()
		s := step
		mu.Unlock()
		if s != nil {
			s.Chunk(chunk)
		}
	})
	// A compaction asks the session.compact hooks first.
	hooks.OnCompactDecision(func(ctx context.Context, info harness.CompactInfo) (harness.CompactVerdict, error) {
		e := r.get()
		if e == nil || mods.IsRaised(ctx, "session.compact") {
			return harness.CompactVerdict{}, nil
		}
		d := e.SessionCompact(ctx, agentID, info.Trigger, info.Instructions, modsMessages(info.Messages, false))
		v := harness.CompactVerdict{Skip: d.Skip}
		if d.Rewritten {
			ins := d.Instructions
			v.Instructions = &ins
		}
		return v, nil
	})
	r.watchRows(agentID, h)
	hooks.OnBeforeTool(func(ctx context.Context, call msg.ToolCall) (harness.BeforeToolResult, error) {
		e := r.get()
		if e == nil || r.isModTool(call.Name) {
			return harness.BeforeToolResult{}, nil
		}
		args, _ := json.Marshal(call.Arguments)
		outcome, handle := e.BeginTool(ctx, mods.ToolCall{ID: call.ID, Tool: claudeToolName(call.Name), Args: args, AgentID: agentID})
		if outcome.Deny != "" {
			return harness.BeforeToolResult{Block: &harness.ToolBlock{Reason: outcome.Deny}}, nil
		}
		if outcome.Result != nil {
			// Answering a kiln tool in its place is not wired in v1: the
			// tool runs as usual.
			diag.L().Info("mods: a tool.call answer for a built-in tool is not supported; the tool runs", "tool", call.Name)
		}
		// tool.check: the hooks' verdict on the call as it will run. Only a
		// deny is applied; the permission gate, which runs next, decides
		// the rest.
		checked := args
		if outcome.Args != nil {
			checked = outcome.Args
		}
		if dec, why, ok := e.ToolCheck(ctx, claudeToolName(call.Name), checked, call.ID, agentID); ok && dec == "deny" {
			if why == "" {
				why = "a tool.check hook denied this call"
			}
			handle.Finish(nil, true)
			return harness.BeforeToolResult{Block: &harness.ToolBlock{Reason: why}}, nil
		}
		r.mu.Lock()
		r.pending[agentID+"\x00"+call.ID] = handle
		r.mu.Unlock()
		var res harness.BeforeToolResult
		if outcome.Args != nil && string(outcome.Args) != string(args) {
			res.RewrittenArgs = outcome.Args
		}
		return res, nil
	})
	hooks.OnAfterTool(func(ctx context.Context, call msg.ToolCall, result *msg.ToolResultMessage) error {
		r.mu.Lock()
		handle := r.pending[agentID+"\x00"+call.ID]
		delete(r.pending, agentID+"\x00"+call.ID)
		r.mu.Unlock()
		if handle == nil {
			return nil
		}
		var text string
		isError := false
		if result != nil {
			text = msg.TextOf(result.Content)
			isError = result.IsError
		}
		out, _ := json.Marshal(map[string]any{"text": text})
		handle.Finish(out, isError)
		return nil
	})
}

func modsUsage(u msg.Usage, window int) mods.Usage {
	return mods.Usage{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite,
		ContextWindow: window, ContextTokens: u.Input + u.CacheRead + u.CacheWrite}
}

func addUsage(a, b mods.Usage) mods.Usage {
	a.Input += b.Input
	a.Output += b.Output
	a.CacheRead += b.CacheRead
	a.CacheWrite += b.CacheWrite
	a.ContextWindow = b.ContextWindow
	a.ContextTokens = b.ContextTokens
	return a
}

// --- agent.SubagentHooks ------------------------------------------------------

func (r *modsRuntime) BeforeSpawn(ctx context.Context, agentID, agentType string, req *agent.DispatchRequest) string {
	e := r.get()
	if e == nil {
		return ""
	}
	// A $.agent.spawn call raised agent.spawn itself (the hooks' rewrites are
	// in req already) and is no offer to the model (agent.offer governs only
	// the model's own dispatch).
	if !mods.IsRaised(ctx, "agent.spawn") {
		desc, source := "", "built-in"
		if r.agentDefs != nil {
			for _, def := range r.agentDefs() {
				if def.Name == agentType {
					desc, source = def.Description, string(def.Source)
					break
				}
			}
		}
		if !e.AgentOffer(ctx, agentType, desc, source) {
			return "agent " + agentType + " is not offered: a plugin's agent.offer hook withdrew it"
		}
		out := e.Spawn(ctx, mods.Spawn{AgentID: agentID, ToolUseID: agentID, Type: agentType,
			Description: req.Description, Prompt: req.Prompt, Model: req.Model, ParentModel: r.model})
		if out.Deny != "" {
			return out.Deny
		}
		req.Prompt = out.Spawn.Prompt
		if out.Spawn.Model != "" {
			req.Model = out.Spawn.Model
		}
	}
	r.mu.Lock()
	if _, ok := r.agents[agentID]; !ok {
		r.order = append(r.order, agentID)
	}
	r.agents[agentID] = &mods.AgentInfo{ID: agentID, Type: agentType, Description: req.Description, Status: "running"}
	r.mu.Unlock()
	return ""
}

func (r *modsRuntime) Attach(agentID, model string, h *harness.Harness) {
	r.attach(agentID, model, h)
}

func (r *modsRuntime) Tools(agentID string) []*tool.Tool { return r.tools(agentID) }

func (r *modsRuntime) Done(agentID, status string, _ msg.Usage) {
	r.mu.Lock()
	if a, ok := r.agents[agentID]; ok {
		switch status {
		case harness.StatusCompleted:
			a.Status = "completed"
		case harness.StatusAborted:
			a.Status = "killed"
		default:
			a.Status = "failed"
		}
	}
	r.mu.Unlock()
}

// --- mods.Host ----------------------------------------------------------------

type modsHost struct {
	r     *modsRuntime
	usage func() mods.Usage
}

func (h *modsHost) ui() *tui.ModsDriver { return h.r.driver.Load() }

func (h *modsHost) OpenPane(plugin string, req mods.PaneRequest) mods.PaneResult {
	if d := h.ui(); d != nil {
		return d.OpenPane(plugin, req)
	}
	return mods.PaneResult{IsPlaced: false, Reason: "kiln is not showing its interface (print mode)"}
}

func (h *modsHost) ClosePane(id string) bool {
	if d := h.ui(); d != nil {
		return d.ClosePane(id)
	}
	return false
}

func (h *modsHost) Panes() []string {
	if d := h.ui(); d != nil {
		return d.Panes()
	}
	return nil
}

func (h *modsHost) Toast(plugin, text string) {
	if d := h.ui(); d != nil {
		d.Toast(plugin, text)
		return
	}
	diag.L().Info("mods: toast", "plugin", plugin, "text", text)
}

func (h *modsHost) Invalidate() {
	if d := h.ui(); d != nil {
		d.Invalidate()
	}
}

func (h *modsHost) Agents() []mods.AgentInfo {
	h.r.mu.Lock()
	defer h.r.mu.Unlock()
	out := make([]mods.AgentInfo, 0, len(h.r.order))
	for _, id := range h.r.order {
		out = append(out, *h.r.agents[id])
	}
	return out
}

func (h *modsHost) SessionUsage() mods.Usage {
	if h.usage != nil {
		return h.usage()
	}
	return mods.Usage{ContextWindow: h.r.window}
}

func (h *modsHost) Settings() json.RawMessage {
	data, err := os.ReadFile(userClaudeSettingsPath())
	if err != nil || !json.Valid(data) {
		return json.RawMessage(`{}`)
	}
	return data
}

// --- tui.ModsEngine --------------------------------------------------------------

// Render draws a site through the mods' hooks; nothing before they start.
func (r *modsRuntime) Render(ctx context.Context, site mods.Site) []mods.Drawing {
	if e := r.get(); e != nil {
		return e.Render(ctx, site)
	}
	return nil
}

// Press runs a mod button's callback.
func (r *modsRuntime) Press(cb mods.Callback) {
	if e := r.get(); e != nil {
		e.Press(cb)
	}
}

// newDriver binds the TUI's mod panes to the runtime; nil without mods.
func (r *modsRuntime) newDriver() *tui.ModsDriver {
	if r == nil {
		return nil
	}
	d := tui.NewModsDriver(r)
	r.driver.Store(d)
	return d
}

// stepChunk maps one provider stream event to claude-code.d.ts's
// TurnStepChunk, nil for an event that is no chunk (start/end markers).
func stepChunk(ev msg.StreamEvent) mods.StepChunk {
	switch ev.Type {
	case msg.EventTextDelta:
		return mods.StepChunk{"kind": "text", "index": ev.ContentIndex, "text": ev.Delta}
	case msg.EventThinkingDelta:
		return mods.StepChunk{"kind": "thinking", "index": ev.ContentIndex, "text": ev.Delta}
	case msg.EventToolCallStart:
		c := mods.StepChunk{"kind": "tool", "index": ev.ContentIndex, "id": "", "name": ""}
		if ev.Partial != nil && ev.ContentIndex < len(ev.Partial.Content) {
			if tc, ok := ev.Partial.Content[ev.ContentIndex].(msg.ToolCall); ok {
				c["id"], c["name"] = tc.ID, claudeToolName(tc.Name)
			}
		}
		return c
	case msg.EventToolCallDelta:
		return mods.StepChunk{"kind": "input", "index": ev.ContentIndex, "json": ev.Delta}
	case msg.EventDone, msg.EventError:
		return mods.StepChunk{"kind": "stop", "stopReason": string(ev.Reason), "usage": nil}
	}
	return nil
}

// watchRows raises session.append for each row the conversation commits, until
// the runtime closes.
func (r *modsRuntime) watchRows(agentID string, h *harness.Harness) {
	lane, err := h.Lane("main")
	if err != nil {
		return
	}
	_, updates, unsub := lane.Watch()
	r.mu.Lock()
	r.stops = append(r.stops, unsub)
	r.mu.Unlock()
	crash.Go(func() {
		for en := range updates {
			e := r.get()
			if e == nil {
				continue
			}
			if row, ok := appendRow(agentID, en); ok {
				e.SessionAppend(row)
			}
		}
	})
}

// appendRow describes a committed entry as a session.append row.
func appendRow(agentID string, en session.Entry) (mods.AppendRow, bool) {
	row := mods.AppendRow{UUID: en.ID, AgentID: agentID}
	switch en.Type {
	case session.EntryCompaction:
		row.Type, row.Door = "system", "compaction"
		row.Content = []any{map[string]any{"type": "text", "text": en.Summary}}
		return row, true
	case session.EntryMessage:
	default:
		return row, false
	}
	switch m := derefMessage(en.Message).(type) {
	case msg.UserMessage:
		row.Type, row.Role = "user", "user"
		row.Content = apiBlocks(m.Content)
		switch {
		case m.KilnHook != "":
			row.Door, row.Origin = "hook-context", map[string]any{"kind": "engine"}
		case m.KilnTyped != "":
			row.Door, row.Origin = "prompt", map[string]any{"kind": "user"}
		default:
			row.Door = "delivery"
		}
	case msg.AssistantMessage:
		row.Type, row.Role, row.Door = "assistant", "assistant", "response"
		row.Content = apiBlocks(m.Content)
		row.Origin = map[string]any{"kind": "model", "model": m.Model}
	case msg.ToolResultMessage:
		row.Type, row.Role, row.Door = "user", "user", "tool-result"
		block := map[string]any{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": apiBlocks(m.Content)}
		if m.IsError {
			block["is_error"] = true
		}
		row.Content = []any{block}
	default:
		return row, false
	}
	return row, true
}

// services are the conversation-reaching $ methods: what the caller wired
// (messages, compaction) plus the ones the runtime answers itself (the
// running turn, queuing a row).
func (r *modsRuntime) services(base mods.Services) mods.Services {
	svc := base
	svc.AbortTurn = func(_ context.Context, turnID string) error {
		running, _ := r.mainTurn.Load().(string)
		if running == "" || running != turnID {
			return fmt.Errorf("turn %q is not the running turn (%q)", turnID, running)
		}
		lane := r.lane.Load()
		if lane == nil {
			return fmt.Errorf("no running turn to abort")
		}
		return lane.Abort()
	}
	r.toolsFn = base.Tools
	// kiln draws on its one local terminal while the TUI runs; a -p run
	// draws nowhere. There are no remote surfaces to attach.
	svc.Surfaces = func() []string {
		if r.driver.Load() != nil {
			return []string{"terminal"}
		}
		return nil
	}
	r.promptServices(&svc)
	return svc
}
