package faux

import (
	"fmt"
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
// Usage may accompany Text or Thinking.
type Step struct {
	Text          string         `yaml:"text,omitempty"`
	Thinking      string         `yaml:"thinking,omitempty"`
	ToolCall      *ToolCallSpec  `yaml:"tool_call,omitempty"`
	ToolCalls     []ToolCallSpec `yaml:"tool_calls,omitempty"`
	OnToolResult  string         `yaml:"on_tool_result,omitempty"`
	OnToolResults []string       `yaml:"on_tool_results,omitempty"`
	Then          []Step         `yaml:"then,omitempty"`
	Usage         *UsageSpec     `yaml:"usage,omitempty"`
	Error         *ErrorSpec     `yaml:"error,omitempty"`
	Delay         string         `yaml:"delay,omitempty"`
}

// ToolCallSpec describes a scripted tool call.
type ToolCallSpec struct {
	Name string         `yaml:"name"`
	Args map[string]any `yaml:"args"`
	ID   string         `yaml:"id"`
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
	text     *string
	thinking *string
	toolCall *ToolCallSpec
	usage    *UsageSpec
	delay    time.Duration
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
				pending = append(pending, contentStep{toolCall: &s.ToolCalls[i]})
			}
			flushPending(nil)

		default:
			cs, err := toContentStep(s)
			if err != nil {
				return nil, err
			}
			pending = append(pending, cs)
			if s.ToolCall != nil {
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
