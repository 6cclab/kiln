package harness

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrepato/harness/internal/compaction"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/session"
)

// CompactionSettings is an alias for internal/compaction's Settings type
// (Enabled, ReserveTokens, KeepRecentTokens): now that internal/compaction
// exists (it did not when this package's design started; see doc.go), this
// package uses it directly rather than a local stand-in.
type CompactionSettings = compaction.Settings

// autoCompact checks whether the lane's branch at tip has crossed the
// configured compaction threshold and, if so, runs one compaction pass. It
// is called at the end of each turn in drive(); errors are surfaced as
// fault events rather than failing the run, matching pi's "compaction is
// best-effort, a run's assistant response is not" behavior.
// autoCompact returns the branch's tip after the check: unchanged if no
// compaction ran, or the new compaction entry's id if one did (the caller
// must update its own tip bookkeeping, since this commits directly to
// storage rather than through drive()'s normal write sequence).
func (l *Lane) autoCompact(ctx context.Context, tip string) string {
	if !l.h.opts.Compaction.Enabled {
		return tip
	}
	pathEntries, err := l.h.opts.Storage.ScanBranch(session.BranchScan{Start: tip, Order: "oldestFirst"})
	if err != nil {
		return tip
	}
	_, cfg, err := l.resolveModel()
	if err != nil {
		return tip
	}
	model, ok := l.h.opts.Registry.GetModel(cfg.Model.Provider, cfg.Model.ModelID)
	if !ok {
		return tip
	}
	usage := compaction.CalculateContextTokens(pathEntries)
	if !compaction.ShouldCompact(usage.Tokens, model.ContextWindow, l.h.opts.Compaction) {
		return tip
	}
	if err := l.runCompaction(ctx, pathEntries, model, cfg, nil); err != nil {
		l.h.events.Emit(Event{Type: EventFault, Lane: l.name, Err: fmt.Errorf("harness: auto-compaction failed: %w", err)})
		return tip
	}
	if newTip, ok := l.GetTipID(); ok {
		return newTip
	}
	return tip
}

// runCompaction is the shared body of Lane.Compact and autoCompact: prepare
// the cut point, call the model to summarize, and commit the resulting
// session.EntryCompaction.
func (l *Lane) runCompaction(ctx context.Context, pathEntries []session.Entry, model provider.Model, cfg session.LaneConfiguration, customInstructions *string) error {
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
	p, ok := l.h.opts.Registry.Provider(cfg.Model.Provider)
	if !ok {
		return fmt.Errorf("harness: unknown provider %q", cfg.Model.Provider)
	}

	for _, fn := range l.h.hooks.beforeCompaction {
		if fn == nil {
			continue
		}
		if err := runGuarded(func() error { return fn(ctx) }); err != nil {
			l.h.events.Emit(Event{Type: EventHandlerError, Lane: l.name, HookName: "before_compaction", Err: err})
		}
	}
	l.h.events.Emit(Event{Type: EventCompactionStart, Lane: l.name})

	result, err := compaction.Compact(ctx, prep, p, model, customInstructions, provider.ThinkingLevel(cfg.ThinkingLevel))
	if err != nil {
		return err
	}

	detailsRaw, err := json.Marshal(result.Details)
	if err != nil {
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
	if err != nil {
		return err
	}
	if _, err := l.h.opts.Storage.Commit([]session.Write{entryWrite, tipW}); err != nil {
		return err
	}
	l.h.events.Emit(Event{Type: EventEntryAdded, Lane: l.name, EntryID: entryID, ParentID: tip})
	l.h.events.Emit(Event{Type: EventCompactionEnd, Lane: l.name})
	return nil
}
