package api

import (
	"context"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// splitAll feeds content in pieces of size n (every size covers a tag cut
// at every offset) and returns the joined reasoning and text.
func splitAll(content string, n int) (thinking, text string) {
	var s thinkSplitter
	for i := 0; i < len(content); i += n {
		end := min(i+n, len(content))
		th, tx := s.feed(content[i:end])
		thinking, text = thinking+th, text+tx
	}
	th, tx := s.flush()
	return thinking + th, text + tx
}

// TestThinkSplitter: a reasoning block that opens the reply is split out
// however the stream chunks it; a tag anywhere later, or a lookalike, stays
// text; an unclosed block stays reasoning.
func TestThinkSplitter(t *testing.T) {
	for _, c := range []struct {
		name, content, thinking, text string
	}{
		{"think block", "<think>plan the edit</think>\n\nDone.", "plan the edit", "Done."},
		{"leading whitespace", "\n <thinking>a</thinking>b", "a", "b"},
		{"other tag names", "<reasoning>r</reasoning>x", "r", "x"},
		{"no tag", "Plain answer.", "", "Plain answer."},
		{"tag later in the reply", "Wrap it:\n<think>x</think>", "", "Wrap it:\n<think>x</think>"},
		{"lookalike tag", "<thinker>hi", "", "<thinker>hi"},
		{"html first", "<div>x</div>", "", "<div>x</div>"},
		{"close tag lookalike inside", "<think>a</thin b</think>c", "a</thin b", "c"},
		{"unclosed block", "<think>still going", "still going", ""},
		{"only a partial tag", "<thi", "", "<thi"},
	} {
		for n := 1; n <= len(c.content); n++ {
			th, tx := splitAll(c.content, n)
			if th != c.thinking || tx != c.text {
				t.Errorf("%s (chunks of %d): thinking=%q text=%q, want %q, %q", c.name, n, th, tx, c.thinking, c.text)
				break
			}
		}
	}
}

// TestOpenAIInlineThinkTagsBecomeThinking: through the real stream, a
// leading <think> block in the content field becomes a thinking block and
// stays out of the reply text.
func TestOpenAIInlineThinkTagsBecomeThinking(t *testing.T) {
	body := []byte(`data: {"choices":[{"delta":{"content":"<thi"},"finish_reason":null}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"content":"nk>check go.mod</th"},"finish_reason":null}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"content":"ink>\n\nIt uses Go 1.26."},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n")
	srv := replaySSE(t, 200, "text/event-stream", body)
	model := costedModel(provider.ApiOpenAICompletions, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := (&OpenAICompletionsClient{}).Stream(ctx, model, simpleTranscript(), provider.StreamOptions{}, Auth{APIKey: "k"})
	for range events {
	}
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	var thinking, text string
	for _, c := range final.Content {
		switch b := c.(type) {
		case msg.ThinkingContent:
			thinking += b.Thinking
		case msg.TextContent:
			text += b.Text
		}
	}
	if thinking != "check go.mod" || text != "It uses Go 1.26." {
		t.Fatalf("thinking=%q text=%q, want %q and %q", thinking, text, "check go.mod", "It uses Go 1.26.")
	}
}
