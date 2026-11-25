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
	Requests  int
	ToolCalls int
	// Captured counts write, edit and memory_write calls recorded as
	// proposals; Refused names the other non-read calls refused.
	Captured   int
	Refused    []string
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
// proposals, validated and ready to queue. The review proposes by calling
// write or edit on a file under the skills directory, or memory_write
// (captureCall records each as a proposal instead of running it), or in
// a JSON answer. It may call ReadOnlyTools through tools (nil refuses
// every call); other calls are refused. After maxToolRounds rounds of
// calls it stops with what it has. A final reply that proposes nothing
// and neither is JSON nor says there is nothing gets one request to fix
// it. usage, when set, receives every request's usage
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
	var got proposals
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
				if len(got) == 0 {
					return nil, st, fmt.Errorf("still calling tools after %d rounds", maxToolRounds)
				}
				return got.capped(&st)
			}
			messages = append(messages, *am)
			why := whyOf(am)
			for _, call := range calls {
				st.ToolCalls++
				text, isErr, captured := captureCall(call, in, seen, &got, why)
				if captured {
					if !isErr {
						st.Captured++
					}
				} else {
					text, isErr = runReviewTool(ctx, tools, call, in.SkillsDir, seen)
					if isErr && !ReadOnlyTools[call.Name] {
						st.Refused = append(st.Refused, call.Name)
					}
				}
				messages = append(messages, msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: call.ID, ToolName: call.Name,
					Content: msg.Blocks{msg.Text(text)}, IsError: isErr, Timestamp: time.Now().UnixMilli()})
			}
			continue
		}
		if am.StopReason != msg.StopStop {
			if len(got) > 0 {
				return got.capped(&st)
			}
			return nil, st, fmt.Errorf("answer did not finish (stop reason %q)", am.StopReason)
		}
		text := msg.TextOf(am.Content)
		parsed, err := Parse(text, in, seen)
		var notJSON *notJSONError
		if errors.As(err, &notJSON) {
			if len(got) > 0 || saysNothing(text) {
				got.explain(text)
				return got.capped(&st)
			}
			if !st.Repaired {
				// Small models claim they saved something without a
				// call, or break the JSON; one precise request fixes
				// most of these.
				st.Repaired = true
				messages = append(messages, *am, msg.UserMessage{Role: msg.RoleUser, Timestamp: time.Now().UnixMilli(), Content: msg.Blocks{msg.Text(
					"No proposal was recorded: that reply made no write, edit or memory_write call and is not the JSON object (" +
						notJSON.err.Error() + "). If there is something to keep, make those calls now. If not, reply NOTHING.")}})
				continue
			}
			return nil, st, err
		}
		for _, p := range parsed {
			got.add(p)
		}
		out, st, capErr := got.capped(&st)
		if err == nil {
			err = capErr
		}
		return out, st, err
	}
}

