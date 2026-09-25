package harness

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/session/jsonl"
	"github.com/andrepato/harness/internal/tool"
	"github.com/andrepato/harness/internal/tools"
)

// fakeProvider is a minimal, in-package provider.Provider used only by
// this file's tests, for scripting an assistant message with two tool
// calls in one turn — something internal/testkit/faux cannot express (a
// tool_call step always ends its turn there; see its doc.go). It is not
// part of internal/testkit/faux and does not touch that package.
type fakeProvider struct {
	id    string
	model provider.Model
	// turns are consumed one per Stream call, in order; the last one
	// repeats once exhausted.
	turns []func() *msg.AssistantMessage
	calls int
}

func (p *fakeProvider) ID() string   { return p.id }
func (p *fakeProvider) Name() string { return p.id }
func (p *fakeProvider) Auth() provider.AuthSpec {
	return provider.AuthSpec{Kind: provider.AuthKindNone}
}
func (p *fakeProvider) Models() []provider.Model                { return []provider.Model{p.model} }
func (p *fakeProvider) RefreshModels(ctx context.Context) error { return nil }
func (p *fakeProvider) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	idx := p.calls
	if idx >= len(p.turns) {
		idx = len(p.turns) - 1
	}
	p.calls++
	ch := make(chan msg.StreamEvent)
	close(ch) // no streamed frames needed: wait() below carries the final message directly.
	final := p.turns[idx]()
	return ch, func() (*msg.AssistantMessage, error) { return final, nil }
}

func assistantText(text string) *msg.AssistantMessage {
	return &msg.AssistantMessage{
		API: "fake", Model: "fake-1", Provider: "fake",
		Role: msg.RoleAssistant, StopReason: msg.StopStop, Timestamp: 1,
		Content: msg.Blocks{msg.Text(text)},
	}
}

func assistantToolCalls(calls ...msg.ToolCall) *msg.AssistantMessage {
	blocks := make(msg.Blocks, len(calls))
	for i, c := range calls {
		blocks[i] = c
	}
	return &msg.AssistantMessage{
		API: "fake", Model: "fake-1", Provider: "fake",
		Role: msg.RoleAssistant, StopReason: msg.StopToolUse, Timestamp: 1,
		Content: blocks,
	}
}

