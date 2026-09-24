package api

// Tests for the openai-completions thinkingFormat variants ported in
// openai_completions.go's applyThinkingFormat/resolveChatTemplateValues
// (openai-completions.js:600-720). Each test builds the wire request
// directly via buildOpenAIRequest (no HTTP) and asserts the exact field
// shape pi would send, citing the source line range in a comment.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

func strPtr(s string) *string { return &s }

func reasoningModel(thinkingFormat string, extraCompat map[string]any) provider.Model {
	compat := map[string]any{}
	if thinkingFormat != "" {
		compat["thinkingFormat"] = thinkingFormat
	}
	compat["supportsReasoningEffort"] = true
	for k, v := range extraCompat {
		compat[k] = v
	}
	raw, _ := json.Marshal(compat)
	return provider.Model{
		ID:        "reasoning-model",
		Name:      "reasoning-model",
		Api:       provider.ApiOpenAICompletions,
		Provider:  "faux",
		BaseURL:   "http://example.invalid",
		Reasoning: true,
		MaxTokens: 4096,
		Compat:    raw,
		ThinkingLevelMap: provider.ThinkingLevelMap{
			provider.ThinkingHigh: strPtr("max"),
			provider.ThinkingOff:  strPtr("none"),
		},
	}
}

func simpleTranscript() []msg.Message {
	return []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}
}

// openai-completions.js:619-629 (zai): thinking:{type:"enabled",clear_thinking:false}
// plus reasoning_effort when supported and reasoning was requested; {type:"disabled"}
// with no reasoning_effort when it wasn't.
func TestThinkingFormatZai(t *testing.T) {
	model := reasoningModel("zai", nil)

	req := buildOpenAIRequest(model, simpleTranscript(), provider.StreamOptions{ThinkingLevel: provider.ThinkingHigh})
	var thinking map[string]any
	if err := json.Unmarshal(req.Thinking, &thinking); err != nil {
		t.Fatalf("thinking: %v", err)
	}
	if thinking["type"] != "enabled" || thinking["clear_thinking"] != false {
		t.Fatalf("thinking = %+v, want {type:enabled clear_thinking:false}", thinking)
	}
	if req.ReasoningEffort != "max" {
		t.Fatalf("reasoning_effort = %q, want max (mapped from ThinkingHigh)", req.ReasoningEffort)
	}

	req = buildOpenAIRequest(model, simpleTranscript(), provider.StreamOptions{ThinkingLevel: provider.ThinkingOff})
	if err := json.Unmarshal(req.Thinking, &thinking); err != nil {
		t.Fatalf("thinking: %v", err)
	}
	if thinking["type"] != "disabled" {
		t.Fatalf("thinking = %+v, want {type:disabled}", thinking)
	}
	if req.ReasoningEffort != "" {
		t.Fatalf("reasoning_effort = %q, want empty when reasoning is off", req.ReasoningEffort)
	}
}

// openai-completions.js:630-638 (qwen): top-level enable_thinking bool, plus
// reasoning_effort when supported.
func TestThinkingFormatQwen(t *testing.T) {
	model := reasoningModel("qwen", nil)
	req := buildOpenAIRequest(model, simpleTranscript(), provider.StreamOptions{ThinkingLevel: provider.ThinkingHigh})
	if req.EnableThinking == nil || !*req.EnableThinking {
		t.Fatalf("enable_thinking = %v, want true", req.EnableThinking)
	}
	if req.ReasoningEffort != "max" {
		t.Fatalf("reasoning_effort = %q, want max", req.ReasoningEffort)
	}

	req = buildOpenAIRequest(model, simpleTranscript(), provider.StreamOptions{})
	if req.EnableThinking == nil || *req.EnableThinking {
		t.Fatalf("enable_thinking = %v, want false when reasoning not requested", req.EnableThinking)
	}
}

// openai-completions.js:639-644 (qwen-chat-template):
// chat_template_kwargs: { enable_thinking: !!reasoningEffort, preserve_thinking: true }.
func TestThinkingFormatQwenChatTemplate(t *testing.T) {
	model := reasoningModel("qwen-chat-template", nil)
	req := buildOpenAIRequest(model, simpleTranscript(), provider.StreamOptions{ThinkingLevel: provider.ThinkingHigh})
	if req.ChatTemplateKwargs["enable_thinking"] != true || req.ChatTemplateKwargs["preserve_thinking"] != true {
		t.Fatalf("chat_template_kwargs = %+v", req.ChatTemplateKwargs)
	}
}