// capped is the proposals, at most maxProposals of them.
func (ps proposals) capped(st *Stats) ([]Proposal, Stats, error) {
	if len(ps) > maxProposals {
		return ps[:maxProposals], *st, fmt.Errorf("dropped %d proposal(s) over the limit of %d", len(ps)-maxProposals, maxProposals)
	}
	return ps, *st, nil
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
		return "The learning review cannot run " + call.Name + ". It may read (read, skill) and propose (write or edit a file under " +
			skillsDir + ", memory_write); nothing else. Do not continue the task.", true
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

// rawProposal is one proposal as the review states it, in its JSON answer
// or through a write, edit or memory_write call.
type rawProposal struct {
	Kind     string `json:"kind"`
	Why      string `json:"why"`
	Skill    string `json:"skill"`
	File     string `json:"file"`
	Content  string `json:"content"`
	Key      string `json:"key"`
	Type     string `json:"type"`
	Text     string `json:"text"`
	Evidence string `json:"evidence"`
}

// answer is the JSON the review may reply with.
type answer struct {
	Proposals []rawProposal `json:"proposals"`
}

// Parse reads the review's JSON answer: one object, optionally in a code
// fence or after a thinking block. A reply that is not that object is a
// *notJSONError; a proposal that breaks a rule (validate) is dropped and
// named in the error, the valid ones still returned. seen holds the skill
// files (relative to in.SkillsDir) the review saw whole.
func Parse(text string, in Input, seen map[string]bool) ([]Proposal, error) {
	a, err := decodeAnswer(text)
	if err != nil {
		return nil, &notJSONError{err}
	}
	var out []Proposal
	var dropped []string
	for _, r := range a.Proposals {
		p, bad := validate(r, in, seen)
		if bad != nil {
			dropped = append(dropped, p.Target()+": "+bad.Error())
			continue
		}
		out = append(out, p)
	}
	if len(dropped) > 0 {
		return out, fmt.Errorf("dropped %d proposal(s): %s", len(dropped), strings.Join(dropped, "; "))
	}
	return out, nil
}

// validate turns r into a Proposal, or says why it cannot be one: a path
// outside its skill, a SKILL.md that would not load, empty or unchanged
// content, a replacement for an existing file the review never saw whole,
// or a memory note without a key, text, evidence or known type.
func validate(r rawProposal, in Input, seen map[string]bool) (Proposal, error) {
	p := Proposal{Kind: r.Kind, Why: strings.TrimSpace(r.Why), Session: in.Session}
	switch r.Kind {
	case KindSkill:
		p.Skill, p.File, p.Content = strings.TrimSpace(r.Skill), strings.TrimSpace(r.File), r.Content
		if err := validSkillTarget(p.Skill, p.File); err != nil {
			return p, err
		}
		if p.File == "SKILL.md" {
			if err := validSkillFile(p.Skill, p.Content); err != nil {
				return p, err
			}
		}
		if strings.TrimSpace(p.Content) == "" {
			return p, errors.New("empty content")
		}
		p.Base = Hash(p.SkillPath(in.SkillsDir))
		if current, err := os.ReadFile(p.SkillPath(in.SkillsDir)); err == nil && string(current) == p.Content {
			return p, errors.New("no change")
		}
		if p.Base != "" && !seen[p.Skill+"/"+p.File] {
			// A whole-file replacement written blind drops what the
			// review never saw.
			return p, errors.New("replaces a file the review did not read in full; read it whole first")
		}
	case KindMemory:
		p.Scope, p.Key, p.Type = in.MemoryScope, strings.TrimSpace(r.Key), r.Type
		p.Text, p.Evidence = strings.TrimSpace(r.Text), strings.TrimSpace(r.Evidence)
		switch {
		case in.Memory == nil:
			return p, errors.New("memory is off")
		case p.Key == "" || p.Text == "" || p.Evidence == "":
			return p, errors.New("a memory note needs a key, text and evidence")
		case p.Type != "preference" && p.Type != "fact" && p.Type != "procedure" && p.Type != "observation":
			return p, fmt.Errorf("invalid memory kind %q", p.Type)
		}
	default:
		return p, fmt.Errorf("unknown kind %q", r.Kind)
	}
	return p, nil
}

// proposals collects a review's proposals in order; a later one for the
// same target replaces the earlier.
type proposals []Proposal

func (ps *proposals) add(p Proposal) {
	for i := range *ps {
		if (*ps)[i].Kind == p.Kind && (*ps)[i].Target() == p.Target() {
			(*ps)[i] = p
			return
		}
	}
	*ps = append(*ps, p)
}

// explain gives proposals made without a stated reason the review's
// closing summary as their reason.
func (ps proposals) explain(summary string) {
	line := strings.TrimSpace(whyOf(&msg.AssistantMessage{Content: msg.Blocks{msg.Text(summary)}}))
	if line == defaultWhy {
		return
	}
	for i := range ps {
		if ps[i].Why == defaultWhy {
			ps[i].Why = line
		}
	}
}

func (ps proposals) content(skill, file string) (string, bool) {
	for _, p := range ps {
		if p.Kind == KindSkill && p.Skill == skill && p.File == file {
			return p.Content, true
		}
	}
	return "", false
}

// captureCall turns a write, edit or memory_write call into a proposal
// instead of running it: agents reach for these tools to save what they
// learned, and refusing them is how hermes-agent's reviewer ended up in
// denial loops on weak models. Nothing is written; the result tells the
// model whether its proposal was recorded, or why not so it can fix it.
// ok is false for any other tool.
func captureCall(call msg.ToolCall, in Input, seen map[string]bool, got *proposals, why string) (text string, isErr, ok bool) {
	reject := func(err error) (string, bool, bool) {
		return "Not recorded: " + err.Error() + ".", true, true
	}
	var raw rawProposal
	switch call.Name {
	case "write", "edit":
		path, _ := call.Arguments["path"].(string)
		rel, inside := underDir(in.SkillsDir, path)
		skill, file, _ := strings.Cut(rel, "/")
		if !inside || file == "" {
			return reject(fmt.Errorf("the review only proposes files under %s/<skill>/; it does not change the project", in.SkillsDir))
		}
		content, _ := call.Arguments["content"].(string)
		if call.Name == "edit" {
			current, pending := got.content(skill, file)
			if !pending {
				data, err := os.ReadFile(filepath.Join(in.SkillsDir, skill, filepath.FromSlash(file)))
				if err != nil {
					return reject(errors.New("no such file; use write with the whole file"))
				}
				if !seen[skill+"/"+file] {
					return reject(errors.New("read the file whole before editing it"))
				}
				current = string(data)
			}
			var err error
			if content, err = applyEdits(current, call.Arguments["edits"]); err != nil {
				return reject(err)
			}
			seen[skill+"/"+file] = true
		}
		raw = rawProposal{Kind: KindSkill, Skill: skill, File: file, Content: content, Why: why}
	case "memory_write":
		if action, _ := call.Arguments["action"].(string); action != "remember" {
			return reject(errors.New("the review only proposes new notes (action remember)"))
		}
		str := func(k string) string { v, _ := call.Arguments[k].(string); return v }
		raw = rawProposal{Kind: KindMemory, Key: str("key"), Type: str("kind"), Text: str("text"), Evidence: str("evidence"), Why: why}
	default:
		return "", false, false
	}
	p, err := validate(raw, in, seen)
	if err != nil {
		return reject(err)
	}
	got.add(p)
	return "Recorded as a proposal for the person to approve; nothing was written. Do not repeat it in your reply.", false, true
}

// applyEdits applies edit-tool edits ({oldText, newText}, each oldText
// found exactly once) to content.
func applyEdits(content string, edits any) (string, error) {
	list, _ := edits.([]any)
	if len(list) == 0 {
		return "", errors.New("edits must list at least one {oldText, newText}")
	}
	for _, e := range list {
		m, _ := e.(map[string]any)
		oldText, _ := m["oldText"].(string)
		newText, _ := m["newText"].(string)
		switch n := strings.Count(content, oldText); {
		case oldText == "":
			return "", errors.New("an edit has an empty oldText")
		case n == 0:
			return "", fmt.Errorf("oldText %q is not in the file", clip(oldText, 80))
		case n > 1:
			return "", fmt.Errorf("oldText %q is in the file %d times; include more context", clip(oldText, 80), n)
		}
		content = strings.Replace(content, oldText, newText, 1)
	}
	return content, nil
}

// whyOf is the reason the model gave alongside its calls: the first line
// of the message's text.
func whyOf(am *msg.AssistantMessage) string {
	for _, line := range strings.Split(msg.TextOf(am.Content), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return clip(line, 200)
		}
	}
	return defaultWhy
}

// defaultWhy is a proposal's reason when the review stated none.
const defaultWhy = "proposed during the learning review"

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// saysNothing reports whether a reply means there is nothing to keep, as
// opposed to claiming it saved something it never proposed.
func saysNothing(text string) bool {
	t := strings.ToLower(text)
	if !strings.Contains(t, "nothing") && !strings.Contains(t, "no proposal") {
		return false
	}
	for _, claim := range []string{"saved", "updated", "wrote", "written", "created", "added", "recorded"} {
		if strings.Contains(t, claim) {
			return false
		}
	}
	return true
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
