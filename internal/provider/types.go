// Package provider defines the shapes shared by every model provider: the
// API a model speaks, the Model metadata pi-ai's generated catalog carries
// per model, the Provider interface the harness streams through, and the
// Registry that composes providers into one place.
//
// Model and the compat structs mirror
// node_modules/@earendil-works/pi-ai/dist/types.d.ts faithfully (field names,
// optionality, defaults) because the generated catalog JSON is decoded
// straight into these types.
package provider

import (
	"context"
	"encoding/json"

	"github.com/andrepato/harness/internal/msg"
)

// Api is one of pi-ai's ten known API shapes. Custom strings are also valid
// (pi's Api type is KnownApi | string), so Api is a plain string type rather
// than a closed enum.
type Api string

const (
	ApiAnthropicMessages     Api = "anthropic-messages"
	ApiOpenAICompletions     Api = "openai-completions"
	ApiOpenAIResponses       Api = "openai-responses"
	ApiOpenAICodexResponses  Api = "openai-codex-responses"
	ApiAzureOpenAIResponses  Api = "azure-openai-responses"
	ApiGoogleGenerativeAI    Api = "google-generative-ai"
	ApiGoogleVertex          Api = "google-vertex"
	ApiBedrockConverseStream Api = "bedrock-converse-stream"
	ApiMistralConversations  Api = "mistral-conversations"
	ApiPiMessages            Api = "pi-messages"
)

// ThinkingLevel is a reasoning effort level a model can be asked for.
// "off" (ModelThinkingLevel in pi-ai) is included alongside pi's
// ThinkingLevel ("minimal".."max") because StreamOptions carries either.
type ThinkingLevel string

const (
	ThinkingOff     ThinkingLevel = "off"
	ThinkingMinimal ThinkingLevel = "minimal"
	ThinkingLow     ThinkingLevel = "low"
	ThinkingMedium  ThinkingLevel = "medium"
	ThinkingHigh    ThinkingLevel = "high"
	ThinkingXHigh   ThinkingLevel = "xhigh"
	ThinkingMax     ThinkingLevel = "max"
)

// ModelCostRates is USD per million tokens.
type ModelCostRates struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// ModelCostTier is a request-wide pricing tier: the highest matching input
// threshold applies to the full request.
type ModelCostTier struct {
	ModelCostRates
	InputTokensAbove int `json:"inputTokensAbove"`
}

// ModelCost is a model's pricing, with optional volume tiers.
type ModelCost struct {
	ModelCostRates
	Tiers []ModelCostTier `json:"tiers,omitempty"`
}

// ThinkingLevelMap maps pi thinking levels to provider/model-specific
// values. Missing keys use provider defaults. A null value (represented here
// by a present key with a nil *string) marks a level as unsupported.
type ThinkingLevelMap map[ThinkingLevel]*string

// ModelPromptCache is the best-effort prompt cache lifetime in seconds for
// each retention tier a request can ask for ("short", "long"). A missing tier
// means the lifetime is unknown.
type ModelPromptCache struct {
	Short *int `json:"short,omitempty"`
	Long  *int `json:"long,omitempty"`
}

