package review

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/memory"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

const goodSkill = "---\nname: release-flow\ndescription: Tag and publish a kiln release.\n---\n# Release flow\n"

// fakeModel answers every request with text and records what it was sent.
type fakeModel struct {
	answer string
	stop   msg.StopReason
	system string
	user   string
}

func (f *fakeModel) Stream(_ context.Context, _ provider.Model, transcript []msg.Message, opts provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	f.system = opts.SystemPrompt
	f.user = msg.TextOf(transcript[0].(msg.UserMessage).Content)
	ch := make(chan msg.StreamEvent)
	close(ch)
	stop := f.stop
	if stop == "" {
		stop = msg.StopStop
	}
	return ch, func() (*msg.AssistantMessage, error) {
		return &msg.AssistantMessage{Content: msg.Blocks{msg.Text(f.answer)}, StopReason: stop, Usage: msg.Usage{Input: 100, Output: 20}}, nil
	}
}

func input(t *testing.T, memoryOn bool) Input {
	in := Input{SkillsDir: filepath.Join(t.TempDir(), "skills"), MemoryScope: "/proj", Session: "s1"}
	if memoryOn {
		in.Memory = []memory.Record{}
	}
	return in
}

// TestParseKeepsValidProposalsAndDropsTheRest: a skill file must stay in
// its skill's directory and a SKILL.md must load under its directory's
// name; a memory note needs a key, text, evidence and a known type, and
// is refused when memory is off. Valid proposals survive the bad ones.
func TestParseKeepsValidProposalsAndDropsTheRest(t *testing.T) {
	in := input(t, true)
	answer := "```json\n" + `{"proposals": [
	 {"kind": "skill", "skill": "release-flow", "file": "SKILL.md", "content": ` + quote(goodSkill) + `, "why": "the person walked through it"},
	 {"kind": "skill", "skill": "release-flow", "file": "references/tags.md", "content": "Tag with vX.Y.Z.", "why": "detail"},
	 {"kind": "skill", "skill": "release-flow", "file": "../../.ssh/config", "content": "x", "why": "escape"},
	 {"kind": "skill", "skill": "other", "file": "SKILL.md", "content": ` + quote(goodSkill) + `, "why": "wrong name"},
	 {"kind": "skill", "skill": "Bad_Name", "file": "SKILL.md", "content": "x", "why": "bad name"},
	 {"kind": "memory", "key": "test-command", "type": "procedure", "text": "Run make check.", "evidence": "The person said so.", "why": "stated"},
	 {"kind": "memory", "key": "guess", "type": "preference", "text": "Likes Go.", "evidence": "", "why": "inferred"},
	 {"kind": "memory", "key": "odd", "type": "trait", "text": "x", "evidence": "y", "why": "z"}
	]}` + "\n```"
	got, err := Parse(answer, in)
	if err == nil || !strings.Contains(err.Error(), "dropped 5 proposal(s)") {
		t.Errorf("err = %v, want 5 dropped", err)
	}
	var targets []string
	for _, p := range got {
		targets = append(targets, p.Kind+":"+p.Target())
	}
	want := "skill:release-flow/SKILL.md skill:release-flow/references/tags.md memory:test-command"
	if strings.Join(targets, " ") != want {
		t.Fatalf("kept %v, want %s", targets, want)
	}
	if got[2].Scope != "/proj" || got[0].Session != "s1" || got[0].Base != "" {
		t.Errorf("proposals not stamped with scope, session and base: %+v", got)
	}

	if got, _ := Parse(`{"proposals":[{"kind":"memory","key":"k","type":"fact","text":"t","evidence":"e"}]}`, input(t, false)); len(got) != 0 {
		t.Errorf("memory proposal kept with memory off: %+v", got)
	}
	if _, err := Parse("I think nothing.", in); err == nil {
		t.Error("a reply with no JSON parsed")
	}
}

func quote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, "\\", "\\\\"), "\n", "\\n") + `"`
}

