package compaction

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// seqStreamer answers call i with "summary i", recording every request.
// block makes it stream nothing and wait for the request's cancellation.
type seqStreamer struct {
	mu    sync.Mutex
	calls []fakeCall
	block bool
	// events, when set, are streamed before the answer.
	events []msg.StreamEvent
}

func (f *seqStreamer) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{model: model, transcript: transcript, opts: opts})
	n := len(f.calls)
	f.mu.Unlock()
	ch := make(chan msg.StreamEvent)
	go func() {
		defer close(ch)
		if f.block {
			<-ctx.Done()
			return
		}
		for _, ev := range f.events {
			ch <- ev
		}
	}()
	return ch, func() (*msg.AssistantMessage, error) {
		if f.block {
			return &msg.AssistantMessage{Role: msg.RoleAssistant, StopReason: msg.StopAborted, ErrorMessage: "Request was aborted"}, nil
		}
		return scriptedAssistant(fmt.Sprintf("summary %d", n), msg.Usage{Input: 10, Output: 5}), nil
	}
}

// requestTokens is the estimated size of one recorded request: system
// prompt plus user text, as the request budget counts them.
func requestTokens(c fakeCall) int {
	return estimateText(c.opts.SystemPrompt) + estimateText(msg.TextOf(c.transcript[0].(msg.UserMessage).Content))
}

// longHistory is n user/assistant exchanges of about tokensEach tokens each.
func longHistory(n, tokensEach int) []msg.Message {
	body := strings.Repeat("word ", tokensEach*4/5)
	var out []msg.Message
	for i := 0; i < n; i++ {
		out = append(out,
			msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text(fmt.Sprintf("question %d: %s", i, body))}},
			msg.AssistantMessage{Role: msg.RoleAssistant, StopReason: msg.StopStop, Content: msg.Blocks{msg.Text(fmt.Sprintf("answer %d: %s", i, body))}},
		)
	}
	return out
}

func smallModel(window int) provider.Model {
	return provider.Model{ID: "small", Provider: "local", Api: provider.ApiOpenAICompletions, ContextWindow: window, MaxTokens: 8192}
}

// TestCompactFitsWhenUnderWindow: history that fits sends exactly one
// request, as pi does.
func TestCompactFitsWhenUnderWindow(t *testing.T) {
	f := &seqStreamer{}
	prep := &Preparation{MessagesToSummarize: longHistory(3, 1000), Settings: Settings{ReserveTokens: 16384}}
	res, err := Compact(context.Background(), prep, f, smallModel(128000), nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("%d requests for history that fits one, want 1", len(f.calls))
	}
	if !strings.HasPrefix(res.Summary, "summary 1") {
		t.Errorf("summary = %q", res.Summary)
	}
}

// TestCompactSplitsWhenOverWindow: ~60k tokens of history summarised by a
// 16k-token model goes out in parts that each fit the window with room for
// the summary's output, each later part carrying the summary so far, and
// the result is the last part's summary.
func TestCompactSplitsWhenOverWindow(t *testing.T) {
	const window = 16384
	f := &seqStreamer{}
	var progress []Progress
	prep := &Preparation{MessagesToSummarize: longHistory(30, 1000), Settings: Settings{ReserveTokens: 1638}}
	res, err := CompactWith(context.Background(), prep, f, smallModel(window), nil, "", Options{OnProgress: func(p Progress) { progress = append(progress, p) }})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) < 4 {
		t.Fatalf("%d requests for ~60k tokens into a 16k window, want it split into at least 4", len(f.calls))
	}
	for i, c := range f.calls {
		in := requestTokens(c)
		if in+c.opts.MaxTokens > window {
			t.Errorf("request %d: ~%d prompt tokens + %d output > %d-token window", i+1, in, c.opts.MaxTokens, window)
		}
		user := msg.TextOf(c.transcript[0].(msg.UserMessage).Content)
		if i > 0 && !strings.Contains(user, fmt.Sprintf("<previous-summary>\nsummary %d\n</previous-summary>", i)) {
			t.Errorf("request %d does not carry the summary so far", i+1)
		}
	}
	// Every exchange reached some request, in order.
	var all strings.Builder
	for _, c := range f.calls {
		all.WriteString(msg.TextOf(c.transcript[0].(msg.UserMessage).Content))
	}
	last := -1
	for i := 0; i < 30; i++ {
		at := strings.Index(all.String(), fmt.Sprintf("question %d:", i))
		if at < 0 || at < last {
			t.Fatalf("exchange %d missing or out of order", i)
		}
		last = at
	}
	if want := fmt.Sprintf("summary %d", len(f.calls)); !strings.HasPrefix(res.Summary, want) {
		t.Errorf("summary = %q, want the last part's %q", res.Summary, want)
	}
	if len(progress) == 0 || progress[0].Parts != len(f.calls) || progress[len(progress)-1].Part != len(f.calls) {
		t.Errorf("progress reports = %+v, want parts 1..%d of %d", progress, len(f.calls), len(f.calls))
	}
}

