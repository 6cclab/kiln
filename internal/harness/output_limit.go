package harness

import (
	"fmt"

	"github.com/andrepato/harness/internal/msg"
)

// maxOutputLimitRecoveries is how many times in one operation a reply that
// stopped at the output token limit, with no tool call, is continued
// before the turn ends with a notice. Claude Code (2.1.319) allows 3.
const maxOutputLimitRecoveries = 3

// outputLimitSource marks the continuation message (msg.UserMessage.
// KilnHook): the model reads it, but no user wrote it, so it never
// replays as a "you" block.
const outputLimitSource = "OutputLimit"

// The two continuation messages are Claude Code's own wording, the second
// adapted: kiln keeps a cut-off reasoning block in the conversation.
const (
	outputLimitResume = "Output token limit hit. Resume directly — no apology, no recap of what you were doing. " +
		"Pick up mid-thought if that is where the cut happened. Break remaining work into smaller pieces."
	outputLimitWhileReasoning = "Output token limit hit while you were still reasoning, before this response produced any text " +
		"or tool call. Reason much more briefly and produce your answer or next tool call well within the output limit."
)

// outputLimitContinuation is the message that asks the model to go on
// after final stopped at the output limit.
func outputLimitContinuation(final *msg.AssistantMessage) string {
	for _, c := range final.Content {
		if t, ok := c.(msg.TextContent); ok && t.Text != "" {
			return outputLimitResume
		}
	}
	return outputLimitWhileReasoning
}

// outputLimitError is the fault shown when the reply still stopped at the
// limit after every continuation. Its message is a sentence for the user.
type outputLimitError struct {
	tokens, times int
}

func (e outputLimitError) Error() string {
	return fmt.Sprintf("The reply stopped at the output token limit (%d tokens) %d times in a row, so kiln stopped asking the model to continue. "+
		"Raise the model's output limit, or ask for the work in smaller pieces.", e.tokens, e.times)
}

// outputLimitExhausted is that fault for final, the last cut reply.
func outputLimitExhausted(final *msg.AssistantMessage) error {
	return outputLimitError{tokens: final.Usage.Output, times: maxOutputLimitRecoveries + 1}
}
