package review

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/memory"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// Streamer is the model-calling surface a review needs; every
// provider.Provider satisfies it.
type Streamer interface {
	Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error))
}

// Budgets for what the review is shown and may do.
const (
	skillsChars       = 32_000 // kiln skills shown in full; the rest by name
	maxAnswerTokens   = 8_192
	maxProposals      = 6
	maxToolRounds     = 6 // rounds of read-only tool calls before the answer
	DefaultRunTimeout = 5 * time.Minute
)

// Conversation is what the review continues: the session's last request
// and its answer, exactly as sent (harness.SideRequest), so the provider's
// prompt cache serves the whole conversation and only the review's own
// instruction is new. The tools stay declared as they were, for the same
// reason; ToolRunner decides which calls run.
type Conversation struct {
	Streamer Streamer
	Model    provider.Model
	Messages []msg.Message
	Options  provider.StreamOptions
}

// ToolRunner runs a read-only tool call the review makes (ReadOnlyTools)
// and returns its result text, isError set when it failed or was refused.
type ToolRunner func(ctx context.Context, call msg.ToolCall) (text string, isError bool)

// ReadOnlyTools are the tools the review may call: it reads files and
// loads skills to check what it proposes. Every other call is refused
// with a reminder that the review only proposes.
var ReadOnlyTools = map[string]bool{"read": true, "skill": true}

// Input is what one review knows besides the conversation.
type Input struct {
	SkillsDir string
	// Memory are the project's recent notes, so the review does not
	// propose one that already exists. Nil when memory is off, which also
	// stops it proposing memory notes.
	Memory []memory.Record
	// MemoryScope is the project's memory scope, stamped on memory
	// proposals.
	MemoryScope string
	Session     string
	// Usage, when set, counts each kiln skill's use and age, so the
	// review sees which skills earn their place (usage.go).
	Usage map[string]SkillUse
}

// Stats is what a review cost and did, for the run log.
type Stats struct {
	Requests   int
	ToolCalls  int
	Refused    int
	Repaired   bool
	CacheRead  int // prompt tokens the provider served from its cache
	InputTotal int // all prompt tokens, cached or not
}

// Instruction is the review's request: its rules, kiln's skills and the
// project's memory notes, sent as one user message after the
// conversation. It also returns the skill files it showed in full: the
// review may replace those without reading them first.
func Instruction(in Input) (text string, shown map[string]bool) {
	var b strings.Builder
	b.WriteString(reviewRules(in.Memory != nil))
	b.WriteString("\n\n<kiln_skills dir=\"" + in.SkillsDir + "\">\n")
	skills, shown := skillsContext(in.SkillsDir, skillsChars, in.Usage)
	b.WriteString(skills)
	b.WriteString("\n</kiln_skills>\n")
	if in.Memory != nil {
		b.WriteString("\n<memory_notes>\n")
		for _, r := range in.Memory {
			fmt.Fprintf(&b, "- %s (%s): %s\n", r.Key, r.Kind, r.Text)
		}
		b.WriteString("</memory_notes>\n")
	}
	return b.String(), shown
}

