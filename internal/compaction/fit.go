package compaction

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// Fitting a summary request into the summarising model's window.
//
// pi sends everything older than the retained tail in one request, sized
// for the model that grew the conversation. After a switch to a smaller
// model that request no longer fits: a 107k-token conversation summarised
// by a 49k-token Ollama model was sent whole, and Ollama does not refuse an
// oversized prompt, it silently truncates it to num_ctx and spends minutes
// on what is left.
//
// Two ways out were considered:
//
//   - Summarise with another model whose window fits (the outgoing one, or
//     the largest available). Rejected as the default: it spends on a model
//     the user did not choose for this request, possibly on another,
//     metered provider (the same line kiln draws for subagent dispatch),
//     and the model may not be reachable at all (offline, local-only).
//     It stays available on request: /compact after a model switch
//     summarises with the outgoing model when that one fits (see
//     harness.Lane.CompactWith).
//   - Summarise in parts that each fit (this file). Each part is sent with
//     the summary so far as <previous-summary>, using pi's own update prompt
//     (updateSummarizationPrompt) — the same rolling update pi applies when
//     it compacts an already-compacted conversation. Every request fits by
//     construction, it works with whatever model is current, including a
//     small local one, and when everything fits it sends exactly the one
//     request pi would.
//
// Claude Code also summarises with the current model and does not split
// the history; when its summary request is itself too long it drops the
// oldest turns and retries. Splitting keeps them.
//
// Sizes are estimates from the chars/4 estimator this package already uses
// for every other budget (EstimateTokens). It undercounts text that
// tokenizes densely (code and JSON run nearer 3 chars per token), so only
// fitSafety of the space left after the fixed costs is filled.

// fitSafety is the share of the remaining window a part may fill, as
// measured by the chars/4 estimate: 0.75 keeps a part inside the window for
// text that tokenizes at 3 chars per token.
const fitSafety = 0.75

// minPartTokens is the least room a part may have. A window that leaves
// less than this after the prompt and the summary's own output cannot be
// summarised usefully, and the caller is told so rather than sent a
// request the model cannot hold.
const minPartTokens = 512

// partSeparatorTokens is the estimated cost of the "\n\n" joining two
// serialized messages.
const partSeparatorTokens = 1

// estimateText is the chars/4 estimate for plain prompt text.
func estimateText(s string) int { return ceilDiv4(len(s)) }

// textBudget is how many estimated tokens of conversation (plus previous
// summary) fit in one request to a model with the given window, after the
// system prompt, the instructions and the reserved output.
func textBudget(window, maxOutput, fixedTokens int) int {
	free := window - maxOutput - fixedTokens
	if free <= 0 {
		return 0
	}
	return int(float64(free) * fitSafety)
}

// PlanParts splits serialized messages into consecutive runs that each fit
// one summary request. first is the room for the first part; later parts
// have room for less, since each carries the summary so far, bounded by
// the summary's own output limit. A message too big for any part on its
// own gets a part to itself and is cut down to fit (see fitPart). The
// result is the end index (exclusive) of each part. An empty input plans
// one empty part, so a caller still makes the request pi would.
func PlanParts(parts []string, first, later int) []int {
	if len(parts) == 0 {
		return []int{0}
	}
	var ends []int
	budget := first
	used := 0
	for i, p := range parts {
		cost := estimateText(p) + partSeparatorTokens
		if used > 0 && used+cost > budget {
			ends = append(ends, i)
			budget = later
			used = 0
		}
		used += cost
	}
	return append(ends, len(parts))
}

// fitPart joins one planned part's messages and, if a single oversized
// message makes it larger than budget, cuts it down to fit, keeping the
// head and saying how much was dropped.
func fitPart(parts []string, budget int) string {
	text := strings.Join(parts, "\n\n")
	if estimateText(text) <= budget {
		return text
	}
	return truncateForSummary(text, budget*4)
}

