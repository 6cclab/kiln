package compaction

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// Streamer is the minimal model-calling surface Compact and SummarizeBranch
// need. Its Stream method has the same signature as provider.Provider's, so
// any provider.Provider already satisfies Streamer; tests use a fake.
type Streamer interface {
	Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error))
}

// Error is returned by Compact and SummarizeBranch. Code is a
// backend-independent code: "aborted" or "summarization_failed", matching
// pi's CompactionError/BranchSummaryError (harness/types.js). pi models
// these as two distinct Error subclasses (by name); this port uses one type
// for both, since nothing here dispatches on the class name.
type Error struct {
	Code    string
	Message string
	Cause   error
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Cause }

// SUMMARIZATION_SYSTEM_PROMPT is pi's SUMMARIZATION_SYSTEM_PROMPT
// (harness/compaction/compaction.js), copied verbatim.
const SummarizationSystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`

// summarizationPrompt is pi's SUMMARIZATION_PROMPT, copied verbatim.
const summarizationPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

// updateSummarizationPrompt is pi's UPDATE_SUMMARIZATION_PROMPT, copied
// verbatim.
const updateSummarizationPrompt = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

Update the existing structured summary with new information. RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context from the new messages
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Next Steps" based on what was accomplished
- PRESERVE exact file paths, function names, and error messages
- If something is no longer relevant, you may remove it

Use this EXACT format:

## Goal
[Preserve existing goals, add new ones if the task expanded]

## Constraints & Preferences
- [Preserve existing, add new ones discovered]

## Progress
### Done
- [x] [Include previously done items AND newly completed items]

### In Progress
- [ ] [Current work - update based on progress]

### Blocked
- [Current blockers - remove if resolved]

## Key Decisions
- **[Decision]**: [Brief rationale] (preserve all previous, add new)

## Next Steps
1. [Update based on current state]

## Critical Context
- [Preserve important context, add new if needed]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

// turnPrefixSummarizationPrompt is pi's TURN_PREFIX_SUMMARIZATION_PROMPT,
// copied verbatim.
const turnPrefixSummarizationPrompt = `This is the PREFIX of a turn that was too large to keep. The SUFFIX (recent work) is retained.

Summarize the prefix to provide context for the retained suffix:

## Original Request
[What did the user ask for in this turn?]

## Early Progress
- [Key decisions and work done in the prefix]

## Context for Suffix
- [Information needed to understand the retained recent work]

Be concise. Focus on what's needed to understand the kept suffix.`

// Result is pi's CompactResult (harness/compaction/compaction.d.ts).
type Result struct {
	// Summary is the text that replaces compacted history in future
	// context, including any trailing <read-files>/<modified-files> tags.
	Summary string
	// TokensBefore is Preparation.TokensBefore, carried through.
	TokensBefore int
	// Usage is the LLM usage from the call(s) that generated Summary.
	Usage msg.Usage
	// RetainedTail is Preparation.RetainedTail, carried through: it becomes
	// the new session.EntryCompaction's RetainedTail.
	RetainedTail []msg.Message
	// Details is the file-operation summary to store on the new
	// session.EntryCompaction's Details field.
	Details CompactionDetails
}

// CompactionDetails is pi's CompactionDetails
// (harness/compaction/compaction.d.ts).
type CompactionDetails struct {
	ReadFiles     []string `json:"readFiles"`
	ModifiedFiles []string `json:"modifiedFiles"`
}

// maxTokensFor is pi's inline `Math.min(Math.floor(fraction * reserveTokens),
// model.maxTokens > 0 ? model.maxTokens : Infinity)`.
func maxTokensFor(fraction float64, reserveTokens int, modelMaxTokens int) int {
	m := int(math.Floor(fraction * float64(reserveTokens)))
	if modelMaxTokens > 0 && modelMaxTokens < m {
		return modelMaxTokens
	}
	return m
}

// runSimple sends one non-tool user-role request through streamer and
// returns the assistant's text and usage. It is this port's stand-in for
// pi's completeSimpleWithRetries + retryAssistantCall: no retry policy, no
// telemetry context, since neither exists on this side of the port yet.
func runSimple(ctx context.Context, streamer Streamer, model provider.Model, systemPrompt, userText string, maxTokens int, thinkingLevel provider.ThinkingLevel) (string, msg.Usage, error) {
	opts := provider.StreamOptions{SystemPrompt: systemPrompt, MaxTokens: maxTokens}
	if model.Reasoning && thinkingLevel != "" && thinkingLevel != provider.ThinkingOff {
		opts.ThinkingLevel = thinkingLevel
	}
	transcript := []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text(userText)}, Timestamp: time.Now().UnixMilli()},
	}
	ch, wait := streamer.Stream(ctx, model, transcript, opts)
	for range ch {
		// Drain; Compact only needs the final assistant message.
	}
	am, err := wait()
	if err != nil {
		return "", msg.Usage{}, err
	}
	if am == nil {
		return "", msg.Usage{}, &Error{Code: "summarization_failed", Message: "summarization failed: no response"}
	}
	switch am.StopReason {
	case msg.StopAborted:
		message := am.ErrorMessage
		if message == "" {
			message = "Summarization aborted"
		}
		return "", msg.Usage{}, &Error{Code: "aborted", Message: message}
	case msg.StopError:
		message := am.ErrorMessage
		if message == "" {
			message = "Unknown error"
		}
		return "", msg.Usage{}, &Error{Code: "summarization_failed", Message: fmt.Sprintf("Summarization failed: %s", message)}
	}
	return msg.TextOf(am.Content), am.Usage, nil
}

