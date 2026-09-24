package permission

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
)

func work(t *testing.T) string {
	t.Helper()
	return filepath.Join(os.TempDir(), "harness-test-workspace")
}

func newGate(t *testing.T) *Gate {
	t.Helper()
	return NewGate(GateOptions{
		// Deliberately permissive: the boundary must hold regardless.
		Permissions: settings.Permissions{Allow: []string{"read", "write", "bash"}},
		Mode:        settings.ModeAuto,
		Roots:       []string{work(t)},
	})
}

func TestGateWorkspaceBoundary(t *testing.T) {
	ctx := context.Background()

	t.Run("allows paths inside the workspace", func(t *testing.T) {
		g := newGate(t)
		p := filepath.Join(work(t), "a.ts")
		blocked, err := g.Check(ctx, Request{ToolName: "read", PrimaryArg: p, Args: map[string]any{"path": p}})
		if err != nil {
			t.Fatal(err)
		}
		if blocked != nil {
			t.Errorf("expected nil, got %+v", blocked)
		}
	})

	t.Run("blocks a path outside the workspace even when the rule allows the tool", func(t *testing.T) {
		g := newGate(t)
		home, _ := os.UserHomeDir()
		key := filepath.Join(home, ".ssh", "id_rsa")
		blocked, err := g.Check(ctx, Request{ToolName: "read", PrimaryArg: key, Args: map[string]any{"path": key}})
		if err != nil {
			t.Fatal(err)
		}
		if blocked == nil {
			t.Error("escape was not blocked")
		}
	})

	t.Run("blocks traversal out of the workspace via ..", func(t *testing.T) {
		g := newGate(t)
		escape := filepath.Join(work(t), "..", "escape.txt")
		blocked, err := g.Check(ctx, Request{ToolName: "read", PrimaryArg: escape, Args: map[string]any{"path": escape}})
		if err != nil {
			t.Fatal(err)
		}
		if blocked == nil {
			t.Error("expected traversal to be blocked")
		}
	})

	t.Run("allows an outside path once its directory is added", func(t *testing.T) {
		g := newGate(t)
		outside := filepath.Join(os.TempDir(), "elsewhere", "f.txt")
		blocked, err := g.Check(ctx, Request{ToolName: "read", PrimaryArg: outside, Args: map[string]any{"path": outside}})
		if err != nil {
			t.Fatal(err)
		}
		if blocked == nil {
			t.Error("expected outside path to be blocked before adding root")
		}
		g.AddRoot(filepath.Join(os.TempDir(), "elsewhere"))
		blocked, err = g.Check(ctx, Request{ToolName: "read", PrimaryArg: outside, Args: map[string]any{"path": outside}})
		if err != nil {
			t.Fatal(err)
		}
		if blocked != nil {
			t.Errorf("expected nil after adding root, got %+v", blocked)
		}
	})

	t.Run("refuses rather than proceeds when there is no way to ask", func(t *testing.T) {
		g := NewGate(GateOptions{Mode: settings.ModeManual, Roots: []string{work(t)}})
		p := filepath.Join(work(t), "a.ts")
		blocked, err := g.Check(ctx, Request{ToolName: "write", PrimaryArg: p, Args: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		if blocked == nil {
			t.Error("expected block")
		}
	})

	t.Run("remembers an allow-always choice for the rest of the session", func(t *testing.T) {
		g := NewGate(GateOptions{Mode: settings.ModeManual, Roots: []string{work(t)}})
		asked := 0
		g.SetPrompter(func(ctx context.Context, req Request) (PromptChoice, error) {
			asked++
			return PromptChoice{Kind: PromptAllowAlways}, nil
		})
		req := Request{ToolName: "write", PrimaryArg: filepath.Join(work(t), "a.ts"), Args: map[string]any{}}
		if _, err := g.Check(ctx, req); err != nil {
			t.Fatal(err)
		}
		if _, err := g.Check(ctx, req); err != nil {
			t.Fatal(err)
		}
		if asked != 1 {
			t.Errorf("asked %d times, want 1", asked)
		}
	})

	t.Run("does not prompt for a write inside the workspace in auto mode", func(t *testing.T) {
		g := NewGate(GateOptions{Mode: settings.ModeAuto, Roots: []string{work(t)}})
		asked := 0
		g.SetPrompter(func(ctx context.Context, req Request) (PromptChoice, error) {
			asked++
			return PromptChoice{Kind: PromptAllow}, nil
		})
		file := filepath.Join(work(t), "src", "a.ts")
		blocked, err := g.Check(ctx, Request{ToolName: "write", PrimaryArg: file, Args: map[string]any{"path": file}})
		if err != nil {
			t.Fatal(err)
		}
		if blocked != nil {
			t.Errorf("expected nil, got %+v", blocked)
		}
		if asked != 0 {
			t.Error("auto mode prompted for a write inside the workspace")
		}
	})

	t.Run("still prompts in auto mode for a path outside the workspace", func(t *testing.T) {
		g := NewGate(GateOptions{Mode: settings.ModeAuto, Roots: []string{work(t)}})
		asked := 0
		g.SetPrompter(func(ctx context.Context, req Request) (PromptChoice, error) {
			asked++
			return PromptChoice{Kind: PromptDeny}, nil
		})
		home, _ := os.UserHomeDir()
		outside := filepath.Join(home, ".ssh", "authorized_keys")
		blocked, err := g.Check(ctx, Request{ToolName: "write", PrimaryArg: outside, Args: map[string]any{"path": outside}})
		if err != nil {
			t.Fatal(err)
		}
		if blocked == nil {
			t.Error("expected block")
		}
		if asked != 1 {
			t.Error("auto mode wrote outside the workspace without asking")
		}
	})

	t.Run("honours a deny rule in auto mode", func(t *testing.T) {
		g := NewGate(GateOptions{
			Permissions: settings.Permissions{Deny: []string{"Write"}},
			Mode:        settings.ModeAuto,
			Roots:       []string{work(t)},
		})
		file := filepath.Join(work(t), "a.ts")
		blocked, err := g.Check(ctx, Request{ToolName: "write", PrimaryArg: file, Args: map[string]any{"path": file}})
		if err != nil {
			t.Fatal(err)
		}
		if blocked == nil {
			t.Error("expected deny rule to block")
		}
	})

	t.Run("turns a denial into feedback for the model, not an error", func(t *testing.T) {
		g := NewGate(GateOptions{Mode: settings.ModeManual, Roots: []string{work(t)}})
		g.SetPrompter(func(ctx context.Context, req Request) (PromptChoice, error) {
			return PromptChoice{Kind: PromptDeny, Feedback: "use the staging bucket"}, nil
		})
		blocked, err := g.Check(ctx, Request{ToolName: "write", PrimaryArg: filepath.Join(work(t), "a.ts"), Args: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		if blocked == nil {
			t.Fatal("expected block")
		}
		if !contains(blocked.Reason, "use the staging bucket") {
			t.Errorf("reason %q does not include feedback", blocked.Reason)
		}
	})
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
