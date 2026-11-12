package cli

import "github.com/andrepato/harness/internal/msg"

// appendRuntimeContext keeps volatile retrieval and task state at the end of
// the request. Rewriting a historical user message would invalidate the cache
// prefix through every later tool turn. The context stays unstored so permission,
// source-freshness and branch changes still take effect on the next request.
func appendRuntimeContext(transcript []msg.Message, text string) []msg.Message {
	out := make([]msg.Message, len(transcript), len(transcript)+1)
	copy(out, transcript)
	return append(out, msg.UserMessage{
		Role:     msg.RoleUser,
		Content:  msg.Blocks{msg.Text(text)},
		KilnHook: "RuntimeContext", // Never use derived context as a user query.
	})
}