// OpenAICompletionsCompat is the subset of pi-ai's OpenAICompletionsCompat
// (types.d.ts) the harness reads. Fields beyond this subset still round-trip
// through Model.Compat (json.RawMessage) unmodified; they are simply not
// exposed as typed accessors because nothing in the harness branches on them
// this phase.
type OpenAICompletionsCompat struct {
	// SupportsStore: whether the provider supports the `store` field.
	// Default: auto-detected from URL.
	SupportsStore *bool `json:"supportsStore,omitempty"`
	// SupportsDeveloperRole: whether the provider supports the `developer`
	// role (vs `system`). Default: auto-detected from URL.
	SupportsDeveloperRole *bool `json:"supportsDeveloperRole,omitempty"`
	// SupportsReasoningEffort: whether the provider supports
	// `reasoning_effort`. Default: auto-detected from URL.
	SupportsReasoningEffort *bool `json:"supportsReasoningEffort,omitempty"`
	// SupportsUsageInStreaming: whether the provider supports
	// `stream_options: { include_usage: true }`. Default: true.
	SupportsUsageInStreaming *bool `json:"supportsUsageInStreaming,omitempty"`
	// SupportsStrictMode: whether the provider supports the `strict` field
	// in tool definitions. Default: false.
	SupportsStrictMode *bool `json:"supportsStrictMode,omitempty"`
	// MaxTokensField: which field to use for max tokens
	// ("max_completion_tokens" | "max_tokens"). Default: auto-detected.
	MaxTokensField string `json:"maxTokensField,omitempty"`
	// ThinkingFormat: format for the reasoning/thinking parameter. Default:
	// "openai". See openai-completions.js:600-720 for the per-format request
	// shape; ported in openai_completions.go's reasoning block.
	ThinkingFormat string `json:"thinkingFormat,omitempty"`
	// ChatTemplateKwargs: kwargs to send as `chat_template_kwargs` when
	// ThinkingFormat is "chat-template". A value may be an object shaped
	// like pi's `ChatTemplateKwargValue`, e.g. `{"$var": "thinking.enabled"}`
	// or `{"$var": "thinking.budget"}` (decoded here as
	// `map[string]any{"$var": "thinking.enabled"}`), which the client
	// substitutes with a pi-controlled thinking value at request time
	// (resolveChatTemplateKwargValue); any other value passes through
	// unchanged.
	ChatTemplateKwargs map[string]any `json:"chatTemplateKwargs,omitempty"`
	// ChatTemplateArgs: same as ChatTemplateKwargs but for `chat_template_args`
	// when ThinkingFormat is "baseten".
	ChatTemplateArgs map[string]any `json:"chatTemplateArgs,omitempty"`
	// ZaiToolStream: whether z.ai supports top-level `tool_stream: true` for
	// streaming tool call deltas. Default: false.
	ZaiToolStream *bool `json:"zaiToolStream,omitempty"`
}

// AnthropicMessagesCompat is the subset of pi-ai's AnthropicMessagesCompat
// the harness reads, mirrored the same way as OpenAICompletionsCompat above.
type AnthropicMessagesCompat struct {
	// SupportsEagerToolInputStreaming: whether the provider accepts per-tool
	// `eager_input_streaming`. Default: true.
	SupportsEagerToolInputStreaming *bool `json:"supportsEagerToolInputStreaming,omitempty"`
	// SupportsLongCacheRetention: whether the provider supports
	// `cache_control.ttl: "1h"`. Default: true.
	SupportsLongCacheRetention *bool `json:"supportsLongCacheRetention,omitempty"`
	// SupportsCacheControlOnTools: whether cache_control markers are
	// accepted on tool definitions. Default: true.
	SupportsCacheControlOnTools *bool `json:"supportsCacheControlOnTools,omitempty"`
	// SupportsTemperature: whether the model accepts the `temperature`
	// field. Default: true.
	SupportsTemperature *bool `json:"supportsTemperature,omitempty"`
	// SupportsStrictTools: whether the provider supports Anthropic strict
	// tool schemas. Default: false.
	SupportsStrictTools *bool `json:"supportsStrictTools,omitempty"`
	// ForceAdaptiveThinking: force adaptive thinking regardless of model id.
	// Default: false.
	ForceAdaptiveThinking *bool `json:"forceAdaptiveThinking,omitempty"`
	// AllowEmptySignature: replay empty thinking signatures as
	// `signature: ""` instead of converting thinking to text. Default: false.
	AllowEmptySignature *bool `json:"allowEmptySignature,omitempty"`
}

