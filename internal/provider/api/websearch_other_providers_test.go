package api

import (
	"encoding/json"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// serverToolOnly is a ToolDef offering nothing but a server tool
// declaration -- the shape web_search takes for every provider.
var serverToolOnly = []provider.ToolDef{{Name: "web_search", ServerTool: webSearchServerTool}}

// TestNonAnthropicProviders_OmitServerTool guards that a provider server
// tool (ServerTool set on the ToolDef) is never declared to a non-Anthropic
// API: each of these clients has no way to execute it, so offering it
// would let a model call a tool that never resolves.
func TestNonAnthropicProviders_OmitServerTool(t *testing.T) {
	model := provider.Model{ID: "m", MaxTokens: 1024}
	transcript := []msg.Message{msg.UserMessage{Content: msg.Blocks{msg.Text("hi")}}}
	opts := provider.StreamOptions{Tools: serverToolOnly}

	t.Run("openai_completions", func(t *testing.T) {
		req := buildOpenAIRequest(model, transcript, opts)
		if len(req.Tools) != 0 {
			t.Fatalf("Tools = %+v, want none", req.Tools)
		}
	})
	t.Run("openai_responses", func(t *testing.T) {
		out := convertResponsesTools(model, opts.Tools, provider.OpenAIResponsesCompat{})
		if len(out) != 0 {
			t.Fatalf("tools = %+v, want none", out)
		}
	})
	t.Run("openai_codex_responses", func(t *testing.T) {
		out := convertCodexTools(opts.Tools, false)
		if len(out) != 0 {
			t.Fatalf("tools = %+v, want none", out)
		}
	})
	t.Run("azure_openai_responses", func(t *testing.T) {
		out := buildAzureTools(model, opts)
		if len(out) != 0 {
			t.Fatalf("tools = %+v, want none", out)
		}
	})
	t.Run("google_generative_ai", func(t *testing.T) {
		req := buildGoogleRequest(model, transcript, opts)
		for _, tool := range req.Tools {
			if len(tool.FunctionDeclarations) != 0 {
				t.Fatalf("FunctionDeclarations = %+v, want none", tool.FunctionDeclarations)
			}
		}
	})
	t.Run("mistral_conversations", func(t *testing.T) {
		out := convertToMistralTools(opts.Tools, model)
		if len(out) != 0 {
			t.Fatalf("tools = %+v, want none", out)
		}
	})
	t.Run("bedrock_converse_stream", func(t *testing.T) {
		cfg := buildBedrockToolConfig(opts.Tools)
		if cfg != nil && len(cfg.Tools) != 0 {
			t.Fatalf("tools = %+v, want none", cfg.Tools)
		}
	})
}

// TestOpenAICompletions_ProviderBlockDropped guards that an assistant
// message's msg.ProviderBlock (recorded while running against Anthropic)
// contributes nothing to an OpenAI-completions replay: no case in
// buildOpenAIRequest's content-block switch matches it, so it is silently
// skipped rather than sent or causing a panic.
func TestOpenAICompletions_ProviderBlockDropped(t *testing.T) {
	pb := msg.ProviderBlock{Provider: "anthropic", Type: "providerBlock", Raw: json.RawMessage(`{"type":"server_tool_use"}`)}
	transcript := []msg.Message{
		msg.UserMessage{Content: msg.Blocks{msg.Text("search please")}},
		msg.AssistantMessage{Content: msg.Blocks{msg.Text("ok"), pb}},
	}
	req := buildOpenAIRequest(provider.Model{ID: "m", MaxTokens: 1024}, transcript, provider.StreamOptions{})
	if len(req.Messages) != 2 {
		t.Fatalf("Messages len = %d, want 2", len(req.Messages))
	}
	assistant := req.Messages[1]
	if assistant.Content != "ok" {
		t.Fatalf("assistant.Content = %+v, want just the text block (ProviderBlock silently dropped)", assistant.Content)
	}
	if len(assistant.ToolCalls) != 0 {
		t.Fatalf("assistant.ToolCalls = %+v, want none", assistant.ToolCalls)
	}
}
