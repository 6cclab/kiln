// Package msg holds the message and content types shared by the session
// store, the provider layer and the agent loop.
//
// The shapes mirror pi-ai's types.d.ts (TextContent, ThinkingContent,
// ImageContent, ToolCall, Usage, UserMessage, AssistantMessage,
// ToolResultMessage, SystemMessage) because sessions on disk hold these
// messages verbatim and the TypeScript harness must keep reading them.
// Field names in JSON are pi's, not Go's. Struct fields are declared in
// alphabetical order so that encoded output matches pi's key order.
package msg

import (
	"encoding/json"
	"fmt"
)

// Role is the discriminator on a Message.
type Role string

const (
	RoleSystem     Role = "system"
	RoleUser       Role = "user"
	RoleAssistant  Role = "assistant"
	RoleToolResult Role = "toolResult"
)

// StopReason is why an assistant message ended.
type StopReason string

const (
	StopPending  StopReason = "pending"
	StopStop     StopReason = "stop"
	StopLength   StopReason = "length"
	StopToolUse  StopReason = "toolUse"
	StopError    StopReason = "error"
	StopAborted  StopReason = "aborted"
	StopDeferred StopReason = "deferred"
	// StopPause is Anthropic's "pause_turn": a long server-tool turn (e.g.
	// an extended web search) was cut off for interim delivery, not
	// finished. The caller must re-send the conversation, including the
	// partial assistant message this stop reason came with, to let the
	// turn continue -- see internal/harness/turn.go's drive().
	StopPause StopReason = "pause"
)

// Content is one block inside a message. Concrete types: TextContent,
// ThinkingContent, ImageContent, ToolCall.
type Content interface {
	contentType() string
}

// TextContent is a text block.
type TextContent struct {
	Text          string `json:"text"`
	TextSignature string `json:"textSignature,omitempty"`
	Type          string `json:"type"`
}

// ThinkingContent is a reasoning block.
type ThinkingContent struct {
	Redacted          bool   `json:"redacted,omitempty"`
	Thinking          string `json:"thinking"`
	ThinkingSignature string `json:"thinkingSignature,omitempty"`
	Type              string `json:"type"`
}

// ImageContent is a base64 image block.
type ImageContent struct {
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
	Type     string `json:"type"`
}

// ToolCall is a model-issued tool invocation.
type ToolCall struct {
	Arguments map[string]any `json:"arguments"`
	ID        string         `json:"id"`
	// InvalidArgs holds the raw, not-yet-JSON argument text when the
	// provider layer could not parse the model's accumulated tool-call
	// arguments as JSON (e.g. a truncated/malformed input_json_delta
	// stream). When set, Arguments is left empty rather than silently
	// defaulting to {}, so a consumer can tell "no arguments" apart from
	// "arguments the model sent were broken".
	//
	// TODO(harness/turn.go beginTool): turn.go must check
	// `call.InvalidArgs != ""` before executing a tool and, if set, return
	// an error tool.Result (e.g. tool.Errorf("tool call arguments were not
	// valid JSON: %s", call.InvalidArgs) or a more specific parse-error
	// message) instead of calling t.Execute with empty/zero-value args.
	// turn.go is owned by another agent this phase, so that two-line
	// change is not made here -- see this task's report.
	InvalidArgs      string `json:"invalidArgs,omitempty"`
	Name             string `json:"name"`
	Namespace        string `json:"namespace,omitempty"`
	ThoughtSignature string `json:"thoughtSignature,omitempty"`
	Type             string `json:"type"`
}

// ProviderBlock carries one provider-native content block verbatim: a
// block shape this harness does not model as its own content type (e.g.
// Anthropic's server_tool_use / web_search_tool_result), kept as raw JSON
// so it round-trips on disk and can be replayed to the provider that sent
// it exactly as it was received. Raw holds the block's own JSON object
// (its own "type" field is inside Raw, not read from this struct's Type
// field, which is always the fixed discriminator "providerBlock" so
// UnmarshalContent can route to this type).
//
// The harness turn loop (internal/harness/turn.go) must not treat this as
// a tool call: msg.ToolCallsOf only matches msg.ToolCall, so a
// ProviderBlock is silently invisible to it, which is exactly the
// "must not treat it as a tool call" requirement -- no extra guard needed.
type ProviderBlock struct {
	// Provider names which provider's wire format Raw is in (e.g.
	// "anthropic"). A provider's request converter must replay Raw
	// verbatim only when Provider matches its own name; every other
	// provider's converter leaves the block out of its request entirely
	// (a plain type switch over msg.Content already does this: none of
	// them have a case for ProviderBlock, so it falls through unmatched).
	Provider string          `json:"provider"`
	Raw      json.RawMessage `json:"raw"`
	Type     string          `json:"type"`
}

func (TextContent) contentType() string     { return "text" }
func (ThinkingContent) contentType() string { return "thinking" }
func (ImageContent) contentType() string    { return "image" }
func (ToolCall) contentType() string        { return "toolCall" }
func (ProviderBlock) contentType() string   { return "providerBlock" }

