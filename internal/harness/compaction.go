package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/andrepato/harness/internal/compaction"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/session"
)

// CompactionSettings is an alias for internal/compaction's Settings type
// (Enabled, ReserveTokens, KeepRecentTokens): now that internal/compaction
// exists (it did not when this package's design started; see doc.go), this
// package uses it directly rather than a local stand-in.
type CompactionSettings = compaction.Settings

// Compaction triggers, carried on compaction_start (Event.CompactionTrigger).
const (
	TriggerManual   = "manual"
	TriggerAuto     = "auto"
	TriggerOverflow = "overflow"
)

// autoCompact checks whether the lane's branch at tip has crossed the
// configured compaction threshold and, if so, runs one compaction pass. It
// is called before an operation's first request and at the end of each
// tool step in drive(). Compaction is best-effort, a run's assistant
// response is not (pi's behaviour), so a failure does not fail the run:
// autoCompact returns it as a *CompactionFailedError for drive to report
// once it knows whether the request recovered (fitRequest may still
// compact it to fit, see drive).
// autoCompact returns the branch's tip after the check: unchanged if no
// compaction ran, or the new compaction entry's id if one did (the caller
// must update its own tip bookkeeping, since this commits directly to
// storage rather than through drive()'s normal write sequence).
func (l *Lane) autoCompact(ctx context.Context, tip string) (string, error) {
	if !l.h.opts.Compaction.Enabled {
		return tip, nil
	}
	pathEntries, err := l.h.opts.Storage.ScanBranch(session.BranchScan{Start: tip, Order: "oldestFirst"})
	if err != nil {
		return tip, nil
	}
	_, cfg, err := l.resolveModel()
	if err != nil {
		return tip, nil
	}
	model, ok := l.h.opts.Registry.GetModel(cfg.Model.Provider, cfg.Model.ModelID)
	if !ok {
		return tip, nil
	}
	usage := compaction.CalculateContextTokens(pathEntries)
	if !compaction.ShouldCompact(usage.Tokens, model.ContextWindow, l.h.opts.Compaction) {
		return tip, nil
	}
	if err := l.runCompaction(ctx, pathEntries, model, cfg, nil, TriggerAuto); err != nil {
		if ctx.Err() != nil { // an interrupted turn is not a compaction fault
			return tip, nil
		}
		return tip, &CompactionFailedError{Err: err}
	}
	if newTip, ok := l.GetTipID(); ok {
		return newTip, nil
	}
	return tip, nil
}

// CompactionFailedError is an automatic compaction that failed, retry
// included, in words for the user: CompactionReason says why without
// transport jargon.
type CompactionFailedError struct{ Err error }

func (e *CompactionFailedError) Error() string {
	return "Auto-compaction failed: " + CompactionReason(e.Err) + ". The conversation is as it was"
}

func (e *CompactionFailedError) Unwrap() error { return e.Err }

// CompactionReason says in plain words why a compaction request failed:
// the model went quiet, the connection dropped, or the error itself when
// it is not one of those.
func CompactionReason(err error) string {
	var stalled *StallError
	var cerr *compaction.Error
	var si provider.StreamInterrupted
	switch {
	case errors.As(err, &stalled), errors.As(err, &cerr) && cerr.Code == "stalled":
		return "the model stopped responding"
	case errors.Is(err, context.DeadlineExceeded):
		return "the request timed out"
	case errors.As(err, &si):
		return "the connection to the model dropped mid-answer"
	}
	return err.Error()
}

// compactionRetriable reports whether a failed compaction request is worth
// sending again: the same transient failures a model request retries, and
// a stalled summary.
func compactionRetriable(err error) bool {
	var cerr *compaction.Error
	if errors.As(err, &cerr) && cerr.Code == "stalled" {
		return true
	}
	return isRetriable(err)
}

// compactionAttempts is how many times one compaction is tried.
const compactionAttempts = 2

