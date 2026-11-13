package cli

import (
	"github.com/andrepato/harness/internal/msg"
)

// modsMessages lists a conversation the way claude-code.d.ts's
// $.session.messages does: one { role, text, toolUses } per user or assistant
// message, a tool's result folded into the assistant's toolUse that asked for
// it ({ result, text, isError }). With api, ApiMessage instead: { role,
// content } with the blocks intact, a tool result a user-role tool_result
// block. The system prompt and kiln's own rows are not messages.
func modsMessages(msgs []msg.Message, api bool) []any {
	if api {
		return modsAPIMessages(msgs)
	}
	uses := map[string]map[string]any{}
	out := []any{}
	for _, m := range msgs {
		switch v := derefMessage(m).(type) {
		case msg.UserMessage:
			out = append(out, map[string]any{"role": "user", "text": msg.TextOf(v.Content), "toolUses": []any{}})
		case msg.AssistantMessage:
			calls := []any{}
			for _, c := range msg.ToolCallsOf(v.Content) {
				row := map[string]any{"tool_use_id": c.ID, "tool": claudeToolName(c.Name), "input": orEmptyArgs(c.Arguments)}
				uses[c.ID] = row
				calls = append(calls, row)
			}
			out = append(out, map[string]any{"role": "assistant", "text": msg.TextOf(v.Content), "toolUses": calls})
		case msg.ToolResultMessage:
			if row, ok := uses[v.ToolCallID]; ok {
				text := msg.TextOf(v.Content)
				row["result"] = text
				row["text"] = text
				if v.IsError {
					row["isError"] = true
				}
			}
		}
	}
	return out
}

func modsAPIMessages(msgs []msg.Message) []any {
	out := []any{}
	for _, m := range msgs {
		switch v := derefMessage(m).(type) {
		case msg.UserMessage:
			out = append(out, map[string]any{"role": "user", "content": apiBlocks(v.Content)})
		case msg.AssistantMessage:
			out = append(out, map[string]any{"role": "assistant", "content": apiBlocks(v.Content)})
		case msg.ToolResultMessage:
			block := map[string]any{"type": "tool_result", "tool_use_id": v.ToolCallID, "content": apiBlocks(v.Content)}
			if v.IsError {
				block["is_error"] = true
			}
			out = append(out, map[string]any{"role": "user", "content": []any{block}})
		}
	}
	return out
}

func apiBlocks(blocks msg.Blocks) []any {
	out := []any{}
	for _, b := range blocks {
		switch c := b.(type) {
		case msg.TextContent:
			out = append(out, map[string]any{"type": "text", "text": c.Text})
		case msg.ThinkingContent:
			out = append(out, map[string]any{"type": "thinking", "thinking": c.Thinking})
		case msg.ImageContent:
			out = append(out, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": c.MimeType, "data": c.Data}})
		case msg.ToolCall:
			out = append(out, map[string]any{"type": "tool_use", "id": c.ID, "name": claudeToolName(c.Name), "input": orEmptyArgs(c.Arguments)})
		}
	}
	return out
}

func orEmptyArgs(a map[string]any) map[string]any {
	if a == nil {
		return map[string]any{}
	}
	return a
}

// derefMessage turns a *UserMessage and the like into the value form.
func derefMessage(m msg.Message) msg.Message {
	switch v := m.(type) {
	case *msg.UserMessage:
		if v != nil {
			return *v
		}
	case *msg.AssistantMessage:
		if v != nil {
			return *v
		}
	case *msg.ToolResultMessage:
		if v != nil {
			return *v
		}
	}
	return m
}
