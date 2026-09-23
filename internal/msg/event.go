package msg

// EventType is the kind of an assistant stream event.
type EventType string

// Stream event kinds, in the order a well-formed stream may emit them:
// start, then any of the *_start/*_delta/*_end groups, then done or error.
const (
	EventStart         EventType = "start"
	EventTextStart     EventType = "text_start"
	EventTextDelta     EventType = "text_delta"
	EventTextEnd       EventType = "text_end"
	EventThinkingStart EventType = "thinking_start"
	EventThinkingDelta EventType = "thinking_delta"
	EventThinkingEnd   EventType = "thinking_end"
	EventToolCallStart EventType = "toolcall_start"
	EventToolCallDelta EventType = "toolcall_delta"
	EventToolCallEnd   EventType = "toolcall_end"
	EventDone          EventType = "done"
	EventError         EventType = "error"
)

// StreamEvent is one item of an assistant response stream. It mirrors
// pi-ai's AssistantMessageEvent. Partial points at the shared, live
// response-so-far message that the producer mutates in place; it is not a
// snapshot. Message is set on done, Error on error.
type StreamEvent struct {
	Type         EventType
	ContentIndex int
	// Delta carries text_delta, thinking_delta and toolcall_delta payloads.
	Delta string
	// Content carries the final text of text_end and thinking_end.
	Content string
	// ToolCall is set on toolcall_end.
	ToolCall *ToolCall
	// Reason is set on done (stop, length, toolUse, deferred) and on error
	// (aborted, error).
	Reason  StopReason
	Partial *AssistantMessage
	Message *AssistantMessage
	Error   *AssistantMessage
}
