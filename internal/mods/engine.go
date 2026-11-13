package mods

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"

	"github.com/andrepato/harness/internal/diag"
)

// Budgets, from Claude Code's HookBudget (claude-code.d.ts): a hook's own
// time per dispatch, and a .catch handler's grace.
const (
	DefaultHookBudget  = 10 * time.Second
	DefaultCatchBudget = 1 * time.Second
)

// ErrClosed is returned by calls on an engine after Close.
var ErrClosed = errors.New("mods: engine closed")

// supportedEvents are the events kiln raises in v1. A hook on any other
// event registers (so the module loads) but never runs; Load logs it.
var supportedEvents = map[string]bool{
	"session.start":       true,
	"command.run":         true,
	"tool.call":           true,
	"agent.spawn":         true,
	"turn.start":          true,
	"turn.step":           true,
	"turn.complete":       true,
	"ui.render":           true,
	"ui.resolve":          true,
	"session.end":         true,
	"session.compact":     true,
	"session.append":      true,
	"turn.abort":          true,
	"tool.check":          true,
	"tool.describe":       true,
	"command.describe":    true,
	"agent.offer":         true,
	"prompt.submit":       true,
	"prompt.fill":         true,
	"prompt.suggest":      true,
	"prompt.edit":         true,
	"prompt.change":       true,
	"prompt.autocomplete": true,
	"prompt.section":      true,
	"prompt.compose":      true,
	"prompt.attachment":   true,
	"prompt.mention":      true,
	"session.send":        true,
	"session.receive":     true,
	"session.measure":     true,
	"config.set":          true,
	"config.describe":     true,
	"telemetry.log":       true,
	"telemetry.mark":      true,
	"skill.prompt":        true,
	"ui.press":            true,
	"ui.scroll":           true,
	"ui.focus":            true,
	"ui.status":           true,
	"ui.log":              true,
	"ui.notice":           true,
	"ui.notify":           true,
	"ui.copy":             true,
	"ui.selection":        true,
	"ui.blit":             true,
	// session.attach and session.detach are left out on purpose: kiln has
	// one local terminal and no remote client to attach, so a hook on them
	// logs "kiln does not raise" and never runs.
}

// pinnedFields are the fields of each event a hook may not change on the
// way down: one left out of next's argument is kept, a different value
// rejects the next call (claude-code.d.ts: "Pinned").
var pinnedFields = map[string][]string{
	"tool.call":           {"tool", "tool_use_id", "agentId"},
	"agent.spawn":         {"tool_use_id", "fork", "parentModel", "parentAgentId", "permissionMode", "name"},
	"turn.start":          {"turnId"},
	"turn.step":           {"turnId", "index", "messageCount", "agentId"},
	"turn.complete":       {"turnId", "agentId"},
	"command.run":         {"command"},
	"tool.check":          {"tool", "input", "tool_use_id", "agentId", "ceiling"},
	"tool.describe":       {"tool", "provider"},
	"command.describe":    {"command", "immediate", "provider"},
	"agent.offer":         {"agent", "description", "source", "provider"},
	"ui.render":           {"surface", "component", "requestId"},
	"ui.resolve":          {"surface", "component"},
	"prompt.submit":       {"origin", "command", "isMeta", "attachments"},
	"prompt.fill":         {"origin"},
	"prompt.suggest":      {"origin"},
	"prompt.edit":         {"origin", "key"},
	"prompt.change":       {"text", "cursor", "origin"},
	"prompt.autocomplete": {"text", "cursor", "token", "start"},
	"prompt.section":      {"name"},
	"prompt.compose":      {"model"},
	"prompt.attachment":   {"type", "origin", "agentId"},
	"prompt.mention":      {"mention", "agentId"},
	"session.send":        {"origin", "agentId"},
	"session.receive":     {"agentId"},
	"config.set":          {"key", "previous", "provider", "origin"},
	"config.describe":     {"key", "provider"},
	"telemetry.log":       {"to"},
	"ui.press":            {"plugin", "element", "component", "requestId", "surface"},
	"ui.scroll":           {"component", "requestId", "by", "bodyRows", "contentRows", "origin"},
	"ui.focus":            {"component", "requestId", "plugin", "origin"},
	"skill.prompt":        {"skill", "frontmatterProblem", "source", "pluginName", "path"},
}

// gatingEvents must resolve to an object; anything else is a hook that
// "returned what the site refuses" and fails.
var gatingEvents = map[string]bool{
	"session.start": true, "command.run": true, "tool.call": true, "agent.spawn": true,
	"turn.start": true, "turn.step": true, "turn.complete": true,
	"tool.check": true, "tool.describe": true, "command.describe": true, "agent.offer": true,
	"prompt.submit": true, "prompt.fill": true, "prompt.suggest": true, "prompt.edit": true,
	"prompt.change": true, "prompt.autocomplete": true, "prompt.section": true,
	"prompt.compose": true, "prompt.attachment": true, "prompt.mention": true,
	"session.send": true, "session.receive": true,
	"config.set": true, "config.describe": true, "skill.prompt": true,
}

