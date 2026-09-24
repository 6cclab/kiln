package compaction

import (
	"encoding/json"
	"fmt"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// Preparation is what Prepare produces: everything Compact needs to call
// the model and everything the caller needs to write a new
// session.EntryCompaction. It corresponds to pi's CompactionPreparation
// (harness/compaction/compaction.d.ts), plus FirstKeptEntryID, which pi
// does not track (pi's caller re-derives the parent id from the returned
// message arrays; this port's caller needs the id directly to set the new
// compaction entry's ParentID).
type Preparation struct {
	// MessagesToSummarize are the messages the summarization prompt is
	// built from.
	MessagesToSummarize []msg.Message
	// TurnPrefixMessages are the prefix of a split turn, summarized
	// separately, when IsSplitTurn is true. Empty otherwise.
	TurnPrefixMessages []msg.Message
	// RetainedTail is kept verbatim after compaction; it becomes the new
	// session.EntryCompaction's RetainedTail.
	RetainedTail []msg.Message
	// IsSplitTurn is whether the chosen cut point falls inside an
	// in-progress turn.
	IsSplitTurn bool
	// TokensBefore is the estimated context tokens the path represented
	// before this compaction, from CalculateContextTokens(pathEntries).
	TokensBefore int
	// FirstKeptEntryID is the id of the first retained entry (real, or a
	// prior compaction's synthetic ":retained:N" id), or "" if the cut
	// point fell at or past the end of the compactable range.
	FirstKeptEntryID string
	// PreviousSummary is the prior compaction's summary, non-nil when this
	// is an iterative update.
	PreviousSummary *string
	// FileOps are the file operations extracted from MessagesToSummarize
	// (and, for a split turn, TurnPrefixMessages too), plus whatever a
	// prior compaction's Details already recorded.
	FileOps FileOperations
	// Settings are the settings Prepare was called with.
	Settings Settings
}

// compactionDetails is the shape pi (and this port) stores in a
// session.EntryCompaction's Details field.
type compactionDetails struct {
	ReadFiles     []string `json:"readFiles"`
	ModifiedFiles []string `json:"modifiedFiles"`
}

// extractFileOperations mirrors compaction.js's extractFileOperations: seed
// from the previous compaction's recorded Details (so file history survives
// across iterative compactions), then walk the messages being summarized.
func extractFileOperations(messages []msg.Message, entries []session.Entry, prevCompactionIndex int) FileOperations {
	fileOps := CreateFileOps()
	if prevCompactionIndex >= 0 {
		prev := entries[prevCompactionIndex]
		if len(prev.Details) > 0 {
			var det compactionDetails
			if err := json.Unmarshal(prev.Details, &det); err == nil {
				for _, p := range det.ReadFiles {
					fileOps.Read[p] = struct{}{}
				}
				for _, p := range det.ModifiedFiles {
					fileOps.Edited[p] = struct{}{}
				}
			}
		}
	}
	for _, m := range messages {
		ExtractFileOpsFromMessage(m, &fileOps)
	}
	return fileOps
}

// Prepare is pi's prepareCompaction (harness/compaction/compaction.js).
// It returns (nil, nil) when compaction is not applicable: an empty path,
// or a path whose tip is already a compaction entry (pi: ok(undefined)).
func Prepare(pathEntries []session.Entry, settings Settings) (*Preparation, error) {
	if len(pathEntries) == 0 || pathEntries[len(pathEntries)-1].Type == session.EntryCompaction {
		return nil, nil
	}

	prevCompactionIndex := -1
	for i := len(pathEntries) - 1; i >= 0; i-- {
		if pathEntries[i].Type == session.EntryCompaction {
			prevCompactionIndex = i
			break
		}
	}

	var previousSummary *string
	compactableEntries := pathEntries
	if prevCompactionIndex >= 0 {
		prevCompaction := pathEntries[prevCompactionIndex]
		summary := prevCompaction.Summary
		previousSummary = &summary

		virtual := make([]session.Entry, len(prevCompaction.RetainedTail))
		for i, m := range prevCompaction.RetainedTail {
			parentID := prevCompaction.ID
			if i > 0 {
				parentID = fmt.Sprintf("%s:retained:%d", prevCompaction.ID, i-1)
			}
			virtual[i] = session.Entry{
				Type:      session.EntryMessage,
				ID:        fmt.Sprintf("%s:retained:%d", prevCompaction.ID, i),
				ParentID:  &parentID,
				Seq:       prevCompaction.Seq,
				Timestamp: m.MessageTimestamp(),
				Message:   m,
			}
		}
		compactableEntries = make([]session.Entry, 0, len(virtual)+len(pathEntries)-prevCompactionIndex-1)
		compactableEntries = append(compactableEntries, virtual...)
		compactableEntries = append(compactableEntries, pathEntries[prevCompactionIndex+1:]...)
	}

	boundaryEnd := len(compactableEntries)
	tokensBefore := CalculateContextTokens(pathEntries).Tokens
	cutPoint := FindCutPoint(compactableEntries, 0, boundaryEnd, settings.KeepRecentTokens)

	historyEnd := cutPoint.CutIndex
	if cutPoint.IsSplitTurn {
		historyEnd = cutPoint.TurnStartIndex
	}

	var messagesToSummarize []msg.Message
	for i := 0; i < historyEnd; i++ {
		if m := getMessageFromEntryForCompaction(compactableEntries[i]); m != nil {
			messagesToSummarize = append(messagesToSummarize, m)
		}
	}

	var turnPrefixMessages []msg.Message
	if cutPoint.IsSplitTurn {
		for i := cutPoint.TurnStartIndex; i < cutPoint.CutIndex; i++ {
			if m := getMessageFromEntryForCompaction(compactableEntries[i]); m != nil {
				turnPrefixMessages = append(turnPrefixMessages, m)
			}
		}
	}

	var retainedTail []msg.Message
	for i := cutPoint.CutIndex; i < boundaryEnd; i++ {
		if m := getMessageFromEntryForCompaction(compactableEntries[i]); m != nil {
			retainedTail = append(retainedTail, m)
		}
	}

	fileOps := extractFileOperations(messagesToSummarize, pathEntries, prevCompactionIndex)
	if cutPoint.IsSplitTurn {
		for _, m := range turnPrefixMessages {
			ExtractFileOpsFromMessage(m, &fileOps)
		}
	}

	firstKeptEntryID := ""
	if cutPoint.CutIndex < boundaryEnd {
		firstKeptEntryID = compactableEntries[cutPoint.CutIndex].ID
	}

	return &Preparation{
		MessagesToSummarize: messagesToSummarize,
		TurnPrefixMessages:  turnPrefixMessages,
		RetainedTail:        retainedTail,
		IsSplitTurn:         cutPoint.IsSplitTurn,
		TokensBefore:        tokensBefore,
		FirstKeptEntryID:    firstKeptEntryID,
		PreviousSummary:     previousSummary,
		FileOps:             fileOps,
		Settings:            settings,
	}, nil
}