// openai-completions.js:645-650, 765-788 (chat-template): configurable
// chat_template_kwargs resolved via resolveChatTemplateKwargValue's
// $var substitution rules.
func TestThinkingFormatChatTemplate(t *testing.T) {
	model := reasoningModel("chat-template", map[string]any{
		"chatTemplateKwargs": map[string]any{
			"enable_thinking": map[string]any{"$var": "thinking.enabled"},
			"effort_level":    map[string]any{"$var": "thinking.effort"}, // falls through to the mapped-level branch
			"static_flag":     "always-present",
			"skip_when_off":   map[string]any{"$var": "thinking.effort", "omitWhenOff": true},
		},
	})

	req := buildOpenAIRequest(model, simpleTranscript(), provider.StreamOptions{ThinkingLevel: provider.ThinkingHigh})
	if req.ChatTemplateKwargs["enable_thinking"] != true {
		t.Fatalf("enable_thinking = %v, want true", req.ChatTemplateKwargs["enable_thinking"])
	}
	if req.ChatTemplateKwargs["effort_level"] != "max" {
		t.Fatalf("effort_level = %v, want max (mapped ThinkingHigh)", req.ChatTemplateKwargs["effort_level"])
	}
	if req.ChatTemplateKwargs["static_flag"] != "always-present" {
		t.Fatalf("static_flag = %v, want passthrough", req.ChatTemplateKwargs["static_flag"])
	}
	if req.ChatTemplateKwargs["skip_when_off"] != "max" {
		t.Fatalf("skip_when_off = %v, want max (reasoning is on)", req.ChatTemplateKwargs["skip_when_off"])
	}

	// Reasoning off: enable_thinking -> false, omitWhenOff key dropped entirely.
	req = buildOpenAIRequest(model, simpleTranscript(), provider.StreamOptions{})
	if req.ChatTemplateKwargs["enable_thinking"] != false {
		t.Fatalf("enable_thinking = %v, want false", req.ChatTemplateKwargs["enable_thinking"])
	}
	if _, present := req.ChatTemplateKwargs["skip_when_off"]; present {
		t.Fatalf("skip_when_off should be omitted when reasoning is off, got %v", req.ChatTemplateKwargs["skip_when_off"])
	}
}

// openai-completions.js:651-665 (baseten): chat_template_args via the same
// resolver, plus reasoning_effort falling back to model.thinkingLevelMap?.off
// (not the raw level) when reasoning wasn't requested.
func TestThinkingFormatBaseten(t *testing.T) {
	model := reasoningModel("baseten", map[string]any{
		"chatTemplateArgs": map[string]any{"thinking": map[string]any{"$var": "thinking.enabled"}},
	})

	req := buildOpenAIRequest(model, simpleTranscript(), provider.StreamOptions{ThinkingLevel: provider.ThinkingHigh})
	if req.ChatTemplateArgs["thinking"] != true {
		t.Fatalf("chat_template_args.thinking = %v, want true", req.ChatTemplateArgs["thinking"])
	}
	if req.ReasoningEffort != "max" {
		t.Fatalf("reasoning_effort = %q, want max", req.ReasoningEffort)
	}

	req = buildOpenAIRequest(model, simpleTranscript(), provider.StreamOptions{})
	if req.ReasoningEffort != "none" {
		t.Fatalf("reasoning_effort = %q, want none (mapped off value, not the raw empty level)", req.ReasoningEffort)
	}
}

// openai-completions.js:666-677 (deepseek): thinking:{type:"enabled"|"disabled"}
// plus reasoning_effort when supported and requested.
func TestThinkingFormatDeepseek(t *testing.T) {
	model := reasoningModel("deepseek", nil)
	req := buildOpenAIRequest(model, simpleTranscript(), provider.StreamOptions{ThinkingLevel: provider.ThinkingHigh})
	var thinking map[string]any
	json.Unmarshal(req.Thinking, &thinking)
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking = %+v, want {type:enabled}", thinking)
	}
	if req.ReasoningEffort != "max" {
		t.Fatalf("reasoning_effort = %q, want max", req.ReasoningEffort)
	}

	req = buildOpenAIRequest(model, simpleTranscript(), provider.StreamOptions{})
	json.Unmarshal(req.Thinking, &thinking)
	if thinking["type"] != "disabled" {
		t.Fatalf("thinking = %+v, want {type:disabled}", thinking)
	}
	if req.ReasoningEffort != "" {
		t.Fatalf("reasoning_effort = %q, want empty when off", req.ReasoningEffort)
	}
}

// openai-completions.js:690-695 (ant-ling): reasoning:{effort} sent only
// when reasoningEffort is set AND the mapped effort is non-null.
func TestThinkingFormatAntLing(t *testing.T) {
	model := reasoningModel("ant-ling", nil)
	req := buildOpenAIRequest(model, simpleTranscript(), provider.StreamOptions{ThinkingLevel: provider.ThinkingHigh})
	var reasoning map[string]any
	if err := json.Unmarshal(req.Reasoning, &reasoning); err != nil {
		t.Fatalf("reasoning: %v", err)
	}
	if reasoning["effort"] != "max" {
		t.Fatalf("reasoning.effort = %v, want max", reasoning["effort"])
	}

	// Not requested at all -> omitted.
	req = buildOpenAIRequest(model, simpleTranscript(), provider.StreamOptions{})
	if req.Reasoning != nil {
		t.Fatalf("reasoning = %s, want omitted when reasoning wasn't requested", req.Reasoning)
	}

	// Mapped to explicit null -> omitted even though requested.
	nullMap := reasoningModel("ant-ling", nil)
	nullMap.ThinkingLevelMap[provider.ThinkingLow] = nil
	req = buildOpenAIRequest(nullMap, simpleTranscript(), provider.StreamOptions{ThinkingLevel: provider.ThinkingLow})
	if req.Reasoning != nil {
		t.Fatalf("reasoning = %s, want omitted when the mapped effort is explicitly null", req.Reasoning)
	}
}