// runCompaction is the shared body of Lane.Compact and autoCompact: prepare
// the cut point, call summariser to summarize, and commit the resulting
// session.EntryCompaction. summariser is normally the lane's own model;
// Lane.CompactWith may name another. The cut point and the summary's size
// always come from the harness's compaction settings, which follow the
// lane's current model: the result has to fit that model, whoever writes it.
func (l *Lane) runCompaction(ctx context.Context, pathEntries []session.Entry, summariser provider.Model, cfg session.LaneConfiguration, customInstructions *string, trigger string) error {
	prep, err := compaction.Prepare(pathEntries, l.h.opts.Compaction)
	if err != nil {
		return err
	}
	if prep == nil {
		return nil // nothing to compact (empty branch, or tip is already a compaction entry)
	}
	if len(prep.MessagesToSummarize) == 0 && len(prep.TurnPrefixMessages) == 0 {
		return nil // everything is recent and kept verbatim: no summary call to pay for
	}
	p, ok := l.h.opts.Registry.Provider(summariser.Provider)
	if !ok {
		return fmt.Errorf("harness: unknown provider %q", summariser.Provider)
	}

	for _, fn := range l.h.hooks.beforeCompaction {
		if fn == nil {
			continue
		}
		if err := runGuarded(func() error { return fn(ctx) }); err != nil {
			l.h.events.Emit(Event{Type: EventHandlerError, Lane: l.name, HookName: "before_compaction", Err: err})
		}
	}
	label := summariser.Provider + "/" + summariser.ID
	l.h.events.Emit(Event{Type: EventCompactionStart, Lane: l.name, CompactionTrigger: trigger, CompactionModel: label})

	// One retry for a transient failure (a stall, a dropped connection),
	// said on the progress row, so a compaction that recovers leaves no
	// error behind.
	var result compaction.Result
	lastPart := 0
	for attempt := 1; ; attempt++ {
		result, err = compaction.CompactWith(ctx, prep, p, summariser, customInstructions, provider.ThinkingLevel(cfg.ThinkingLevel), compaction.Options{
			OnProgress: func(pr compaction.Progress) {
				lastPart = pr.Part
				l.h.events.Emit(Event{Type: EventCompactionProgress, Lane: l.name, CompactionModel: pr.Model,
					CompactionPart: pr.Part, CompactionParts: pr.Parts,
					CompactionPromptTokens: pr.PromptTokens, CompactionOutputTokens: pr.OutputTokens})
			},
			FirstEventTimeout: l.h.opts.StallFirstEvent,
			IdleTimeout:       l.h.opts.StallIdle,
		})
		if err == nil || ctx.Err() != nil || attempt >= compactionAttempts || !compactionRetriable(err) {
			break
		}
		l.h.events.Emit(Event{Type: EventCompactionRetry, Lane: l.name, CompactionTrigger: trigger, CompactionModel: label,
			CompactionPart: lastPart, Attempt: attempt + 1, MaxAttempts: compactionAttempts, RetryError: CompactionReason(err), Err: err})
	}
	if err != nil {
		// compaction_end always follows compaction_start, so a status line
		// that showed the start can clear on failure and cancellation too.
		l.h.events.Emit(Event{Type: EventCompactionEnd, Lane: l.name, CompactionTrigger: trigger, CompactionModel: label, Err: err})
		return err
	}

	detailsRaw, err := json.Marshal(result.Details)
	if err != nil {
		l.h.events.Emit(Event{Type: EventCompactionEnd, Lane: l.name, CompactionTrigger: trigger, CompactionModel: label, Err: err})
		return err
	}
	tip, _ := l.GetTipID()
	var parent *string
	if tip != "" {
		parent = &tip
	}
	entryID := l.newID()
	entryWrite := session.EntryWrite{Entry: session.Entry{
		ID:           entryID,
		ParentID:     parent,
		Type:         session.EntryCompaction,
		Summary:      result.Summary,
		RetainedTail: result.RetainedTail,
		TokensBefore: int64(result.TokensBefore),
		Details:      detailsRaw,
		Usage:        &result.Usage,
	}}
	tipW, err := session.SetValue(session.BranchTip(l.name), &entryID)
	if err == nil {
		_, err = l.h.opts.Storage.Commit([]session.Write{entryWrite, tipW})
	}
	if err != nil {
		l.h.events.Emit(Event{Type: EventCompactionEnd, Lane: l.name, CompactionTrigger: trigger, CompactionModel: label, Err: err})
		return err
	}
	l.h.events.Emit(Event{Type: EventEntryAdded, Lane: l.name, EntryID: entryID, ParentID: tip})
	l.h.events.Emit(Event{Type: EventCompactionEnd, Lane: l.name, CompactionTrigger: trigger, CompactionModel: label, CompactionSummary: result.Summary})
	return nil
}

// ContextOverflowError is a request kiln refused to send because the
// conversation does not fit the model's window, even after compacting it.
// Providers such as Ollama do not reject an oversized prompt: they cut it
// to the window and answer what is left, so the check has to happen here.
type ContextOverflowError struct {
	Model string
	// Tokens is the request's estimated size (system prompt, tools and
	// conversation); Limit is the most kiln sends to Model (its window
	// less room for the reply, see requestLimit).
	Tokens, Limit, Window int
}

func (e *ContextOverflowError) Error() string {
	return fmt.Sprintf("the conversation (~%s tokens with the system prompt and tools) does not fit %s's %s-token window, even after compacting it; /clear to start over, or /model to a model with a larger window",
		kTokens(e.Tokens), e.Model, kTokens(e.Window))
}

func kTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// requestTokens estimates the size of a request: the system prompt, the
// active tools' schemas and the transcript, by the chars/4 estimate the
// rest of the context accounting uses. It does not use the provider's
// last reported usage, which was counted by whatever model answered last,
// not the one about to be asked.
func (l *Lane) requestTokens(transcript []msg.Message) int {
	n := (len(l.h.opts.SystemPrompt) + 3) / 4
	if tools, err := l.ToolSchemaTokens(); err == nil {
		n += tools
	}
	for _, m := range transcript {
		n += compaction.EstimateTokens(m)
	}
	return n
}

// RequestLimit is the most kiln sends a model with the given window in
// one request: the window less room for the reply, which is the
// compaction reserve but never more than a tenth of the window (the
// reserve's own share; a test or a tiny window can set a reserve that
// would leave no room for any prompt). 0 when the window is unknown.
func RequestLimit(window, reserve int) int {
	if window <= 0 {
		return 0
	}
	if tenth := window / 10; reserve <= 0 || reserve > tenth {
		reserve = tenth
	}
	return window - reserve
}

func (l *Lane) requestLimit(model provider.Model) int {
	return RequestLimit(model.ContextWindow, l.h.opts.Compaction.ReserveTokens)
}

// fitRequest builds the transcript for the next request and makes sure it
// fits the model's window: if it does not, it compacts once (when the
// operation has not already) and checks again. It returns the tip, which
// moves if a compaction ran, and the transcript to send, or a
// *ContextOverflowError when nothing more can be done.
func (l *Lane) fitRequest(ctx context.Context, tip string, recoveryUsed *bool) (string, []msg.Message, error) {
	for {
		entries, err := l.h.opts.Storage.ScanBranch(session.BranchScan{Start: tip, Order: "oldestFirst"})
		if err != nil {
			return tip, nil, err
		}
		transcript := l.invokeTransformContext(ctx, entriesToTranscript(entries))
		model, cfg, err := l.resolveModel()
		if err != nil {
			return tip, nil, err
		}
		limit := l.requestLimit(model)
		tokens := l.requestTokens(transcript)
		if limit <= 0 || tokens <= limit {
			return tip, transcript, nil
		}
		overflow := &ContextOverflowError{Model: cfg.Model.Provider + "/" + cfg.Model.ModelID, Tokens: tokens, Limit: limit, Window: model.ContextWindow}
		if *recoveryUsed || !l.h.opts.Compaction.Enabled {
			return tip, nil, overflow
		}
		*recoveryUsed = true
		before, _ := l.GetTipID()
		if err := l.runCompaction(ctx, entries, model, cfg, nil, TriggerOverflow); err != nil {
			if ctx.Err() != nil {
				return tip, nil, ctx.Err()
			}
			return tip, nil, fmt.Errorf("%w (compacting it failed: %v)", overflow, err)
		}
		after, _ := l.GetTipID()
		if after == before {
			// Nothing old enough to summarise: compaction cannot help.
			return tip, nil, overflow
		}
		tip = after
	}
}

// overflowErrorPattern matches the errors providers return for a request
// larger than the model's context, so drive can compact once and retry.
var overflowErrorPattern = regexp.MustCompile(`(?i)prompt is too long|context[_ ]length|context window|maximum context|too many (input )?tokens|input is too long|exceeds? (the )?(model'?s? )?(max(imum)? )?context`)

// isContextOverflow reports whether err is a provider refusing a request
// for being larger than the model's context.
func isContextOverflow(err error) bool {
	return err != nil && overflowErrorPattern.MatchString(err.Error())
}

// compactForOverflow compacts the branch at tip now, after a provider
// refused a request as too large. It reports the new tip and whether a
// compaction entry was written.
func (l *Lane) compactForOverflow(ctx context.Context, tip string) (string, bool) {
	if !l.h.opts.Compaction.Enabled {
		return tip, false
	}
	entries, err := l.h.opts.Storage.ScanBranch(session.BranchScan{Start: tip, Order: "oldestFirst"})
	if err != nil {
		return tip, false
	}
	model, cfg, err := l.resolveModel()
	if err != nil {
		return tip, false
	}
	if err := l.runCompaction(ctx, entries, model, cfg, nil, TriggerOverflow); err != nil {
		l.h.events.Emit(Event{Type: EventFault, Lane: l.name, Err: fmt.Errorf("harness: overflow compaction failed: %w", err)})
		return tip, false
	}
	newTip, _ := l.GetTipID()
	return newTip, newTip != tip
}
