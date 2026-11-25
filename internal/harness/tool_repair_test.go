package harness

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
)

// TestRepairToolNameOnlyFixesUnambiguousSlips: case, "-" for "_" and a
// wrapper prefix are repaired when exactly one tool matches; anything else
// is not, and close names are offered instead.
func TestRepairToolNameOnlyFixesUnambiguousSlips(t *testing.T) {
	callable := []string{"bash", "read", "write", "web_fetch", "mcp__git__status"}
	for in, want := range map[string]string{"Bash": "bash", "functions.read": "read", "web-fetch": "web_fetch", "default_api:write": "write", "MCP__git__status": "mcp__git__status"} {
		if got, ok := repairToolName(in, callable); !ok || got != want {
			t.Errorf("repairToolName(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"read_file", "shell", "writ"} {
		if got, ok := repairToolName(in, callable); ok {
			t.Errorf("repairToolName(%q) = %q, want no repair", in, got)
		}
	}
	if _, ok := repairToolName("Read", []string{"read", "READ"}); ok {
		t.Error("an ambiguous name was repaired")
	}
	if got := closeToolNames("read_file", callable); !reflect.DeepEqual(got, []string{"read"}) {
		t.Errorf("closeToolNames(read_file) = %v, want [read]", got)
	}
	if got := closeToolNames("writ", callable); len(got) == 0 || got[0] != "write" {
		t.Errorf("closeToolNames(writ) = %v, want write first", got)
	}
}

// TestToolCallsAreRepairedBeforeTheyRun: a call named "Bash" with its
// timeout as a string runs as bash with a number, and the committed reply
// carries the repaired call; an unknown name is refused with close names.
func TestToolCallsAreRepairedBeforeTheyRun(t *testing.T) {
	script := "model: faux-1\nsteps:\n" +
		"  - tool_call: {name: Bash, args: {command: \"echo repaired\", timeout: \"30\"}, id: b1}\n" +
		"  - on_tool_result: b1\n" +
		"    then:\n" +
		"      - tool_call: {name: read_file, args: {path: x}, id: r1}\n" +
		"      - on_tool_result: r1\n" +
		"        then:\n" +
		"          - text: \"done\"\n"
	rig := newTestRig(t, script, []string{"bash", "read"})
	var results []*msg.ToolResultMessage
	rig.H.Events().On(EventToolEnd, func(ev Event) { results = append(results, ev.ToolResult) })
	lane := rig.mustLane("main")
	if _, err := lane.Prompt(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("%d tool results, want 2", len(results))
	}
	if r := results[0]; r.IsError || r.ToolName != "bash" || !strings.Contains(msg.TextOf(r.Content), "repaired") {
		t.Errorf("first call = %s %v %q, want bash to run", r.ToolName, r.IsError, msg.TextOf(r.Content))
	}
	if r := results[1]; !r.IsError || !strings.Contains(msg.TextOf(r.Content), "There is no tool named read_file, so the call did not run. Did you mean read?") {
		t.Errorf("second call = %q, want a refusal naming read", msg.TextOf(r.Content))
	}
	entries, _ := lane.FindEntries(context.Background())
	for _, e := range entries {
		if am, ok := e.Message.(msg.AssistantMessage); ok {
			for _, c := range am.Content {
				if call, ok := c.(msg.ToolCall); ok && call.ID == "b1" {
					if call.Name != "bash" || call.Arguments["timeout"] != float64(30) {
						t.Errorf("committed call = %s %v, want bash with timeout 30", call.Name, call.Arguments)
					}
				}
			}
		}
	}
}
