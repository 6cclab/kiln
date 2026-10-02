//go:build e2e

package e2e

// Tool-call input that the permission gate and the tool would read
// differently. The gate reads keys from a map by exact name; the tools
// decode the same JSON into structs with encoding/json, which matches
// field names case-insensitively. A call whose input spells a parameter
// in another case ("PATH") would reach the tool as "path" while the gate
// saw no path at all. The turn loop refuses such input before the hooks,
// the gate or the tool run (tool.CheckArgs via internal/harness beginTool).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestToolArgs_CaseVariantKeysNeverReachTheTool drives a scripted model
// that sends write calls aimed outside the workspace through a
// case-variant key, in acceptEdits mode (where a write inside the
// workspace runs without asking). The outside file must not be created,
// the decoy inside the workspace must not be written either, and the
// session transcript must carry the refusal the model was shown.
func TestToolArgs_CaseVariantKeysNeverReachTheTool(t *testing.T) {
	cases := []struct {
		name string
		args string // YAML flow mapping; %s is the outside path
	}{
		{"decoy path plus upper-case PATH", `{path: ok.txt, PATH: "%s", content: pwned}`},
		{"upper-case PATH only", `{PATH: "%s", content: pwned}`},
		{"mixed-case Path only", `{Path: "%s", content: pwned}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proj := scratchProject(t)
			home, sessDir := scratchHome(t)
			// The "outside" target lives in a scratch directory that is
			// neither the project nor HOME, so nothing real is at risk.
			outsideDir := t.TempDir()
			outside := filepath.Join(outsideDir, "zshrc")

			script := "model: faux-1\nsteps:\n" +
				"  - tool_call: {name: write, args: " + sprintfScript(tc.args, outside) + ", id: w1}\n" +
				"  - on_tool_result: w1\n" +
				"    then:\n" +
				"      - text: \"done\"\n"
			addr, _ := startFaux(t, script)
			run := runHarness(t, proj, baseEnv(home, sessDir, addr),
				"-p", "write the file", "--output-format", "json", "--permission-mode", "acceptEdits")
			if run.Code != 0 {
				t.Fatalf("exit code %d, stderr=%s", run.Code, run.Stderr)
			}

			if _, err := os.Stat(outside); err == nil {
				t.Fatalf("%s was written: a case-variant key reached the write tool", outside)
			}
			if _, err := os.Stat(filepath.Join(proj, "ok.txt")); err == nil {
				t.Errorf("ok.txt was written: the call should have been refused outright")
			}

			raw, err := os.ReadFile(sessionFile(t, sessDir, proj))
			if err != nil {
				t.Fatal(err)
			}
			result, ok := toolResultFor(t, raw, "toolu_w1")
			if !ok {
				t.Fatalf("no tool result for toolu_w1 in the session transcript")
			}
			if !result.IsError || !strings.Contains(result.Text, "did not run") || !strings.Contains(result.Text, "only in case") {
				t.Errorf("tool result shown to the model = %+v, want an error refusing the case-variant key", result)
			}
		})
	}
}

type toolResultSeen struct {
	IsError bool
	Text    string
}

// toolResultFor finds the committed toolResult entry for callID in a JSONL
// session file (one JSON array of records per line).
func toolResultFor(t *testing.T, raw []byte, callID string) (toolResultSeen, bool) {
	t.Helper()
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "[") {
			continue // blank, or the session header object
		}
		var records []struct {
			Kind    string `json:"kind"`
			Message struct {
				Role       string `json:"role"`
				ToolCallID string `json:"toolCallId"`
				IsError    bool   `json:"isError"`
				Content    []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &records); err != nil {
			t.Fatalf("parse session line: %v\n%s", err, line)
		}
		for _, r := range records {
			if r.Kind != "entry" || r.Message.Role != "toolResult" || r.Message.ToolCallID != callID {
				continue
			}
			var text strings.Builder
			for _, c := range r.Message.Content {
				text.WriteString(c.Text)
			}
			return toolResultSeen{IsError: r.Message.IsError, Text: text.String()}, true
		}
	}
	return toolResultSeen{}, false
}
