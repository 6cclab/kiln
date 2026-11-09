package mods

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestSessionFacts drives $.session.root / .model: the engine returns the
// Options SessionRoot, and session.model calls the live Model closure.
func TestSessionFacts(t *testing.T) {
	root := t.TempDir()
	h := newHarness(t, Options{
		Cwd:         root,
		SessionRoot: root,
		Model:       func() string { return "claude-opus-4-8" },
	})
	h.load(t, "facts")
	h.e.SessionStart(ctxT(t))
	got := runCommand(t, h, "facts", "")

	var out struct {
		Root  string `json:"root"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("facts = %q: %v", got, err)
	}
	if out.Root != root {
		t.Errorf("session.root = %q, want %q", out.Root, root)
	}
	if out.Model != "claude-opus-4-8" {
		t.Errorf("session.model = %q, want claude-opus-4-8 (live Model closure)", out.Model)
	}
	if strings.Contains(h.logs.String(), "is not available in kiln") {
		t.Errorf("a $ call was rejected:\n%s", h.logs)
	}
}

// TestModelClassify drives $.model.classify: the text and labels reach the
// Classify closure, a matched label comes back, and "" maps to null.
func TestModelClassify(t *testing.T) {
	var gotText string
	var gotLabels []string
	classify := func(_ context.Context, text string, labels []string) (string, error) {
		gotText, gotLabels = text, labels
		if text == "none" {
			return "", nil // no label fits -> null
		}
		return "feature", nil
	}
	h := newHarness(t, Options{Cwd: t.TempDir(), Classify: classify})
	h.load(t, "classify")
	h.e.SessionStart(ctxT(t))

	if got := runCommand(t, h, "label", "add dark mode"); got != `{"picked":"feature"}` {
		t.Errorf("label = %q, want feature", got)
	}
	if gotText != "add dark mode" {
		t.Errorf("Classify text = %q, want %q", gotText, "add dark mode")
	}
	if strings.Join(gotLabels, ",") != "bug,feature,chore" {
		t.Errorf("Classify labels = %v, want [bug feature chore]", gotLabels)
	}
	// "" from the closure must surface to the mod as null.
	if got := runCommand(t, h, "label", "none"); got != `{"picked":"NULL"}` {
		t.Errorf("label(none) = %q, want NULL (empty label -> null)", got)
	}
	if strings.Contains(h.logs.String(), "is not available in kiln") {
		t.Errorf("a $ call was rejected:\n%s", h.logs)
	}
}

// TestMcpCall drives $.mcp.call: server+tool+args reach the McpCall closure,
// and the JSON it returns (an MCP CallToolResult) parses back as McpToolResult.
func TestMcpCall(t *testing.T) {
	var gotServer, gotTool string
	var gotArgs map[string]any
	call := func(_ context.Context, server, tool string, args map[string]any) (json.RawMessage, error) {
		gotServer, gotTool, gotArgs = server, tool, args
		return json.RawMessage(`{"content":[{"type":"text","text":"pong"}],"isError":false}`), nil
	}
	h := newHarness(t, Options{Cwd: t.TempDir(), McpCall: call})
	h.load(t, "mcptier")
	h.e.SessionStart(ctxT(t))
	got := runCommand(t, h, "call", "")

	var out struct {
		Text    string `json:"text"`
		IsError bool   `json:"isError"`
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("call = %q: %v", got, err)
	}
	if gotServer != "srv" || gotTool != "echo" {
		t.Errorf("McpCall got server=%q tool=%q, want srv/echo", gotServer, gotTool)
	}
	if gotArgs["msg"] != "hi" {
		t.Errorf("McpCall args = %v, want msg=hi", gotArgs)
	}
	if out.Text != "pong" || out.IsError {
		t.Errorf("result = %+v, want {text:pong isError:false}", out)
	}
	if strings.Contains(h.logs.String(), "is not available in kiln") {
		t.Errorf("a $ call was rejected:\n%s", h.logs)
	}
}
