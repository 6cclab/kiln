package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/claude/skills"
	mcpgate "github.com/andrepato/harness/internal/mcp"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session/jsonl"
	tkfaux "github.com/andrepato/harness/internal/testkit/faux"
	"github.com/andrepato/harness/internal/testkit/fauxtest"
)

// startFaux starts a scripted faux server and points HARNESS_FAUX_ADDR at
// it (registering the "faux" provider for the duration of the test, via
// t.Setenv), and returns the server so a test can inspect Requests().
func startFaux(t *testing.T, scriptYAML string) *tkfaux.Server {
	t.Helper()
	addr, srv := fauxtest.Start(t, scriptYAML)
	t.Setenv("HARNESS_FAUX_ADDR", addr)
	t.Setenv("HARNESS_FAUX_API", "anthropic-messages")
	return srv
}

// chdirTemp chdirs into dir for the duration of the test (Run resolves its
// own cwd via os.Getwd, mirroring cli.ts's process.cwd()).
func chdirTemp(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

// scratchProject builds a fresh project directory, a fresh $HOME and a
// fresh session store, all isolated per test, and chdirs into the project.
// Returns the project directory.
func scratchProject(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	sessDir := t.TempDir()
	t.Setenv("HARNESS_SESSIONS_DIR", sessDir)
	proj := t.TempDir()
	chdirTemp(t, proj)
	// Run() (like cli.ts's process.cwd()) resolves cwd via os.Getwd(),
	// which on macOS resolves /var's symlink to /private/var — re-resolve
	// here too, or sessionFile's directory-name computation (which must
	// match exactly) would look in the wrong place.
	resolved, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// baseArgs returns Args with every slice field non-nil (matching what
// cli.Parse itself produces) and the model pinned to the faux provider, so
// tests never depend on Ollama being reachable.
func baseArgs() Args {
	return Args{
		Positional:      []string{},
		AddDir:          []string{},
		AllowedTools:    []string{},
		DisallowedTools: []string{},
		Unknown:         []string{},
		Model:           "faux/faux-1",
	}
}

// sessionFile finds the single .jsonl session file under sessDir for cwd,
// using jsonl's own directory-naming scheme.
func sessionFile(t *testing.T, sessDir, cwd string) string {
	t.Helper()
	dir := filepath.Join(sessDir, jsonl.DirectoryName(cwd))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read session dir %s: %v", dir, err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".jsonl") {
			return filepath.Join(dir, e.Name())
		}
	}
	t.Fatalf("no .jsonl session file under %s", dir)
	return ""
}

// ndjsonTypes parses stdout as NDJSON stream events and returns each
// line's "type" field, in order.
func ndjsonTypes(t *testing.T, stdout string) []string {
	t.Helper()
	var types []string
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("parse stream-json line %q: %v", line, err)
		}
		ty, _ := ev["type"].(string)
		types = append(types, ty)
	}
	return types
}

const fixBugScript = `model: faux-1
steps:
  - text: "I'll look at the file."
  - thinking: "the add function subtracts"
  - tool_call: {name: read, args: {path: src/math.js}, id: tc1}
  - on_tool_result: tc1
    then:
      - tool_call:
          name: edit
          args:
            path: src/math.js
            edits:
              - oldText: "return a - b;"
                newText: "return a + b;"
          id: tc2
  - on_tool_result: tc2
    then:
      - text: "Fixed."
        usage: {input: 812, output: 34}
`

