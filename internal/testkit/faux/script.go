package faux

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Script is the top-level YAML document loaded by the faux server. Either
// the single-model form (Model + Steps) or the multi-model form (Models) is
// used; see modelScripts.
type Script struct {
	Model  string            `yaml:"model,omitempty"`
	Steps  []Step            `yaml:"steps,omitempty"`
	Models map[string][]Step `yaml:"models,omitempty"`
}

// modelScripts returns the per-model step lists a Script represents,
// normalizing the single-model form (Model/Steps) into a one-entry map
// keyed by the (possibly defaulted) model name. When Models is set, it is
// returned as-is and Model/Steps are ignored.
func (s *Script) modelScripts() map[string][]Step {
	if len(s.Models) > 0 {
		return s.Models
	}
	model := s.Model
	if model == "" {
		model = "faux-1"
	}
	return map[string][]Step{model: s.Steps}
}

// Step is one entry in a script's steps list. Exactly one of its
// "kind" fields (Text, Thinking, ToolCall, ToolCalls, OnToolResult,
// OnToolResults, Error, Delay) is expected to be set per YAML node, though
// Usage may accompany Text or Thinking, and DisconnectAfter may accompany
// Text or Thinking.
type Step struct {
	Text            string          `yaml:"text,omitempty"`
	Thinking        string          `yaml:"thinking,omitempty"`
	ToolCall        *ToolCallSpec   `yaml:"tool_call,omitempty"`
	ToolCalls       []ToolCallSpec  `yaml:"tool_calls,omitempty"`
	OnToolResult    string          `yaml:"on_tool_result,omitempty"`
	OnToolResults   []string        `yaml:"on_tool_results,omitempty"`
	Then            []Step          `yaml:"then,omitempty"`
	Usage           *UsageSpec      `yaml:"usage,omitempty"`
	Error           *ErrorSpec      `yaml:"error,omitempty"`
	Delay           string          `yaml:"delay,omitempty"`
	DisconnectAfter *disconnectSpec `yaml:"disconnect_after,omitempty"`
	// EndTurn closes the current turn after this step, so the next text
	// step answers the following request instead of merging into this one.
	EndTurn bool `yaml:"end_turn,omitempty"`
	// ChunkDelay sleeps between the streamed chunks of this step's text,
	// so a live, still-arriving reply stays on screen long enough to see.
	ChunkDelay string `yaml:"chunk_delay,omitempty"`
	// RawBlocks emits each object verbatim as its own Anthropic content
	// block (content_block_start/[input_json_delta]/content_block_stop),
	// for scripting provider-native blocks this package has no first-class
	// step for (server_tool_use, web_search_tool_result). A
	// "server_tool_use"-typed block streams its "input" field via
	// input_json_delta chunks, as the real API does; every other type is
	// sent complete in content_block_start, with no deltas. Anthropic-only
	// (the OpenAI handler ignores this field); always ends its own turn,
	// like a tool_call.
	RawBlocks []map[string]any `yaml:"raw_blocks,omitempty"`
	// StopReason overrides this turn's stop_reason (e.g. "pause_turn"),
	// in place of the normal end_turn/tool_use inference. Anthropic-only.
	StopReason string `yaml:"stop_reason,omitempty"`
}

// ToolCallSpec describes a scripted tool call. Args and RawArgs are
// mutually exclusive: Args is marshaled normally as the tool's input,
// while RawArgs is spliced into the response verbatim (see raw_args in
// the package doc) to let a script emit intentionally invalid tool-call
// JSON for fault-injection tests.
type ToolCallSpec struct {
	Name    string         `yaml:"name"`
	Args    map[string]any `yaml:"args"`
	RawArgs string         `yaml:"raw_args,omitempty"`
	ID      string         `yaml:"id"`
}

// validate checks that a ToolCallSpec's fields are internally consistent.
func (spec ToolCallSpec) validate() error {
	if spec.Args != nil && spec.RawArgs != "" {
		return fmt.Errorf("faux: tool call %q: args and raw_args are mutually exclusive", spec.Name)
	}
	return nil
}

// disconnectSpec describes a disconnect_after fault: a point, expressed
// either as a number of response body bytes or as a duration since the
// response began, at which the server closes the connection instead of
// completing the response. Exactly one of Bytes or Duration is set,
// depending on which YAML form was used.
type disconnectSpec struct {
	Bytes    int
	Duration time.Duration
}

