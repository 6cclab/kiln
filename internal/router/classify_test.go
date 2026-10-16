package router

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// fakeStreamer answers each Stream call with a canned assistant message,
// or blocks until the context ends when hang is set.
type fakeStreamer struct {
	answer *msg.AssistantMessage
	err    error
	hang   bool
	got    struct {
		transcript []msg.Message
		opts       provider.StreamOptions
	}
}

func (f *fakeStreamer) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	f.got.transcript, f.got.opts = transcript, opts
	ch := make(chan msg.StreamEvent)
	close(ch)
	return ch, func() (*msg.AssistantMessage, error) {
		if f.hang {
			<-ctx.Done()
			return &msg.AssistantMessage{StopReason: msg.StopAborted, ErrorMessage: "aborted"}, nil
		}
		return f.answer, f.err
	}
}

func answeringComplexity(text string) *fakeStreamer {
	return &fakeStreamer{answer: &msg.AssistantMessage{StopReason: msg.StopStop, Content: msg.Blocks{msg.Text(text)}, Usage: msg.Usage{Input: 40, Output: 5}}}
}

func llmClassifierWith(s Streamer) *LLMClassifier {
	return &LLMClassifier{Resolve: func() (Streamer, provider.Model, error) {
		return s, provider.Model{ID: "fast-1", Provider: "faux", MaxTokens: 4096}, nil
	}}
}

// The exact request LLMClassifier.Request builds: jev's wording, with
// the prompt JSON-encoded inside a <prompt> wrapper.
func TestLLMClassifier_Request(t *testing.T) {
	c := &LLMClassifier{}
	system, user := c.Request(`say "hi"`)
	if !strings.Contains(system, "How demanding is the software engineering work requested in `prompt`?") {
		t.Errorf("system missing jev's instructions: %q", system)
	}
	if !strings.Contains(system, "Ordinary features, fixes, reviews, or questions") ||
		!strings.Contains(system, "Subtle design, cross-cutting changes, or hard debugging") {
		t.Errorf("system missing jev's criteria: %q", system)
	}
	want := `<prompt>"say \"hi\""</prompt>`
	if user != want {
		t.Errorf("user = %q, want %q", user, want)
	}
}

// What is actually sent on the wire: no tools, thinking off, the 64-token
// cap (or the model's own cap if smaller), and the built request.
func TestLLMClassifier_SendsTheBuiltRequest(t *testing.T) {
	s := answeringComplexity(`{"complexity":"complex"}`)
	c := llmClassifierWith(s)
	got, err := c.Classify(context.Background(), "a hard bug")
	if err != nil {
		t.Fatal(err)
	}
	if got != Complex {
		t.Errorf("got %q, want complex", got)
	}
	system, user := c.Request("a hard bug")
	var sent string
	if len(s.got.transcript) == 1 {
		for _, b := range s.got.transcript[0].(msg.UserMessage).Content {
			sent += b.(msg.TextContent).Text
		}
	}
	if s.got.opts.SystemPrompt != system || len(s.got.transcript) != 1 || sent != user {
		t.Error("the streamer was not sent the request Request builds")
	}
	if s.got.opts.MaxTokens != classifierMaxTokens || len(s.got.opts.Tools) != 0 || s.got.opts.ThinkingLevel != provider.ThinkingOff {
		t.Errorf("opts = %+v, want %d max tokens, no tools, thinking off", s.got.opts, classifierMaxTokens)
	}
}

