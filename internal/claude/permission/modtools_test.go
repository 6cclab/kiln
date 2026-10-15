package permission

import (
	"context"
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
)

// TestModToolsNeedNoPrompt: a mod's own tool is answered by the mod, so in
// manual mode it runs without a prompt, as in Claude Code; an ask rule
// still asks and a deny rule still refuses. Any other MCP tool still asks.
func TestModToolsNeedNoPrompt(t *testing.T) {
	asked := 0
	prompter := func(context.Context, Request) (PromptChoice, error) {
		asked++
		return PromptChoice{Kind: PromptAllow}, nil
	}
	g := NewGate(GateOptions{Mode: settings.ModeManual, Prompt: prompter, Roots: []string{t.TempDir()}})
	g.SetModTools([]string{"mcp__squad__step"})

	if r, err := g.Check(context.Background(), Request{ToolName: "mcp__squad__step"}); r != nil || err != nil || asked != 0 {
		t.Fatalf("mod tool: block=%v err=%v asked=%d, want allowed without a prompt", r, err, asked)
	}
	if _, _ = g.Check(context.Background(), Request{ToolName: "mcp__other__thing"}); asked != 1 {
		t.Fatalf("another MCP tool should still ask; asked=%d", asked)
	}

	deny := NewGate(GateOptions{Mode: settings.ModeManual, Prompt: prompter, Permissions: settings.Permissions{Deny: []string{"mcp__squad__step"}}})
	deny.SetModTools([]string{"mcp__squad__step"})
	if r, _ := deny.Check(context.Background(), Request{ToolName: "mcp__squad__step"}); r == nil {
		t.Fatal("a deny rule must still refuse a mod tool")
	}
}
