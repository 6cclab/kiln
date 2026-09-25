package eval

import (
	"strings"
	"testing"
)

func TestParseVerdict(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
		score   float64
	}{
		{"clean", `{"score": 8, "reasons": ["did the thing", "was fast"]}`, false, 8},
		{"prose wrapped", "Sure, here is my verdict:\n```json\n{\"score\": 6.5, \"reasons\": [\"partial\"]}\n```\nHope that helps!", false, 6.5},
		{"reason singular", `{"score": 9, "reason": "great job"}`, false, 9},
		{"invalid, no object", "the agent did fine, I'd give it a 9", true, 0},
		{"invalid json inside braces", `{score: 9 not json}`, true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := parseVerdict([]byte(tt.raw))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got verdict %+v", v)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if v.Score != tt.score {
				t.Fatalf("score = %v, want %v", v.Score, tt.score)
			}
			if len(v.Reasons) == 0 {
				t.Fatal("expected at least one reason")
			}
		})
	}
}

func TestBuildRubricPrompt(t *testing.T) {
	prompt := buildRubricPrompt(simpleRubric("Did it fix the bug?"), JudgeInputs{
		Prompt:    "fix the bug",
		FinalText: "Fixed it.",
		Diff:      "*** changed: src/math.js\n",
		ToolCalls: []ToolCallRecord{{Name: "edit"}},
	})
	if prompt == "" {
		t.Fatal("expected a non-empty prompt")
	}
	for _, want := range []string{"Did it fix the bug?", "fix the bug", "Fixed it.", "edit(", "changed: src/math.js", "JSON object"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}
