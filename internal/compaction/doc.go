// Package compaction ports pi-agent-core's compaction and
// branch-summarization modules
// (node_modules/@earendil-works/pi-agent-core/dist/harness/compaction/*.js)
// to the Go session model.
//
// # What "compaction" means here
//
// A session's linear history (a []session.Entry path from root to tip) grows
// without bound. When the estimated context it represents gets close to a
// model's context window, the harness replaces the older portion with an
// LLM-written summary (a session.EntryCompaction entry carrying Summary,
// RetainedTail and TokensBefore) and keeps only a turn-aware "tail" of
// recent entries verbatim. This package supplies the pure decision logic
// (ShouldCompact, FindCutPoint), the preparation step that turns a path into
// "what to summarize" plus "what to keep" (Prepare), and the LLM call that
// turns that into a Result (Compact). SummarizeBranch/PrepareBranchSummary
// are the sibling used by navigation when abandoning a branch: pi's
// branch-summarization.js, not compaction.js.
//
// # Deviations from pi forced by the Go message model
//
// pi's AgentMessage union includes synthetic roles ("bashExecution",
// "custom", "branchSummary", "compactionSummary") that never leave the
// session/session-tree layer; pi's convertToLlm (harness/messages.js)
// rewrites them into a plain user message (wrapping the summary text in
// COMPACTION_SUMMARY_PREFIX/SUFFIX or BRANCH_SUMMARY_PREFIX/SUFFIX) right
// before an LLM ever sees them, and pi's estimateTokens/findValidCutPoints
// still see the *pre-conversion* synthetic role further upstream.
//
// This Go port's msg.Message has only four concrete roles (system, user,
// assistant, toolResult) — session.EntryCompaction and
// session.EntryBranchSummary carry their summary text directly on the Entry,
// never as a msg.Message. So this package performs pi's convertToLlm step
// eagerly, in getMessageFromEntry/getMessageFromEntryBranch (messages.go):
// a compaction or branch-summary entry becomes a msg.UserMessage wrapping
// the same prefix/suffix text immediately, rather than being carried as a
// distinct role until the prompt is built. Two measurable consequences:
//
//   - EstimateTokens, when it estimates a message that originated from a
//     compaction/branch-summary entry folded into a previous compaction's
//     RetainedTail, counts the wrapper prefix/suffix text (~20 tokens) that
//     pi's estimator (operating pre-conversion, on message.summary.length
//     alone) does not. This only affects entries that are themselves the
//     retained tail of an earlier compaction; it does not change which cut
//     point is chosen in the tests in this package's testdata.
//   - SerializeConversation in this package accepts already-converted
//     []msg.Message (i.e. it is pi's serializeConversation applied to
//     convertToLl's *output*); there is no separate ConvertToLLM step here
//     because the conversion already happened when the Entry was read.
//
// Additionally, ToolCall.Arguments is a Go map[string]any, which has no
// defined iteration order, unlike a parsed JS object's insertion order.
// SerializeConversation sorts argument keys alphabetically for determinism;
// pi's tool-call rendering order follows whatever order the model emitted
// (and JSON.parse preserved). This can only reorder key=value pairs within
// one `toolName(k=v, ...)` segment; it never changes which segments appear.
//
// # Streamer
//
// Compact and SummarizeBranch call the model through a Streamer, a minimal
// interface with the same method signature as provider.Provider.Stream.
// Any provider.Provider already satisfies it structurally; tests substitute
// a fake that returns a scripted assistant message.
package compaction
