package cli

import (
	"reflect"
	"testing"
)

// Ported from test/cli-args.test.ts. Flag names and semantics come from
// `claude --help`; the point of matching them is muscle memory.

func TestParseAcceptsSpaceAndEqualsForm(t *testing.T) {
	if got := Parse([]string{"--model", "ollama/qwen3.8"}).Model; got != "ollama/qwen3.8" {
		t.Errorf("space form: got %q", got)
	}
	if got := Parse([]string{"--model=ollama/qwen3.8"}).Model; got != "ollama/qwen3.8" {
		t.Errorf("equals form: got %q", got)
	}
}

func TestParseShortAliases(t *testing.T) {
	if !Parse([]string{"-c"}).ContinueLatest {
		t.Error("-c should set ContinueLatest")
	}
	if !Parse([]string{"-p", "hello"}).Print {
		t.Error("-p should set Print")
	}
	if !Parse([]string{"-v"}).Version {
		t.Error("-v should set Version")
	}
	if !Parse([]string{"-h"}).Help {
		t.Error("-h should set Help")
	}
	if got := Parse([]string{"-n", "my-session"}).Name; got != "my-session" {
		t.Errorf("-n: got %q", got)
	}
}

func TestParseResumeWithNoIdMeansLatest(t *testing.T) {
	a := Parse([]string{"--resume"})
	if !a.ResumeLatest || a.Resume != "" {
		t.Errorf("bare --resume: latest=%v id=%q", a.ResumeLatest, a.Resume)
	}
	// A following flag must not be swallowed as the id.
	a = Parse([]string{"--resume", "--verbose"})
	if !a.ResumeLatest {
		t.Error("--resume --verbose: expected latest")
	}
	if !a.Verbose {
		t.Error("--resume --verbose: expected verbose set")
	}
	a = Parse([]string{"--resume", "abc123"})
	if a.ResumeLatest || a.Resume != "abc123" {
		t.Errorf("--resume abc123: latest=%v id=%q", a.ResumeLatest, a.Resume)
	}
}

func TestParseCollectsPrintPrompt(t *testing.T) {
	a := Parse([]string{"-p", "what", "does", "this", "do"})
	if a.PrintPrompt != "what does this do" {
		t.Errorf("got %q", a.PrintPrompt)
	}
}

func TestParseRecognisesSubcommands(t *testing.T) {
	a := Parse([]string{"login", "anthropic"})
	if a.Command != "login" {
		t.Errorf("command: got %q", a.Command)
	}
	if !reflect.DeepEqual(a.Positional, []string{"anthropic"}) {
		t.Errorf("positional: got %v", a.Positional)
	}
}

func TestParseAccumulatesAddDir(t *testing.T) {
	a := Parse([]string{"--add-dir", "/a", "--add-dir", "/b"})
	if !reflect.DeepEqual(a.AddDir, []string{"/a", "/b"}) {
		t.Errorf("got %v", a.AddDir)
	}
}

func TestParseValidatesEnumeratedValues(t *testing.T) {
	if got := Parse([]string{"--effort", "high"}).Effort; got != "high" {
		t.Errorf("effort high: got %q", got)
	}
	if got := Parse([]string{"--effort", "extreme"}).Effort; got != "" {
		t.Errorf("effort extreme: expected unset, got %q", got)
	}
	if got := Parse([]string{"--output-format", "json"}).OutputFormat; got != "json" {
		t.Errorf("output-format json: got %q", got)
	}
	if got := Parse([]string{"--output-format", "yaml"}).OutputFormat; got != "" {
		t.Errorf("output-format yaml: expected unset, got %q", got)
	}
}

func TestParseMaxTurns(t *testing.T) {
	if got := Parse([]string{"--max-turns", "3"}).MaxTurns; got != 3 {
		t.Errorf("max-turns 3: got %d", got)
	}
	if got := Parse([]string{"--max-turns=5"}).MaxTurns; got != 5 {
		t.Errorf("max-turns=5: got %d", got)
	}
	if got := Parse([]string{}).MaxTurns; got != 0 {
		t.Errorf("unset max-turns: got %d, want 0 (unlimited)", got)
	}
}

