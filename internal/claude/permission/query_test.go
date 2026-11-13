package permission

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
)

// A query answers allow, ask or deny without ever calling the prompter.
func TestQueryAsksNobody(t *testing.T) {
	ctx := context.Background()
	asked := 0
	g := NewGate(GateOptions{
		Permissions: settings.Permissions{Allow: []string{"read"}, Deny: []string{"bash"}},
		Mode:        settings.ModeManual,
		Roots:       []string{work(t)},
	})
	g.SetPrompter(func(context.Context, Request) (PromptChoice, error) {
		asked++
		return PromptChoice{Kind: PromptAllow}, nil
	})
	p := filepath.Join(work(t), "a.ts")

	v, err := g.Query(ctx, Request{ToolName: "write", PrimaryArg: p, Args: map[string]any{}})
	if err != nil || v.Decision != "ask" {
		t.Errorf("an unruled write = %+v, %v; want ask", v, err)
	}
	v, err = g.Query(ctx, Request{ToolName: "bash", PrimaryArg: "ls", Args: map[string]any{}})
	if err != nil || v.Decision != "deny" || v.Reason == "" {
		t.Errorf("a denied bash = %+v, %v; want deny with a reason", v, err)
	}
	v, err = g.Query(ctx, Request{ToolName: "read", PrimaryArg: p, Args: map[string]any{"path": p}})
	if err != nil || v.Decision != "allow" {
		t.Errorf("an allowed read = %+v, %v; want allow", v, err)
	}
	if asked != 0 {
		t.Errorf("a query put %d questions to the prompter, want none", asked)
	}
}