// UnmarshalYAML accepts either an integer (a byte count, e.g.
// "disconnect_after: 20") or a duration string (e.g.
// "disconnect_after: 200ms").
func (d *disconnectSpec) UnmarshalYAML(value *yaml.Node) error {
	var n int
	if err := value.Decode(&n); err == nil {
		d.Bytes = n
		return nil
	}
	var s string
	if err := value.Decode(&s); err != nil {
		return fmt.Errorf("faux: invalid disconnect_after: %w", err)
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("faux: invalid disconnect_after %q: %w", s, err)
	}
	d.Duration = dur
	return nil
}

// UsageSpec describes token usage reported for a turn.
type UsageSpec struct {
	Input  int `yaml:"input"`
	Output int `yaml:"output"`
}

// ErrorSpec describes a scripted error response.
type ErrorSpec struct {
	Status  int    `yaml:"status"`
	Type    string `yaml:"type"`
	Message string `yaml:"message"`
}

// contentStep is a normalized, flattened piece of turn content.
type contentStep struct {
	text            *string
	thinking        *string
	toolCall        *ToolCallSpec
	usage           *UsageSpec
	delay           time.Duration
	chunkDelay      time.Duration
	disconnectAfter *disconnectSpec
	// rawBlock is one object from a raw_blocks step, sent verbatim as an
	// Anthropic content block. See Step.RawBlocks.
	rawBlock map[string]any
	// stopReasonOverride, when set on any content step of a turn, replaces
	// that turn's inferred stop_reason. See Step.StopReason and turn's
	// stopReasonOverride method.
	stopReasonOverride *string
}

// turn is one flattened unit of script execution: either a scripted
// error, or a scripted response built from content steps, optionally
// gated on one or more tool result ids from the request that triggers it.
type turn struct {
	isError            bool
	errSpec            ErrorSpec
	requireToolResults []string
	content            []contentStep
}

// parseScriptYAML parses a YAML document into a Script.
func parseScriptYAML(doc string) (*Script, error) {
	var s Script
	if err := yaml.Unmarshal([]byte(doc), &s); err != nil {
		return nil, fmt.Errorf("faux: parse script: %w", err)
	}
	if len(s.Models) == 0 && s.Model == "" {
		s.Model = "faux-1"
	}
	return &s, nil
}

// loadScriptFile reads and parses a YAML script from disk.
func loadScriptFile(path string) (*Script, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("faux: read script %s: %w", path, err)
	}
	s, err := parseScriptYAML(string(data))
	if err != nil {
		return nil, fmt.Errorf("faux: %s: %w", path, err)
	}
	return s, nil
}

// flattenSteps converts a nested step list into a flat sequence of turns.
func flattenSteps(steps []Step) ([]turn, error) {
	var turns []turn
	var pending []contentStep

	flushPending := func(requireIDs []string) {
		if len(pending) > 0 || len(requireIDs) > 0 {
			turns = append(turns, turn{requireToolResults: requireIDs, content: pending})
			pending = nil
		}
	}

	for _, s := range steps {
		switch {
		case s.Error != nil:
			flushPending(nil)
			turns = append(turns, turn{isError: true, errSpec: *s.Error})

		case s.OnToolResult != "" || len(s.OnToolResults) > 0:
			flushPending(nil)
			ids := onToolResultIDs(s)
			inner, err := flattenSteps(s.Then)
			if err != nil {
				return nil, err
			}
			if len(inner) == 0 {
				turns = append(turns, turn{requireToolResults: ids})
				continue
			}
			inner[0].requireToolResults = ids
			turns = append(turns, inner...)

		case len(s.ToolCalls) > 0:
			for i := range s.ToolCalls {
				if err := s.ToolCalls[i].validate(); err != nil {
					return nil, err
				}
				pending = append(pending, contentStep{toolCall: &s.ToolCalls[i]})
			}
			flushPending(nil)

		case len(s.RawBlocks) > 0:
			// Unlike a tool_call, a raw_blocks step does not itself force a
			// turn boundary: a server tool (e.g. web_search) resolves
			// without a client round-trip, so Anthropic can keep writing
			// the same assistant message -- text, a search, more text --
			// all in one turn. It merges with surrounding text/thinking
			// steps exactly like they merge with each other, and only ends
			// its turn when explicitly told to (end_turn/stop_reason), same
			// as a text step.
			for i, rb := range s.RawBlocks {
				cs := contentStep{rawBlock: rb}
				if i == len(s.RawBlocks)-1 && s.StopReason != "" {
					sr := s.StopReason
					cs.stopReasonOverride = &sr
				}
				pending = append(pending, cs)
			}
			if s.EndTurn || s.StopReason != "" {
				flushPending(nil)
			}

		default:
			cs, err := toContentStep(s)
			if err != nil {
				return nil, err
			}
			pending = append(pending, cs)
			if s.ToolCall != nil || s.DisconnectAfter != nil || s.EndTurn || s.StopReason != "" {
				// A disconnect_after step, like a tool_call, always ends
				// its own turn: it's a one-shot fault against a single
				// request, and the cursor must move on to the next step
				// so a client's retry lands on it instead of repeating
				// the same cut (see the disconnect_after doc section).
				flushPending(nil)
			}
		}
	}
	flushPending(nil)
	return turns, nil
}

