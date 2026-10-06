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
//
// # Two paths to a summary (fastpath.go, fit.go)
//
// CompactWith can reach a Result two ways. Both produce the same stored
// shape (a Result whose Summary/RetainedTail/Details go on a
// session.EntryCompaction exactly as before); they differ in what request
// they send and why.
//
//   - The cache-friendly path (fastpath.go, "path 1"): one request that is
//     the live request the agent loop would send next for this lane --
//     same system prompt, same tool definitions, same message prefix a
//     provider's prefix cache (Ollama) or cache_read (Anthropic) last
//     processed -- with one summarization turn appended. A provider
//     serving from cache only has to read the new turn, not the whole
//     conversation again: measured locally (Ollama, qwen3:8b), a 15.5k-token
//     prompt cold took 61s to first token; the same prefix plus one new
//     question took 0.2s. This is what this change is for: a remote
//     qwen3.8:latest session (49k ctx) spent 3m12s on a 10k-token
//     serialize-and-split part that shared no prefix with anything the
//     model had already read.
//   - The serialize-and-split path (fit.go, "path 2", pre-existing): builds
//     an unrelated prompt from SerializeConversation's plain-text rendering
//     of just the messages being summarized, under SummarizationSystemPrompt,
//     split into parts that each fit the model's window when the whole
//     thing does not. No request here shares a prefix with a live
//     conversation turn, so a provider's cache never helps it, but it works
//     regardless of window size and never depends on what the provider
//     last cached.
//
// CompactWith tries path 1 first when Options.FastPath is set (nil skips
// straight to path 2, so every existing caller that does not set it keeps
// doing exactly what it always did). Per this package's test suite and
// fastpath.go's tryFastPath doc comment, ANY failure of path 1 falls back
// to path 2 rather than surfacing: the request does not fit the window,
// the request errors (including a stall), or the model calls a tool
// despite being told not to (fastPathPrompt's "do not call any tool";
// StreamOptions has no portable tool_choice="none" this port's API clients
// all honour, so the instruction plus this fallback is the enforcement).
//
// What path 1 summarizes, and why. A naive translation of "summarize the
// messages before the cut point, keep the rest verbatim" into the
// cache-friendly shape would still only send the pre-cut messages -- but
// that is itself a different, shorter prefix than what the model's cache
// holds (the FULL conversation including the retained tail), so trimming
// the request to "just the summarizable part" throws away most of the
// cache hit this path exists for. Path 1 instead sends the conversation
// as-is, in full, and asks for one summary of everything above the
// appended turn. Preparation.RetainedTail (the cut point's verbatim tail)
// is untouched by which path ran -- it is still exactly what Prepare chose,
// stored on the new EntryCompaction exactly as path 2 would store it. The
// only difference is that path 1's Summary is generated by a model that
// also saw the retained tail's content (redundant with it being kept
// verbatim, not incorrect: the tail is still literally present in the next
// request either way). A previous compaction's summary needs no separate
// handling here either: ContextMessages already wraps it into the live
// transcript's first message (see "Deviations" above), so it is simply
// part of what path 1's single request reads, with no <previous-summary>
// tag of its own.
//
// Split turns (Preparation.IsSplitTurn/TurnPrefixMessages) exist in path 2
// to keep each of two separate requests (history, then turn prefix) inside
// the window; path 1 sends one request for the whole live transcript
// regardless, so it never needs the distinction -- one request where path 2
// would have sent two, whenever path 1's input fits at all.
//
// Path 1's output is capped at compact.go's summaryOutputCap (4096): the
// structured summary format (fastPathPrompt) is seven short headed
// sections, a few hundred tokens in ordinary use. A summary that reaches
// the cap (StopLength) is not used; path 2 then writes it with its own
// budget, maxTokensFor(0.8, ReserveTokens, model.MaxTokens), so a long
// summary is never cut off. Thinking is always off for a summarization
// request (both paths):
// internal/harness's caller passes provider.ThinkingOff regardless of the
// lane's own configured thinking level, since a structured-summary request
// should never spend output tokens reasoning about how to write one.
package compaction
