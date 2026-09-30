package permission

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// TestConcurrentCheckPromptsOnce drives two goroutines through Check for
// the exact same ask-verdict request at once. Only one of them may ever be
// inside the prompter at a time (asserted via a counter that must never
// exceed 1), and once the first prompt answers AllowAlways the second
// Check must see the session grant and return allow without invoking the
// prompter again.
func TestConcurrentCheckPromptsOnce(t *testing.T) {
	g := NewGate(GateOptions{Mode: settings.ModeManual, Roots: []string{work(t)}})

	var inFlight int32
	var maxInFlight int32
	var promptCalls int32
	g.SetPrompter(func(ctx context.Context, req Request) (PromptChoice, error) {
		n := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&maxInFlight)
			if n <= old || atomic.CompareAndSwapInt32(&maxInFlight, old, n) {
				break
			}
		}
		atomic.AddInt32(&promptCalls, 1)
		// Give the second goroutine a window to (wrongly) enter the
		// prompter concurrently if promptMu were not held.
		time.Sleep(20 * time.Millisecond)
		atomic.AddInt32(&inFlight, -1)
		return PromptChoice{Kind: PromptAllowAlways}, nil
	})

	req := Request{ToolName: "bash", PrimaryArg: "touch made.txt", Args: map[string]any{}}

	var wg sync.WaitGroup
	results := make([]*BlockResult, 2)
	errs := make([]error, 2)
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = g.Check(context.Background(), req)
		}(i)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&maxInFlight); got > 1 {
		t.Fatalf("prompter had %d concurrent invocations, want at most 1", got)
	}
	if got := atomic.LoadInt32(&promptCalls); got != 1 {
		t.Fatalf("prompter called %d times, want exactly 1 (the second Check should see the session grant)", got)
	}
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Check[%d]: %v", i, err)
		}
		if results[i] != nil {
			t.Fatalf("Check[%d] = %+v, want allow (nil)", i, results[i])
		}
	}
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

// TestGateBashDontAskGrantsThePrefixItNames: "Yes, and don't ask again for:
// npm test *" used to grant only the exact command string, so the next
// "npm test -- other" asked again despite the label. It now grants the
// named prefix, judged per segment, so it never covers a command it did
// not name (qa/findings *bash-dont-ask-grants-exact-command).
func TestGateBashDontAskGrantsThePrefixItNames(t *testing.T) {
	g := NewGate(GateOptions{Mode: settings.ModeManual, Roots: []string{work(t)}})
	var asked []string
	g.SetPrompter(func(ctx context.Context, req Request) (PromptChoice, error) {
		asked = append(asked, req.PrimaryArg)
		if len(asked) == 1 {
			return PromptChoice{Kind: PromptAllowAlways}, nil
		}
		return PromptChoice{Kind: PromptDeny}, nil
	})
	ctx := context.Background()
	check := func(cmd string) bool {
		r, err := g.Check(ctx, Request{ToolName: "bash", PrimaryArg: cmd})
		if err != nil {
			t.Fatal(err)
		}
		return r == nil
	}
	if !check("cd api && npm test -- upload") {
		t.Fatal("first call: allow-always should approve it")
	}
	if !check("npm test -- download") || len(asked) != 1 {
		t.Errorf("same prefix should be approved without asking; asked=%q", asked)
	}
	if check("npm test && rm -rf build") || len(asked) != 2 {
		t.Errorf("a line with an unnamed command must still ask; asked=%q", asked)
	}
}