// OpenAIResponsesCompat is the subset of pi-ai's OpenAIResponsesCompat
// (types.d.ts) the harness reads. It is shared by openai-responses,
// openai-codex-responses and azure-openai-responses (types.d.ts:829 maps all
// three Apis to this one compat type). Fields beyond this subset still
// round-trip through Model.Compat unmodified.
type OpenAIResponsesCompat struct {
	// SupportsDeveloperRole: whether the provider supports the `developer`
	// role (vs `system`) for instructions. Default: true.
	SupportsDeveloperRole *bool `json:"supportsDeveloperRole,omitempty"`
	// SupportsMidConvoSystemMessages: whether the exact model accepts
	// developer/system messages after the conversation has started. When
	// false, later system messages are folded into the leading system
	// message. Default: false.
	SupportsMidConvoSystemMessages *bool `json:"supportsMidConvoSystemMessages,omitempty"`
	// SupportsLongCacheRetention: whether the provider supports long prompt
	// cache retention (prompt_cache_retention: "24h" on models without
	// SupportsExplicitPromptCacheMode). Default: true.
	SupportsLongCacheRetention *bool `json:"supportsLongCacheRetention,omitempty"`
	// SupportsStrictMode: whether the provider supports strict JSON-schema
	// function tools. Default: false.
	SupportsStrictMode *bool `json:"supportsStrictMode,omitempty"`
	// SupportsExplicitPromptCacheMode: whether the model accepts
	// `prompt_cache_options` (OpenAI GPT-5.6+ prompt caching). Default: false.
	SupportsExplicitPromptCacheMode *bool `json:"supportsExplicitPromptCacheMode,omitempty"`
	// SupportsMaxOutputTokens: whether the provider accepts the
	// `max_output_tokens` parameter. Default: true.
	SupportsMaxOutputTokens *bool `json:"supportsMaxOutputTokens,omitempty"`
}

// MistralConversationsCompat is the subset of pi-ai's
// MistralConversationsCompat the harness reads.
type MistralConversationsCompat struct {
	// SupportsMidConvoSystemMessages: whether the exact model accepts system
	// messages after the conversation has started. Default: false.
	SupportsMidConvoSystemMessages *bool `json:"supportsMidConvoSystemMessages,omitempty"`
}

// OpenAIResponsesCompat decodes Model.Compat as an OpenAIResponsesCompat.
// Returns the zero value (all defaults) if Compat is empty or does not
// decode. Shared by openai-responses, openai-codex-responses and
// azure-openai-responses.
func (m Model) OpenAIResponsesCompat() OpenAIResponsesCompat {
	var c OpenAIResponsesCompat
	if len(m.Compat) == 0 {
		return c
	}
	_ = json.Unmarshal(m.Compat, &c)
	return c
}

// MistralConversationsCompat decodes Model.Compat as a
// MistralConversationsCompat. Returns the zero value if Compat is empty or
// does not decode.
func (m Model) MistralConversationsCompat() MistralConversationsCompat {
	var c MistralConversationsCompat
	if len(m.Compat) == 0 {
		return c
	}
	_ = json.Unmarshal(m.Compat, &c)
	return c
}

// boolOr returns *b if b is non-nil, else def.
func boolOr(b *bool, def bool) bool {
	if b == nil {
		return def
	}
	return *b
}

// SupportsStore reports Compat.SupportsStore, defaulting to false (pi
// auto-detects this from the base URL; the harness has no URL heuristic
// this phase, so an explicit compat override is required).
func (m Model) SupportsStore() bool {
	c := m.OpenAICompletionsCompat()
	return boolOr(c.SupportsStore, false)
}

// SupportsDeveloperRole reports Compat.SupportsDeveloperRole, default false.
func (m Model) SupportsDeveloperRole() bool {
	c := m.OpenAICompletionsCompat()
	return boolOr(c.SupportsDeveloperRole, false)
}

// SupportsReasoningEffort reports Compat.SupportsReasoningEffort, default false.
func (m Model) SupportsReasoningEffort() bool {
	c := m.OpenAICompletionsCompat()
	return boolOr(c.SupportsReasoningEffort, false)
}