// TestCompactTinyWindow: a 4k window still gets requests that fit, and a
// single message larger than any part is cut down rather than sent whole.
func TestCompactTinyWindow(t *testing.T) {
	const window = 4096
	f := &seqStreamer{}
	huge := msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text(strings.Repeat("paste ", 20000))}}
	prep := &Preparation{MessagesToSummarize: append(longHistory(4, 300), huge), Settings: Settings{ReserveTokens: 512}}
	if _, err := Compact(context.Background(), prep, f, smallModel(window), nil, ""); err != nil {
		t.Fatal(err)
	}
	for i, c := range f.calls {
		if in := requestTokens(c); in+c.opts.MaxTokens > window {
			t.Errorf("request %d: ~%d prompt tokens + %d output > %d-token window", i+1, in, c.opts.MaxTokens, window)
		}
	}
	lastUser := msg.TextOf(f.calls[len(f.calls)-1].transcript[0].(msg.UserMessage).Content)
	if !strings.Contains(lastUser, "more characters truncated") {
		t.Error("the oversized message was not cut down to fit")
	}
}

// TestCompactWindowTooSmall: a window the prompt and summary already fill
// is an error naming the numbers, and nothing is sent.
func TestCompactWindowTooSmall(t *testing.T) {
	f := &seqStreamer{}
	prep := &Preparation{MessagesToSummarize: longHistory(2, 100), Settings: Settings{ReserveTokens: 2048}}
	_, err := Compact(context.Background(), prep, f, smallModel(2048), nil, "")
	var ce *Error
	if !errors.As(err, &ce) || ce.Code != "summarization_failed" || !strings.Contains(ce.Message, "window") {
		t.Fatalf("err = %v, want a summarization_failed error about the window", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("%d requests sent to a window that cannot hold one", len(f.calls))
	}
}

// TestPlanParts covers the planner's edges directly.
func TestPlanParts(t *testing.T) {
	ten := strings.Repeat("x", 40) // 10 tokens, 11 with the separator
	cases := []struct {
		name         string
		parts        []string
		first, later int
		want         []int
	}{
		{"empty", nil, 100, 100, []int{0}},
		{"all fit", []string{ten, ten, ten}, 100, 100, []int{3}},
		{"split", []string{ten, ten, ten, ten}, 22, 22, []int{2, 4}},
		{"later smaller", []string{ten, ten, ten, ten}, 33, 11, []int{3, 4}},
		{"oversized alone", []string{ten, strings.Repeat("x", 400), ten}, 22, 22, []int{1, 2, 3}},
	}
	for _, c := range cases {
		if got := PlanParts(c.parts, c.first, c.later); fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: PlanParts = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestCompactStallWatchdog: a model that never sends anything is given
// up on after the first-event limit, as a "stalled" error, not left to
// hang; a cancellation by the caller is still "aborted".
func TestCompactStallWatchdog(t *testing.T) {
	prep := &Preparation{MessagesToSummarize: longHistory(1, 50), Settings: Settings{ReserveTokens: 2048}}
	f := &seqStreamer{block: true}
	start := time.Now()
	_, err := CompactWith(context.Background(), prep, f, smallModel(32768), nil, "", Options{FirstEventTimeout: func(int) time.Duration { return 50 * time.Millisecond }})
	var ce *Error
	if !errors.As(err, &ce) || ce.Code != "stalled" {
		t.Fatalf("err = %v, want a stalled error", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("watchdog took %s", time.Since(start))
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err = CompactWith(ctx, prep, &seqStreamer{block: true}, smallModel(32768), nil, "", Options{})
	if !errors.As(err, &ce) || ce.Code != "aborted" {
		t.Fatalf("cancelled: err = %v, want aborted", err)
	}
}

// TestDefaultFirstEventTimeout pins the allowance to the measured
// throughput it was derived from: a 36k-token part on a host ten times
// slower than the measured one still gets its full read time.
func TestDefaultFirstEventTimeout(t *testing.T) {
	if got := DefaultFirstEventTimeout(0); got != 2*time.Minute {
		t.Errorf("floor = %s", got)
	}
	if got, min := DefaultFirstEventTimeout(36000), 36000*time.Second/20; got < min {
		t.Errorf("36k tokens: %s, want at least %s", got, min)
	}
}
