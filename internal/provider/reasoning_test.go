package provider

import (
	"testing"

	"github.com/andrepato/harness/internal/msg"
)

func TestSuppressionForModel(t *testing.T) {
	cases := []struct {
		name string
		m    SuppressionForModel
		want NoThinkSuppression
	}{
		{"non-reasoning model needs nothing", SuppressionForModel{ID: "llama3", Provider: "ollama", Reasoning: false}, SuppressionNone},
		{"reasoning model on a hosted provider needs nothing", SuppressionForModel{ID: "claude-x", Provider: "anthropic", Reasoning: true}, SuppressionNone},
		{"verified-clean qwen on ollama needs nothing", SuppressionForModel{ID: "Qwen3.5:9b", Provider: "ollama", Reasoning: true}, SuppressionNone},
		{"known-bad qwen on ollama needs the suffix", SuppressionForModel{ID: "qwen3-cc:latest", Provider: "ollama", Reasoning: true}, SuppressionNoThinkSuffix},
		{"unknown reasoning model on ollama defaults to the suffix", SuppressionForModel{ID: "some-new-model", Provider: "ollama", Reasoning: true}, SuppressionNoThinkSuffix},
	}
	for _, c := range cases {
		got := suppressionDecide(c.m, SuppressionOptions{})
		if got != c.want {
			t.Errorf("%s: suppressionDecide(%+v) = %q, want %q", c.name, c.m, got, c.want)
		}
	}
}

func TestSuppressionOverrideWins(t *testing.T) {
	got := suppressionDecide(SuppressionForModel{ID: "qwen3-cc:latest", Provider: "ollama", Reasoning: true}, SuppressionOptions{Override: SuppressionNone})
	if got != SuppressionNone {
		t.Fatalf("override did not win: got %q", got)
	}
}

func TestApplySuppressionAppendsToLastUserMessage(t *testing.T) {
	transcript := []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("first")}},
		msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{msg.Text("reply")}},
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("second")}},
	}
	out := ApplySuppression(transcript, SuppressionNoThinkSuffix)
	last := out[2].(msg.UserMessage)
	text := last.Content[0].(msg.TextContent).Text
	if text != "second "+NoThink {
		t.Fatalf("last user message = %q, want %q", text, "second "+NoThink)
	}
	// The earlier user message is untouched.
	first := out[0].(msg.UserMessage)
	if first.Content[0].(msg.TextContent).Text != "first" {
		t.Fatalf("first user message mutated: %q", first.Content[0].(msg.TextContent).Text)
	}
}

func TestApplySuppressionNoneIsNoOp(t *testing.T) {
	transcript := []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hello")}},
	}
	out := ApplySuppression(transcript, SuppressionNone)
	if out[0].(msg.UserMessage).Content[0].(msg.TextContent).Text != "hello" {
		t.Fatal("SuppressionNone must not modify the transcript")
	}
}

func TestApplySuppressionNeverDoubleAppends(t *testing.T) {
	transcript := []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("already has " + NoThink)}},
	}
	out := ApplySuppression(transcript, SuppressionNoThinkSuffix)
	text := out[0].(msg.UserMessage).Content[0].(msg.TextContent).Text
	if text != "already has "+NoThink {
		t.Fatalf("expected no double-append, got %q", text)
	}
}

func TestApplySuppressionNoUserMessage(t *testing.T) {
	transcript := []msg.Message{
		msg.SystemMessage{Role: msg.RoleSystem, Content: msg.Blocks{msg.Text("sys")}},
	}
	out := ApplySuppression(transcript, SuppressionNoThinkSuffix)
	if len(out) != 1 {
		t.Fatalf("expected transcript length unchanged, got %d", len(out))
	}
}