// Run continues conv with the review's instruction and returns its
// proposals, validated and ready to queue. The review may call
// ReadOnlyTools through tools (nil refuses every call) for up to
// maxToolRounds rounds; an answer that is not the expected JSON gets one
// request to fix it. usage, when set, receives every request's usage
// whether or not the answer parses: the tokens are spent either way.
func Run(ctx context.Context, conv Conversation, in Input, tools ToolRunner, usage func(msg.Usage)) ([]Proposal, Stats, error) {
	instruction, seen := Instruction(in)
	messages := append(append([]msg.Message(nil), conv.Messages...),
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text(instruction)}, Timestamp: time.Now().UnixMilli()})
	opts := conv.Options
	// Cap the answer, unless the session thinks: the thinking budget is
	// clamped under max_tokens, and a different budget invalidates
	// Anthropic's cached messages, so it stays as the session sent it.
	if opts.ThinkingLevel == "" || opts.ThinkingLevel == provider.ThinkingOff {
		opts.MaxTokens = maxAnswerTokens
		if conv.Model.MaxTokens > 0 && conv.Model.MaxTokens < opts.MaxTokens {
			opts.MaxTokens = conv.Model.MaxTokens
		}
	}
	var st Stats
	for round := 0; ; round++ {
		ch, wait := provider.Stream(ctx, conv.Streamer, conv.Model, messages, opts)
		for range ch {
		}
		am, err := wait()
		st.Requests++
		if am != nil {
			st.CacheRead += am.Usage.CacheRead
			st.InputTotal += am.Usage.Input + am.Usage.CacheRead + am.Usage.CacheWrite
			if usage != nil {
				usage(am.Usage)
			}
		}
		switch {
		case err != nil:
			return nil, st, err
		case am == nil:
			return nil, st, errors.New("no response")
		}
		calls := toolCalls(am)
		if len(calls) > 0 {
			if round >= maxToolRounds {
				return nil, st, fmt.Errorf("still calling tools after %d rounds", maxToolRounds)
			}
			messages = append(messages, *am)
			for _, call := range calls {
				st.ToolCalls++
				text, isErr := runReviewTool(ctx, tools, call, in.SkillsDir, seen)
				if isErr {
					st.Refused++
				}
				messages = append(messages, msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: call.ID, ToolName: call.Name,
					Content: msg.Blocks{msg.Text(text)}, IsError: isErr, Timestamp: time.Now().UnixMilli()})
			}
			continue
		}
		if am.StopReason != msg.StopStop {
			return nil, st, fmt.Errorf("answer did not finish (stop reason %q)", am.StopReason)
		}
		proposals, err := Parse(msg.TextOf(am.Content), in, seen)
		var notJSON *notJSONError
		if errors.As(err, &notJSON) && !st.Repaired {
			// Small models wrap the object in prose or break it; one
			// precise request fixes most of these.
			st.Repaired = true
			messages = append(messages, *am, msg.UserMessage{Role: msg.RoleUser, Timestamp: time.Now().UnixMilli(), Content: msg.Blocks{msg.Text(
				"That reply was not the JSON object asked for (" + notJSON.err.Error() + "). Reply again with only the JSON object, " +
					`starting with {"proposals": and nothing before or after it.`)}})
			continue
		}
		return proposals, st, err
	}
}

func toolCalls(am *msg.AssistantMessage) []msg.ToolCall {
	var out []msg.ToolCall
	for _, c := range am.Content {
		if call, ok := c.(msg.ToolCall); ok {
			out = append(out, call)
		}
	}
	return out
}

// runReviewTool runs one of the review's tool calls: a read-only one
// through tools, anything else refused. A whole-file read of a kiln skill
// file marks it seen, so the review may replace it.
func runReviewTool(ctx context.Context, tools ToolRunner, call msg.ToolCall, skillsDir string, seen map[string]bool) (string, bool) {
	if !ReadOnlyTools[call.Name] || tools == nil {
		return "The learning review only reads (read, skill) and proposes; it cannot run " + call.Name +
			". Do not continue the task. Put each change you want in your JSON answer instead.", true
	}
	text, isErr := tools(ctx, call)
	if call.Name == "read" && !isErr {
		_, partial := call.Arguments["offset"]
		if _, limited := call.Arguments["limit"]; limited {
			partial = true
		}
		if path, ok := call.Arguments["path"].(string); ok && !partial {
			if rel, ok := underDir(skillsDir, path); ok {
				seen[rel] = true
			}
		}
	}
	return text, isErr
}

