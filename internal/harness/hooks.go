package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/andrepato/harness/internal/msg"
)

// ToolBlock is what OnBeforeTool returns to refuse a call: it becomes an
// isError tool result carrying Reason, and the tool's Execute is never
// called.
type ToolBlock struct {
	Reason    string
	Terminate bool
}

// BeforeToolResult is OnBeforeTool's return value: either block the call,
// or rewrite its arguments (nil RewrittenArgs leaves them unchanged).
type BeforeToolResult struct {
	Block         *ToolBlock
	RewrittenArgs json.RawMessage
	// PermissionOutcome is the gate wrapper's report of how its decision
	// was reached ("approved" / "auto-approved" / "", mirroring
	// permission.Outcome as a plain string so this package does not need
	// to import internal/claude/permission). Set by the OnBeforeTool
	// handler that owns the permission gate (internal/cli/chat.go); other
	// hooks leave it empty. Read back out of the turn loop's EventToolEnd
	// so the TUI can render it as the tool block's meta.
	PermissionOutcome string
}

// Hooks holds typed, ordered registrations for every lifecycle hook the
// turn loop calls. Each Add* returns an unsubscribe func. A handler that
// panics or returns an error never crashes the loop: it is caught, turned
// into a fault-labelled "handler_error" Event (via the owning Harness's
// Events bus) naming the hook, and treated as a no-op for that call.
type Hooks struct {
	beforeRun        []func(ctx context.Context) error
	beforeDrive      []func(ctx context.Context) error
	beforeRunEnd     []func(ctx context.Context, status string) error
	beforeStop       []func(ctx context.Context, info StopInfo) (StopVerdict, error)
	transformContext []func(ctx context.Context, transcript []msg.Message) ([]msg.Message, error)
	beforeRequest    []func(ctx context.Context) error
	beforePayload    []func(ctx context.Context, payload map[string]any) (map[string]any, error)
	afterResponse    []func(ctx context.Context, m *msg.AssistantMessage) error
	beforeTool       []func(ctx context.Context, call msg.ToolCall) (BeforeToolResult, error)
	afterTool        []func(ctx context.Context, call msg.ToolCall, result *msg.ToolResultMessage) error
	beforeCompaction []func(ctx context.Context) error
	beforeNavigation []func(ctx context.Context, targetID *string) error
}

// NewHooks returns an empty Hooks registry.
func NewHooks() *Hooks { return &Hooks{} }

func (h *Hooks) OnBeforeRun(fn func(ctx context.Context) error) func() {
	h.beforeRun = append(h.beforeRun, fn)
	i := len(h.beforeRun) - 1
	return func() { h.beforeRun[i] = nil }
}

func (h *Hooks) OnBeforeDrive(fn func(ctx context.Context) error) func() {
	h.beforeDrive = append(h.beforeDrive, fn)
	i := len(h.beforeDrive) - 1
	return func() { h.beforeDrive[i] = nil }
}

func (h *Hooks) OnBeforeRunEnd(fn func(ctx context.Context, status string) error) func() {
	h.beforeRunEnd = append(h.beforeRunEnd, fn)
	i := len(h.beforeRunEnd) - 1
	return func() { h.beforeRunEnd[i] = nil }
}

// StopInfo describes the turn an OnBeforeStop handler may keep going.
type StopInfo struct {
	// StopHookActive is true once a handler has continued this operation:
	// the turn it is looking at is already a continuation. It stays true
	// for the rest of the operation and starts false on the next prompt.
	StopHookActive bool
	// Last is the reply that ended the turn.
	Last *msg.AssistantMessage
}

// StopVerdict is an OnBeforeStop handler's answer.
type StopVerdict struct {
	// Continue keeps the operation running: Message is committed to the
	// branch as a user message, and the model is asked again.
	Continue bool
	Message  string
	// Source names what continued the turn (a hook event such as "Stop"),
	// recorded on the message as msg.UserMessage.KilnHook.
	Source string
	// Interrupted reports that the handler was stopped by the operation's
	// context being cancelled: the operation ends as aborted, not
	// completed.
	Interrupted bool
}

