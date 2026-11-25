package review

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// scripted answers each request with the next reply and records what it
// was sent.
type scripted struct {
	replies []*msg.AssistantMessage
	sent    [][]msg.Message
	opts    []provider.StreamOptions
}

func (s *scripted) Stream(_ context.Context, _ provider.Model, transcript []msg.Message, opts provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	s.sent = append(s.sent, append([]msg.Message(nil), transcript...))
	s.opts = append(s.opts, opts)
	reply := s.replies[0]
	s.replies = s.replies[1:]
	ch := make(chan msg.StreamEvent)
	close(ch)
	return ch, func() (*msg.AssistantMessage, error) { return reply, nil }
}

func text(t string) *msg.AssistantMessage {
	return &msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{msg.Text(t)}, StopReason: msg.StopStop, Usage: msg.Usage{Input: 10, CacheRead: 900, Output: 20}}
}

func calls(cs ...msg.ToolCall) *msg.AssistantMessage {
	blocks := msg.Blocks{}
	for _, c := range cs {
		blocks = append(blocks, c)
	}
	return &msg.AssistantMessage{Role: msg.RoleAssistant, Content: blocks, StopReason: msg.StopToolUse, Usage: msg.Usage{Input: 10, CacheRead: 900}}
}

func conversation(s *scripted) Conversation {
	return Conversation{
		Streamer: s,
		Messages: []msg.Message{
			msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("always run make check first")}},
			msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{msg.Text("Noted.")}, StopReason: msg.StopStop},
		},
		Options: provider.StreamOptions{SystemPrompt: "kiln's own system prompt", Tools: []provider.ToolDef{{Name: "read"}, {Name: "write"}}},
	}
}

const memoryAnswer = `{"proposals":[{"kind":"memory","key":"check-first","type":"preference","text":"Run make check first.","evidence":"The person said always.","why":"stated"}]}`

// TestRunContinuesTheConversation: the review is the session's last
// request with one message added (same system prompt, same tools, the
// conversation untouched), so a prompt cache serves the prefix; usage is
// reported for every request.
func TestRunContinuesTheConversation(t *testing.T) {
	in := input(t, true)
	writeSkill(t, in.SkillsDir, "release-flow", goodSkill)
	s := &scripted{replies: []*msg.AssistantMessage{text(memoryAnswer)}}
	conv := conversation(s)
	var spent msg.Usage
	got, st, err := Run(context.Background(), conv, in, nil, func(u msg.Usage) { spent.CacheRead += u.CacheRead })
	if err != nil || len(got) != 1 || got[0].Key != "check-first" {
		t.Fatalf("Run = %+v, %v", got, err)
	}
	if spent.CacheRead != 900 || st.CacheRead != 900 || st.Requests != 1 {
		t.Errorf("usage %+v, stats %+v", spent, st)
	}
	sent := s.sent[0]
	if len(sent) != 3 || msg.TextOf(sent[0].(msg.UserMessage).Content) != "always run make check first" {
		t.Fatalf("sent %d messages, want the conversation plus the instruction", len(sent))
	}
	instruction := msg.TextOf(sent[2].(msg.UserMessage).Content)
	for _, want := range []string{"<kiln_learning_review>", "Do not continue", "Never infer the person's", `"kind": "memory"`, "Tag and publish a kiln release."} {
		if !strings.Contains(instruction, want) {
			t.Errorf("instruction lacks %q", want)
		}
	}
	if o := s.opts[0]; o.SystemPrompt != "kiln's own system prompt" || len(o.Tools) != 2 || o.MaxTokens == 0 {
		t.Errorf("options changed: %+v", o)
	}
	if len(conv.Messages) != 2 {
		t.Error("Run changed the caller's conversation")
	}

	// A thinking session keeps its own max_tokens: the thinking budget
	// is clamped under it, and changing the budget costs the cache.
	s = &scripted{replies: []*msg.AssistantMessage{text(memoryAnswer)}}
	conv = conversation(s)
	conv.Options.ThinkingLevel = provider.ThinkingHigh
	if _, _, err := Run(context.Background(), conv, in, nil, nil); err != nil {
		t.Fatal(err)
	}
	if s.opts[0].MaxTokens != 0 {
		t.Errorf("thinking review max_tokens = %d, want the session's (unset)", s.opts[0].MaxTokens)
	}
}