// underDir is path relative to dir with forward slashes, when path is
// inside dir.
func underDir(dir, path string) (string, bool) {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if !filepath.IsAbs(path) {
		return "", false
	}
	rel, err := filepath.Rel(dir, filepath.Clean(path))
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// notJSONError is Parse's error for an answer that is not the JSON object.
type notJSONError struct{ err error }

func (e *notJSONError) Error() string { return "answer is not the expected JSON: " + e.err.Error() }

// answer is the JSON the review replies with.
type answer struct {
	Proposals []struct {
		Kind     string `json:"kind"`
		Why      string `json:"why"`
		Skill    string `json:"skill"`
		File     string `json:"file"`
		Content  string `json:"content"`
		Key      string `json:"key"`
		Type     string `json:"type"`
		Text     string `json:"text"`
		Evidence string `json:"evidence"`
	} `json:"proposals"`
}

// Parse reads the review's answer: one JSON object, optionally in a code
// fence or after a thinking block. A reply that is not that object is a
// *notJSONError; a proposal that breaks a rule (a path outside its skill,
// a SKILL.md that would not load, a memory note with no evidence, a
// replacement for an existing file the review never saw whole) is dropped
// and named in the error, the valid ones still returned. seen holds the
// skill files (relative to in.SkillsDir) the review saw whole.
func Parse(text string, in Input, seen map[string]bool) ([]Proposal, error) {
	a, err := decodeAnswer(text)
	if err != nil {
		return nil, &notJSONError{err}
	}
	var out []Proposal
	var dropped []string
	for _, r := range a.Proposals {
		p := Proposal{Kind: r.Kind, Why: strings.TrimSpace(r.Why), Session: in.Session}
		var bad error
		switch r.Kind {
		case KindSkill:
			p.Skill, p.File, p.Content = strings.TrimSpace(r.Skill), strings.TrimSpace(r.File), r.Content
			if bad = validSkillTarget(p.Skill, p.File); bad == nil && p.File == "SKILL.md" {
				bad = validSkillFile(p.Skill, p.Content)
			}
			if bad == nil && strings.TrimSpace(p.Content) == "" {
				bad = errors.New("empty content")
			}
			if bad == nil {
				p.Base = Hash(p.SkillPath(in.SkillsDir))
				if current, err := os.ReadFile(p.SkillPath(in.SkillsDir)); err == nil && string(current) == p.Content {
					bad = errors.New("no change")
				} else if p.Base != "" && !seen[p.Skill+"/"+p.File] {
					// A whole-file replacement written blind drops what
					// the review never saw.
					bad = errors.New("replaces a file the review did not read in full")
				}
			}
		case KindMemory:
			p.Scope, p.Key, p.Type = in.MemoryScope, strings.TrimSpace(r.Key), r.Type
			p.Text, p.Evidence = strings.TrimSpace(r.Text), strings.TrimSpace(r.Evidence)
			switch {
			case in.Memory == nil:
				bad = errors.New("memory is off")
			case p.Key == "" || p.Text == "" || p.Evidence == "":
				bad = errors.New("memory note needs a key, text and evidence")
			case p.Type != "preference" && p.Type != "fact" && p.Type != "procedure" && p.Type != "observation":
				bad = fmt.Errorf("invalid memory type %q", p.Type)
			}
		default:
			bad = fmt.Errorf("unknown kind %q", r.Kind)
		}
		if bad != nil {
			dropped = append(dropped, p.Target()+": "+bad.Error())
			continue
		}
		if len(out) == maxProposals {
			dropped = append(dropped, p.Target()+": over the limit of "+fmt.Sprint(maxProposals))
			continue
		}
		out = append(out, p)
	}
	if len(dropped) > 0 {
		return out, fmt.Errorf("dropped %d proposal(s): %s", len(dropped), strings.Join(dropped, "; "))
	}
	return out, nil
}

// skillsContext shows the review kiln's own skills: whole files within
// limit, the rest as name and description, each with its use counts when
// usage is known. It returns the SKILL.md files it showed whole, relative
// to dir.
func skillsContext(dir string, limit int, usage map[string]SkillUse) (string, map[string]bool) {
	shown := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return "(none yet)", shown
	}
	var b strings.Builder
	used := 0
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		attrs := ""
		if u, ok := usage[e.Name()]; ok {
			attrs = u.attrs()
		}
		if used+len(data) <= limit {
			fmt.Fprintf(&b, "<skill name=%q%s>\n%s\n</skill>\n", e.Name(), attrs, data)
			used += len(data)
			shown[e.Name()+"/SKILL.md"] = true
			continue
		}
		desc := ""
		if s, ok := parseDescription(string(data)); ok {
			desc = ": " + s
		}
		fmt.Fprintf(&b, "<skill name=%q%s truncated=\"true\">%s</skill>\n", e.Name(), attrs, desc)
	}
	return b.String(), shown
}