// newConcurrentTestRig is like newTestRig but wires a fakeProvider instead
// of the faux HTTP server, and lets the caller supply extra tools (the
// barrier tool below).
func newConcurrentTestRig(t *testing.T, turns []func() *msg.AssistantMessage, extraTools ...*tool.Tool) (*Harness, *Lane) {
	t.Helper()

	model := provider.Model{ID: "fake-1", Name: "fake-1", Api: provider.ApiAnthropicMessages, Provider: "fake", ContextWindow: 128000, MaxTokens: 4096}
	fp := &fakeProvider{id: "fake", model: model, turns: turns}
	registry := provider.NewRegistry(nil)
	registry.Register(fp)

	cwd := t.TempDir()
	env := execenv.New(cwd)
	toolSet := tools.Builtins(env)
	for _, tl := range extraTools {
		toolSet.Add(tl)
	}

	sessionsRoot := t.TempDir()
	repo, err := jsonl.NewRepo(sessionsRoot)
	if err != nil {
		t.Fatalf("jsonl.NewRepo: %v", err)
	}
	storage, _, err := repo.Create(jsonl.CreateOptions{Cwd: cwd})
	if err != nil {
		t.Fatalf("repo.Create: %v", err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	h, err := New(Options{
		Storage:         storage,
		Registry:        registry,
		Model:           session.ModelRef{Provider: "fake", ModelID: "fake-1"},
		ThinkingLevel:   "off",
		Tools:           toolSet,
		ActiveToolNames: toolSet.Names(),
		SystemPrompt:    "test harness",
		Cwd:             cwd,
	})
	if err != nil {
		t.Fatalf("harness.New: %v", err)
	}
	lane, err := h.Lane("main")
	if err != nil {
		t.Fatalf("Lane: %v", err)
	}
	return h, lane
}

// newBarrierTool builds a Concurrent tool whose Execute blocks until n
// calls have all entered it (a real rendezvous: sequential execution would
// deadlock here, which is exactly what makes this a useful concurrency
// test rather than a trivial one), then blocks further on a per-call
// release channel so the test can control finish order independently of
// start order.
func newBarrierTool(n int) (t *tool.Tool, release func(toolCallID string)) {
	var arrive sync.WaitGroup
	arrive.Add(n)
	releaseCh := struct {
		mu sync.Mutex
		m  map[string]chan struct{}
	}{m: map[string]chan struct{}{}}

	getRelease := func(id string) chan struct{} {
		releaseCh.mu.Lock()
		defer releaseCh.mu.Unlock()
		if ch, ok := releaseCh.m[id]; ok {
			return ch
		}
		ch := make(chan struct{})
		releaseCh.m[id] = ch
		return ch
	}

	t = &tool.Tool{
		Name:       "barrier_tool",
		Label:      "Barrier",
		Concurrent: true,
		Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
		Execute: func(ctx context.Context, args json.RawMessage, onUpdate tool.Update, inv tool.Invocation) (tool.Result, error) {
			arrive.Done()
			arrive.Wait() // the barrier: blocks forever if calls run sequentially.
			select {
			case <-getRelease(inv.ToolCallID):
			case <-ctx.Done():
				return tool.Result{}, ctx.Err()
			}
			return tool.Text("done:" + inv.ToolCallID), nil
		},
	}
	release = func(toolCallID string) { close(getRelease(toolCallID)) }
	return t, release
}

// TestConcurrentToolsOverlapAndCommitInSourceOrder scripts one assistant
// message with two calls to a Concurrent barrier tool. The barrier proves
// they actually ran on separate goroutines at once (a sequential
// implementation would deadlock and this test would time out); releasing
// them in reverse source order proves the committed write order does not
// just mirror finish order. It asserts the two toolResult entries chain in
// source order (entry[1].ParentID == entry[0].ID) and that op.state ends
// at "checkpoint" as usual.
func TestConcurrentToolsOverlapAndCommitInSourceOrder(t *testing.T) {
	barrierTool, release := newBarrierTool(2)

	tc0 := msg.NewToolCall("tc0", "barrier_tool", map[string]any{})
	tc1 := msg.NewToolCall("tc1", "barrier_tool", map[string]any{})

	turns := []func() *msg.AssistantMessage{
		func() *msg.AssistantMessage { return assistantToolCalls(tc0, tc1) },
		func() *msg.AssistantMessage { return assistantText("done") },
	}
	h, lane := newConcurrentTestRig(t, turns, barrierTool)

	resultCh := make(chan RunResult, 1)
	errCh := make(chan error, 1)
	go func() {
		r, err := lane.Prompt(context.Background(), "go", nil)
		resultCh <- r
		errCh <- err
	}()

	// Release in reverse source order once both calls are known to have
	// started (a small settle window after both would have reached the
	// barrier; the barrier itself is what actually guarantees overlap —
	// this sleep only avoids racing the release against arrival).
	time.Sleep(100 * time.Millisecond)
	release("tc1")
	release("tc0")

	var result RunResult
	select {
	case result = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("Prompt did not return: the two barrier_tool calls likely ran sequentially and deadlocked")
	}
	if err := <-errCh; err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("status = %q, want completed (err=%v)", result.Status, result.Error)
	}

	entries, err := lane.FindEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var toolResults []session.Entry
	for _, e := range entries {
		if e.Type == session.EntryMessage && e.Message != nil && e.Message.MessageRole() == "toolResult" {
			toolResults = append(toolResults, e)
		}
	}
	if len(toolResults) != 2 {
		t.Fatalf("got %d toolResult entries, want 2", len(toolResults))
	}
	// FindEntries order is not asserted here; identify by content instead.
	byCallID := map[string]session.Entry{}
	for _, e := range toolResults {
		tr := e.Message.(msg.ToolResultMessage)
		byCallID[tr.ToolCallID] = e
	}
	e0, ok0 := byCallID["tc0"]
	e1, ok1 := byCallID["tc1"]
	if !ok0 || !ok1 {
		t.Fatalf("expected toolResult entries for tc0 and tc1, got %+v", byCallID)
	}
	if e1.ParentID == nil || *e1.ParentID != e0.ID {
		t.Fatalf("tc1's toolResult entry does not chain after tc0's: e0.ID=%s e1.ParentID=%v", e0.ID, e1.ParentID)
	}

	st, err := lane.laneState()
	if err != nil {
		t.Fatal(err)
	}
	if st.CurrentOperationID != nil {
		t.Fatalf("lane state still has a current operation after completion: %v", *st.CurrentOperationID)
	}
	_ = h
}

// TestNonConcurrentToolsStayStrictlySequential scripts two calls to
// ordinary (non-Concurrent) bash in one assistant message and asserts the
// second call's Execute only starts after the first one's has returned —
// the behavior this whole feature must leave untouched for tools that
// don't opt in.
func TestNonConcurrentToolsStayStrictlySequential(t *testing.T) {
	var mu sync.Mutex
	var order []string
	var firstDone bool

	seqTool := &tool.Tool{
		Name:       "seq_tool",
		Label:      "Seq",
		Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
		Execute: func(ctx context.Context, args json.RawMessage, onUpdate tool.Update, inv tool.Invocation) (tool.Result, error) {
			mu.Lock()
			order = append(order, "start:"+inv.ToolCallID)
			startedSecondBeforeFirstDone := inv.ToolCallID == "tc1" && !firstDone
			mu.Unlock()
			if startedSecondBeforeFirstDone {
				t.Errorf("tc1 started before tc0 finished: tool calls ran concurrently")
			}
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			if inv.ToolCallID == "tc0" {
				firstDone = true
			}
			order = append(order, "end:"+inv.ToolCallID)
			mu.Unlock()
			return tool.Text("done:" + inv.ToolCallID), nil
		},
	}

	tc0 := msg.NewToolCall("tc0", "seq_tool", map[string]any{})
	tc1 := msg.NewToolCall("tc1", "seq_tool", map[string]any{})
	turns := []func() *msg.AssistantMessage{
		func() *msg.AssistantMessage { return assistantToolCalls(tc0, tc1) },
		func() *msg.AssistantMessage { return assistantText("done") },
	}
	_, lane := newConcurrentTestRig(t, turns, seqTool)

	result, err := lane.Prompt(context.Background(), "go", nil)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("status = %q, want completed (err=%v)", result.Status, result.Error)
	}

	want := []string{"start:tc0", "end:tc0", "start:tc1", "end:tc1"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}