// onToolResultIDs collects a step's required tool result ids from its
// singular (OnToolResult) and plural (OnToolResults) forms, in that order.
func onToolResultIDs(s Step) []string {
	var ids []string
	if s.OnToolResult != "" {
		ids = append(ids, s.OnToolResult)
	}
	ids = append(ids, s.OnToolResults...)
	return ids
}

func toContentStep(s Step) (contentStep, error) {
	var cs contentStep
	if s.Text != "" {
		t := s.Text
		cs.text = &t
	}
	if s.Thinking != "" {
		t := s.Thinking
		cs.thinking = &t
	}
	if s.ToolCall != nil {
		if err := s.ToolCall.validate(); err != nil {
			return cs, err
		}
		cs.toolCall = s.ToolCall
	}
	if s.Usage != nil {
		cs.usage = s.Usage
	}
	if s.Delay != "" {
		d, err := time.ParseDuration(s.Delay)
		if err != nil {
			return cs, fmt.Errorf("faux: invalid delay %q: %w", s.Delay, err)
		}
		cs.delay = d
	}
	if s.ChunkDelay != "" {
		d, err := time.ParseDuration(s.ChunkDelay)
		if err != nil {
			return cs, fmt.Errorf("faux: invalid chunk_delay %q: %w", s.ChunkDelay, err)
		}
		cs.chunkDelay = d
	}
	if s.DisconnectAfter != nil {
		cs.disconnectAfter = s.DisconnectAfter
	}
	if s.StopReason != "" {
		sr := s.StopReason
		cs.stopReasonOverride = &sr
	}
	return cs, nil
}

// hasToolCall reports whether a turn ends with a scripted tool call.
func (t turn) hasToolCall() bool {
	for _, c := range t.content {
		if c.toolCall != nil {
			return true
		}
	}
	return false
}

// lastUsage returns the last scripted usage in the turn, if any.
func (t turn) lastUsage() *UsageSpec {
	var u *UsageSpec
	for _, c := range t.content {
		if c.usage != nil {
			u = c.usage
		}
	}
	return u
}

// totalDelay sums every delay step in the turn.
func (t turn) totalDelay() time.Duration {
	var d time.Duration
	for _, c := range t.content {
		d += c.delay
	}
	return d
}

// disconnectSpec returns the turn's scripted disconnect fault, if any (the
// last one wins, mirroring lastUsage).
func (t turn) disconnectSpec() *disconnectSpec {
	var d *disconnectSpec
	for _, c := range t.content {
		if c.disconnectAfter != nil {
			d = c.disconnectAfter
		}
	}
	return d
}

// stopReasonOverride returns the turn's scripted stop_reason override, if
// any (the last one wins, mirroring lastUsage), empty otherwise.
func (t turn) stopReasonOverride() string {
	var s string
	for _, c := range t.content {
		if c.stopReasonOverride != nil {
			s = *c.stopReasonOverride
		}
	}
	return s
}

// sleepOrGone waits d before a scripted response, returning false early if
// the client gives up first. A long delay stands for a model that never
// answers; without this its handler outlived the request the client had
// already cancelled, and held the server open at test cleanup.
func sleepOrGone(r *http.Request, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-r.Context().Done():
		return false
	}
}
