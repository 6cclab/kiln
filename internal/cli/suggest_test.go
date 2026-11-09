package cli

import (
	"os"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestPromptSuggestionsEnabled(t *testing.T) {
	tests := []struct {
		name    string
		env     string // "" means unset
		setEnv  bool
		setting *bool
		want    bool
	}{
		{"default off", "", false, nil, false},
		{"setting on", "", false, boolPtr(true), true},
		{"setting off", "", false, boolPtr(false), false},
		{"env true wins over nil setting", "true", true, nil, true},
		{"env 1 wins over setting off", "1", true, boolPtr(false), true},
		{"env false wins over setting on", "false", true, boolPtr(true), false},
		{"env 0 wins over setting on", "0", true, boolPtr(true), false},
		{"unrecognised env falls back to setting", "maybe", true, boolPtr(true), true},
		{"unrecognised env falls back to default", "maybe", true, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setEnv {
				t.Setenv("CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION", tt.env)
			} else {
				// t.Setenv can't unset; rely on the var being absent in CI.
				// Guard by setting it to an unrecognised sentinel only when
				// the test needs the unset path — here we skip if present.
				if _, ok := os.LookupEnv("CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION"); ok {
					t.Skip("CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION is set in the environment")
				}
			}
			if got := promptSuggestionsEnabled(tt.setting); got != tt.want {
				t.Fatalf("promptSuggestionsEnabled(%v) = %v, want %v", tt.setting, got, tt.want)
			}
		})
	}
}

func TestCleanSuggestion(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "run the tests", "run the tests"},
		{"leading/trailing space", "  add error handling  ", "add error handling"},
		{"double quotes stripped", `"write the migration"`, "write the migration"},
		{"single quotes stripped", `'ship it'`, "ship it"},
		{"backticks stripped", "`make check`", "make check"},
		{"list marker stripped", "- fix the flaky test", "fix the flaky test"},
		{"numbered marker stripped", "1. deploy to staging", "deploy to staging"},
		{"first non-empty line only", "\n\nrename the handler\nand update callers", "rename the handler"},
		{"empty is no suggestion", "", ""},
		{"whitespace is no suggestion", "   \n  ", ""},
		{"nothing word is no suggestion", "nothing", ""},
		{"none word is no suggestion", "None", ""},
		{"no suggestion phrase", "No suggestion", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cleanSuggestion(tt.in); got != tt.want {
				t.Fatalf("cleanSuggestion(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCleanSuggestionCapsLength(t *testing.T) {
	long := ""
	for i := 0; i < 200; i++ {
		long += "x"
	}
	got := cleanSuggestion(long)
	if r := []rune(got); len(r) > 121 { // 120 + the ellipsis
		t.Fatalf("cleanSuggestion did not cap length: got %d runes", len(r))
	}
	if got[len(got)-len("…"):] != "…" {
		t.Fatalf("capped suggestion should end with an ellipsis, got %q", got)
	}
}
