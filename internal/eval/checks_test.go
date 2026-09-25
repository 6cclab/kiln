package eval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/testkit/faux"
	"github.com/andrepato/harness/internal/testkit/scenario"
)

func boolExit(n int) *int { return &n }

func TestChecks(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "src", "math.js"), []byte("function add(a,b){return a+b;}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	baseArt := func() Artifacts {
		return Artifacts{
			ProjectDir: tmp,
			Result: ResultJSON{
				OK:   true,
				Text: "Fixed the bug in add().",
				ToolCalls: []ToolCallRecord{
					{Name: "read"},
					{Name: "edit"},
				},
				Blocked:  []string{},
				NumTurns: 3,
			},
		}
	}

	tests := []struct {
		name    string
		check   scenario.Check
		art     Artifacts
		wantOK  bool
		skipped bool
	}{
		{"file_matches pass", scenario.Check{Type: "file_matches", Path: "src/math.js", Regex: "return a\\+b"}, baseArt(), true, false},
		{"file_matches fail", scenario.Check{Type: "file_matches", Path: "src/math.js", Regex: "return a-b"}, baseArt(), false, false},
		{"file_equals pass", scenario.Check{Type: "file_equals", Path: "src/math.js", Expect: "function add(a,b){return a+b;}"}, baseArt(), true, false},
		{"file_equals fail", scenario.Check{Type: "file_equals", Path: "src/math.js", Expect: "nope"}, baseArt(), false, false},
		{"file_absent pass", scenario.Check{Type: "file_absent", Path: "does/not/exist"}, baseArt(), true, false},
		{"file_absent fail", scenario.Check{Type: "file_absent", Path: "src/math.js"}, baseArt(), false, false},
		{"command pass", scenario.Check{Type: "command", Run: "exit 0"}, baseArt(), true, false},
		{"command fail", scenario.Check{Type: "command", Run: "exit 3", ExpectExit: boolExit(0)}, baseArt(), false, false},
		{"command expect_exit", scenario.Check{Type: "command", Run: "exit 3", ExpectExit: boolExit(3)}, baseArt(), true, false},
		{"result_ok pass", scenario.Check{Type: "result_ok"}, baseArt(), true, false},
		{"result_ok fail", scenario.Check{Type: "result_ok"}, Artifacts{Result: ResultJSON{OK: false}}, false, false},
		{"text_matches pass", scenario.Check{Type: "text_matches", Regex: "Fixed"}, baseArt(), true, false},
		{"text_matches fail", scenario.Check{Type: "text_matches", Regex: "Broken"}, baseArt(), false, false},
		{"tool_used pass", scenario.Check{Type: "tool_used", Name: "edit"}, baseArt(), true, false},
		{"tool_used fail", scenario.Check{Type: "tool_used", Name: "bash"}, baseArt(), false, false},
		{"tool_not_used pass", scenario.Check{Type: "tool_not_used", Name: "bash"}, baseArt(), true, false},
		{"tool_not_used fail", scenario.Check{Type: "tool_not_used", Name: "edit"}, baseArt(), false, false},
		{"tool_calls pass", scenario.Check{Type: "tool_calls", Max: 5}, baseArt(), true, false},
		{"tool_calls fail min", scenario.Check{Type: "tool_calls", Min: 5}, baseArt(), false, false},
		{"no_permission_blocks pass", scenario.Check{Type: "no_permission_blocks"}, baseArt(), true, false},
		{"no_permission_blocks fail", scenario.Check{Type: "no_permission_blocks"}, Artifacts{Result: ResultJSON{Blocked: []string{"bash"}}}, false, false},
		{"permission_blocked pass", scenario.Check{Type: "permission_blocked", Min: 1}, Artifacts{Result: ResultJSON{Blocked: []string{"bash"}}}, true, false},
		{"permission_blocked fail", scenario.Check{Type: "permission_blocked", Min: 1}, Artifacts{Result: ResultJSON{}}, false, false},
		{"turns pass", scenario.Check{Type: "turns", Max: 5}, baseArt(), true, false},
		{"turns fail", scenario.Check{Type: "turns", Max: 2}, baseArt(), false, false},
		{"compaction_occurred pass", scenario.Check{Type: "compaction_occurred"}, Artifacts{CompactionOccurred: true}, true, false},
		{"compaction_occurred fail", scenario.Check{Type: "compaction_occurred"}, Artifacts{CompactionOccurred: false}, false, false},
		{"subagent_used pass", scenario.Check{Type: "subagent_used", Role: "fast"}, Artifacts{LogEvents: []LogEvent{{Msg: "subagent model resolved", KV: map[string]string{"requested": "fast"}}}}, true, false},
		{"subagent_used fail", scenario.Check{Type: "subagent_used", Role: "fast"}, Artifacts{}, false, false},
		{"system_prompt_tokens skip (no faux)", scenario.Check{Type: "system_prompt_tokens", Max: 100}, baseArt(), false, true},
		{"system_prompt_tokens pass", scenario.Check{Type: "system_prompt_tokens", Max: 1000}, Artifacts{FauxRequests: []faux.Request{{System: "short prompt"}}}, true, false},
		{"system_prompt_tokens fail", scenario.Check{Type: "system_prompt_tokens", Max: 1}, Artifacts{FauxRequests: []faux.Request{{System: "a much longer system prompt than one token"}}}, false, false},
		{"usage pass", scenario.Check{Type: "usage", MaxInput: 1000}, baseArt(), true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := RunChecks(&scenario.Scenario{Checks: []scenario.Check{tt.check}}, tt.art)
			if len(results) != 1 {
				t.Fatalf("got %d results, want 1", len(results))
			}
			r := results[0]
			if r.Skipped != tt.skipped {
				t.Fatalf("skipped = %v, want %v (detail: %s)", r.Skipped, tt.skipped, r.Detail)
			}
			if !tt.skipped && r.Pass != tt.wantOK {
				t.Fatalf("pass = %v, want %v (detail: %s)", r.Pass, tt.wantOK, r.Detail)
			}
		})
	}
}

func TestCheckSessionUsageUnknownType(t *testing.T) {
	r := runOne(scenario.Check{Type: "bogus"}, Artifacts{})
	if r.Pass {
		t.Fatal("unknown check type should not pass")
	}
}
