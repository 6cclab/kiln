// Print mode (`-p`): run one prompt, emit the result, exit.
//
// Ported from src/print.ts. Permission handling is the important difference
// from interactive mode: there is nobody to ask, so the gate refuses
// anything requiring confirmation rather than proceeding.
//
// # Deviation from the TypeScript reference
//
// print.ts reads the final assistant text off the "message_end" event's
// carried `message`. The Go harness's EventMessageEnd (internal/harness/
// events.go, turn.go:205) carries no payload at all — only Lane/OperationID/
// EntryID. The final AssistantMessage is instead carried on EventMessageUpdate's
// StreamEvent when StreamEvent.Type == msg.EventDone (turn.go:366, via
// msg.StreamEvent.Message). Collector therefore subscribes to
// EventMessageUpdate and reacts on the "done" sub-event rather than to
// EventMessageEnd directly, to get the same "one assistant text chunk"
// semantics print.ts gets from message_end. This is a mechanism
// substitution, not a behavior change: it fires exactly once per assistant
// turn, with the same final text.
package cli

import (
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// StreamEvent is one line of `--output-format stream-json` output. It is a
// union of four shapes (tool_start, tool_end, assistant, result); which
// fields are meaningful depends on Type. MarshalJSON renders exactly one of
// the four exact shapes below, field names and order matching print.ts's
// StreamEvent union, extended (P1) with the enriched result fields Claude
// Code's own result reports: usage/cost/duration/turn accounting, and an
// optional non-empty reason (e.g. "max-turns-exceeded"):
//
//	{"type":"tool_start","name":..,"arg":..?}
//	{"type":"tool_end","name":..,"isError":bool}
//	{"type":"assistant","text":..}
//	{"type":"result","ok":bool,"text":..,"blocked":[..],
//	 "usage":{"input":..,"output":..,"cache_read":..,"cache_write":..},
//	 "total_cost_usd":..,"duration_ms":..,"num_turns":..,
//	 "num_tool_calls":..,"reason":..?}
type StreamEvent struct {
	Type string

	// tool_start
	Name string
	Arg  *string

	// tool_end
	IsError bool

	// assistant
	Text string

	// result
	OK           bool
	Blocked      []string
	Usage        msg.Usage
	CostUSD      float64
	DurationMS   int64
	Turns        int
	NumToolCalls int
	Reason       string
}

// usageJSON is the wire shape of a result event's "usage" field: Claude
// Code's own field names (snake_case, and only the four totals it reports),
// not msg.Usage's own camelCase json tags.
type usageJSON struct {
	Input      int `json:"input"`
	Output     int `json:"output"`
	CacheRead  int `json:"cache_read"`
	CacheWrite int `json:"cache_write"`
}

func usageJSONOf(u msg.Usage) usageJSON {
	return usageJSON{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite}
}

// MarshalJSON renders the exact shape for e.Type, so a field meaningless
// for that variant (e.g. "arg" on a tool_end) never appears.
func (e StreamEvent) MarshalJSON() ([]byte, error) {
	switch e.Type {
	case "tool_start":
		return json.Marshal(struct {
			Type string  `json:"type"`
			Name string  `json:"name"`
			Arg  *string `json:"arg,omitempty"`
		}{Type: e.Type, Name: e.Name, Arg: e.Arg})
	case "tool_end":
		return json.Marshal(struct {
			Type    string `json:"type"`
			Name    string `json:"name"`
			IsError bool   `json:"isError"`
		}{Type: e.Type, Name: e.Name, IsError: e.IsError})
	case "assistant":
		return json.Marshal(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{Type: e.Type, Text: e.Text})
	case "result":
		blocked := e.Blocked
		if blocked == nil {
			blocked = []string{}
		}
		return json.Marshal(struct {
			Type         string    `json:"type"`
			OK           bool      `json:"ok"`
			Text         string    `json:"text"`
			Blocked      []string  `json:"blocked"`
			Usage        usageJSON `json:"usage"`
			TotalCostUSD float64   `json:"total_cost_usd"`
			DurationMS   int64     `json:"duration_ms"`
			NumTurns     int       `json:"num_turns"`
			NumToolCalls int       `json:"num_tool_calls"`
			Reason       string    `json:"reason,omitempty"`
		}{
			Type: e.Type, OK: e.OK, Text: e.Text, Blocked: blocked,
			Usage: usageJSONOf(e.Usage), TotalCostUSD: e.CostUSD, DurationMS: e.DurationMS,
			NumTurns: e.Turns, NumToolCalls: e.NumToolCalls, Reason: e.Reason,
		})
	default:
		return json.Marshal(struct {
			Type string `json:"type"`
		}{Type: e.Type})
	}
}

// toolStartEvent builds the {"type":"tool_start","name":...,"arg":...?} shape.
func toolStartEvent(name string, arg *string) StreamEvent {
	return StreamEvent{Type: "tool_start", Name: name, Arg: arg}
}

// toolEndEvent builds the {"type":"tool_end","name":...,"isError":bool} shape.
func toolEndEvent(name string, isError bool) StreamEvent {
	return StreamEvent{Type: "tool_end", Name: name, IsError: isError}
}

// assistantEvent builds the {"type":"assistant","text":...} shape.
func assistantEvent(text string) StreamEvent {
	return StreamEvent{Type: "assistant", Text: text}
}

// resultEvent builds the terminal "result" StreamEvent from a finished
// PrintResult, carrying every enriched field alongside ok/text/blocked.
func resultEvent(r PrintResult) StreamEvent {
	return StreamEvent{
		Type: "result", OK: r.OK, Text: r.Text, Blocked: r.Blocked,
		Usage: r.Usage, CostUSD: r.CostUSD, DurationMS: r.DurationMS,
		Turns: r.Turns, NumToolCalls: r.NumToolCalls, Reason: r.Reason,
	}
}

// toolCall is one entry of PrintResult.ToolCalls. Arg and Blocked are
// pointers/omitted so JSON encoding drops them when unset, matching
// print.ts's JSON.stringify of an `undefined` field.
type toolCall struct {
	Name    string  `json:"name"`
	Arg     *string `json:"arg,omitempty"`
	Blocked *bool   `json:"blocked,omitempty"`
}

// PrintResult is the accumulated outcome of one print-mode run. Mirrors
// print.ts's PrintResult, enriched (P1) with the usage/cost/duration/turn
// accounting Claude Code's own result carries.
type PrintResult struct {
	Text      string
	ToolCalls []toolCall
	Blocked   []string
	OK        bool

	// Usage and CostUSD are the run's aggregate token/cost totals, copied
	// from session.SessionStats (session.State.GetStats(), reachable via
	// harness.Harness.Stats()) as of when Finish is called.
	Usage   msg.Usage
	CostUSD float64
	// DurationMS is time.Since(NewCollector's construction) in
	// milliseconds — wall-clock time for the whole run, not just model time.
	DurationMS int64
	// Turns is the number of harness.EventTurnEnd events observed.
	Turns int
	// NumToolCalls is len(ToolCalls).
	NumToolCalls int
	// Reason is non-empty only when the run was cut short for a reason
	// worth reporting (e.g. "max-turns-exceeded"); omitted from JSON when
	// empty.
	Reason string
}

// Collector subscribes to a harness.Events bus and accumulates a
// PrintResult as the run progresses, optionally streaming each event to a
// writer as NDJSON (`--output-format stream-json`).
type Collector struct {
	// Stream, when non-nil, receives every StreamEvent as it happens.
	Stream func(StreamEvent)

	// GetBlocked is wired to the permission gate's block log. Finish calls it
	// once to populate PrintResult.Blocked, matching print.ts's
	// `opts.gate?.getBlocked() ?? []`.
	GetBlocked func() []string

	text      strings.Builder
	toolCalls []toolCall
	unsubs    []func()
	startedAt time.Time
	turns     int
}

// NewCollector builds a Collector and subscribes it to events. Call
// Unsubscribe (or just let the Events bus die with the run) to stop
// listening. startedAt is recorded here so Finish's DurationMS covers the
// whole run, not just the time since the last event.
func NewCollector(events *harness.Events) *Collector {
	c := &Collector{startedAt: time.Now()}
	c.unsubs = append(c.unsubs,
		events.On(harness.EventToolStart, c.onToolStart),
		events.On(harness.EventToolEnd, c.onToolEnd),
		events.On(harness.EventMessageUpdate, c.onMessageUpdate),
		events.On(harness.EventTurnEnd, c.onTurnEnd),
	)
	return c
}

// onTurnEnd counts one assistant turn per harness.EventTurnEnd, matching
// how internal/harness/turn.go's drive loop emits it: once per iteration,
// whether or not that turn made tool calls.
func (c *Collector) onTurnEnd(ev harness.Event) {
	c.turns++
}

// Unsubscribe detaches the Collector from the Events bus.
func (c *Collector) Unsubscribe() {
	for _, u := range c.unsubs {
		u()
	}
	c.unsubs = nil
}

// toolArg extracts args["command"] if it is a string, else args["path"],
// matching print.ts:63-65 exactly.
func toolArg(args map[string]any) *string {
	if s, ok := args["command"].(string); ok {
		return &s
	}
	if v, ok := args["path"]; ok {
		if s, ok := v.(string); ok {
			return &s
		}
	}
	return nil
}

func (c *Collector) onToolStart(ev harness.Event) {
	arg := toolArg(ev.ToolArgs)
	c.toolCalls = append(c.toolCalls, toolCall{Name: ev.ToolName, Arg: arg})
	if c.Stream != nil {
		c.Stream(toolStartEvent(ev.ToolName, arg))
	}
}

func (c *Collector) onToolEnd(ev harness.Event) {
	isError := ev.ToolResult != nil && ev.ToolResult.IsError
	if c.Stream != nil {
		c.Stream(toolEndEvent(ev.ToolName, isError))
	}
}

// onMessageUpdate reacts to the "done" sub-event of an assistant response
// stream, which is where the Go harness carries the final AssistantMessage
// (see the package doc for why this substitutes for message_end).
func (c *Collector) onMessageUpdate(ev harness.Event) {
	se := ev.StreamEvent
	if se == nil || se.Type != msg.EventDone || se.Message == nil {
		return
	}
	chunk := msg.TextOf(se.Message.Content)
	if chunk == "" {
		return
	}
	c.text.WriteString(chunk)
	c.text.WriteByte('\n')
	if c.Stream != nil {
		c.Stream(assistantEvent(chunk))
	}
}

// Finish closes out the run: it reads the block log (if wired), builds the
// final PrintResult (copying Usage/CostUSD from stats, and reason verbatim),
// emits the terminal "result" stream event, and returns the result. ok is
// the caller's run-status verdict (e.g. result.Status ==
// harness.StatusCompleted, overridden to false by callers like
// runPrintMode's --max-turns handling). stats is the harness's aggregate
// session totals as of run end (harness.Harness.Stats(), i.e.
// session.State.GetStats()). reason is copied straight into
// PrintResult.Reason; pass "" when there is nothing to report.
func (c *Collector) Finish(ok bool, stats session.SessionStats, reason string) PrintResult {
	var blocked []string
	if c.GetBlocked != nil {
		blocked = c.GetBlocked()
	}
	text := strings.TrimSpace(c.text.String())
	result := PrintResult{
		Text: text, ToolCalls: c.toolCalls, Blocked: blocked, OK: ok,
		Usage: stats.Usage, CostUSD: stats.Usage.Cost.Total,
		DurationMS:   time.Since(c.startedAt).Milliseconds(),
		Turns:        c.turns,
		NumToolCalls: len(c.toolCalls),
		Reason:       reason,
	}
	if c.Stream != nil {
		c.Stream(resultEvent(result))
	}
	return result
}

// printJSON is the shape formatted for `--output-format json`. Field order
// matches print.ts's JSON.stringify({ ok, text, toolCalls, blocked }, null, 2),
// extended (P1) with the same usage/cost/duration/turn fields the "result"
// stream event carries.
type printJSON struct {
	OK           bool       `json:"ok"`
	Text         string     `json:"text"`
	ToolCalls    []toolCall `json:"toolCalls"`
	Blocked      []string   `json:"blocked"`
	Usage        usageJSON  `json:"usage"`
	TotalCostUSD float64    `json:"total_cost_usd"`
	DurationMS   int64      `json:"duration_ms"`
	NumTurns     int        `json:"num_turns"`
	NumToolCalls int        `json:"num_tool_calls"`
	Reason       string     `json:"reason,omitempty"`
}

// FormatPrintResult renders r for the given output format. "stream-json"
// returns "" because everything was already streamed as it happened.
func FormatPrintResult(r PrintResult, format string) string {
	switch format {
	case "stream-json":
		return ""
	case "json":
		toolCalls := r.ToolCalls
		if toolCalls == nil {
			toolCalls = []toolCall{}
		}
		blocked := r.Blocked
		if blocked == nil {
			blocked = []string{}
		}
		b, err := json.MarshalIndent(printJSON{
			OK: r.OK, Text: r.Text, ToolCalls: toolCalls, Blocked: blocked,
			Usage: usageJSONOf(r.Usage), TotalCostUSD: r.CostUSD, DurationMS: r.DurationMS,
			NumTurns: r.Turns, NumToolCalls: r.NumToolCalls, Reason: r.Reason,
		}, "", "  ")
		if err != nil {
			return ""
		}
		return string(b)
	default:
		return strings.TrimSpace(r.Text)
	}
}

// WriteStreamEvent writes one StreamEvent as an NDJSON line to w. Wire this
// up as the Collector's Stream sink for `--output-format stream-json`.
func WriteStreamEvent(w io.Writer, ev StreamEvent) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	w.Write(b)
	w.Write([]byte("\n"))
}