// openai-completions.js:696-703 (together): reasoning:{enabled:bool} always
// sent, plus reasoning_effort when supported and requested.
func TestThinkingFormatTogether(t *testing.T) {
	model := reasoningModel("together", nil)
	req := buildOpenAIRequest(model, simpleTranscript(), provider.StreamOptions{ThinkingLevel: provider.ThinkingHigh})
	var reasoning map[string]any
	json.Unmarshal(req.Reasoning, &reasoning)
	if reasoning["enabled"] != true {
		t.Fatalf("reasoning.enabled = %v, want true", reasoning["enabled"])
	}
	if req.ReasoningEffort != "max" {
		t.Fatalf("reasoning_effort = %q, want max", req.ReasoningEffort)
	}

	req = buildOpenAIRequest(model, simpleTranscript(), provider.StreamOptions{})
	json.Unmarshal(req.Reasoning, &reasoning)
	if reasoning["enabled"] != false {
		t.Fatalf("reasoning.enabled = %v, want false", reasoning["enabled"])
	}
	if req.ReasoningEffort != "" {
		t.Fatalf("reasoning_effort = %q, want empty when off", req.ReasoningEffort)
	}
}

// openai-completions.js:704-711 (string-thinking): top-level thinking is a
// bare string level, not an object.
func TestThinkingFormatStringThinking(t *testing.T) {
	model := reasoningModel("string-thinking", nil)
	req := buildOpenAIRequest(model, simpleTranscript(), provider.StreamOptions{ThinkingLevel: provider.ThinkingHigh})
	var thinking string
	if err := json.Unmarshal(req.Thinking, &thinking); err != nil {
		t.Fatalf("thinking: %v", err)
	}
	if thinking != "max" {
		t.Fatalf("thinking = %q, want max", thinking)
	}

	req = buildOpenAIRequest(model, simpleTranscript(), provider.StreamOptions{})
	json.Unmarshal(req.Thinking, &thinking)
	if thinking != "none" {
		t.Fatalf("thinking = %q, want none (mapped off value)", thinking)
	}
}

// openai-completions.js:712-717 (default "openai" format, unchanged
// behavior): reasoning_effort set from the mapped level, or from the mapped
// "off" value (only if it's a non-nil string) when reasoning isn't requested.
func TestThinkingFormatOpenAIDefault(t *testing.T) {
	model := reasoningModel("", nil)
	req := buildOpenAIRequest(model, simpleTranscript(), provider.StreamOptions{ThinkingLevel: provider.ThinkingHigh})
	if req.ReasoningEffort != "max" {
		t.Fatalf("reasoning_effort = %q, want max", req.ReasoningEffort)
	}
	req = buildOpenAIRequest(model, simpleTranscript(), provider.StreamOptions{})
	if req.ReasoningEffort != "none" {
		t.Fatalf("reasoning_effort = %q, want none (mapped off value)", req.ReasoningEffort)
	}
}

// z.ai's compat.zaiToolStream gate (openai-completions.js:600-603): tool_stream:true
// is only sent when there are tools AND the compat flag is set.
func TestZaiToolStream(t *testing.T) {
	model := reasoningModel("zai", map[string]any{"zaiToolStream": true})
	req := buildOpenAIRequest(model, simpleTranscript(), provider.StreamOptions{
		Tools: []provider.ToolDef{{Name: "search", Description: "search"}},
	})
	if req.ToolStream == nil || !*req.ToolStream {
		t.Fatalf("tool_stream = %v, want true", req.ToolStream)
	}

	req = buildOpenAIRequest(model, simpleTranscript(), provider.StreamOptions{})
	if req.ToolStream != nil {
		t.Fatalf("tool_stream = %v, want omitted with no tools", req.ToolStream)
	}
}

// openai-completions.js:395-420: reasoning_text is a third fallback field
// (after reasoning_content, reasoning) for providers that spell it that way.
func TestResponseReasoningTextFallback(t *testing.T) {
	body := []byte(`data: {"choices":[{"delta":{"reasoning_text":"thinking..."},"finish_reason":null}]}` + "\n\n" +
		`data: {"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n")
	srv := replaySSE(t, 200, "text/event-stream", body)
	model := costedModel(provider.ApiOpenAICompletions, srv.URL)
	client := &OpenAICompletionsClient{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := client.Stream(ctx, model, simpleTranscript(), provider.StreamOptions{}, Auth{APIKey: "k"})
	for range events {
	}
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	thinking, ok := final.Content[0].(msg.ThinkingContent)
	if !ok || thinking.Thinking != "thinking..." {
		t.Fatalf("content[0] = %+v, want thinking %q", final.Content[0], "thinking...")
	}
}
