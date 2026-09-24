package compaction

import (
	"sort"
	"strings"

	"github.com/andrepato/harness/internal/msg"
)

// FileOperations is pi's FileOperations (harness/compaction/utils.d.ts):
// file paths touched while accumulating a compaction or branch summary.
type FileOperations struct {
	// Read is files read but not necessarily modified.
	Read map[string]struct{}
	// Written is files written by full-file write operations.
	Written map[string]struct{}
	// Edited is files modified by edit operations.
	Edited map[string]struct{}
}

// CreateFileOps is pi's createFileOps.
func CreateFileOps() FileOperations {
	return FileOperations{
		Read:    map[string]struct{}{},
		Written: map[string]struct{}{},
		Edited:  map[string]struct{}{},
	}
}

// toolFileArg pulls the "path" argument out of a tool call the way pi's
// extractFileOpsFromMessage does: args.path, only if it is a string.
func toolFileArg(tc msg.ToolCall) (string, bool) {
	v, ok := tc.Arguments["path"]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", false
	}
	return s, true
}

// ExtractFileOpsFromMessage is pi's extractFileOpsFromMessage
// (harness/compaction/utils.js): only assistant messages carry tool calls,
// and only the "read"/"write"/"edit" tool names (with a string "path"
// argument) count.
func ExtractFileOpsFromMessage(m msg.Message, fileOps *FileOperations) {
	am, ok := m.(msg.AssistantMessage)
	if !ok {
		return
	}
	for _, b := range am.Content {
		tc, ok := b.(msg.ToolCall)
		if !ok {
			continue
		}
		path, ok := toolFileArg(tc)
		if !ok {
			continue
		}
		switch tc.Name {
		case "read":
			fileOps.Read[path] = struct{}{}
		case "write":
			fileOps.Written[path] = struct{}{}
		case "edit":
			fileOps.Edited[path] = struct{}{}
		}
	}
}

// ComputeFileLists is pi's computeFileLists: modified is edited∪written; a
// read-only file is one read but never modified. Both lists are sorted.
func ComputeFileLists(fileOps FileOperations) (readFiles, modifiedFiles []string) {
	modified := make(map[string]struct{}, len(fileOps.Edited)+len(fileOps.Written))
	for p := range fileOps.Edited {
		modified[p] = struct{}{}
	}
	for p := range fileOps.Written {
		modified[p] = struct{}{}
	}
	for p := range fileOps.Read {
		if _, isModified := modified[p]; !isModified {
			readFiles = append(readFiles, p)
		}
	}
	sort.Strings(readFiles)
	for p := range modified {
		modifiedFiles = append(modifiedFiles, p)
	}
	sort.Strings(modifiedFiles)
	return readFiles, modifiedFiles
}

// FormatFileOperations is pi's formatFileOperations: appends <read-files>/
// <modified-files> tags to a summary, or "" if both lists are empty.
func FormatFileOperations(readFiles, modifiedFiles []string) string {
	var sections []string
	if len(readFiles) > 0 {
		sections = append(sections, "<read-files>\n"+strings.Join(readFiles, "\n")+"\n</read-files>")
	}
	if len(modifiedFiles) > 0 {
		sections = append(sections, "<modified-files>\n"+strings.Join(modifiedFiles, "\n")+"\n</modified-files>")
	}
	if len(sections) == 0 {
		return ""
	}
	return "\n\n" + strings.Join(sections, "\n\n")
}