// TestReadOnlyBashInWorkspaceDoesNotAsk: in the modes that ask about bash,
// a command that only reads, and only inside the workspace, runs without a
// prompt; one that reads outside it, writes, or is named by an ask rule
// still asks.
func TestReadOnlyBashInWorkspaceDoesNotAsk(t *testing.T) {
	root := work(t)
	for _, mode := range []settings.PermissionMode{settings.ModeManual, settings.ModeAcceptEdits} {
		g := NewGate(GateOptions{Mode: mode, Roots: []string{root}})
		asked := 0
		g.SetPrompter(func(ctx context.Context, req Request) (PromptChoice, error) {
			asked++
			return PromptChoice{Kind: PromptAllow}, nil
		})
		cases := []struct {
			cmd  string
			asks bool
		}{
			{"cat app.py | head -5; ls -la app.py 2>/dev/null", false},
			{"cat " + filepath.Join(root, "app.py"), false},
			{"cd " + root + " && git log --oneline -3", false},
			{"cat /etc/passwd", true},
			{"cat ../secrets.txt", true},
			{"cd /tmp && ls", true},
			{"ls ~/.ssh", true},
			{"touch x", true},
			{"cat app.py > copy.py", true},
		}
		for _, c := range cases {
			asked = 0
			_, outcome, err := g.CheckWithOutcome(context.Background(), Request{ToolName: "bash", PrimaryArg: c.cmd, Args: map[string]any{"command": c.cmd}})
			if err != nil {
				t.Fatal(err)
			}
			if got := asked > 0; got != c.asks {
				t.Errorf("%s: %q asked=%v, want %v (outcome %v)", mode, c.cmd, got, c.asks, outcome)
			}
		}
	}

	g := NewGate(GateOptions{Mode: settings.ModeManual, Roots: []string{root}, Permissions: settings.Permissions{Ask: []string{"Bash(git log:*)"}}})
	asked := 0
	g.SetPrompter(func(ctx context.Context, req Request) (PromptChoice, error) {
		asked++
		return PromptChoice{Kind: PromptAllow}, nil
	})
	g.CheckWithOutcome(context.Background(), Request{ToolName: "bash", PrimaryArg: "git log -3", Args: map[string]any{}})
	if asked != 1 {
		t.Errorf("an ask rule for git log was overridden by the read-only allowance")
	}
}

func TestGrantRuleUsesClaudeCodeSyntax(t *testing.T) {
	for key, want := range map[string]string{
		"bash::npm test":         "Bash(npm test)",
		"web_fetch::example.com": "WebFetch(example.com)",
		"edit::src/a.go":         "Edit(src/a.go)",
		"mcp__grafana__query::":  "mcp__grafana__query",
	} {
		if got := grantRule(key); got != want {
			t.Errorf("grantRule(%q) = %q, want %q", key, got, want)
		}
	}
}

// TestDontAskDeniesWhatWouldPrompt: Claude Code's dontAsk mode never
// prompts. What runs without asking in manual mode still runs, allow rules
// still apply, and everything that would have prompted is refused. kiln
// once treated dontAsk as a blanket allow.
func TestDontAskDeniesWhatWouldPrompt(t *testing.T) {
	ctx := context.Background()
	root := work(t)
	g := NewGate(GateOptions{
		Mode:        settings.ModeDontAsk,
		Roots:       []string{root},
		Permissions: settings.Permissions{Allow: []string{"Bash(npm test)"}, Ask: []string{"Bash(git push:*)"}},
	})
	prompted := false
	g.SetPrompter(func(ctx context.Context, req Request) (PromptChoice, error) {
		prompted = true
		return PromptChoice{Kind: PromptAllow}, nil
	})
	cases := []struct {
		name    string
		req     Request
		allowed bool
	}{
		{"read-only tool runs", Request{ToolName: "read", PrimaryArg: filepath.Join(root, "a.go"), Args: map[string]any{"path": filepath.Join(root, "a.go")}}, true},
		{"read-only bash runs", Request{ToolName: "bash", PrimaryArg: "ls", Args: map[string]any{"command": "ls"}}, true},
		{"allow rule runs", Request{ToolName: "bash", PrimaryArg: "npm test", Args: map[string]any{"command": "npm test"}}, true},
		{"other bash is refused", Request{ToolName: "bash", PrimaryArg: "rm -rf build", Args: map[string]any{"command": "rm -rf build"}}, false},
		{"an edit is refused", Request{ToolName: "edit", PrimaryArg: filepath.Join(root, "a.go"), Args: map[string]any{"path": filepath.Join(root, "a.go")}}, false},
		{"an ask rule is refused, not asked", Request{ToolName: "bash", PrimaryArg: "git push origin", Args: map[string]any{"command": "git push origin"}}, false},
	}
	for _, c := range cases {
		blocked, err := g.Check(ctx, c.req)
		if err != nil {
			t.Fatal(err)
		}
		if (blocked == nil) != c.allowed {
			t.Errorf("%s: blocked=%v, want allowed=%v", c.name, blocked, c.allowed)
		}
	}
	if prompted {
		t.Error("dontAsk mode prompted")
	}
}

