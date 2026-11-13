package tui

import (
	"fmt"
	"hash/fnv"
	"strings"
	"sync/atomic"
)

// Transcript rows a mod's ui.render hook may draw in the engine's place
// (UserMessage, AssistantMessage, ToolUse, ToolGroup, InfoNotice, and
// CommandOutput in bridge.go). The render functions are plain functions that
// the live commit paths and the replay both call, so the drawer is one
// process-wide binding (Bridge.SetRowDrawer); with none bound, or when no mod
// draws the row, the engine's own rows come back unchanged.

// RowDrawFunc returns the rows a mod drew for a component, nil when none did.
type RowDrawFunc = func(component, requestID string, props map[string]any, width int) []string

var modRowDrawer atomic.Pointer[RowDrawFunc]

// drawModRow asks the bound drawer for a mod's rows (nil: draw the engine's).
func drawModRow(component, requestID string, props map[string]any, width int) []string {
	f := modRowDrawer.Load()
	if f == nil || *f == nil {
		return nil
	}
	return (*f)(component, requestID, props, width)
}

// rowID names a row for ui.render's requestId. kiln keeps no message or tool
// call id on a rendered row, so it is a stable hash of what the row says: the
// same row has the same id in the live transcript and in a replay.
func rowID(kind, text string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(text))
	return fmt.Sprintf("%s-%08x", kind, h.Sum32())
}

// modAssistantRows is a mod's drawing of an assistant message (nil: none).
// kiln commits a reply's markdown as one block, so isFirstOfReply is true.
func modAssistantRows(text string, width int) []string {
	return drawModRow("AssistantMessage", rowID("assistant", text),
		map[string]any{"text": text, "isFirstOfReply": true}, width)
}

// toolViewProps are the ToolUse props of a call as kiln knows it: the
// identifying argument stands for the input, the result lines for the output.
func toolViewProps(v ToolCallView) map[string]any {
	p := map[string]any{
		"tool": v.Name, "input": v.PrimaryArg,
		"isRunning": v.Status == CallRunning, "isErrored": v.Status == CallError, "isInterrupted": false,
	}
	if v.Status != CallRunning && len(v.ResultLines) > 0 {
		p["output"] = strings.Join(v.ResultLines, "\n")
	}
	return p
}