// TestRunReadsAndRefusesWrites: a read runs and its result goes back to
// the model; a write is refused without running; a whole read of a
// skill file lets the review replace it, while a replacement for a file
// it never read is dropped.
func TestRunReadsAndRefusesWrites(t *testing.T) {
	in := input(t, false)
	ref := filepath.Join(in.SkillsDir, "release-flow", "references", "tags.md")
	writeSkill(t, in.SkillsDir, "release-flow", goodSkill)
	if err := os.MkdirAll(filepath.Dir(ref), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{ref, filepath.Join(filepath.Dir(ref), "unread.md")} {
		if err := os.WriteFile(f, []byte("old\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	answer := `{"proposals":[` +
		`{"kind":"skill","skill":"release-flow","file":"references/tags.md","content":"new\n","why":"read first"},` +
		`{"kind":"skill","skill":"release-flow","file":"references/unread.md","content":"new\n","why":"blind"}]}`
	s := &scripted{replies: []*msg.AssistantMessage{
		calls(msg.NewToolCall("c1", "read", map[string]any{"path": ref}), msg.NewToolCall("c2", "write", map[string]any{"path": ref, "content": "x"})),
		text(answer),
	}}
	var ran []string
	tools := func(_ context.Context, call msg.ToolCall) (string, bool) {
		ran = append(ran, call.Name)
		return "old\n", false
	}
	got, st, err := Run(context.Background(), conversation(s), in, tools, nil)
	if len(got) != 1 || got[0].File != "references/tags.md" {
		t.Fatalf("kept %+v, want only the file it read", got)
	}
	if err == nil || !strings.Contains(err.Error(), "did not read in full") {
		t.Errorf("err = %v, want the blind replacement named", err)
	}
	if strings.Join(ran, ",") != "read" || st.ToolCalls != 2 || st.Refused != 1 {
		t.Errorf("ran %v, stats %+v; want only the read run", ran, st)
	}
	results := s.sent[1][len(s.sent[1])-2:]
	if r := results[1].(msg.ToolResultMessage); !r.IsError || !strings.Contains(msg.TextOf(r.Content), "only reads") {
		t.Errorf("write result = %+v, want a refusal", r)
	}
	if _, err := os.Stat(ref); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(ref); string(data) != "old\n" {
		t.Error("the refused write ran")
	}
}

// TestRunRepairsAnAnswerThatIsNotJSON: prose instead of the object gets
// one request to fix it; prose twice is an error.
func TestRunRepairsAnAnswerThatIsNotJSON(t *testing.T) {
	in := input(t, true)
	s := &scripted{replies: []*msg.AssistantMessage{text("I saved the preference."), text("Sure:\n```json\n" + memoryAnswer + "\n```")}}
	got, st, err := Run(context.Background(), conversation(s), in, nil, nil)
	if err != nil || len(got) != 1 || !st.Repaired || st.Requests != 2 {
		t.Fatalf("Run = %+v, %+v, %v", got, st, err)
	}
	last := s.sent[1][len(s.sent[1])-1].(msg.UserMessage)
	if !strings.Contains(msg.TextOf(last.Content), "not the JSON object") {
		t.Errorf("repair request = %q", msg.TextOf(last.Content))
	}
	s = &scripted{replies: []*msg.AssistantMessage{text("no"), text("still no")}}
	if _, _, err := Run(context.Background(), conversation(s), in, nil, nil); err == nil {
		t.Error("prose twice was accepted")
	}
	s = &scripted{replies: []*msg.AssistantMessage{{Content: msg.Blocks{msg.Text("{")}, StopReason: msg.StopLength}}}
	if _, _, err := Run(context.Background(), conversation(s), in, nil, nil); err == nil || !strings.Contains(err.Error(), "did not finish") {
		t.Errorf("cut-off answer: err = %v", err)
	}
}

// TestRunStopsAModelThatOnlyCallsTools: a review that keeps calling tools
// ends after maxToolRounds rounds instead of looping.
func TestRunStopsAModelThatOnlyCallsTools(t *testing.T) {
	var replies []*msg.AssistantMessage
	for i := 0; i <= maxToolRounds; i++ {
		replies = append(replies, calls(msg.NewToolCall("c", "read", map[string]any{"path": "/x"})))
	}
	s := &scripted{replies: replies}
	_, st, err := Run(context.Background(), conversation(s), input(t, false), func(context.Context, msg.ToolCall) (string, bool) { return "x", false }, nil)
	if err == nil || st.Requests != maxToolRounds+1 {
		t.Fatalf("err = %v after %d requests", err, st.Requests)
	}
}

// TestDecodeAnswerFindsTheObject: the answer after a thinking block, in a
// fence, after a stray object, or with prose around it.
func TestDecodeAnswerFindsTheObject(t *testing.T) {
	for _, reply := range []string{
		memoryAnswer,
		"<think>maybe {\"proposals\": [broken</think>\n" + memoryAnswer,
		"Here you go:\n```json\n" + memoryAnswer + "\n```\nDone.",
		`{"note": "x"} ` + memoryAnswer,
	} {
		a, err := decodeAnswer(reply)
		if err != nil || len(a.Proposals) != 1 || a.Proposals[0].Key != "check-first" {
			t.Errorf("decodeAnswer(%q) = %+v, %v", reply, a, err)
		}
	}
	if a, err := decodeAnswer(`{"proposals": []}`); err != nil || len(a.Proposals) != 0 {
		t.Errorf("empty list: %+v, %v", a, err)
	}
	if _, err := decodeAnswer(`{"note": "x"}`); err == nil {
		t.Error("an object without proposals decoded")
	}
}

func writeSkill(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