func TestParseMaxTurnsInvalidValue(t *testing.T) {
	for _, argv := range [][]string{
		{"--max-turns", "0"},
		{"--max-turns", "-1"},
		{"--max-turns", "abc"},
	} {
		a := Parse(argv)
		if a.MaxTurns != 0 {
			t.Errorf("Parse(%v).MaxTurns = %d, want 0", argv, a.MaxTurns)
		}
		if a.MaxTurnsErr == "" {
			t.Errorf("Parse(%v).MaxTurnsErr = \"\", want a usage error", argv)
		}
	}
}

func TestParseReportsUnknownFlags(t *testing.T) {
	if got := Parse([]string{"--not-a-flag"}).Unknown; !reflect.DeepEqual(got, []string{"--not-a-flag"}) {
		t.Errorf("got %v", got)
	}
	if got := Parse([]string{"--verbose"}).Unknown; len(got) != 0 {
		t.Errorf("expected no unknown flags, got %v", got)
	}
}

func TestParseRealisticInvocation(t *testing.T) {
	a := Parse([]string{
		"-p",
		"fix the build",
		"--model=ollama/qwen3.8:latest",
		"--permission-mode",
		"acceptEdits",
		"--add-dir",
		"/tmp/x",
		"--allowed-tools",
		"Bash(git *) Edit",
		"--output-format=json",
		"--verbose",
	})
	if a.PrintPrompt != "fix the build" {
		t.Errorf("printPrompt: got %q", a.PrintPrompt)
	}
	if a.Model != "ollama/qwen3.8:latest" {
		t.Errorf("model: got %q", a.Model)
	}
	if a.PermissionMode != "acceptEdits" {
		t.Errorf("permissionMode: got %q", a.PermissionMode)
	}
	if !reflect.DeepEqual(a.AddDir, []string{"/tmp/x"}) {
		t.Errorf("addDir: got %v", a.AddDir)
	}
	if !reflect.DeepEqual(a.AllowedTools, []string{"Bash(git *)", "Edit"}) {
		t.Errorf("allowedTools: got %v", a.AllowedTools)
	}
	if a.OutputFormat != "json" {
		t.Errorf("outputFormat: got %q", a.OutputFormat)
	}
	if !a.Verbose {
		t.Error("expected verbose")
	}
}

func TestSplitToolListKeepsParenRuleIntact(t *testing.T) {
	got := SplitToolList("Bash(git *) Edit")
	want := []string{"Bash(git *)", "Edit"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestSplitToolListSeveralParenRules(t *testing.T) {
	got := SplitToolList("Bash(npm run *) Bash(git commit *) Read")
	want := []string{"Bash(npm run *)", "Bash(git commit *)", "Read"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestSplitToolListCommaForm(t *testing.T) {
	got := SplitToolList("Read,Write,Edit")
	want := []string{"Read", "Write", "Edit"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestSplitToolListNoSplitInsideParensOnComma(t *testing.T) {
	got := SplitToolList("Bash(a,b)")
	want := []string{"Bash(a,b)"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestSplitToolListEmptyInput(t *testing.T) {
	if got := SplitToolList(""); len(got) != 0 {
		t.Errorf("got %v", got)
	}
	if got := SplitToolList("   "); len(got) != 0 {
		t.Errorf("got %v", got)
	}
}

func TestParseSettingSources(t *testing.T) {
	a := Parse([]string{"--setting-sources", "user, project ,local"})
	want := []string{"user", "project", "local"}
	if !reflect.DeepEqual(a.SettingSources, want) {
		t.Errorf("got %v want %v", a.SettingSources, want)
	}
}

func TestParseValuedFlagDoesNotSwallowFollowingFlag(t *testing.T) {
	a := Parse([]string{"--add-dir", "-v"})
	if len(a.AddDir) != 0 {
		t.Errorf("expected --add-dir to take no value, got %v", a.AddDir)
	}
	if !a.Version {
		t.Error("expected -v to still be parsed")
	}
}