// SupportsUsageInStreaming reports Compat.SupportsUsageInStreaming, default true.
func (m Model) SupportsUsageInStreaming() bool {
	c := m.OpenAICompletionsCompat()
	return boolOr(c.SupportsUsageInStreaming, true)
}

// SupportsStrictMode reports the strict-tool-schema compat flag for m.Api,
// with pi's per-API default (whichever compat applies to m.Api is
// consulted):
//   - anthropic-messages: AnthropicMessagesCompat.SupportsStrictTools, default false.
//   - openai-codex-responses (`model.compat?.supportsStrictMode ?? true`,
//     openai-codex-responses.js:374) and azure-openai-responses
//     (`model.compat?.supportsStrictMode ?? true`, azure-openai-responses.js
//     buildParams): OpenAIResponsesCompat.SupportsStrictMode, default true.
//   - openai-responses (`model.compat?.supportsStrictMode ?? false`,
//     openai-responses.js getCompat): OpenAIResponsesCompat.SupportsStrictMode,
//     default false.
//   - everything else (openai-completions, mistral-conversations, ...):
//     OpenAICompletionsCompat.SupportsStrictMode, default false.
func (m Model) SupportsStrictMode() bool {
	switch m.Api {
	case ApiAnthropicMessages:
		c := m.AnthropicMessagesCompat()
		return boolOr(c.SupportsStrictTools, false)
	case ApiOpenAICodexResponses, ApiAzureOpenAIResponses:
		c := m.OpenAIResponsesCompat()
		return boolOr(c.SupportsStrictMode, true)
	case ApiOpenAIResponses:
		c := m.OpenAIResponsesCompat()
		return boolOr(c.SupportsStrictMode, false)
	default:
		c := m.OpenAICompletionsCompat()
		return boolOr(c.SupportsStrictMode, false)
	}
}

// MaxTokensField reports Compat.MaxTokensField, default "max_tokens".
func (m Model) MaxTokensField() string {
	c := m.OpenAICompletionsCompat()
	if c.MaxTokensField != "" {
		return c.MaxTokensField
	}
	return "max_tokens"
}

// ThinkingFormat reports Compat.ThinkingFormat, default "openai".
func (m Model) ThinkingFormat() string {
	c := m.OpenAICompletionsCompat()
	if c.ThinkingFormat != "" {
		return c.ThinkingFormat
	}
	return "openai"
}

// OpenAICompletionsCompat decodes Model.Compat as an OpenAICompletionsCompat.
// Returns the zero value (all defaults) if Compat is empty or does not
// decode.
func (m Model) OpenAICompletionsCompat() OpenAICompletionsCompat {
	var c OpenAICompletionsCompat
	if len(m.Compat) == 0 {
		return c
	}
	_ = json.Unmarshal(m.Compat, &c)
	return c
}

// AnthropicMessagesCompat decodes Model.Compat as an AnthropicMessagesCompat.
// Returns the zero value (all defaults) if Compat is empty or does not
// decode.
func (m Model) AnthropicMessagesCompat() AnthropicMessagesCompat {
	var c AnthropicMessagesCompat
	if len(m.Compat) == 0 {
		return c
	}
	_ = json.Unmarshal(m.Compat, &c)
	return c
}