// TestQueueAndApply: proposals queue with increasing ids and survive
// listing; approving a skill writes it, approving one whose file changed
// since it was proposed is refused, and a memory proposal lands in the
// store with its evidence marked as the review's.
func TestQueueAndApply(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	q := Queue{Dir: filepath.Join(dir, "proposals")}
	skillsDir := filepath.Join(dir, "skills")
	store, err := memory.Open(filepath.Join(dir, "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	added, err := q.Add([]Proposal{
		{Kind: KindSkill, Skill: "release-flow", File: "SKILL.md", Content: goodSkill, Why: "w"},
		{Kind: KindMemory, Scope: "/proj", Key: "test-command", Type: "procedure", Text: "Run make check.", Evidence: "said so", Why: "w"},
	})
	if err != nil || len(added) != 2 || added[0].ID != 1 || added[1].ID != 2 {
		t.Fatalf("Add = %+v, %v", added, err)
	}
	more, _ := q.Add([]Proposal{{Kind: KindSkill, Skill: "release-flow", File: "SKILL.md", Content: goodSkill + "more\n", Why: "w"}})
	if more[0].ID != 3 {
		t.Errorf("third id = %d, want 3", more[0].ID)
	}
	listed, err := q.List()
	if err != nil || len(listed) != 3 {
		t.Fatalf("List = %+v, %v", listed, err)
	}

	if err := Apply(ctx, listed[0], skillsDir, store); err != nil {
		t.Fatalf("apply new skill: %v", err)
	}
	if data, _ := os.ReadFile(filepath.Join(skillsDir, "release-flow", "SKILL.md")); string(data) != goodSkill {
		t.Errorf("skill file = %q", data)
	}
	// Proposal 3 was made when the file did not exist; it exists now.
	if err := Apply(ctx, listed[2], skillsDir, store); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Errorf("stale proposal applied: %v", err)
	}
	if err := Apply(ctx, listed[1], skillsDir, store); err != nil {
		t.Fatalf("apply memory: %v", err)
	}
	rec, err := store.Current(ctx, "/proj", "test-command", "")
	if err != nil || rec == nil || rec.Text != "Run make check." || rec.Evidence != "kiln review: said so" {
		t.Errorf("memory record = %+v, %v", rec, err)
	}
	if err := Apply(ctx, listed[1], skillsDir, nil); err == nil {
		t.Error("memory applied with no store")
	}

	if err := q.Remove(1); err != nil {
		t.Fatal(err)
	}
	if err := q.Remove(1); err == nil {
		t.Error("removing a missing proposal did not fail")
	}
	if left, _ := q.List(); len(left) != 2 {
		t.Errorf("%d left, want 2", len(left))
	}
}

// TestDigestKeepsWhatTheReviewNeeds: the person's words, the agent's text,
// tool calls by name and failed results; kiln's own notes are left out,
// and past the limit the newest lines are kept.
func TestDigestKeepsWhatTheReviewNeeds(t *testing.T) {
	messages := []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("first request")}},
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("memory block")}, KilnHook: "RuntimeContext"},
		msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{msg.Text("on it"), msg.NewToolCall("c1", "bash", map[string]any{"command": "make check"})}},
		msg.ToolResultMessage{Role: msg.RoleToolResult, ToolName: "bash", IsError: true, Content: msg.Blocks{msg.Text("FAIL: lint")}},
		msg.ToolResultMessage{Role: msg.RoleToolResult, ToolName: "read", Content: msg.Blocks{msg.Text("file body")}},
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("stop running lint twice")}},
	}
	got := Digest(messages, 10_000)
	for _, want := range []string{"PERSON: first request", "AGENT: on it", "AGENT TOOL CALL: bash make check", "TOOL FAILED (bash): FAIL: lint", "PERSON: stop running lint twice"} {
		if !strings.Contains(got, want) {
			t.Errorf("digest lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "memory block") || strings.Contains(got, "file body") {
		t.Errorf("digest has kiln's note or a successful result:\n%s", got)
	}
	short := Digest(messages, 40)
	if !strings.HasPrefix(short, "[earlier conversation omitted]") || !strings.Contains(short, "stop running lint twice") {
		t.Errorf("limited digest = %q, want the newest kept", short)
	}
}

// TestRunSendsTheConversationAndReturnsProposals: one request carrying the
// digest and kiln's skills, the usage reported, the answer parsed; a reply
// cut off is an error, not a partial set of proposals.
func TestRunSendsTheConversationAndReturnsProposals(t *testing.T) {
	in := input(t, true)
	if err := os.MkdirAll(filepath.Join(in.SkillsDir, "release-flow"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(in.SkillsDir, "release-flow", "SKILL.md"), []byte(goodSkill), 0o644); err != nil {
		t.Fatal(err)
	}
	in.Messages = []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("always run make check first")}}}
	model := &fakeModel{answer: `{"proposals":[{"kind":"memory","key":"check-first","type":"preference","text":"Run make check first.","evidence":"The person said always.","why":"stated"}]}`}
	var spent msg.Usage
	got, err := Run(context.Background(), model, provider.Model{}, in, func(u msg.Usage) { spent = u })
	if err != nil || len(got) != 1 || got[0].Key != "check-first" {
		t.Fatalf("Run = %+v, %v", got, err)
	}
	if spent.Input != 100 {
		t.Errorf("usage not reported: %+v", spent)
	}
	if !strings.Contains(model.user, "PERSON: always run make check first") || !strings.Contains(model.user, "Tag and publish a kiln release.") {
		t.Errorf("request lacks the conversation or the kiln skill:\n%s", model.user)
	}
	if !strings.Contains(model.system, "Never infer the person's") || !strings.Contains(model.system, `"kind": "memory"`) {
		t.Errorf("system prompt lacks the grounding rule or the memory shape:\n%s", model.system)
	}

	model.stop = msg.StopLength
	if _, err := Run(context.Background(), model, provider.Model{}, in, nil); err == nil {
		t.Error("a cut-off answer was accepted")
	}
}
