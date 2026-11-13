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

// answeringUser reports whether this request is the first one after a user
// message (typed, steered or a channel push): scanning back from the end, a
// user message comes before any assistant reply or tool result. Derived
// context such as memory and hook messages (KilnHook set) is skipped.
//
// Skill and capability suggestions go only into that request. Appended to
// every step of a tool loop, they were the newest thing the model saw after
// each tool result, so it answered them again and again ("these suggestions
// only matched on 'ok, go'", 11 times in one 42-message session).
func answeringUser(transcript []msg.Message) bool {
	for i := len(transcript) - 1; i >= 0; i-- {
		user, ok := transcript[i].(msg.UserMessage)
		if !ok {
			return false
		}
		if user.KilnHook == "" {
			return true
		}
	}
	return false
}