// A model with a smaller MaxTokens caps the request further.
func TestLLMClassifier_CapsToModelMaxTokens(t *testing.T) {
	s := answeringComplexity(`{"complexity":"standard"}`)
	c := &LLMClassifier{Resolve: func() (Streamer, provider.Model, error) {
		return s, provider.Model{ID: "tiny", Provider: "faux", MaxTokens: 10}, nil
	}}
	if _, err := c.Classify(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if s.got.opts.MaxTokens != 10 {
		t.Errorf("MaxTokens = %d, want 10 (the model's own cap)", s.got.opts.MaxTokens)
	}
}

// Strict parsing: duplicate keys, trailing text, an unknown value, and a
// non-string value all count as no answer.
func TestParseComplexity(t *testing.T) {
	good := map[string]Complexity{
		`{"complexity":"standard"}`:                  Standard,
		`{"complexity":"complex"}`:                   Complex,
		` {"complexity":"STANDARD"} `:                Standard,
		"```json\n{\"complexity\":\"complex\"}\n```": Complex,
	}
	for in, want := range good {
		got, err := ParseComplexity(in)
		if err != nil || got != want {
			t.Errorf("ParseComplexity(%q) = %q, %v, want %q", in, got, err, want)
		}
	}
	bad := []string{
		``,
		`not json`,
		`{"complexity":"standard"} trailing`,
		`{"complexity":"standard"}{"complexity":"complex"}`,
		`{"complexity":"standard","complexity":"complex"}`,
		`{"complexity":"standard","Complexity":"complex"}`,
		`{"complexity":"maybe"}`,
		`{"complexity":7}`,
		`{"decision":"standard"}`,
	}
	for _, in := range bad {
		if got, err := ParseComplexity(in); err == nil {
			t.Errorf("ParseComplexity(%q) = %q, want an error", in, got)
		}
	}
}

// Every failure path — garbage, empty, a provider error, a model error, no
// response, and a truncated (cut-off) answer — is an error: jev only
// trusts stopReason === "stop".
func TestLLMClassifier_FailuresAreErrors(t *testing.T) {
	cases := map[string]*fakeStreamer{
		"garbage":        answeringComplexity("I think this is complex."),
		"empty":          answeringComplexity(""),
		"provider error": {err: errors.New("503 overloaded")},
		"model error":    {answer: &msg.AssistantMessage{StopReason: msg.StopError, ErrorMessage: "invalid api key"}},
		"no response":    {},
		"truncated":      {answer: &msg.AssistantMessage{StopReason: msg.StopLength, Content: msg.Blocks{msg.Text(`{"complexi`)}}},
	}
	for name, s := range cases {
		if got, err := llmClassifierWith(s).Classify(context.Background(), "x"); err == nil {
			t.Errorf("%s: got %q, want an error", name, got)
		}
	}
}

func TestLLMClassifier_Timeout(t *testing.T) {
	c := llmClassifierWith(&fakeStreamer{hang: true})
	c.Timeout = 30 * time.Millisecond
	start := time.Now()
	_, err := c.Classify(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("the timeout did not bound the call")
	}
}

func TestLLMClassifier_NoModel(t *testing.T) {
	c := &LLMClassifier{Resolve: func() (Streamer, provider.Model, error) { return nil, provider.Model{}, errors.New("unknown provider") }}
	if _, err := c.Classify(context.Background(), "x"); err == nil {
		t.Error("want an error")
	}
	if _, err := (&LLMClassifier{}).Classify(context.Background(), "x"); err == nil {
		t.Error("nil Resolve: want an error")
	}
}

// OnUsage is called on every outcome, including failures with partial
// usage (a truncated answer still spent tokens).
func TestLLMClassifier_OnUsageCalledOnEveryOutcome(t *testing.T) {
	cases := []struct {
		name    string
		s       *fakeStreamer
		wantErr bool
	}{
		{"success", answeringComplexity(`{"complexity":"standard"}`), false},
		{"truncated", &fakeStreamer{answer: &msg.AssistantMessage{StopReason: msg.StopLength, Content: msg.Blocks{msg.Text(`{"complexi`)}, Usage: msg.Usage{Input: 10, Output: 64}}}, true},
		{"garbage", answeringComplexity("not json at all"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []msg.Usage
			cl := llmClassifierWith(c.s)
			cl.OnUsage = func(_ provider.Model, u msg.Usage) { got = append(got, u) }
			_, err := cl.Classify(context.Background(), "x")
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if len(got) != 1 {
				t.Fatalf("OnUsage called %d times, want 1", len(got))
			}
		})
	}
}

// No usage at all (a provider error before any usage is known) does not
// call OnUsage: there is nothing to report.
func TestLLMClassifier_OnUsageNotCalledWithoutAnAnswer(t *testing.T) {
	var called bool
	cl := llmClassifierWith(&fakeStreamer{err: errors.New("down")})
	cl.OnUsage = func(provider.Model, msg.Usage) { called = true }
	if _, err := cl.Classify(context.Background(), "x"); err == nil {
		t.Fatal("want an error")
	}
	if called {
		t.Error("OnUsage called with no response at all")
	}
}
