package compaction

import (
	"context"
	"encoding/json"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/session"
)

// branchSummaryPreamble is pi's BRANCH_SUMMARY_PREAMBLE
// (harness/compaction/branch-summarization.js), copied verbatim.
const branchSummaryPreamble = `The user explored a different conversation branch before returning here.
Summary of that exploration:

`

// branchSummaryPrompt is pi's BRANCH_SUMMARY_PROMPT, copied verbatim.
const branchSummaryPrompt = `Create a structured summary of this conversation branch for context when returning later.

Use this EXACT format:

## Goal
[What was the user trying to accomplish in this branch?]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Work that was started but not finished]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [What should happen next to continue this work]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

// branchSummaryMaxTokens is pi's inline {maxTokens: 2048} passed to the
// branch summarization request (branch-summarization.js). Unlike Compact,
// pi's branch summary call never applies reasoning/thinkingLevel.
const branchSummaryMaxTokens = 2048

// BranchPreparation is pi's BranchPreparation (harness/compaction/
// branch-summarization.d.ts).
type BranchPreparation struct {
	// Messages are selected for the branch summary, oldest first.
	Messages []msg.Message
	// FileOps are file operations extracted from Messages, seeded from any
	// nested branch_summary entries' recorded Details.
	FileOps FileOperations
	// TotalTokens is the estimated token count of Messages.
	TotalTokens int
}

// BranchSummaryResult is pi's BranchSummaryResult.
type BranchSummaryResult struct {
	Summary       string
	Usage         *msg.Usage
	ReadFiles     []string
	ModifiedFiles []string
}

// branchSummaryDetails is the shape stored on a session.EntryBranchSummary's
// Details field, read back by PrepareBranchSummary to seed FileOps for a
// branch_summary entry nested inside a later branch summary's range.
type branchSummaryDetails struct {
	ReadFiles     []string `json:"readFiles"`
	ModifiedFiles []string `json:"modifiedFiles"`
}

// PrepareBranchSummary is pi's prepareBranchEntries (harness/compaction/
// branch-summarization.js): walk entries newest-first, converting each to a
// message (dropping toolResult messages and custom entries), accumulating
// file operations and a token budget. tokenBudget <= 0 means unlimited,
// matching pi's `tokenBudget = 0` default.
func PrepareBranchSummary(entries []session.Entry, tokenBudget int) BranchPreparation {
	fileOps := CreateFileOps()
	for _, e := range entries {
		if e.Type != session.EntryBranchSummary || len(e.Details) == 0 {
			continue
		}
		var det branchSummaryDetails
		if err := json.Unmarshal(e.Details, &det); err != nil {
			continue
		}
		for _, p := range det.ReadFiles {
			fileOps.Read[p] = struct{}{}
		}
		for _, p := range det.ModifiedFiles {
			fileOps.Edited[p] = struct{}{}
		}
	}

	var messages []msg.Message
	totalTokens := 0
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		m := getMessageFromEntryBranch(e)
		if m == nil {
			continue
		}
		ExtractFileOpsFromMessage(m, &fileOps)
		tokens := EstimateTokens(m)
		if tokenBudget > 0 && totalTokens+tokens > tokenBudget {
			if e.Type == session.EntryCompaction || e.Type == session.EntryBranchSummary {
				if float64(totalTokens) < float64(tokenBudget)*0.9 {
					messages = append([]msg.Message{m}, messages...)
					totalTokens += tokens
				}
			}
			break
		}
		messages = append([]msg.Message{m}, messages...)
		totalTokens += tokens
	}

	return BranchPreparation{Messages: messages, FileOps: fileOps, TotalTokens: totalTokens}
}

// SummarizeBranch is pi's generateBranchSummaryWithRequest
// (harness/compaction/branch-summarization.js).
func SummarizeBranch(ctx context.Context, prep BranchPreparation, streamer Streamer, model provider.Model, customInstructions *string, replaceInstructions bool) (BranchSummaryResult, error) {
	if len(prep.Messages) == 0 {
		return BranchSummaryResult{Summary: "No content to summarize", ReadFiles: []string{}, ModifiedFiles: []string{}}, nil
	}

	conversationText := SerializeConversation(prep.Messages)
	var instructions string
	switch {
	case replaceInstructions && customInstructions != nil && *customInstructions != "":
		instructions = *customInstructions
	case customInstructions != nil && *customInstructions != "":
		instructions = branchSummaryPrompt + "\n\nAdditional focus: " + *customInstructions
	default:
		instructions = branchSummaryPrompt
	}
	promptText := "<conversation>\n" + conversationText + "\n</conversation>\n\n" + instructions

	text, usage, err := runSimple(ctx, streamer, model, SummarizationSystemPrompt, promptText, branchSummaryMaxTokens, "")
	if err != nil {
		return BranchSummaryResult{}, err
	}

	summary := branchSummaryPreamble + text
	readFiles, modifiedFiles := ComputeFileLists(prep.FileOps)
	summary += FormatFileOperations(readFiles, modifiedFiles)

	return BranchSummaryResult{
		Summary:       summary,
		Usage:         &usage,
		ReadFiles:     readFiles,
		ModifiedFiles: modifiedFiles,
	}, nil
}