// TestRun_PrintStreamJSON_FixBug drives Run end to end against faux with
// --output-format stream-json: the model reads src/math.js, edits it, and
// replies "Fixed." Ported scenario from testdata/faux/fix-bug.yaml.
func TestRun_PrintStreamJSON_FixBug(t *testing.T) {
	srv := startFaux(t, fixBugScript)
	proj := scratchProject(t)
	sessDir := os.Getenv("HARNESS_SESSIONS_DIR")

	if err := os.MkdirAll(filepath.Join(proj, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	mathJS := "function add(a, b) {\n  return a - b;\n}\n"
	if err := os.WriteFile(filepath.Join(proj, "src", "math.js"), []byte(mathJS), 0o644); err != nil {
		t.Fatal(err)
	}

	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "fix the bug"
	args.OutputFormat = "stream-json"
	args.PermissionMode = "acceptEdits"

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit code %d, stderr=%s", code, stderr.String())
	}

	// The Collector reacts to a turn's completed assistant message, which
	// fires as soon as that message finishes streaming — before its tool
	// call (if any) executes. Turn 1's message carries both "I'll look at
	// the file." and the read tool_call, so its "assistant" event comes
	// first; turn 2 (the edit tool_call alone) has no text, so it emits no
	// assistant event; turn 3's "Fixed." is the final one.
	types := ndjsonTypes(t, stdout.String())
	want := []string{"assistant", "tool_start", "tool_end", "tool_start", "tool_end", "assistant", "result"}
	if fmt.Sprint(types) != fmt.Sprint(want) {
		t.Fatalf("event sequence = %v, want %v", types, want)
	}

	var sawAssistantFixed, sawResultOK bool
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var ev map[string]any
		_ = json.Unmarshal([]byte(line), &ev)
		switch ev["type"] {
		case "assistant":
			if ev["text"] == "Fixed." {
				sawAssistantFixed = true
			}
		case "result":
			if ok, _ := ev["ok"].(bool); ok {
				sawResultOK = true
			}
		}
	}
	if !sawAssistantFixed {
		t.Error(`did not see {"type":"assistant","text":"Fixed."}`)
	}
	if !sawResultOK {
		t.Error(`did not see {"type":"result","ok":true,...}`)
	}

	fixed, err := os.ReadFile(filepath.Join(proj, "src", "math.js"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fixed), "return a + b;") {
		t.Errorf("src/math.js not fixed, got:\n%s", fixed)
	}

	// A session JSONL exists with a toolResult for the edit call.
	sess := sessionFile(t, sessDir, proj)
	raw, err := os.ReadFile(sess)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"toolName":"edit"`) || !strings.Contains(string(raw), `"role":"toolResult"`) {
		t.Errorf("session file has no edit toolResult entry:\n%s", raw)
	}

	// Faux recorded the system prompt and the edit tool's schema.
	reqs := srv.Requests()
	if len(reqs) == 0 {
		t.Fatal("faux recorded no requests")
	}
	last := reqs[len(reqs)-1]
	if !strings.HasPrefix(last.System, agent.BasePrompt) {
		t.Errorf("recorded system prompt does not start with agent.BasePrompt: %q", last.System)
	}
	var sawEditSchema bool
	for _, tl := range last.Tools {
		if tl.Name == "edit" && strings.Contains(string(tl.Schema), "oldText") {
			sawEditSchema = true
		}
	}
	if !sawEditSchema {
		t.Errorf("recorded request tools missing edit's schema: %+v", last.Tools)
	}
}

// TestRun_PrintFormats checks the "json" and "text" output-format shapes
// against the same fix-bug scenario.
func TestRun_PrintFormats(t *testing.T) {
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			startFaux(t, fixBugScript)
			proj := scratchProject(t)
			if err := os.MkdirAll(filepath.Join(proj, "src"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(proj, "src", "math.js"), []byte("return a - b;\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			args := baseArgs()
			args.Print = true
			args.PrintPrompt = "fix the bug"
			args.OutputFormat = format
			args.PermissionMode = "acceptEdits"

			var stdout, stderr bytes.Buffer
			code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader(""))
			if code != 0 {
				t.Fatalf("exit code %d, stderr=%s", code, stderr.String())
			}

			switch format {
			case "json":
				var parsed struct {
					OK        bool             `json:"ok"`
					Text      string           `json:"text"`
					ToolCalls []map[string]any `json:"toolCalls"`
					Blocked   []string         `json:"blocked"`
				}
				if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
					t.Fatalf("parse json output: %v\n%s", err, stdout.String())
				}
				if !parsed.OK {
					t.Error("ok = false")
				}
				// The Collector concatenates every turn's assistant text,
				// not just the final one (see print.go's onMessageUpdate):
				// turn 1's "I'll look at the file." and turn 3's "Fixed."
				if !strings.Contains(parsed.Text, "Fixed.") {
					t.Errorf("text = %q, want it to contain %q", parsed.Text, "Fixed.")
				}
				if len(parsed.ToolCalls) < 2 {
					t.Errorf("toolCalls = %v, want at least 2", parsed.ToolCalls)
				}
			case "text":
				if !strings.Contains(stdout.String(), "Fixed.") {
					t.Errorf("stdout = %q, want it to contain %q", stdout.String(), "Fixed.")
				}
			}
		})
	}
}

const bashCallScript = `model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "echo hi | tee hi.txt"}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`

// TestRun_ManualMode_BlocksBash checks that --permission-mode manual
// refuses a tool call with nobody to ask, rather than proceeding: the
// gate's refusal reason becomes the tool's own (isError) result, the model
// still gets to respond to it (bashCallScript's on_tool_result step fires
// regardless of the result's isError, matching a real provider — a tool
// error is content, not a wire failure), and the run's overall "blocked"
// list still names the refusal for the caller to see.
func TestRun_ManualMode_BlocksBash(t *testing.T) {
	startFaux(t, bashCallScript)
	scratchProject(t)

	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "run a command"
	args.OutputFormat = "json"
	args.PermissionMode = "manual"

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}

	var parsed struct {
		Blocked []string `json:"blocked"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("parse json output: %v\n%s", err, stdout.String())
	}
	found := false
	for _, b := range parsed.Blocked {
		if strings.Contains(b, "requires confirmation and no prompt is available") {
			found = true
		}
	}
	if !found {
		t.Errorf("blocked = %v, want an entry mentioning \"requires confirmation and no prompt is available\"", parsed.Blocked)
	}
}

