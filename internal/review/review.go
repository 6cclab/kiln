package review

import (
	"context"
	"encoding/json"
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

// Budgets for what the review is shown and may answer.
const (
	transcriptChars   = 48_000 // the conversation digest, newest kept
	skillsChars       = 32_000 // kiln skills shown in full; the rest by name
	userChars         = 2_000  // one user message
	assistantChars    = 1_200  // one assistant reply's text
	toolErrorChars    = 400    // one failed tool result
	maxAnswerTokens   = 8_192
	maxProposals      = 6
	DefaultRunTimeout = 5 * time.Minute
)

// Input is what one review reads.
type Input struct {
	Messages  []msg.Message
	SkillsDir string
	// Memory are the project's recent notes, so the review does not
	// propose one that already exists. Nil when memory is off, which also
	// stops it proposing memory notes.
	Memory []memory.Record
	// MemoryScope is the project's memory scope, stamped on memory
	// proposals.
	MemoryScope string
	Session     string
}

// Request builds the review's system prompt and its one user message.
func Request(in Input) (system, user string) {
	var b strings.Builder
	b.WriteString("<conversation>\n")
	b.WriteString(Digest(in.Messages, transcriptChars))
	b.WriteString("\n</conversation>\n\n<kiln_skills dir=\"" + in.SkillsDir + "\">\n")
	b.WriteString(skillsContext(in.SkillsDir, skillsChars))
	b.WriteString("\n</kiln_skills>\n")
	if in.Memory != nil {
		b.WriteString("\n<memory_notes>\n")
		for _, r := range in.Memory {
			fmt.Fprintf(&b, "- %s (%s): %s\n", r.Key, r.Kind, r.Text)
		}
		b.WriteString("</memory_notes>\n")
	}
	return systemPrompt(in.Memory != nil), b.String()
}

// Run sends one review and returns its proposals, validated and ready to
// queue. usage, when set, receives the call's usage whether or not the
// answer parses: the tokens are spent either way.
func Run(ctx context.Context, s Streamer, model provider.Model, in Input, usage func(msg.Usage)) ([]Proposal, error) {
	system, user := Request(in)
	maxTokens := maxAnswerTokens
	if model.MaxTokens > 0 && model.MaxTokens < maxTokens {
		maxTokens = model.MaxTokens
	}
	ch, wait := provider.Stream(ctx, s, model, []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text(user)}, Timestamp: time.Now().UnixMilli()},
	}, provider.StreamOptions{SystemPrompt: system, MaxTokens: maxTokens})
	for range ch {
	}
	am, err := wait()
	if am != nil && usage != nil {
		usage(am.Usage)
	}
	switch {
	case err != nil:
		return nil, err
	case am == nil:
		return nil, errors.New("no response")
	case am.StopReason != msg.StopStop:
		return nil, fmt.Errorf("answer did not finish (stop reason %q)", am.StopReason)
	}
	return Parse(msg.TextOf(am.Content), in)
}

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
// fence. A reply that is not that object is an error; a proposal that
// breaks a rule (a path outside its skill, a SKILL.md that would not load,
// a memory note with no evidence) is dropped and named in the error, the
// valid ones still returned.
func Parse(text string, in Input) ([]Proposal, error) {
	raw := strings.TrimSpace(text)
	if i := strings.Index(raw, "{"); i >= 0 {
		raw = raw[i:]
	}
	if i := strings.LastIndex(raw, "}"); i >= 0 {
		raw = raw[:i+1]
	}
	var a answer
	if err := json.Unmarshal([]byte(raw), &a); err != nil {
		return nil, fmt.Errorf("answer is not the expected JSON: %w", err)
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

// Digest renders a conversation for the review, newest kept within limit:
// what the person typed, the agent's replies, each tool call by name, and
// the text of failed tool results (where corrections usually start).
// Notes kiln added for the model (memory, hooks) are left out.
func Digest(messages []msg.Message, limit int) string {
	var lines []string
	for _, m := range messages {
		switch v := m.(type) {
		case msg.UserMessage:
			if v.KilnHook != "" {
				continue
			}
			if t := strings.TrimSpace(msg.TextOf(v.Content)); t != "" {
				lines = append(lines, "PERSON: "+clip(t, userChars))
			}
		case msg.AssistantMessage:
			if t := strings.TrimSpace(msg.TextOf(v.Content)); t != "" {
				lines = append(lines, "AGENT: "+clip(t, assistantChars))
			}
			for _, c := range v.Content {
				if call, ok := c.(msg.ToolCall); ok {
					lines = append(lines, "AGENT TOOL CALL: "+call.Name+" "+clip(argSummary(call.Arguments), 200))
				}
			}
		case msg.ToolResultMessage:
			if v.IsError {
				lines = append(lines, "TOOL FAILED ("+v.ToolName+"): "+clip(strings.TrimSpace(msg.TextOf(v.Content)), toolErrorChars))
			}
		}
	}
	total := 0
	start := len(lines)
	for start > 0 && total+len(lines[start-1])+1 <= limit {
		start--
		total += len(lines[start]) + 1
	}
	if start > 0 {
		return "[earlier conversation omitted]\n" + strings.Join(lines[start:], "\n")
	}
	return strings.Join(lines, "\n")
}

func argSummary(args map[string]any) string {
	for _, k := range []string{"command", "path", "file_path", "pattern", "query", "url", "name", "skill"} {
		if s, ok := args[k].(string); ok && s != "" {
			return strings.ReplaceAll(s, "\n", " ")
		}
	}
	return ""
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// skillsContext shows the review kiln's own skills: whole files within
// limit, the rest as name and description, so it can patch one in place.
func skillsContext(dir string, limit int) string {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return "(none yet)"
	}
	var b strings.Builder
	used := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		if used+len(data) <= limit {
			fmt.Fprintf(&b, "<skill name=%q>\n%s\n</skill>\n", e.Name(), data)
			used += len(data)
			continue
		}
		desc := ""
		if s, ok := parseDescription(string(data)); ok {
			desc = ": " + s
		}
		fmt.Fprintf(&b, "<skill name=%q truncated=\"true\">%s</skill>\n", e.Name(), desc)
	}
	return b.String()
}
