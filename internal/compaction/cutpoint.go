package compaction

import (
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// CutPointResult is pi's cut-point return shape
// (harness/compaction/compaction.d.ts's inline findCutPoint result),
// renamed per this port's naming: firstKeptEntryIndex -> CutIndex.
type CutPointResult struct {
	// CutIndex is the index of the first entry retained after compaction
	// (pi: firstKeptEntryIndex).
	CutIndex int
	// TurnStartIndex is the index of the turn-start entry when the cut
	// splits a turn, otherwise -1.
	TurnStartIndex int
	// IsSplitTurn is whether the selected cut point splits an in-progress
	// turn (an assistant toolCall from its toolResults).
	IsSplitTurn bool
}

// findValidCutPoints mirrors compaction.js's findValidCutPoints. A cut may
// land right before a user or assistant message, or right before a
// branch_summary entry; it may never land right before a toolResult message
// (that would separate a tool call from its own result) nor before a
// compaction or custom entry.
func findValidCutPoints(entries []session.Entry, startIndex, endIndex int) []int {
	var cutPoints []int
	for i := startIndex; i < endIndex; i++ {
		e := entries[i]
		switch e.Type {
		case session.EntryMessage:
			if e.Message == nil {
				continue
			}
			switch e.Message.MessageRole() {
			case msg.RoleUser, msg.RoleAssistant:
				cutPoints = append(cutPoints, i)
			}
		case session.EntryBranchSummary:
			cutPoints = append(cutPoints, i)
		}
	}
	return cutPoints
}

// FindTurnStartIndex mirrors compaction.js's findTurnStartIndex: scanning
// backward from entryIndex, the user-visible message (or branch_summary)
// that starts the turn containing it.
func FindTurnStartIndex(entries []session.Entry, entryIndex, startIndex int) int {
	for i := entryIndex; i >= startIndex; i-- {
		e := entries[i]
		if e.Type == session.EntryBranchSummary {
			return i
		}
		if e.Type == session.EntryMessage && e.Message != nil && e.Message.MessageRole() == msg.RoleUser {
			return i
		}
	}
	return -1
}

// FindCutPoint is pi's findCutPoint (harness/compaction/compaction.js): the
// compaction cut point that keeps approximately keepRecentTokens of recent
// history, never landing between an assistant's tool call and its tool
// result unless every valid cut point in range is already past that point
// (forced by walking cutPoints for the first one >= i).
func FindCutPoint(entries []session.Entry, startIndex, endIndex, keepRecentTokens int) CutPointResult {
	cutPoints := findValidCutPoints(entries, startIndex, endIndex)
	if len(cutPoints) == 0 {
		return CutPointResult{CutIndex: startIndex, TurnStartIndex: -1, IsSplitTurn: false}
	}

	accumulatedTokens := 0
	cutIndex := cutPoints[0]
	for i := endIndex - 1; i >= startIndex; i-- {
		e := entries[i]
		if e.Type != session.EntryMessage || e.Message == nil {
			continue
		}
		accumulatedTokens += EstimateTokens(e.Message)
		if accumulatedTokens >= keepRecentTokens {
			for _, c := range cutPoints {
				if c >= i {
					cutIndex = c
					break
				}
			}
			break
		}
	}

	// Pull any branch_summary/custom entries sitting directly before the
	// chosen cut into the retained side, so a standalone summary is never
	// left dangling in the summarized-away portion.
	for cutIndex > startIndex {
		prev := entries[cutIndex-1]
		if prev.Type == session.EntryCompaction || prev.Type == session.EntryMessage {
			break
		}
		cutIndex--
	}

	cutEntry := entries[cutIndex]
	isUserMessage := cutEntry.Type == session.EntryMessage && cutEntry.Message != nil && cutEntry.Message.MessageRole() == msg.RoleUser
	turnStartIndex := -1
	if !isUserMessage {
		turnStartIndex = FindTurnStartIndex(entries, cutIndex, startIndex)
	}
	isSplitTurn := !isUserMessage && turnStartIndex != -1

	return CutPointResult{
		CutIndex:       cutIndex,
		TurnStartIndex: turnStartIndex,
		IsSplitTurn:    isSplitTurn,
	}
}