// hooksFixture returns the absolute path to a script under
// internal/claude/hooks/testdata/hooks. Resolved via runtime.Caller rather
// than a relative path, because by the time a test calls this it has
// already chdir'd into its own scratch project (scratchProject) — a
// relative path would resolve against that, not against this source file.
func hooksFixture(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "claude", "hooks", "testdata", "hooks", name)
}

// writeHookSettings writes a scratch .claude/settings.json under proj
// wiring one command on one event, with an optional tool matcher.
func writeHookSettings(t *testing.T, proj, event, matcher, command string) {
	t.Helper()
	settings := map[string]any{
		"hooks": map[string]any{
			event: []map[string]any{
				{
					"matcher": matcher,
					"hooks": []map[string]any{
						{"type": "command", "command": command},
					},
				},
			},
		},
	}
	data, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(proj, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

const readCallScript = `model: faux-1
steps:
  - tool_call: {name: read, args: {path: foo.txt}, id: tc1}
  - on_tool_result: tc1
    then:
      - text: "done"
`

// TestRun_PreToolUseHook_Blocks wires block-exit2.sh (exit 2, "not allowed
// here" on stderr) on PreToolUse and checks that the blocked call comes
// back as tool_end isError:true, and that the run's overall "blocked" list
// (populated from every before_tool refusal, hook or gate — see chat.go's
// recordBlocked) names the reason.
func TestRun_PreToolUseHook_Blocks(t *testing.T) {
	startFaux(t, readCallScript)
	proj := scratchProject(t)
	writeHookSettings(t, proj, "PreToolUse", "*", hooksFixture(t, "block-exit2.sh"))

	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "read the file"
	args.OutputFormat = "stream-json"
	args.PermissionMode = "acceptEdits"

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader(""))
	_ = code // the run still completes (the model gets an error result and replies); only the tool call itself is blocked.

	var sawBlockedToolEnd bool
	var blocked []string
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		var ev map[string]any
		_ = json.Unmarshal([]byte(line), &ev)
		if ev["type"] == "tool_end" {
			if isErr, _ := ev["isError"].(bool); isErr {
				sawBlockedToolEnd = true
			}
		}
		if ev["type"] == "result" {
			if b, ok := ev["blocked"].([]any); ok {
				for _, x := range b {
					blocked = append(blocked, fmt.Sprint(x))
				}
			}
		}
	}
	if !sawBlockedToolEnd {
		t.Errorf("no tool_end with isError:true in:\n%s", stdout.String())
	}
	var foundReason bool
	for _, b := range blocked {
		if strings.Contains(b, "not allowed here") {
			foundReason = true
		}
	}
	if !foundReason {
		t.Errorf("blocked = %v, want an entry containing the hook's stderr reason", blocked)
	}
}

