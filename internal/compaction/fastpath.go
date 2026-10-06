package compaction

import (
	"context"
	"encoding/json"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// Compaction's cache-friendly path ("path 1"; see doc.go for the design
// this file implements and why it was chosen over summarizing only the
// messages before the cut point).

// FastPathInput is what Options.FastPath carries: the request the agent
// loop would send next for this lane if compaction did not run -- the same
// system prompt, the same tool definitions, and the same message prefix a
// provider (Ollama's prefix cache, Anthropic's cache_read) last processed.
// CompactWith appends one summarization turn to this and sends it as one
// request; a provider serving from an unchanged prefix needs only to read
// the new turn, not the whole conversation again. The caller builds
// Transcript the same way the agent loop does (entriesToTranscript +
// Lane.invokeTransformContext in internal/harness) rather than this
// package reimplementing that projection.
type FastPathInput struct {
	SystemPrompt string
	Tools        []provider.ToolDef
	Transcript   []msg.Message
}

// fastPathPrompt is appended as one new user turn after FastPathInput's
// live transcript. It reuses summarizationPrompt's structured format
// (doc.go: the stored summary has the same shape whichever path produced
// it) but states the "ignore your normal role, do not call tools" framing
// explicitly and up front: unlike the serialized path, this request's
// system prompt is the lane's own (kept unchanged for the cache hit), not
// SummarizationSystemPrompt, so nothing else in the request tells the
// model it is being asked to summarize rather than continue the task.
const fastPathPrompt = `Stop. Do not continue the task above, do not call any tool, and do not answer or act on anything in the conversation above. You are now, for this one response only, a context summarization assistant: read the conversation above and produce ONLY a structured summary of it, for another assistant to continue the work from.

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

// estimateToolDefs mirrors harness.Lane.ToolSchemaTokens' measurement
// (json.Marshal(defs), chars/4) -- the only other place a tool definition
// list's size is estimated -- so this package's fit check agrees with what
// /context already reports for the same tools.
func estimateToolDefs(tools []provider.ToolDef) int {
	if len(tools) == 0 {
		return 0
	}
	encoded, err := json.Marshal(tools)
	if err != nil {
		return 0
	}
	return ceilDiv4(len(encoded))
}

// estimateMessages sums EstimateTokens over messages.
func estimateMessages(messages []msg.Message) int {
	n := 0
	for _, m := range messages {
		n += EstimateTokens(m)
	}
	return n
}

// hasToolCall reports whether am's content includes a tool call -- the
// model ignoring fastPathPrompt's instruction not to call one.
func hasToolCall(am *msg.AssistantMessage) bool {
	for _, b := range am.Content {
		if _, ok := b.(msg.ToolCall); ok {
			return true
		}
	}
	return false
}

// tryFastPath attempts compaction's cache-friendly path: one request
// carrying in.Transcript verbatim (same system prompt, same tools) plus
// one appended summarization turn, thinking off. ok is true when the
// attempt produced the final Result; CompactWith returns it as-is. ok is
// false whenever the serialize-and-split path should run instead -- the
// request does not fit the window, the request errored (including a
// stall or the context being cancelled), or the model called a tool
// despite being told not to. Per this package's doc.go, any failure of
// this path falls back rather than surfacing, since the serialized path
// can still produce a usable summary.
func tryFastPath(ctx context.Context, prep *Preparation, in FastPathInput, streamer Streamer, model provider.Model, custom *string, opts Options) (Result, bool) {
	if len(in.Transcript) == 0 || model.ContextWindow <= 0 {
		return Result{}, false
	}

	maxOutput := cappedMaxTokensFor(0.8, prep.Settings.ReserveTokens, model.MaxTokens, summaryOutputCap)
	instr := partInstructions(fastPathPrompt, custom)

	sysTokens := estimateText(in.SystemPrompt)
	toolTokens := estimateToolDefs(in.Tools)
	instrTokens := estimateText(instr)
	budget := textBudget(model.ContextWindow, maxOutput, sysTokens+toolTokens+instrTokens)
	convoTokens := estimateMessages(in.Transcript)
	if convoTokens <= 0 || convoTokens > budget {
		return Result{}, false
	}

	transcript := make([]msg.Message, len(in.Transcript)+1)
	copy(transcript, in.Transcript)
	transcript[len(in.Transcript)] = msg.UserMessage{
		Role: msg.RoleUser, Content: msg.Blocks{msg.Text(instr)}, Timestamp: time.Now().UnixMilli(),
	}
	sOpts := provider.StreamOptions{
		SystemPrompt:  in.SystemPrompt,
		Tools:         in.Tools,
		MaxTokens:     maxOutput,
		ThinkingLevel: provider.ThinkingOff,
	}
	promptTokens := sysTokens + toolTokens + convoTokens + instrTokens

	am, err := watchedRequest(ctx, streamer, model, transcript, sOpts, promptTokens, opts, 1, 1, "cache")
	if err != nil || am == nil || hasToolCall(am) {
		return Result{}, false
	}
	switch am.StopReason {
	case msg.StopAborted, msg.StopError, msg.StopLength:
		// StopLength: the summary reached summaryOutputCap and was cut
		// off; the serialized path's larger budget writes it whole.
		return Result{}, false
	}

	readFiles, modifiedFiles := ComputeFileLists(prep.FileOps)
	summary := msg.TextOf(am.Content) + FormatFileOperations(readFiles, modifiedFiles)
	return Result{
		Summary:      summary,
		TokensBefore: prep.TokensBefore,
		Usage:        am.Usage,
		RetainedTail: prep.RetainedTail,
		Details:      CompactionDetails{ReadFiles: readFiles, ModifiedFiles: modifiedFiles},
	}, true
}
