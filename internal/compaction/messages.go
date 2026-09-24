package compaction

import (
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// Wrapper text pi wraps a compaction/branch summary in when it is converted
// to an LLM-visible message. Copied verbatim from
// harness/messages.js:COMPACTION_SUMMARY_PREFIX/SUFFIX and
// BRANCH_SUMMARY_PREFIX/SUFFIX.
const (
	compactionSummaryPrefix = "The conversation history before this point was compacted into the following summary:\n\n<summary>\n"
	compactionSummarySuffix = "\n</summary>"
	branchSummaryPrefix     = "The following is a summary of a branch that this conversation came back from:\n\n<summary>\n"
	branchSummarySuffix     = "</summary>"
)

// wrapSummary builds the user message pi's convertToLlm produces for role
// "compactionSummary"/"branchSummary": a single text block, the summary
// wrapped in prefix/suffix, timestamped like the source entry.
func wrapSummary(prefix, summary, suffix string, timestamp int64) msg.UserMessage {
	return msg.UserMessage{
		Role:      msg.RoleUser,
		Content:   msg.Blocks{msg.Text(prefix + summary + suffix)},
		Timestamp: timestamp,
	}
}

// wrapCompactionSummary converts a session.EntryCompaction entry into the
// user message pi's convertToLlm produces for role "compactionSummary".
// See doc.go for why this happens eagerly in the Go port.
func wrapCompactionSummary(e session.Entry) msg.UserMessage {
	return wrapSummary(compactionSummaryPrefix, e.Summary, compactionSummarySuffix, e.Timestamp)
}

// wrapBranchSummary converts a session.EntryBranchSummary entry into the
// user message pi's convertToLlm produces for role "branchSummary".
func wrapBranchSummary(e session.Entry) msg.UserMessage {
	return wrapSummary(branchSummaryPrefix, e.Summary, branchSummarySuffix, e.Timestamp)
}

// getMessageFromEntry mirrors compaction.js's getMessageFromEntry: a
// message entry yields its message verbatim (including toolResult, unlike
// the branch-summary variant below); compaction/branch-summary entries are
// converted eagerly; a custom entry yields nil.
func getMessageFromEntry(e session.Entry) msg.Message {
	switch e.Type {
	case session.EntryMessage:
		if e.Message == nil {
			return nil
		}
		return e.Message
	case session.EntryBranchSummary:
		return wrapBranchSummary(e)
	case session.EntryCompaction:
		return wrapCompactionSummary(e)
	default:
		return nil
	}
}

// getMessageFromEntryForCompaction mirrors compaction.js's
// getMessageFromEntryForCompaction: identical to getMessageFromEntry except
// a compaction entry itself never becomes a message to re-summarize (it
// cannot appear inside compactableEntries anyway; ported for fidelity).
func getMessageFromEntryForCompaction(e session.Entry) msg.Message {
	if e.Type == session.EntryCompaction {
		return nil
	}
	return getMessageFromEntry(e)
}

// getMessageFromEntryBranch mirrors branch-summarization.js's local
// getMessageFromEntry: like the one above, except a message entry whose
// role is toolResult yields nothing (a branch summary never quotes raw tool
// output on its own).
func getMessageFromEntryBranch(e session.Entry) msg.Message {
	switch e.Type {
	case session.EntryMessage:
		if e.Message == nil || e.Message.MessageRole() == msg.RoleToolResult {
			return nil
		}
		return e.Message
	case session.EntryBranchSummary:
		return wrapBranchSummary(e)
	case session.EntryCompaction:
		return wrapCompactionSummary(e)
	default:
		return nil
	}
}