var nameRule = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Clock is the time the mod API reads ($.clock) and its timers run on, and
// separately the time hook budgets and the busy-loop watchdog run on. Tests
// inject fake ones.
type Clock interface {
	Now() time.Time
	// AfterFunc calls f (on any goroutine) after d; stop cancels it and
	// reports whether it did.
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

// Options configure an Engine. The zero value works.
type Options struct {
	// CacheDir caches bundled modules by content hash; "" bundles every load.
	CacheDir string
	// Cwd is the session's working directory (session.start's e.cwd and
	// $.session.cwd()).
	Cwd string
	// SessionID is the session's id ($.session.id()); "" when unknown.
	SessionID string
	// Clock backs $.clock; nil is the real clock.
	Clock Clock
	// BudgetClock runs hook budgets and the watchdog that interrupts a hook
	// that never yields; nil is the real clock.
	BudgetClock Clock
	// Logger receives every line the engine reports (a skipped hook, a
	// rejected $ call); nil is the run log (diag.L()).
	Logger *slog.Logger
	// LookupEnv backs $.env.get; nil is os.LookupEnv.
	LookupEnv func(string) (string, bool)
	// SessionRoot is the session's repository root ($.session.root()); "" when
	// the cwd is not inside a repo.
	SessionRoot string
	// Model returns the session's current model id ($.session.model()); nil
	// yields "". Called on the engine's worker, so it must be cheap.
	Model func() string
	// McpCall backs $.mcp.call: invoke an MCP tool (server, tool, args) and
	// return its result marshaled as JSON (Claude Code's McpToolResult). nil
	// rejects mcp.call. Called from a background goroutine, so it may block.
	McpCall func(ctx context.Context, server, tool string, args map[string]any) (json.RawMessage, error)
	// Classify backs $.model.classify: pick the label that fits text, or ""
	// when none does (the mod sees null). nil rejects model.classify. Called
	// from a background goroutine, so it may block.
	Classify func(ctx context.Context, text string, labels []string) (string, error)
	// Version is kiln's build version ($.session.version().version); "" when
	// unknown.
	Version string
	// Repo backs $.session.repo: describe the working copy's repository, or nil
	// when the cwd is not inside one (the mod sees null). nil rejects
	// session.repo. Called from a background goroutine, so it may block.
	Repo func(ctx context.Context) (map[string]any, error)
	// Turns backs $.session.turns: how many prompts the user has sent this
	// session. nil rejects session.turns. Called from a background goroutine,
	// so it may block.
	Turns func(ctx context.Context) (int, error)
	// Complete backs $.model.complete: run a one-shot model request and return
	// the ModelCompleteResult as a map. nil rejects model.complete. Called from
	// a background goroutine, so it may block.
	Complete func(ctx context.Context, req ModelRequest) (map[string]any, error)
	// Fork backs $.model.fork: continue the main thread with one more prompt on
	// the session model and return the ModelCompleteResult as a map. nil rejects
	// model.fork. Called from a background goroutine, so it may block.
	Fork func(ctx context.Context, prompt string) (map[string]any, error)
	// Services are what kiln lends the $ methods that reach beyond the engine.
	Services Services
	// HookBudget and CatchBudget override DefaultHookBudget and
	// DefaultCatchBudget (tests shorten them).
	HookBudget  time.Duration
	CatchBudget time.Duration
}

// Module is one hooks module to load: the plugin's name (its tools are
// mcp__<Plugin>__<name>), its root directory, the module's absolute path
// (plugins.Modules), and the plugin's options: the values the person set
// (pluginConfigs). Load fills in each userConfig default plugin.json
// declares that Options leaves out, as Claude Code does.
type Module struct {
	Plugin  string
	Root    string
	Path    string
	Options map[string]any
}

// ModelTextBlock is one block of a model.complete prompt or system prompt. Cache
// marks a block as a prompt-cache breakpoint (Claude Code's { text, cache: true }).
type ModelTextBlock struct {
	Text  string
	Cache bool
}

// ModelRequest is a parsed $.model.complete request, handed to Options.Complete.
// Model is the requested model (an alias, a full provider/model id, a bare model
// id, or "" for the session model). Effort is "" or one of low/medium/high/
// xhigh/max. MaxTokens is 0 when the mod left it out (the closure applies the
// default).
type ModelRequest struct {
	Model     string
	Prompt    []ModelTextBlock
	System    []ModelTextBlock
	MaxTokens int
	Effort    string
}

// Engine runs loaded mods. Every method is safe from any goroutine except
// the Host's own callbacks, which run on the engine's goroutine and must not
// call back into the engine synchronously.
type Engine struct {
	host   Host
	opts   Options
	log    *slog.Logger
	clock  Clock
	w      *worker
	state  *stateStore
	closed atomic.Bool
	init   error

	// mediaItems are the attachments of the prompt submissions in flight.
	mediaItems mediaStore

	// JavaScript helpers from the prelude; worker-only.
	js struct {
		elementSym  *goja.Symbol
		table       goja.Value
		resolve     goja.Callable
		claudeCode  goja.Value
		deepFreeze  goja.Callable
		then        goja.Callable
		isAsyncIter goja.Callable
		drain       goja.Callable
		streamOf    goja.Callable
		makeFeed    goja.Callable
		feedPush    goja.Callable
		makeDollar  goja.Callable
		assign      goja.Callable
		parse       goja.Callable
		stringify   goja.Callable
		apiCall     goja.Value
		apiNames    goja.Value
	}

	// Worker-only state.
	plugins       []*plugin
	byName        map[string]*plugin
	hooks         map[string][]*hook
	callbacks     map[int]callbackEntry
	siteCallbacks map[string][]int
	// tables are the element tables the ui.resolve chains left, by plugin and
	// component ("plugin\x00Component"); a plugin with none reads the terminal's.
	tables       map[string]goja.Value
	nextCallback int
	paneOwner    map[string]string
	paneTitle    map[string]string
	// warnedUnimpl remembers the plugin+call pairs an unimplemented $ call
	// already logged, so a mod that polls one (squad calls $.session.id and
	// $.fs.* every few seconds) warns once, not on every tick.
	warnedUnimpl map[string]bool

	// mu guards what kiln reads from other goroutines.
	mu       sync.Mutex
	commands []CommandSpec
	tools    []ToolSpec

	// measured is the context fill the last session.measure carried (guarded
	// by mu), so the next one names the units that moved.
	measured *measuredUsage
}

type plugin struct {
	name    string
	root    string
	options goja.Value
	timers  map[*timer]bool
	loaded  bool
	// rows are the /config rows of the plugin's userConfig fields.
	rows []configRow
}

type hook struct {
	plugin  *plugin
	event   string
	matcher *goja.Object
	fn      goja.Callable
	catch   goja.Callable
}

type callbackEntry struct {
	plugin *plugin
	fn     goja.Callable
	// element is the key of the element the function was a prop of; component,
	// requestID and surface name the site (ui.press's event).
	element, component, requestID, surface string
}

// New starts an engine. It loads nothing until Load.
func New(host Host, opts Options) *Engine {
	e := &Engine{
		host:          host,
		opts:          opts,
		log:           opts.Logger,
		clock:         opts.Clock,
		state:         newStateStore(),
		byName:        map[string]*plugin{},
		hooks:         map[string][]*hook{},
		callbacks:     map[int]callbackEntry{},
		siteCallbacks: map[string][]int{},
		tables:        map[string]goja.Value{},
		paneOwner:     map[string]string{},
		paneTitle:     map[string]string{},
		warnedUnimpl:  map[string]bool{},
	}
	if e.log == nil {
		e.log = diag.L()
	}
	if e.clock == nil {
		e.clock = realClock{}
	}
	if e.opts.BudgetClock == nil {
		e.opts.BudgetClock = realClock{}
	}
	if e.opts.LookupEnv == nil {
		e.opts.LookupEnv = os.LookupEnv
	}
	if e.opts.HookBudget <= 0 {
		e.opts.HookBudget = DefaultHookBudget
	}
	if e.opts.CatchBudget <= 0 {
		e.opts.CatchBudget = DefaultCatchBudget
	}
	e.w = newWorker(e.opts.HookBudget, e.opts.BudgetClock, func(r any, stack []byte) {
		e.log.Error("mods: panic in the engine's worker", "panic", fmt.Sprint(r), "stack", string(stack))
	})
	if !e.w.do(func() { e.init = e.setup() }) {
		e.init = ErrClosed
	}
	if e.init != nil {
		e.log.Error("mods: engine setup failed", "err", e.init)
	}
	return e
}

// setup runs the prelude and keeps its helpers.
func (e *Engine) setup() error {
	vm := e.w.vm
	vm.SetMaxCallStackSize(4096)
	factory, err := vm.RunScript("kiln:prelude.js", preludeSource)
	if err != nil {
		return fmt.Errorf("prelude: %w", err)
	}
	fn, ok := goja.AssertFunction(factory)
	if !ok {
		return errors.New("prelude: not a function")
	}
	natives := vm.NewObject()
	for k, v := range e.w.natives(e.consoleLog) {
		if err := natives.Set(k, v); err != nil {
			return err
		}
	}
	got, err := fn(goja.Undefined(), vm.GlobalObject(), natives)
	if err != nil {
		return fmt.Errorf("prelude: %w", err)
	}
	h := got.ToObject(vm)
	sym, ok := h.Get("ELEMENT").(*goja.Symbol)
	if !ok {
		return errors.New("prelude: no element symbol")
	}
	e.js.elementSym = sym
	e.js.table = h.Get("terminalTable")
	if f, ok := goja.AssertFunction(h.Get("resolveTable")); ok {
		e.js.resolve = f
	}
	e.js.claudeCode = h.Get("claudeCode")
	for name, dst := range map[string]*goja.Callable{
		"deepFreeze": &e.js.deepFreeze, "then": &e.js.then, "isAsyncIterator": &e.js.isAsyncIter,
		"drain": &e.js.drain, "streamOf": &e.js.streamOf, "makeFeed": &e.js.makeFeed, "feedPush": &e.js.feedPush, "makeDollar": &e.js.makeDollar, "assign": &e.js.assign,
	} {
		f, ok := goja.AssertFunction(h.Get(name))
		if !ok {
			return fmt.Errorf("prelude: no %s", name)
		}
		*dst = f
	}
	jsonObj := vm.Get("JSON").ToObject(vm)
	e.js.parse, _ = goja.AssertFunction(jsonObj.Get("parse"))
	e.js.stringify, _ = goja.AssertFunction(jsonObj.Get("stringify"))
	e.js.apiCall = vm.ToValue(e.apiCall)
	e.js.apiNames = vm.ToValue(apiNames())
	return nil
}

func (e *Engine) consoleLog(level, text string) {
	switch level {
	case "error":
		e.log.Error("mods: console: " + text)
	case "warn":
		e.log.Warn("mods: console: " + text)
	case "debug":
		e.log.Debug("mods: console: " + text)
	default:
		e.log.Info("mods: console: " + text)
	}
}

// Close stops every timer and the engine's goroutine. Later calls do
// nothing (Render returns nil, BeginTool runs the tool as if no mod loaded).
func (e *Engine) Close() {
	if e.closed.Swap(true) {
		return
	}
	e.w.do(func() {
		for _, p := range e.plugins {
			for t := range p.timers {
				t.cancel()
			}
		}
	})
	e.w.close()
}

// Load bundles and registers each module, in order. One module failing
// (bundling, evaluating, or its register throwing) does not stop the
// others; its error names the plugin. A module whose register succeeded has
// its hooks in place; the ones that failed register none.
func (e *Engine) Load(mods []Module) []error {
	var errs []error
	for _, m := range mods {
		if err := e.loadOne(m); err != nil {
			errs = append(errs, fmt.Errorf("mod %s: %w", m.Plugin, err))
			e.log.Warn("mods: load failed", "plugin", m.Plugin, "module", m.Path, "err", err)
		}
	}
	return errs
}

func (e *Engine) loadOne(m Module) error {
	if e.init != nil {
		return e.init
	}
	if e.closed.Load() {
		return ErrClosed
	}
	if !nameRule.MatchString(m.Plugin) && !pluginNameRule.MatchString(m.Plugin) {
		return fmt.Errorf("plugin name %q is not usable in a tool name", m.Plugin)
	}
	opts, err := OptionsFor(m.Root, m.Options)
	if err != nil {
		return err
	}
	code, err := bundle(m.Root, m.Path, e.opts.CacheDir)
	if err != nil {
		return fmt.Errorf("bundle: %w", err)
	}
	var loadErr error
	if !e.w.do(func() { loadErr = e.evaluate(m, code, opts) }) {
		return ErrClosed
	}
	return loadErr
}

var pluginNameRule = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// evaluate runs a bundled module and its register(on, options); worker-only.
func (e *Engine) evaluate(m Module, code string, opts map[string]any) error {
	vm := e.w.vm
	if _, dup := e.byName[m.Plugin]; dup {
		return fmt.Errorf("a mod named %s is already loaded", m.Plugin)
	}
	p := &plugin{name: m.Plugin, root: m.Root, timers: map[*timer]bool{}, rows: pluginConfigRows(m.Plugin, m.Root, opts)}
	prog, err := goja.Compile(m.Path, "(function (module, exports, require) {"+code+"\n})", true)
	if err != nil {
		return fmt.Errorf("compile: %w", err)
	}
	wrapper, err := vm.RunProgram(prog)
	if err != nil {
		return fmt.Errorf("evaluate: %w", jsErr(err))
	}
	wrap, _ := goja.AssertFunction(wrapper)
	module := vm.NewObject()
	exports := vm.NewObject()
	_ = module.Set("exports", exports)
	require := vm.ToValue(func(name string) goja.Value {
		if name == "claude-code" {
			return e.js.claudeCode
		}
		panic(vm.NewTypeError("a hooks module may import only its plugin's files and \"claude-code\", not %q", name))
	})
	if _, err := wrap(goja.Undefined(), module, exports, require); err != nil {
		return fmt.Errorf("evaluate: %w", jsErr(err))
	}
	reg, ok := goja.AssertFunction(module.Get("exports").ToObject(vm).Get("register"))
	if !ok {
		return errors.New("the module exports no register function")
	}
	optVal, err := e.toJS(opts)
	if err != nil {
		return fmt.Errorf("options: %w", err)
	}
	p.options = optVal
	var pending []*hook
	on := vm.ToValue(func(call goja.FunctionCall) goja.Value {
		return e.on(p, &pending, call)
	})
	if _, err := reg(goja.Undefined(), on, optVal); err != nil {
		return fmt.Errorf("register: %w", jsErr(err))
	}
	p.loaded = true
	e.plugins = append(e.plugins, p)
	e.byName[p.name] = p
	for _, h := range pending {
		e.hooks[h.event] = append(e.hooks[h.event], h)
	}
	return nil
}

// expandPattern is the events a pattern names: an event, or "noun.*" for every
// event of that noun kiln raises ("telemetry.*"). A noun kiln raises none of
// stays as written, so it logs like any event kiln does not raise.
func expandPattern(pat string) []string {
	noun, ok := strings.CutSuffix(pat, ".*")
	if !ok {
		return []string{pat}
	}
	var out []string
	for ev := range supportedEvents {
		if strings.HasPrefix(ev, noun+".") {
			out = append(out, ev)
		}
	}
	if len(out) == 0 {
		return []string{pat}
	}
	sort.Strings(out)
	return out
}

// on is the mod's on(pattern, [matcher,] hook): it returns a registration
// whose .catch(handler) sets the hook's error handler.
func (e *Engine) on(p *plugin, pending *[]*hook, call goja.FunctionCall) goja.Value {
	vm := e.w.vm
	if p.loaded {
		e.log.Warn(fmt.Sprintf("%s: on() called after register returned; the hook is ignored", p.name))
		return e.registration(nil)
	}
	var events []string
	switch pat := call.Argument(0).Export().(type) {
	case string:
		events = expandPattern(pat)
	case []any:
		for _, x := range pat {
			s, ok := x.(string)
			if !ok {
				panic(vm.NewTypeError("on: an event pattern lists event names"))
			}
			events = append(events, s)
		}
	default:
		panic(vm.NewTypeError("on: the first argument names an event"))
	}
	var matcher *goja.Object
	fnArg := call.Argument(1)
	if len(call.Arguments) >= 3 {
		if m, ok := call.Argument(1).(*goja.Object); ok {
			matcher = m
		}
		fnArg = call.Argument(2)
	}
	fn, ok := goja.AssertFunction(fnArg)
	if !ok {
		panic(vm.NewTypeError("on(%q): the hook is not a function", events[0]))
	}
	var regs []*hook
	for _, ev := range events {
		if !supportedEvents[ev] {
			e.log.Info(fmt.Sprintf("%s: kiln does not raise %s (mods v1); this hook never runs", p.name, ev))
		}
		h := &hook{plugin: p, event: ev, matcher: matcher, fn: fn}
		regs = append(regs, h)
		*pending = append(*pending, h)
	}
	return e.registration(regs)
}

func (e *Engine) registration(hs []*hook) goja.Value {
	vm := e.w.vm
	reg := vm.NewObject()
	_ = reg.Set("catch", func(call goja.FunctionCall) goja.Value {
		fn, ok := goja.AssertFunction(call.Argument(0))
		if !ok {
			panic(vm.NewTypeError(".catch takes a handler function"))
		}
		for _, h := range hs {
			h.catch = fn
		}
		return goja.Undefined()
	})
	return reg
}

// OptionsFor returns a plugin's options: configured, plus the default of
// every userConfig field plugin.json declares that configured leaves out.
func OptionsFor(root string, configured map[string]any) (map[string]any, error) {
	out := map[string]any{}
	data, err := os.ReadFile(filepath.Join(root, ".claude-plugin", "plugin.json"))
	if err == nil {
		var manifest struct {
			UserConfig map[string]struct {
				Default json.RawMessage `json:"default"`
			} `json:"userConfig"`
		}
		if err := json.Unmarshal(data, &manifest); err != nil {
			return nil, fmt.Errorf("plugin.json: %w", err)
		}
		for k, f := range manifest.UserConfig {
			if len(f.Default) == 0 {
				continue
			}
			var v any
			if json.Unmarshal(f.Default, &v) == nil {
				out[k] = v
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for k, v := range configured {
		out[k] = v
	}
	return out, nil
}

// --- the middleware chain ----------------------------------------------------

// chain is one dispatch of one event through a list of hooks, then core.
type chain struct {
	e         *Engine
	event     string
	hooks     []*hook
	streaming bool
	core      func(input goja.Value) goja.Value
	feeds     map[int]goja.Value
}

// feedFor is the chunk feed of stage i of a streaming chain: the hook at index
// i, or core at len(hooks). The outermost stage's feed keeps nothing.
func (c *chain) feedFor(i int) goja.Value {
	if f, ok := c.feeds[i]; ok {
		return f
	}
	if c.feeds == nil {
		c.feeds = map[int]goja.Value{}
	}
	f, err := c.e.js.makeFeed(goja.Undefined(), c.e.w.vm.ToValue(i == 0))
	if err != nil {
		f = goja.Undefined()
	}
	c.feeds[i] = f
	return f
}

// frame is one hook's run within a chain (or its .catch handler's).
type frame struct {
	c      *chain
	i      int
	h      *hook
	input  goja.Value
	plugin *plugin

	resolve func(any) error

	isCatch bool
	parent  *frame // the failed hook's frame, for a catch frame
	errInfo goja.Value

	settled     bool
	failed      bool
	called      bool
	nextPromise goja.Value

	budget  time.Duration
	used    time.Duration
	since   time.Time
	pending int
	stop    func() bool // cancels the budget timer; nil when none is armed
}

func (f *frame) isRender() bool { return f.c != nil && f.c.event == "ui.render" }

// run starts the chain at hook i; it returns a promise of the result.
func (c *chain) run(i int, input goja.Value) goja.Value {
	e := c.e
	for i < len(c.hooks) && !e.matches(c.hooks[i], input) {
		i++
	}
	if i >= len(c.hooks) {
		return e.promiseOf(c.core(input))
	}
	p, resolve, _ := e.w.vm.NewPromise()
	f := &frame{c: c, i: i, h: c.hooks[i], input: input, plugin: c.hooks[i].plugin, resolve: resolve, budget: e.opts.HookBudget}
	f.start(f.h.fn)
	return e.w.vm.ToValue(p)
}

// start calls the hook (or the catch handler) and watches what it returns.
func (f *frame) start(fn goja.Callable) {
	e := f.c.e
	f.since = e.opts.BudgetClock.Now()
	f.arm()
	var args []goja.Value
	dollar, err := e.js.makeDollar(goja.Undefined(), e.js.apiCall, e.w.vm.ToValue(f), e.js.apiNames)
	if err != nil {
		f.fail("throw", jsErr(err).Error())
		return
	}
	args = append(args, dollar, f.input, f.makeNext())
	ret, err := fn(goja.Undefined(), args...)
	if err != nil {
		if failKind(err) == "timeout" {
			// The watchdog interrupted the runtime: nothing more can run in
			// this job, so the hook fails in the next one.
			e.w.post(func() { f.fail("timeout", "ran without yielding past its budget") })
			return
		}
		f.fail("throw", jsErr(err).Error())
		return
	}
	if f.c.streaming {
		if is, _ := e.js.isAsyncIter(goja.Undefined(), ret); is != nil && is.ToBoolean() {
			ret, err = e.js.drain(goja.Undefined(), ret, f.c.feedFor(f.i))
			if err != nil {
				f.fail(failKind(err), jsErr(err).Error())
				return
			}
		}
	}
	e.then(ret, f.succeed, func(reason goja.Value) {
		f.fail("throw", reasonText(reason))
	})
}

func (f *frame) succeed(v goja.Value) {
	if f.settled {
		return
	}
	if gatingEvents[f.c.event] {
		if _, ok := v.(*goja.Object); !ok {
			f.fail("throw", fmt.Sprintf("returned %s where %s takes an object", typeName(v), f.c.event))
			return
		}
	}
	f.settled = true
	f.stopTimer()
	_ = f.resolve(v)
}

// fail handles a hook that threw, timed out or returned the wrong shape:
// its .catch handler answers if it has one; otherwise the result next
// produced stands if it called next, or the rest of the chain runs in its
// place (claude-code docs: "Handle a hook that fails").
func (f *frame) fail(kind, message string) {
	if f.settled {
		return
	}
	f.settled = true
	f.failed = true
	f.stopTimer()
	e := f.c.e
	what := "threw " + message
	if kind == "timeout" {
		what = "timed out: " + message
	}
	if f.isCatch {
		e.log.Warn(fmt.Sprintf("%s: %s .catch handler failed (%s); the hook is skipped", f.plugin.name, f.c.event, what),
			"plugin", f.plugin.name, "event", f.c.event)
		f.parent.fallback()
		return
	}
	if f.h.catch != nil {
		e.log.Warn(fmt.Sprintf("%s: %s hook failed (%s); its .catch handler answers", f.plugin.name, f.c.event, what),
			"plugin", f.plugin.name, "event", f.c.event)
		f.runCatch(kind, message)
		return
	}
	e.log.Warn(fmt.Sprintf("%s: %s hook skipped: %s", f.plugin.name, f.c.event, what),
		"plugin", f.plugin.name, "event", f.c.event)
	f.fallback()
}

// fallback settles a failed hook's link without it: what its next call
// produced, or the rest of the chain run on its behalf.
func (f *frame) fallback() {
	if f.nextPromise != nil {
		_ = f.resolve(f.nextPromise)
		return
	}
	np := f.c.run(f.i+1, f.input)
	f.nextPromise = np
	_ = f.resolve(np)
}

func (f *frame) runCatch(kind, message string) {
	e := f.c.e
	info := e.w.vm.NewObject()
	_ = info.Set("kind", kind)
	_ = info.Set("message", message)
	_ = info.Set("budget", e.opts.CatchBudget.Milliseconds())
	cf := &frame{
		c: f.c, i: f.i, h: f.h, input: f.input, plugin: f.plugin, resolve: f.resolve,
		isCatch: true, parent: f, errInfo: info, budget: e.opts.CatchBudget,
	}
	cf.start(f.h.catch)
}

// makeNext builds the next function a hook receives, with next.called,
// next.error (in a catch handler), next.budget, next.signal and next.origin.
func (f *frame) makeNext() goja.Value {
	e := f.c.e
	vm := e.w.vm
	owner := f
	if f.isCatch {
		owner = f.parent
	}
	next := vm.ToValue(func(call goja.FunctionCall) goja.Value {
		return f.callNext(owner, call.Argument(0))
	}).(*goja.Object)
	_ = next.DefineAccessorProperty("called", vm.ToValue(func() bool { return owner.called }), nil, goja.FLAG_FALSE, goja.FLAG_TRUE)
	budget := vm.NewObject()
	_ = budget.Set("ms", f.budget.Milliseconds())
	_ = budget.DefineAccessorProperty("remainingMs", vm.ToValue(func() int64 { return f.remaining().Milliseconds() }), nil, goja.FLAG_FALSE, goja.FLAG_TRUE)
	_ = next.Set("budget", budget)
	signal := vm.NewObject()
	_ = signal.Set("aborted", false)
	_ = signal.Set("reason", goja.Undefined())
	noop := func(goja.FunctionCall) goja.Value { return goja.Undefined() }
	_ = signal.Set("throwIfAborted", noop)
	_ = signal.Set("addEventListener", noop)
	_ = signal.Set("removeEventListener", noop)
	_ = next.Set("signal", signal)
	origin := vm.NewObject()
	_ = origin.Set("plugin", "engine")
	_ = origin.Set("tier", "core")
	_ = next.Set("origin", origin)
	_ = next.Set("to", func(goja.FunctionCall) goja.Value {
		return e.rejected("next.to is only for managed mods (prependPlugins/appendPlugins), which kiln does not have")
	})
	if f.errInfo != nil {
		_ = next.Set("error", f.errInfo)
	}
	return next
}

func (f *frame) callNext(owner *frame, arg goja.Value) goja.Value {
	e := f.c.e
	if f.failed {
		return f.wrapNext(e.rejected(fmt.Sprintf("%s: this %s hook already failed; its next is closed", f.plugin.name, f.c.event)))
	}
	ev := arg
	if ev == nil || goja.IsUndefined(ev) {
		ev = f.input
	}
	ev, err := e.pin(f.c.event, f.input, ev)
	if err != nil {
		return f.wrapNext(e.rejected(err.Error()))
	}
	if owner.nextPromise == nil {
		owner.called = true
		owner.nextPromise = f.c.run(f.i+1, ev)
	}
	np := owner.nextPromise
	f.pause()
	resume := func(goja.Value) { f.resume() }
	e.then(np, resume, resume)
	return f.wrapNext(np)
}

// wrapNext is what next returns: the promise, or on a streaming event an
// async iterator to yield* from.
func (f *frame) wrapNext(p goja.Value) goja.Value {
	if !f.c.streaming {
		return p
	}
	next := f.i + 1
	for next < len(f.c.hooks) && !f.c.e.matches(f.c.hooks[next], f.input) {
		next++
	}
	it, err := f.c.e.js.streamOf(goja.Undefined(), p, f.c.feedFor(next))
	if err != nil {
		return p
	}
	return it
}

// --- the budget clock: a hook's own time, stopped while next is in flight.

func (f *frame) remaining() time.Duration {
	used := f.used
	if f.pending == 0 && !f.settled {
		used += f.c.e.opts.BudgetClock.Now().Sub(f.since)
	}
	if r := f.budget - used; r > 0 {
		return r
	}
	return 0
}

func (f *frame) arm() {
	f.stopTimer()
	e := f.c.e
	f.stop = e.opts.BudgetClock.AfterFunc(f.remaining(), func() { e.w.post(f.checkBudget) })
}

func (f *frame) stopTimer() {
	if f.stop != nil {
		f.stop()
		f.stop = nil
	}
}

func (f *frame) pause() {
	f.pending++
	if f.pending == 1 && !f.settled {
		f.used += f.c.e.opts.BudgetClock.Now().Sub(f.since)
		f.stopTimer()
	}
}

func (f *frame) resume() {
	f.pending--
	if f.pending == 0 && !f.settled {
		f.since = f.c.e.opts.BudgetClock.Now()
		f.arm()
	}
}

func (f *frame) checkBudget() {
	if f.settled || f.pending > 0 {
		return
	}
	if f.remaining() > 0 {
		f.arm()
		return
	}
	f.fail("timeout", fmt.Sprintf("its own time passed the %v budget", f.budget))
}

// --- chain helpers -------------------------------------------------------------

// matches applies a hook's matcher to an event: every field it names must
// equal the event's (a list matches any of its values, a RegExp tests).
func (e *Engine) matches(h *hook, input goja.Value) bool {
	if h.matcher == nil {
		return true
	}
	obj, ok := input.(*goja.Object)
	if !ok {
		return false
	}
	for _, k := range h.matcher.Keys() {
		want := h.matcher.Get(k)
		got := obj.Get(k)
		if got == nil {
			got = goja.Undefined()
		}
		if !e.matchValue(want, got) {
			return false
		}
	}
	return true
}

func (e *Engine) matchValue(want, got goja.Value) bool {
	if wo, ok := want.(*goja.Object); ok {
		switch wo.ClassName() {
		case "Array":
			n := int(wo.Get("length").ToInteger())
			for i := 0; i < n; i++ {
				if e.matchValue(wo.Get(fmt.Sprint(i)), got) {
					return true
				}
			}
			return false
		case "RegExp":
			test, ok := goja.AssertFunction(wo.Get("test"))
			if !ok {
				return false
			}
			r, err := test(wo, got)
			return err == nil && r.ToBoolean()
		case "Object":
			// { props: { kind: "x" } } matches on the nested field, as the
			// d.ts's ToolProgress example does.
			gotObj, ok := got.(*goja.Object)
			if !ok {
				return false
			}
			for _, k := range wo.Keys() {
				g := gotObj.Get(k)
				if g == nil {
					g = goja.Undefined()
				}
				if !e.matchValue(wo.Get(k), g) {
					return false
				}
			}
			return true
		}
	}
	return want.StrictEquals(got)
}

// pin enforces an event's pinned fields on next's argument.
func (e *Engine) pin(event string, orig, ev goja.Value) (goja.Value, error) {
	evObj, ok := ev.(*goja.Object)
	if !ok {
		return nil, fmt.Errorf("next takes the %s event object, got %s", event, typeName(ev))
	}
	if ev == orig {
		return ev, nil
	}
	origObj, _ := orig.(*goja.Object)
	if origObj == nil {
		return ev, nil
	}
	var missing *goja.Object
	for _, k := range pinnedFields[event] {
		was := origObj.Get(k)
		now := evObj.Get(k)
		if now == nil || goja.IsUndefined(now) {
			if was != nil && !goja.IsUndefined(was) {
				if missing == nil {
					missing = e.w.vm.NewObject()
				}
				_ = missing.Set(k, was)
			}
			continue
		}
		if was == nil || !was.StrictEquals(now) {
			return nil, fmt.Errorf("%s: %s is pinned; a rewrite of it is refused", event, k)
		}
	}
	if missing != nil {
		v, err := e.js.assign(goja.Undefined(), ev, missing)
		if err != nil {
			return nil, err
		}
		ev = v
	}
	frozen, err := e.js.deepFreeze(goja.Undefined(), ev)
	if err != nil {
		return nil, err
	}
	return frozen, nil
}

// then calls ok or fail once p (a promise or a plain value) settles.
func (e *Engine) then(p goja.Value, ok, fail func(goja.Value)) {
	vm := e.w.vm
	if p == nil {
		p = goja.Undefined()
	}
	okFn := vm.ToValue(func(call goja.FunctionCall) goja.Value { ok(call.Argument(0)); return goja.Undefined() })
	failFn := vm.ToValue(func(call goja.FunctionCall) goja.Value { fail(call.Argument(0)); return goja.Undefined() })
	if _, err := e.js.then(goja.Undefined(), p, okFn, failFn); err != nil {
		fail(vm.ToValue(err.Error()))
	}
}

func (e *Engine) promiseOf(v goja.Value) goja.Value {
	if v == nil {
		v = goja.Undefined()
	}
	if o, ok := v.(*goja.Object); ok {
		if _, isPromise := o.Export().(*goja.Promise); isPromise {
			return v
		}
	}
	p, resolve, _ := e.w.vm.NewPromise()
	_ = resolve(v)
	return e.w.vm.ToValue(p)
}

func (e *Engine) rejected(message string) goja.Value {
	vm := e.w.vm
	p, _, reject := vm.NewPromise()
	var reason any = message
	if errObj, err := vm.New(vm.Get("Error"), vm.ToValue(message)); err == nil {
		reason = errObj
	}
	_ = reject(reason)
	return vm.ToValue(p)
}

// runChain dispatches input through hooks then core, calling done on the
// worker once the outermost link settles.
func (e *Engine) runChain(c *chain, input goja.Value, done func(v goja.Value, err error)) {
	top := c.run(0, input)
	e.then(top, func(v goja.Value) { done(v, nil) }, func(r goja.Value) { done(nil, errors.New(reasonText(r))) })
}

// newChain is a chain over every hook registered on event.
func (e *Engine) newChain(event string, core func(goja.Value) goja.Value) *chain {
	hs := append([]*hook(nil), e.hooks[event]...)
	return &chain{e: e, event: event, hooks: hs, streaming: event == "turn.step", core: core}
}

// toJS converts Go data to a deeply frozen JavaScript value through JSON.
func (e *Engine) toJS(v any) (goja.Value, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return e.parseJSON(data, true)
}

func (e *Engine) parseJSON(data []byte, freeze bool) (goja.Value, error) {
	v, err := e.js.parse(goja.Undefined(), e.w.vm.ToValue(string(data)))
	if err != nil {
		return nil, err
	}
	if freeze {
		return e.js.deepFreeze(goja.Undefined(), v)
	}
	return v, nil
}

// fromJS converts a JavaScript value to JSON; ok is false for undefined (and
// for anything JSON.stringify drops).
func (e *Engine) fromJS(v goja.Value) (json.RawMessage, bool) {
	if v == nil || goja.IsUndefined(v) {
		return nil, false
	}
	s, err := e.js.stringify(goja.Undefined(), v)
	if err != nil || s == nil || goja.IsUndefined(s) {
		return nil, false
	}
	return json.RawMessage(s.String()), true
}

// --- waiting on the worker -------------------------------------------------------

// await posts start to the worker and waits for it to send a result, ctx
// to end, or the engine to close.
func await[T any](ctx context.Context, e *Engine, start func(send func(T))) (T, error) {
	var zero T
	if e.closed.Load() {
		return zero, ErrClosed
	}
	if e.init != nil {
		return zero, e.init
	}
	ch := make(chan T, 1)
	var once sync.Once
	send := func(v T) { once.Do(func() { ch <- v }) }
	if !e.w.post(func() { start(send) }) {
		return zero, ErrClosed
	}
	select {
	case v := <-ch:
		return v, nil
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-e.w.done:
		select {
		case v := <-ch:
			return v, nil
		default:
			return zero, ErrClosed
		}
	}
}

// settleTimeout bounds the calls that take no context: long enough for a
// hook and its catch handler to run out their budgets, twice.
func (e *Engine) settleTimeout() time.Duration {
	return 2*(e.opts.HookBudget+e.opts.CatchBudget) + time.Second
}

// sync waits until every job posted so far has run (tests).
func (e *Engine) sync() { e.w.do(func() {}) }

// --- errors -----------------------------------------------------------------------

func jsErr(err error) error {
	var ex *goja.Exception
	if errors.As(err, &ex) {
		return errors.New(ex.Value().String())
	}
	var intr *goja.InterruptedError
	if errors.As(err, &intr) {
		return fmt.Errorf("interrupted: %v", intr.Value())
	}
	return err
}

func failKind(err error) string {
	var intr *goja.InterruptedError
	if errors.As(err, &intr) {
		return "timeout"
	}
	return "throw"
}

func reasonText(r goja.Value) string {
	if r == nil || goja.IsUndefined(r) {
		return "undefined"
	}
	if o, ok := r.(*goja.Object); ok {
		if goErr, ok := o.Export().(error); ok {
			return goErr.Error()
		}
	}
	return r.String()
}

func typeName(v goja.Value) string {
	switch {
	case v == nil || goja.IsUndefined(v):
		return "undefined"
	case goja.IsNull(v):
		return "null"
	}
	if o, ok := v.(*goja.Object); ok {
		return o.ClassName()
	}
	return fmt.Sprintf("%T", v.Export())
}