// generateSummary is pi's generateSummaryWithRequest (harness/compaction/
// compaction.js), specialized to a Streamer.
func generateSummary(ctx context.Context, streamer Streamer, model provider.Model, reserveTokens int, customInstructions, previousSummary *string, thinkingLevel provider.ThinkingLevel, currentMessages []msg.Message) (string, msg.Usage, error) {
	basePrompt := summarizationPrompt
	if previousSummary != nil {
		basePrompt = updateSummarizationPrompt
	}
	if customInstructions != nil && *customInstructions != "" {
		basePrompt = basePrompt + "\n\nAdditional focus: " + *customInstructions
	}

	conversationText := SerializeConversation(currentMessages)
	promptText := "<conversation>\n" + conversationText + "\n</conversation>\n\n"
	if previousSummary != nil {
		promptText += "<previous-summary>\n" + *previousSummary + "\n</previous-summary>\n\n"
	}
	promptText += basePrompt

	maxTokens := maxTokensFor(0.8, reserveTokens, model.MaxTokens)
	return runSimple(ctx, streamer, model, SummarizationSystemPrompt, promptText, maxTokens, thinkingLevel)
}

// generateTurnPrefixSummary is pi's generateTurnPrefixSummary.
func generateTurnPrefixSummary(ctx context.Context, streamer Streamer, model provider.Model, reserveTokens int, thinkingLevel provider.ThinkingLevel, messages []msg.Message) (string, msg.Usage, error) {
	conversationText := SerializeConversation(messages)
	promptText := "<conversation>\n" + conversationText + "\n</conversation>\n\n" + turnPrefixSummarizationPrompt
	maxTokens := maxTokensFor(0.5, reserveTokens, model.MaxTokens)
	return runSimple(ctx, streamer, model, SummarizationSystemPrompt, promptText, maxTokens, thinkingLevel)
}

// Compact is pi's compactWithRequest (harness/compaction/compaction.js):
// generate the summary (or, for a split turn, the history summary and the
// turn-prefix summary, joined), append file-operation tags, and return a
// Result ready to store as a session.EntryCompaction.
func Compact(ctx context.Context, prep *Preparation, streamer Streamer, model provider.Model, customInstructions *string, thinkingLevel provider.ThinkingLevel) (Result, error) {
	var summary string
	var summaryUsage msg.Usage

	if prep.IsSplitTurn && len(prep.TurnPrefixMessages) > 0 {
		historyText := "No prior history."
		var historyUsage msg.Usage
		haveHistoryUsage := false
		if len(prep.MessagesToSummarize) > 0 {
			text, usage, err := generateSummary(ctx, streamer, model, prep.Settings.ReserveTokens, customInstructions, prep.PreviousSummary, thinkingLevel, prep.MessagesToSummarize)
			if err != nil {
				return Result{}, err
			}
			historyText = text
			historyUsage = usage
			haveHistoryUsage = true
		}
		prefixText, prefixUsage, err := generateTurnPrefixSummary(ctx, streamer, model, prep.Settings.ReserveTokens, thinkingLevel, prep.TurnPrefixMessages)
		if err != nil {
			return Result{}, err
		}
		summary = historyText + "\n\n---\n\n**Turn Context (split turn):**\n\n" + prefixText
		if haveHistoryUsage {
			summaryUsage = historyUsage.Add(prefixUsage)
		} else {
			summaryUsage = prefixUsage
		}
	} else {
		text, usage, err := generateSummary(ctx, streamer, model, prep.Settings.ReserveTokens, customInstructions, prep.PreviousSummary, thinkingLevel, prep.MessagesToSummarize)
		if err != nil {
			return Result{}, err
		}
		summary = text
		summaryUsage = usage
	}

	readFiles, modifiedFiles := ComputeFileLists(prep.FileOps)
	summary += FormatFileOperations(readFiles, modifiedFiles)

	return Result{
		Summary:      summary,
		TokensBefore: prep.TokensBefore,
		Usage:        summaryUsage,
		RetainedTail: prep.RetainedTail,
		Details:      CompactionDetails{ReadFiles: readFiles, ModifiedFiles: modifiedFiles},
	}, nil
}