// Text builds a text block.
func Text(s string) TextContent { return TextContent{Text: s, Type: "text"} }

// Thinking builds a thinking block.
func Thinking(s string) ThinkingContent { return ThinkingContent{Thinking: s, Type: "thinking"} }

// Image builds an image block.
func Image(mime, data string) ImageContent {
	return ImageContent{Data: data, MimeType: mime, Type: "image"}
}

// NewToolCall builds a toolCall block.
func NewToolCall(id, name string, args map[string]any) ToolCall {
	if args == nil {
		args = map[string]any{}
	}
	return ToolCall{Arguments: args, ID: id, Name: name, Type: "toolCall"}
}

// Blocks is a list of content blocks with JSON (un)marshalling on the
// "type" discriminator. A bare JSON string decodes to a single text block,
// which is how pi allows `content: string` on user and system messages.
type Blocks []Content

// UnmarshalJSON implements json.Unmarshaler.
func (b *Blocks) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*b = Blocks{Text(s)}
		return nil
	}
	var raws []json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		return err
	}
	out := make(Blocks, 0, len(raws))
	for i, raw := range raws {
		c, err := UnmarshalContent(raw)
		if err != nil {
			return fmt.Errorf("content[%d]: %w", i, err)
		}
		out = append(out, c)
	}
	*b = out
	return nil
}

// UnmarshalContent decodes one content block by its "type".
func UnmarshalContent(raw []byte) (Content, error) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	switch probe.Type {
	case "text":
		var c TextContent
		return c, json.Unmarshal(raw, &c)
	case "thinking":
		var c ThinkingContent
		return c, json.Unmarshal(raw, &c)
	case "image":
		var c ImageContent
		return c, json.Unmarshal(raw, &c)
	case "toolCall":
		var c ToolCall
		return c, json.Unmarshal(raw, &c)
	case "providerBlock":
		var c ProviderBlock
		return c, json.Unmarshal(raw, &c)
	default:
		return nil, fmt.Errorf("unknown content type %q", probe.Type)
	}
}

// Cost is the priced breakdown of a Usage.
type Cost struct {
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	// Search is the cost of provider-executed server-tool searches (e.g.
	// Anthropic web_search at $10/1000 searches), folded into Total the
	// same as every other component. Zero for a request that used no
	// server tool; omitempty so an old session on disk (written before
	// this field existed) round-trips byte-for-byte unchanged.
	Search float64 `json:"search,omitempty"`
	Total  float64 `json:"total"`
}

// Usage is token accounting for one request or one aggregate.
type Usage struct {
	CacheRead    int  `json:"cacheRead"`
	CacheWrite   int  `json:"cacheWrite"`
	CacheWrite1h *int `json:"cacheWrite1h,omitempty"`
	Cost         Cost `json:"cost"`
	Input        int  `json:"input"`
	Output       int  `json:"output"`
	// Reasoning is a subset of Output. nil when the provider reports no split.
	Reasoning   *int `json:"reasoning,omitempty"`
	TotalTokens int  `json:"totalTokens"`
	// ServerToolUse is usage.server_tool_use from the Anthropic Messages
	// API, when the response reported any (currently only web search). nil
	// when the provider did not report this, distinct from a reported
	// zero.
	ServerToolUse *ServerToolUse `json:"serverToolUse,omitempty"`
}

// ServerToolUse is a count of provider-executed server-tool invocations
// within one request, priced separately from token usage.
type ServerToolUse struct {
	WebSearchRequests int `json:"webSearchRequests"`
}

// Add returns u plus o, field by field.
func (u Usage) Add(o Usage) Usage {
	out := u
	out.CacheRead += o.CacheRead
	out.CacheWrite += o.CacheWrite
	out.Input += o.Input
	out.Output += o.Output
	out.TotalTokens += o.TotalTokens
	out.Cost.CacheRead += o.Cost.CacheRead
	out.Cost.CacheWrite += o.Cost.CacheWrite
	out.Cost.Input += o.Cost.Input
	out.Cost.Output += o.Cost.Output
	out.Cost.Search += o.Cost.Search
	out.Cost.Total += o.Cost.Total
	if u.Reasoning != nil || o.Reasoning != nil {
		r := 0
		if u.Reasoning != nil {
			r += *u.Reasoning
		}
		if o.Reasoning != nil {
			r += *o.Reasoning
		}
		out.Reasoning = &r
	}
	if u.ServerToolUse != nil || o.ServerToolUse != nil {
		s := ServerToolUse{}
		if u.ServerToolUse != nil {
			s.WebSearchRequests += u.ServerToolUse.WebSearchRequests
		}
		if o.ServerToolUse != nil {
			s.WebSearchRequests += o.ServerToolUse.WebSearchRequests
		}
		out.ServerToolUse = &s
	}
	return out
}