// Model mirrors pi-ai's Model<TApi> (types.d.ts). Field names and JSON tags
// match pi's exactly since the generated catalog JSON decodes straight into
// this struct.
type Model struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Api       Api    `json:"api"`
	Provider  string `json:"provider"`
	BaseURL   string `json:"baseUrl"`
	Reasoning bool   `json:"reasoning"`
	// ThinkingLevelMap maps pi thinking levels to provider/model-specific
	// values.
	ThinkingLevelMap ThinkingLevelMap  `json:"thinkingLevelMap,omitempty"`
	Input            []string          `json:"input"`
	Cost             ModelCost         `json:"cost"`
	PromptCache      *ModelPromptCache `json:"promptCache,omitempty"`
	ContextWindow    int               `json:"contextWindow"`
	MaxTokens        int               `json:"maxTokens"`
	// SamplingParams are default sampling parameters for this model;
	// per-request keys override these.
	SamplingParams map[string]any    `json:"samplingParams,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	// Compat carries the API-specific compat overrides verbatim. Use
	// OpenAICompletionsCompat()/AnthropicMessagesCompat() or the typed
	// accessors above rather than decoding this directly.
	Compat json.RawMessage `json:"compat,omitempty"`
}

// ToolDef is a provider-neutral tool declaration passed to Stream.
type ToolDef struct {
	Name        string
	Description string
	// Parameters is the tool's JSON Schema, already serialized. Each API
	// client maps this into its own tool-definition shape.
	Parameters json.RawMessage
	// ServerTool, when non-nil, is a provider-native tool declaration
	// (e.g. Anthropic's {"type":"web_search_20250305","name":"web_search",
	// "max_uses":5}) to be sent verbatim in place of the Name/Parameters
	// function schema. Only the client whose wire format this JSON is
	// written for should ever send it; every other client must skip a
	// ToolDef with ServerTool set rather than declare it as a function
	// tool, since the tool has no schema-callable implementation there.
	ServerTool json.RawMessage
}

// StreamOptions configures one Stream call. It mirrors the subset of pi-ai's
// StreamOptions/SimpleStreamOptions the harness drives; cancellation is
// carried on the context passed to Stream rather than as a `signal` field.
type StreamOptions struct {
	ThinkingLevel ThinkingLevel
	MaxTokens     int
	Temperature   *float64
	Tools         []ToolDef
	SystemPrompt  string
}

// AuthKind is the flavor of credential a provider's auth needs.
type AuthKind string

const (
	AuthKindAPIKey AuthKind = "api_key"
	AuthKindOAuth  AuthKind = "oauth"
	AuthKindNone   AuthKind = "none"
)

// AuthSpec describes how a provider authenticates: which env vars hold an
// API key, and whether it also supports (or requires) OAuth login.
type AuthSpec struct {
	Kind AuthKind
	// EnvVars are environment variable names checked, in order, for an API
	// key. Empty for OAuth-only providers.
	EnvVars []string
	// IsSubscription marks OAuth providers backed by a plan rather than
	// metered API usage (e.g. Anthropic Claude Pro/Max), matching pi-ai's
	// auth.oauth.isSubscription.
	IsSubscription bool
}

// StreamInterrupted marks a stream that ended abnormally before its
// terminal event -- an unexpected EOF or another read error while the
// connection was mid-response, as opposed to a clean stream end (a normal
// io.EOF after the terminal SSE/JSON event was already parsed, or a
// context cancellation). It is always retriable, the same as a
// 429/529/5xx status: retry.go's isRetriable recognizes it explicitly so a
// mid-stream disconnect is retried like an overloaded/5xx response,
// instead of surfacing as a bare "unexpected EOF" that isRetriable's
// string-sniffing fallback does not recognize.
type StreamInterrupted struct {
	// Cause is the underlying read/transport error (e.g. io.ErrUnexpectedEOF).
	Cause error
}

func (e StreamInterrupted) Error() string {
	if e.Cause == nil {
		return "provider: stream interrupted"
	}
	return "provider: stream interrupted: " + e.Cause.Error()
}

// Unwrap exposes Cause to errors.Is/errors.As.
func (e StreamInterrupted) Unwrap() error { return e.Cause }

// Provider is one model backend the harness can stream through.
type Provider interface {
	ID() string
	Name() string
	Auth() AuthSpec
	Models() []Model
	// RefreshModels re-fetches this provider's model list (e.g. Ollama's
	// /api/tags). Providers with a static, catalog-sourced list may return
	// nil.
	RefreshModels(ctx context.Context) error
	// Stream starts a completion. The returned channel is closed when the
	// stream ends (after a "done" or "error" event); wait blocks until then
	// and returns the final assistant message or the error that ended the
	// stream.
	Stream(ctx context.Context, model Model, transcript []msg.Message, opts StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error))
}