// OnBeforeStop registers a handler consulted when a turn is about to end
// the operation (the model replied with no tool calls and nothing is
// queued). It runs on the operation's context, so Lane.Abort cancels it.
// The first handler that returns Continue wins; later handlers are not
// consulted for that turn.
func (h *Hooks) OnBeforeStop(fn func(ctx context.Context, info StopInfo) (StopVerdict, error)) func() {
	h.beforeStop = append(h.beforeStop, fn)
	i := len(h.beforeStop) - 1
	return func() { h.beforeStop[i] = nil }
}

func (h *Hooks) OnTransformContext(fn func(ctx context.Context, transcript []msg.Message) ([]msg.Message, error)) func() {
	h.transformContext = append(h.transformContext, fn)
	i := len(h.transformContext) - 1
	return func() { h.transformContext[i] = nil }
}

func (h *Hooks) OnBeforeRequest(fn func(ctx context.Context) error) func() {
	h.beforeRequest = append(h.beforeRequest, fn)
	i := len(h.beforeRequest) - 1
	return func() { h.beforeRequest[i] = nil }
}

func (h *Hooks) OnBeforePayload(fn func(ctx context.Context, payload map[string]any) (map[string]any, error)) func() {
	h.beforePayload = append(h.beforePayload, fn)
	i := len(h.beforePayload) - 1
	return func() { h.beforePayload[i] = nil }
}

func (h *Hooks) OnAfterResponse(fn func(ctx context.Context, m *msg.AssistantMessage) error) func() {
	h.afterResponse = append(h.afterResponse, fn)
	i := len(h.afterResponse) - 1
	return func() { h.afterResponse[i] = nil }
}

// OnBeforeTool registers a handler consulted, in registration order, before
// each tool call executes. The first handler to return a non-nil Block
// wins; later handlers are still run for RewrittenArgs but their Block is
// ignored once one is already set, matching "block short-circuits, args
// keep composing".
func (h *Hooks) OnBeforeTool(fn func(ctx context.Context, call msg.ToolCall) (BeforeToolResult, error)) func() {
	h.beforeTool = append(h.beforeTool, fn)
	i := len(h.beforeTool) - 1
	return func() { h.beforeTool[i] = nil }
}

func (h *Hooks) OnAfterTool(fn func(ctx context.Context, call msg.ToolCall, result *msg.ToolResultMessage) error) func() {
	h.afterTool = append(h.afterTool, fn)
	i := len(h.afterTool) - 1
	return func() { h.afterTool[i] = nil }
}

func (h *Hooks) OnBeforeCompaction(fn func(ctx context.Context) error) func() {
	h.beforeCompaction = append(h.beforeCompaction, fn)
	i := len(h.beforeCompaction) - 1
	return func() { h.beforeCompaction[i] = nil }
}

func (h *Hooks) OnBeforeNavigation(fn func(ctx context.Context, targetID *string) error) func() {
	h.beforeNavigation = append(h.beforeNavigation, fn)
	i := len(h.beforeNavigation) - 1
	return func() { h.beforeNavigation[i] = nil }
}

// --- invocation, with panic/error containment -----------------------------

// runGuarded calls fn, converting a panic into an error, so one hook's bug
// can never crash the turn loop.
func runGuarded(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("hook panicked: %v", r)
		}
	}()
	return fn()
}

func (l *Lane) invokeBeforeRun(ctx context.Context) {
	for _, fn := range l.h.hooks.beforeRun {
		if fn == nil {
			continue
		}
		if err := runGuarded(func() error { return fn(ctx) }); err != nil {
			l.h.events.Emit(Event{Type: EventHandlerError, Lane: l.name, HookName: "before_run", Err: err})
		}
	}
}

func (l *Lane) invokeBeforeDrive(ctx context.Context) {
	for _, fn := range l.h.hooks.beforeDrive {
		if fn == nil {
			continue
		}
		if err := runGuarded(func() error { return fn(ctx) }); err != nil {
			l.h.events.Emit(Event{Type: EventHandlerError, Lane: l.name, HookName: "before_drive", Err: err})
		}
	}
}

func (l *Lane) invokeBeforeRequest(ctx context.Context) {
	for _, fn := range l.h.hooks.beforeRequest {
		if fn == nil {
			continue
		}
		if err := runGuarded(func() error { return fn(ctx) }); err != nil {
			l.h.events.Emit(Event{Type: EventHandlerError, Lane: l.name, HookName: "before_request", Err: err})
		}
	}
}