// TestRun_PreToolUseHook_Rewrites wires rewrite-updated-input.sh (which
// unconditionally rewrites the bash command to "rtk git status") on
// PreToolUse for the bash tool, and checks the harness actually executed
// the rewritten command rather than the model's original one: bashCallScript
// asks for "echo hi", so a toolResult whose content is exactly "hi\n" would
// mean the rewrite never took effect; anything else is evidence it did
// (on this machine, "rtk git status" against a non-git scratch directory
// produces "Not a git repository...", confirmed by running the fixture
// directly).
//
// Deviation from the phase brief: the brief describes this fixture as
// producing "echo rewritten" in the tool result, but the actual fixture
// (internal/claude/hooks/testdata/hooks/rewrite-updated-input.sh) always
// rewrites to "command":"rtk git status" regardless of the original
// command; there is no "echo rewritten" anywhere in it. This test asserts
// what that fixture actually does rather than the brief's description of it.
func TestRun_PreToolUseHook_Rewrites(t *testing.T) {
	startFaux(t, bashCallScript)
	proj := scratchProject(t)
	sessDir := os.Getenv("HARNESS_SESSIONS_DIR")
	writeHookSettings(t, proj, "PreToolUse", "bash", hooksFixture(t, "rewrite-updated-input.sh"))

	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "run a command"
	args.OutputFormat = "text"
	args.PermissionMode = "bypassPermissions"

	var stdout, stderr bytes.Buffer
	_ = Run(context.Background(), args, &stdout, &stderr, strings.NewReader(""))

	sess := sessionFile(t, sessDir, proj)
	raw, err := os.ReadFile(sess)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"toolName":"bash"`) && strings.Contains(string(raw), `"text":"hi\n"`) {
		t.Errorf("bash ran \"echo hi\" unchanged; the PreToolUse rewrite never took effect:\n%s", raw)
	}
}

const unreadScript = `model: faux-1
steps:
  - text: "hi"
`

// TestRun_UserPromptSubmitHook_Context wires context-plaintext.sh (which
// prints "you have 2 unread messages" on stdout, plain text) on
// UserPromptSubmit, and checks that text reaches the model: it must appear
// in the request faux recorded, wrapped in <hook-context> ahead of the
// prompt (chat.go's runPrintMode).
func TestRun_UserPromptSubmitHook_Context(t *testing.T) {
	srv := startFaux(t, unreadScript)
	proj := scratchProject(t)
	writeHookSettings(t, proj, "UserPromptSubmit", "", hooksFixture(t, "context-plaintext.sh"))

	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "hello"
	args.OutputFormat = "text"

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit code %d, stderr=%s", code, stderr.String())
	}

	reqs := srv.Requests()
	if len(reqs) == 0 {
		t.Fatal("faux recorded no requests")
	}
	if !strings.Contains(string(reqs[0].Messages), "you have 2 unread messages") {
		t.Errorf("recorded messages missing hook context: %s", reqs[0].Messages)
	}
	_ = proj
}

// TestRun_SessionStartAndEnd_Hooks wires record-payload.sh on both
// SessionStart and SessionEnd, each writing to its own file (the fixture
// truncates on every write, via `cat > "$HARNESS_TEST_PAYLOAD_FILE"`, so
// one file cannot hold both events — the env var is set inline in each
// hook's own command instead of via the process environment).
func TestRun_SessionStartAndEnd_Hooks(t *testing.T) {
	startFaux(t, unreadScript)
	proj := scratchProject(t)

	startFile := filepath.Join(t.TempDir(), "start.json")
	endFile := filepath.Join(t.TempDir(), "end.json")
	script := hooksFixture(t, "record-payload.sh")

	writeHookSettings(t, proj, "SessionStart", "", fmt.Sprintf("HARNESS_TEST_PAYLOAD_FILE=%s %s", startFile, script))
	// writeHookSettings overwrites .claude/settings.json; merge the second
	// event in by hand instead of calling it again.
	data, err := os.ReadFile(filepath.Join(proj, ".claude", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	hooksMap := parsed["hooks"].(map[string]any)
	hooksMap["SessionEnd"] = []map[string]any{
		{
			"matcher": "",
			"hooks": []map[string]any{
				{"type": "command", "command": fmt.Sprintf("HARNESS_TEST_PAYLOAD_FILE=%s %s", endFile, script)},
			},
		},
	}
	data, err = json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".claude", "settings.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	args := baseArgs()
	args.Print = true
	args.PrintPrompt = "hello"
	args.OutputFormat = "text"

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader(""))
	if code != 0 {
		t.Fatalf("exit code %d, stderr=%s", code, stderr.String())
	}

	startRaw, err := os.ReadFile(startFile)
	if err != nil {
		t.Fatalf("SessionStart never wrote %s: %v", startFile, err)
	}
	if !strings.Contains(string(startRaw), `"hook_event_name":"SessionStart"`) {
		t.Errorf("start payload = %s, want hook_event_name SessionStart", startRaw)
	}

	endRaw, err := os.ReadFile(endFile)
	if err != nil {
		t.Fatalf("SessionEnd never wrote %s: %v", endFile, err)
	}
	if !strings.Contains(string(endRaw), `"hook_event_name":"SessionEnd"`) {
		t.Errorf("end payload = %s, want hook_event_name SessionEnd", endRaw)
	}
	if !strings.Contains(string(endRaw), `"reason":"exit"`) {
		t.Errorf("end payload = %s, want reason \"exit\"", endRaw)
	}
}

// TestRun_NotPrint_RequiresTTY checks that interactive mode (no -p) refuses
// to start against a non-TTY stdin — RunInteractive (phase 7) needs a real
// terminal, and running it against a plain io.Reader would hang or panic
// rather than degrade gracefully, so chat.go's seam checks first.
func TestRun_NotPrint_RequiresTTY(t *testing.T) {
	startFaux(t, unreadScript)
	scratchProject(t)

	args := baseArgs()
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr, strings.NewReader(""))
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "TTY") {
		t.Errorf("stderr = %q, want a mention of requiring a TTY", stderr.String())
	}
}

// benchSkillList builds n synthetic, structurally realistic skills.Skill
// values for BenchmarkSystemPromptAssembly.
func benchSkillList(n int) []skills.Skill {
	list := make([]skills.Skill, n)
	for i := 0; i < n; i++ {
		list[i] = skills.Skill{
			Name:        fmt.Sprintf("skill-%03d", i),
			Description: fmt.Sprintf("Use this skill when the task involves scenario %03d: a short description of when it applies and what it does, matching the length of a typical real skill description.", i),
			Content:     fmt.Sprintf("# Skill %03d\n\nFull instructions body for skill %03d go here.\n", i, i),
			FilePath:    fmt.Sprintf("/Users/bench/.claude/skills/skill-%03d/SKILL.md", i),
		}
	}
	return list
}

// benchMCPTools builds n synthetic, structurally realistic mcpgate.McpTool
// values for BenchmarkSystemPromptAssembly.
func benchMCPTools(n int) []mcpgate.McpTool {
	tools := make([]mcpgate.McpTool, n)
	for i := 0; i < n; i++ {
		server := fmt.Sprintf("server-%02d", i%9)
		name := fmt.Sprintf("tool_%03d", i)
		tools[i] = mcpgate.McpTool{
			Server:        server,
			Name:          name,
			QualifiedName: "mcp__" + server + "__" + name,
			Description:   fmt.Sprintf("Does thing %03d: a realistic one-paragraph description of what this tool does, its inputs, and when to call it, matching the length of real MCP tool descriptions found in production catalogs.", i),
			InputSchema:   json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
		}
	}
	return tools
}

// benchClaudeMD builds a ~20KB CLAUDE.md-like string for
// BenchmarkSystemPromptAssembly.
func benchClaudeMD() string {
	var b strings.Builder
	line := "- Follow this project convention consistently across every file you touch, and check related tests before committing.\n"
	for b.Len() < 20*1024 {
		b.WriteString(line)
	}
	return b.String()
}

// TestFileReadTokensFromToolEnd_OnlyCountsSuccessfulReads covers defect
// 2's data source: /context's "Files read" segment must be sourced from
// real "read" tool results, not fabricated, and must not count a tool
// other than "read" or a failed read.
func TestFileReadTokensFromToolEnd_OnlyCountsSuccessfulReads(t *testing.T) {
	readResult := &msg.ToolResultMessage{
		ToolName: "read",
		Content:  msg.Blocks{msg.Text(strings.Repeat("x", 400))},
	}
	if n, ok := fileReadTokensFromToolEnd("read", readResult); !ok || n != 100 {
		t.Errorf("fileReadTokensFromToolEnd(read, 400 chars) = (%d, %v), want (100, true)", n, ok)
	}

	errResult := &msg.ToolResultMessage{ToolName: "read", IsError: true, Content: msg.Blocks{msg.Text(strings.Repeat("x", 400))}}
	if n, ok := fileReadTokensFromToolEnd("read", errResult); ok {
		t.Errorf("fileReadTokensFromToolEnd(failed read) = (%d, %v), want ok=false", n, ok)
	}

	otherToolResult := &msg.ToolResultMessage{ToolName: "bash", Content: msg.Blocks{msg.Text(strings.Repeat("x", 400))}}
	if n, ok := fileReadTokensFromToolEnd("bash", otherToolResult); ok {
		t.Errorf("fileReadTokensFromToolEnd(bash) = (%d, %v), want ok=false (not a file read)", n, ok)
	}

	if n, ok := fileReadTokensFromToolEnd("read", nil); ok {
		t.Errorf("fileReadTokensFromToolEnd(read, nil result) = (%d, %v), want ok=false", n, ok)
	}
}

// BenchmarkSystemPromptAssembly measures assembling the system prompt from
// memory + the skills index + the MCP tool index, replicating chat.go's
// buildSystemPrompt closure (an unexported local closure inside Run, not
// callable directly) using the same building blocks: nonEmpty,
// formatSkillsIndex and mcpgate.IndexPromptText over mcpgate.InPosture-
// scoped tools.
func BenchmarkSystemPromptAssembly(b *testing.B) {
	memoryText := benchClaudeMD()
	skillList := benchSkillList(30)
	mcpTools := benchMCPTools(100)
	skillsIndex := formatSkillsIndex(skillList, 2_000)
	active := mcpgate.Postures[0]
	appendSystemPrompt := "Additional operator-supplied instructions for this session."

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var scoped []mcpgate.McpTool
		for _, t := range mcpTools {
			if mcpgate.InPosture(t, active) {
				scoped = append(scoped, t)
			}
		}
		mcpIndexText := mcpgate.IndexPromptText(scoped)
		promptParts := []string{defaultSystemPrompt, appendSystemPrompt, memoryText, skillsIndex, mcpIndexText}
		_ = strings.Join(nonEmpty(promptParts), "\n\n")
	}
}

// TestEffortOrSetting: --effort wins; else Claude Code's effortLevel
// applies to a Claude model only; else the level is unset.
func TestEffortOrSetting(t *testing.T) {
	cases := []struct {
		flag, setting string
		claude        bool
		want          string
	}{
		{"high", "medium", true, "high"},
		{"", "Medium", true, "medium"},
		{"", "medium", false, ""},
		{"", "", true, ""},
	}
	for _, c := range cases {
		if got := effortOrSetting(c.flag, c.setting, c.claude); got != c.want {
			t.Errorf("effortOrSetting(%q, %q, %v) = %q, want %q", c.flag, c.setting, c.claude, got, c.want)
		}
	}
}

// kiln's Ollama options must not put a whole-request Timeout on generation:
// a 3-minute one cut compactions on a slow local model off mid-stream.
func TestOllamaOptionsHaveNoWholeRequestTimeout(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "http://127.0.0.1:1")
	if c := ollamaOptionsFromEnv().HTTPClient; c != nil && c.Timeout != 0 {
		t.Fatalf("ollama HTTPClient.Timeout = %s, want none", c.Timeout)
	}
}
