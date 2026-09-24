package harness

import (
	"context"
	"encoding/json"
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
			l.h.events.Emit(Event{Type: EventHandlerError, Lane: l.name, HookName: "before_tool", Err: err})
			continue
		}
		if result.Block == nil && r.Block != nil {
			result.Block = r.Block
		}
		if r.RewrittenArgs != nil {
			result.RewrittenArgs = r.RewrittenArgs
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