// TestPlanLedgerPathIsTheOneExceptionToPlanModeReadOnly: kiln's
// HARNESS_EXP_LEDGER experiment switch (internal/cli/experiments.go) sets
// GateOptions.PlanLedgerPath so exactly one file stays writable in plan
// mode. A relative path given to Check is resolved against the workspace
// root the same as the ledger path itself, so a relative and an absolute
// spelling of the same file agree; everything else, including a path one
// directory over, still hits Decide's ModePlan case and is refused.
// Without PlanLedgerPath set at all, plan mode is unchanged: fully
// read-only.
func TestPlanLedgerPathIsTheOneExceptionToPlanModeReadOnly(t *testing.T) {
	ctx := context.Background()
	root := work(t)
	ledger := filepath.Join(root, ".harness", "plans", "ledger.md")
	g := NewGate(GateOptions{
		Mode:           settings.ModePlan,
		Roots:          []string{root},
		PlanLedgerPath: ledger,
	})
	cases := []struct {
		name    string
		req     Request
		allowed bool
	}{
		{"write to the ledger, absolute path", Request{ToolName: "write", PrimaryArg: ledger, Args: map[string]any{"path": ledger}}, true},
		{"edit the ledger, absolute path", Request{ToolName: "edit", PrimaryArg: ledger, Args: map[string]any{"path": ledger}}, true},
		{"write to the ledger, relative path", Request{ToolName: "write", PrimaryArg: filepath.Join(".harness", "plans", "ledger.md"), Args: map[string]any{"path": filepath.Join(".harness", "plans", "ledger.md")}}, true},
		{"write elsewhere is still refused", Request{ToolName: "write", PrimaryArg: filepath.Join(root, "other.txt"), Args: map[string]any{"path": filepath.Join(root, "other.txt")}}, false},
		{"a near-miss path is still refused", Request{ToolName: "write", PrimaryArg: filepath.Join(root, ".harness", "plans", "ledger2.md"), Args: map[string]any{"path": filepath.Join(root, ".harness", "plans", "ledger2.md")}}, false},
		{"bash is unaffected by the exception", Request{ToolName: "bash", PrimaryArg: "rm " + ledger, Args: map[string]any{"command": "rm " + ledger}}, false},
	}
	for _, c := range cases {
		blocked, err := g.Check(ctx, c.req)
		if err != nil {
			t.Fatal(err)
		}
		if (blocked == nil) != c.allowed {
			t.Errorf("%s: blocked=%v, want allowed=%v", c.name, blocked, c.allowed)
		}
	}

	// With PlanLedgerPath unset, plan mode is exactly as read-only as
	// before this switch existed.
	plain := NewGate(GateOptions{Mode: settings.ModePlan, Roots: []string{root}})
	if blocked, err := plain.Check(ctx, Request{ToolName: "write", PrimaryArg: ledger, Args: map[string]any{"path": ledger}}); err != nil || blocked == nil {
		t.Errorf("no PlanLedgerPath: write to %s allowed=%v, want refused", ledger, blocked == nil)
	}
}