// Progress is what a running compaction reports while it waits on the
// model, for a status line.
type Progress struct {
	// Part is the 1-based request being sent; Parts is how many the
	// compaction plans in total (1 when everything fits in one).
	Part, Parts int
	// Model is "provider/id" of the summarising model.
	Model string
	// Path is "cache" when this part is compaction's cache-friendly path
	// (fastpath.go: the live transcript plus one appended summarization
	// turn), or "serialized" for the serialize-and-split path (this file).
	Path string
	// PromptTokens is the estimated size of this part's request.
	PromptTokens int
	// OutputTokens is the estimated size of what this part has streamed
	// back so far; 0 while the model is still reading the prompt.
	OutputTokens int
}

// PartDone reports one finished summary request's measured cost: what
// compaction_part_done logs (internal/cli/chat.go), so a slow compaction
// can be read from the log alone -- which path it took, how much of the
// prompt the provider served from cache, how long the first token took,
// and how long the whole request ran.
type PartDone struct {
	// Part/Parts/Model/Path mirror Progress's fields for the same request.
	Part, Parts int
	Model, Path string
	// PromptTokens is this part's estimated request size (system prompt,
	// tools when the cache path sent them, and the conversation/instructions).
	PromptTokens int
	// OutputTokens is the estimated size of the text the model returned.
	OutputTokens int
	// CacheRead is the provider-reported cache-read token count on this
	// request's usage (msg.Usage.CacheRead), 0 when the provider did not
	// report one (no cache hit, or a provider/API that does not report it).
	CacheRead int
	// TTFTMs is the time from sending the request to its first streamed
	// event, in milliseconds.
	TTFTMs int64
	// TotalMs is the time from sending the request to the final assistant
	// message, in milliseconds.
	TotalMs int64
}

// Options tune a compaction run. The zero value is the default.
type Options struct {
	// OnProgress, when set, is called as each part starts and as its
	// response streams in. It runs on the compaction's goroutine.
	OnProgress func(Progress)
	// OnPartDone, when set, is called once a part's request finishes
	// successfully (never on a stalled/aborted/errored part -- there is no
	// usage or timing worth logging for one), with its measured cost.
	OnPartDone func(PartDone)
	// FirstEventTimeout bounds the wait for a part's first streamed event,
	// given the part's estimated prompt size. Nil uses DefaultFirstEventTimeout.
	FirstEventTimeout func(promptTokens int) time.Duration
	// IdleTimeout bounds the gap between streamed events once a response
	// has started. Zero uses DefaultIdleTimeout.
	IdleTimeout time.Duration
	// FastPath, when set, carries the live request the agent loop would
	// send next for this lane (fastpath.go's FastPathInput). CompactWith
	// tries it first; nil (the zero value) skips straight to the
	// serialize-and-split path below, as every compaction did before it.
	FastPath *FastPathInput
}

// Stall limits. A model reads the whole prompt before it streams anything,
// so the wait for the first event grows with the prompt. Measured on
// 2026-10-02 against a 27B Q4 model on a remote Ollama GPU host
// (qwen3.8:latest, num_ctx 49152): 10,474 prompt tokens took 47s to the
// first byte (223 tok/s), 24,578 took 127s (195 tok/s), and nothing at all
// streams in between. A CPU-only host is easily ten times slower, so the
// allowance assumes 20 tok/s on top of a two-minute floor: a full 36k-token
// part may take up to ~32 minutes before kiln gives up on it. That is a
// backstop for an unattended run, not the user's way out: Esc cancels at
// any time, and the status line shows how long the model has been reading.
// Once tokens flow they arrive many times a second; five minutes without
// one means the stream is dead.
const (
	stallFloor        = 2 * time.Minute
	slowestPromptRate = 20 // tokens per second
)

// DefaultIdleTimeout is the longest gap allowed between streamed events.
const DefaultIdleTimeout = 5 * time.Minute

// DefaultFirstEventTimeout is the wait allowed for a request's first
// streamed event, given its estimated prompt size.
func DefaultFirstEventTimeout(promptTokens int) time.Duration {
	return stallFloor + time.Duration(promptTokens/slowestPromptRate)*time.Second
}

// errStalled is the cancellation cause runSimple's watchdog records.
type errStalled struct {
	waited      time.Duration
	before      string // "the first token" or "the next token"
	promptToken int
}

func (e errStalled) Error() string {
	return fmt.Sprintf("the model sent nothing for %s while waiting for %s (prompt ~%s tokens)", e.waited.Round(time.Second), e.before, formatK(e.promptToken))
}