// DeferredHandle identifies a provider-side deferred response.
type DeferredHandle struct {
	API         string          `json:"api"`
	Data        json.RawMessage `json:"data,omitempty"`
	ExpiresAt   *int64          `json:"expiresAt,omitempty"`
	ID          string          `json:"id"`
	ModelID     string          `json:"modelId"`
	PollAfterMs *int            `json:"pollAfterMs,omitempty"`
	Provider    string          `json:"provider"`
}

// Message is any transcript message. Concrete types: SystemMessage,
// UserMessage, AssistantMessage, ToolResultMessage.
type Message interface {
	MessageRole() Role
	// Timestamp returns the message's epoch milliseconds.
	MessageTimestamp() int64
}

// SystemMessage carries the system prompt and tool declarations inside a
// transcript context.
type SystemMessage struct {
	Content      Blocks             `json:"content"`
	Role         Role               `json:"role"`
	Sections     map[string]*string `json:"sections,omitempty"`
	Timestamp    int64              `json:"timestamp"`
	ToolsAdded   json.RawMessage    `json:"toolsAdded,omitempty"`
	ToolsRemoved json.RawMessage    `json:"toolsRemoved,omitempty"`
}

// UserMessage is a human turn.
type UserMessage struct {
	Content Blocks `json:"content"`
	// KilnTyped is the line the user typed, when kiln built Content around
	// it (hook context, @file contents, a command's expansion). kiln's own
	// field, absent from pi's type: written to the session so auto mode's
	// classifier can tell what the user said from what was attached, also
	// after a resume (internal/automode). Never sent to a provider. Empty
	// for a message no user typed (a subagent's delegated task, kiln's own
	// follow-ups) and in sessions written before it existed.
	KilnTyped string `json:"kilnTyped,omitempty"`
	Role      Role   `json:"role"`
	Timestamp int64  `json:"timestamp"`
}

// AssistantMessage is a model turn.
type AssistantMessage struct {
	API                   string          `json:"api"`
	Content               Blocks          `json:"content"`
	Deferred              *DeferredHandle `json:"deferred,omitempty"`
	Diagnostics           json.RawMessage `json:"diagnostics,omitempty"`
	EndTurn               *bool           `json:"endTurn,omitempty"`
	ErrorMessage          string          `json:"errorMessage,omitempty"`
	Model                 string          `json:"model"`
	Provider              string          `json:"provider"`
	ProviderThinkingLevel string          `json:"providerThinkingLevel,omitempty"`
	RawStopReason         string          `json:"rawStopReason,omitempty"`
	ResponseID            string          `json:"responseId,omitempty"`
	ResponseModel         string          `json:"responseModel,omitempty"`
	Role                  Role            `json:"role"`
	StopReason            StopReason      `json:"stopReason"`
	Timestamp             int64           `json:"timestamp"`
	Usage                 Usage           `json:"usage"`
}

// ToolResultMessage is the outcome of one tool call.
type ToolResultMessage struct {
	Content    Blocks          `json:"content"`
	Details    json.RawMessage `json:"details,omitempty"`
	IsError    bool            `json:"isError"`
	Role       Role            `json:"role"`
	Timestamp  int64           `json:"timestamp"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Usage      *Usage          `json:"usage,omitempty"`
}

func (m SystemMessage) MessageRole() Role     { return RoleSystem }
func (m UserMessage) MessageRole() Role       { return RoleUser }
func (m AssistantMessage) MessageRole() Role  { return RoleAssistant }
func (m ToolResultMessage) MessageRole() Role { return RoleToolResult }

func (m SystemMessage) MessageTimestamp() int64     { return m.Timestamp }
func (m UserMessage) MessageTimestamp() int64       { return m.Timestamp }
func (m AssistantMessage) MessageTimestamp() int64  { return m.Timestamp }
func (m ToolResultMessage) MessageTimestamp() int64 { return m.Timestamp }

// UnmarshalMessage decodes one message by its "role".
func UnmarshalMessage(raw []byte) (Message, error) {
	var probe struct {
		Role Role `json:"role"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	switch probe.Role {
	case RoleSystem:
		var m SystemMessage
		return m, json.Unmarshal(raw, &m)
	case RoleUser:
		var m UserMessage
		return m, json.Unmarshal(raw, &m)
	case RoleAssistant:
		var m AssistantMessage
		return m, json.Unmarshal(raw, &m)
	case RoleToolResult:
		var m ToolResultMessage
		return m, json.Unmarshal(raw, &m)
	default:
		return nil, fmt.Errorf("unknown message role %q", probe.Role)
	}
}

// TextOf joins the text blocks of a message, one per line, ignoring other
// block kinds. This is the "what did the assistant say" projection used by
// print mode, subagent results and the transcript.
func TextOf(blocks Blocks) string {
	out := ""
	for _, b := range blocks {
		if t, ok := b.(TextContent); ok {
			if out != "" {
				out += "\n"
			}
			out += t.Text
		}
	}
	return out
}

// ToolCallsOf returns the toolCall blocks in order.
func ToolCallsOf(blocks Blocks) []ToolCall {
	var out []ToolCall
	for _, b := range blocks {
		if c, ok := b.(ToolCall); ok {
			out = append(out, c)
		}
	}
	return out
}