func (l *Lane) invokeBeforeRunEnd(ctx context.Context, status string) {
	for _, fn := range l.h.hooks.beforeRunEnd {
		if fn == nil {
			continue
		}
		if err := runGuarded(func() error { return fn(ctx, status) }); err != nil {
			l.h.events.Emit(Event{Type: EventHandlerError, Lane: l.name, HookName: "before_run_end", Err: err})
		}
	}
}

func (l *Lane) invokeBeforeStop(ctx context.Context, info StopInfo) StopVerdict {
	for _, fn := range l.h.hooks.beforeStop {
		if fn == nil {
			continue
		}
		var v StopVerdict
		err := runGuarded(func() error {
			var innerErr error
			v, innerErr = fn(ctx, info)
			return innerErr
		})
		if err != nil {
			if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
				return StopVerdict{Interrupted: true}
			}
			l.h.events.Emit(Event{Type: EventHandlerError, Lane: l.name, HookName: "before_stop", Err: err})
			continue
		}
		if v.Interrupted || v.Continue {
			return v
		}
	}
	return StopVerdict{}
}

func (l *Lane) invokeTransformContext(ctx context.Context, transcript []msg.Message) []msg.Message {
	for _, fn := range l.h.hooks.transformContext {
		if fn == nil {
			continue
		}
		var out []msg.Message
		err := runGuarded(func() error {
			var innerErr error
			out, innerErr = fn(ctx, transcript)
			return innerErr
		})
		if err != nil {
			l.h.events.Emit(Event{Type: EventHandlerError, Lane: l.name, HookName: "transform_context", Err: err})
			continue
		}
		transcript = out
	}
	return transcript
}

func (l *Lane) invokeBeforeTool(ctx context.Context, call msg.ToolCall) BeforeToolResult {
	var result BeforeToolResult
	for _, fn := range l.h.hooks.beforeTool {
		if fn == nil {
			continue
		}
		var r BeforeToolResult
		err := runGuarded(func() error {
			var innerErr error
			r, innerErr = fn(ctx, call)
			return innerErr
		})
		if err != nil {
			// before_tool is where the permission gate runs: a handler
			// that failed did not approve the call, so it does not run.
			// A handler that stopped because the turn was interrupted
			// (Esc while a PreToolUse hook or a prompt was pending) did not
			// fail: say so, rather than show "context canceled" as a
			// permission error.
			if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
				if result.Block == nil {
					result.Block = &ToolBlock{Reason: "interrupted before the call ran, so it did not run."}
				}
				continue
			}
			l.h.events.Emit(Event{Type: EventHandlerError, Lane: l.name, HookName: "before_tool", Err: err})
			if result.Block == nil {
				result.Block = &ToolBlock{Reason: "the permission check failed (" + err.Error() + "), so the call did not run."}
			}
			continue
		}
		if result.Block == nil && r.Block != nil {
			result.Block = r.Block
		}
		if r.RewrittenArgs != nil {
			result.RewrittenArgs = r.RewrittenArgs
		}
		if r.PermissionOutcome != "" {
			result.PermissionOutcome = r.PermissionOutcome
		}
	}
	return result
}

func (l *Lane) invokeAfterTool(ctx context.Context, call msg.ToolCall, result *msg.ToolResultMessage) {
	for _, fn := range l.h.hooks.afterTool {
		if fn == nil {
			continue
		}
		if err := runGuarded(func() error { return fn(ctx, call, result) }); err != nil {
			l.h.events.Emit(Event{Type: EventHandlerError, Lane: l.name, HookName: "after_tool", Err: err})
		}
	}
}

func (l *Lane) invokeAfterResponse(ctx context.Context, m *msg.AssistantMessage) {
	for _, fn := range l.h.hooks.afterResponse {
		if fn == nil {
			continue
		}
		if err := runGuarded(func() error { return fn(ctx, m) }); err != nil {
			l.h.events.Emit(Event{Type: EventHandlerError, Lane: l.name, HookName: "after_response", Err: err})
		}
	}
}