func formatK(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// summaryRequest is one logical summary (the history, or a split turn's
// prefix) that may take several requests.
type summaryRequest struct {
	first, update string // instructions with no previous summary / with one
	maxOutput     int
	previous      *string
	messages      []msg.Message
}

// partInstructions builds the instructions for one part: first, or update
// when a summary so far exists, plus any custom focus.
func partInstructions(base string, custom *string) string {
	if custom != nil && *custom != "" {
		return base + "\n\nAdditional focus: " + *custom
	}
	return base
}

// summaryPrompt is the user text for one request, in pi's layout.
func summaryPrompt(conversation string, previous *string, instructions string) string {
	prompt := "<conversation>\n" + conversation + "\n</conversation>\n\n"
	if previous != nil {
		prompt += "<previous-summary>\n" + *previous + "\n</previous-summary>\n\n"
	}
	return prompt + instructions
}

// plan returns the part boundaries for req against model's window, and the
// room the first and later parts have. A window of 0 (unknown) plans one
// part, as pi would send it.
func (req summaryRequest) plan(model provider.Model, custom *string) (ends []int, first, later int, err error) {
	parts := serializeParts(req.messages)
	if model.ContextWindow <= 0 {
		return []int{len(parts)}, int(^uint(0) >> 1), int(^uint(0) >> 1), nil
	}
	system := estimateText(SummarizationSystemPrompt)
	firstInstr := partInstructions(req.first, custom)
	if req.previous != nil {
		firstInstr = partInstructions(req.update, custom)
	}
	prevTokens := 0
	if req.previous != nil {
		prevTokens = estimateText(*req.previous)
	}
	first = textBudget(model.ContextWindow, req.maxOutput, system+estimateText(firstInstr)) - prevTokens
	// A later part carries the summary written so far, which the request
	// caps at maxOutput tokens.
	later = textBudget(model.ContextWindow, req.maxOutput, system+estimateText(partInstructions(req.update, custom))) - req.maxOutput
	if first < minPartTokens || later < minPartTokens {
		return nil, 0, 0, &Error{Code: "summarization_failed", Message: fmt.Sprintf(
			"Summarization failed: %s's %s-token window leaves no room to summarise in (the prompt and a %s-token summary already fill it)",
			modelLabel(model), formatK(model.ContextWindow), formatK(req.maxOutput))}
	}
	return PlanParts(parts, first, later), first, later, nil
}

func modelLabel(m provider.Model) string {
	if m.Provider == "" {
		return m.ID
	}
	return m.Provider + "/" + m.ID
}

// summarizer runs summary requests in parts, reporting progress across all
// of them.
type summarizer struct {
	streamer Streamer
	model    provider.Model
	thinking provider.ThinkingLevel
	custom   *string
	opts     Options
	part     int // requests started so far
	parts    int // requests planned in total
}

// run sends req's parts in order, each with the summary so far, and
// returns the last summary and the summed usage.
func (s *summarizer) run(ctx context.Context, req summaryRequest, ends []int, first, later int) (string, msg.Usage, error) {
	parts := serializeParts(req.messages)
	previous := req.previous
	var total msg.Usage
	start := 0
	for i, end := range ends {
		budget := later
		if i == 0 {
			budget = first
		}
		instr := partInstructions(req.first, s.custom)
		if previous != nil {
			instr = partInstructions(req.update, s.custom)
		}
		prompt := summaryPrompt(fitPart(parts[start:end], budget), previous, instr)
		start = end
		s.part++
		text, usage, err := runSimpleWatched(ctx, s.streamer, s.model, SummarizationSystemPrompt, prompt, req.maxOutput, s.thinking, s.opts, s.part, s.parts)
		if err != nil {
			return "", msg.Usage{}, err
		}
		total = total.Add(usage)
		summary := text
		previous = &summary
	}
	if previous == nil {
		return "", total, nil
	}
	return *previous, total, nil
}

// watchedRequest sends one request through streamer with a stall watchdog,
// Progress reporting while it streams, and a PartDone report (prompt/output/
// cache-read tokens, time to first token, total time) once it finishes
// without error. It returns the raw assistant message: runSimpleWatched
// (the serialized path, below) and tryFastPath (fastpath.go) each apply
// their own StopReason-to-Error mapping and text/tool-call handling, since
// a tool call in the reply means something different to each (an error to
// runSimpleWatched's callers, since the serialized path never offers
// tools; a fallback-to-serialized signal to tryFastPath, since the
// cache-friendly path does offer them).
//
// path is "cache" or "serialized", carried on both reports so
// compaction_part/compaction_part_done can say which request they
// describe.
func watchedRequest(ctx context.Context, streamer Streamer, model provider.Model, transcript []msg.Message, sOpts provider.StreamOptions, promptTokens int, opts Options, part, parts int, path string) (*msg.AssistantMessage, error) {
	firstWait := DefaultFirstEventTimeout(promptTokens)
	if opts.FirstEventTimeout != nil {
		firstWait = opts.FirstEventTimeout(promptTokens)
	}
	idle := opts.IdleTimeout
	if idle <= 0 {
		idle = DefaultIdleTimeout
	}
	report := func(out int) {
		if opts.OnProgress != nil {
			opts.OnProgress(Progress{Part: part, Parts: parts, Model: modelLabel(model), Path: path, PromptTokens: promptTokens, OutputTokens: out})
		}
	}

	watched, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	watch := NewWatchdog(firstWait, func() {
		cancel(errStalled{waited: firstWait, before: "the first token", promptToken: promptTokens})
	})
	defer watch.Stop()
	stalledIdle := func() {
		cancel(errStalled{waited: idle, before: "the next token", promptToken: promptTokens})
	}

	report(0)
	start := time.Now()
	var ttft time.Duration
	haveFirst := false
	outChars := 0
	ch, wait := streamer.Stream(watched, model, transcript, sOpts)
	for ev := range ch {
		watch.Reset(idle, stalledIdle)
		if !haveFirst {
			ttft = time.Since(start)
			haveFirst = true
		}
		if ev.Delta != "" {
			outChars += len(ev.Delta)
			report(ceilDiv4(outChars))
		}
	}
	am, err := wait()
	total := time.Since(start)

	// A watchdog firing cancels watched with an errStalled cause; checked
	// independently of err and am, since a provider that answers a
	// cancelled stream with a clean result (seqStreamer's "block" case:
	// StopAborted, err nil) still means the watchdog -- not the caller --
	// ended the request, and that is what "stalled" should report.
	var stalled errStalled
	if cause := context.Cause(watched); ctx.Err() == nil && errors.As(cause, &stalled) {
		return nil, &Error{Code: "stalled", Message: "Summarization stopped: " + stalled.Error(), Cause: stalled}
	}
	if err == nil && opts.OnPartDone != nil {
		cacheRead := 0
		if am != nil {
			cacheRead = am.Usage.CacheRead
		}
		opts.OnPartDone(PartDone{
			Part: part, Parts: parts, Model: modelLabel(model), Path: path,
			PromptTokens: promptTokens, OutputTokens: ceilDiv4(outChars), CacheRead: cacheRead,
			TTFTMs: ttft.Milliseconds(), TotalMs: total.Milliseconds(),
		})
	}
	return am, err
}

// runSimpleWatched is runSimple with a stall watchdog and progress
// reports, for the serialized path's one-user-message requests. It cancels
// the request when the model sends nothing for too long and returns an
// Error with Code "stalled" naming how long it waited.
func runSimpleWatched(ctx context.Context, streamer Streamer, model provider.Model, systemPrompt, userText string, maxTokens int, thinkingLevel provider.ThinkingLevel, opts Options, part, parts int) (string, msg.Usage, error) {
	sOpts := provider.StreamOptions{SystemPrompt: systemPrompt, MaxTokens: maxTokens}
	if model.Reasoning && thinkingLevel != "" && thinkingLevel != provider.ThinkingOff {
		sOpts.ThinkingLevel = thinkingLevel
	}
	transcript := []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text(userText)}, Timestamp: time.Now().UnixMilli()},
	}
	promptTokens := estimateText(systemPrompt) + estimateText(userText)
	am, err := watchedRequest(ctx, streamer, model, transcript, sOpts, promptTokens, opts, part, parts, "serialized")
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
