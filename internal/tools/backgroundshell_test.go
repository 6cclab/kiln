package tools_test

// External test package (tools_test rather than tools) so these tests can
// exercise BackgroundShellTools against the real registry,
// internal/agent.BackgroundShells, without creating an import cycle:
// internal/agent already imports internal/tools, so internal/tools's own
// package cannot import internal/agent back (see the deviation note atop
// internal/tools/backgroundshell.go and internal/agent/shells.go).

import (
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
	"github.com/andrepato/harness/internal/tools"
)

func runTool(t *testing.T, tl *tool.Tool, args any) tool.Result {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	result, err := tl.Execute(context.Background(), raw, func(tool.Result) {}, tool.Invocation{ToolName: tl.Name})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return result
}

func text(r tool.Result) string { return msg.TextOf(r.Content) }

func TestBashBackgroundReturnsShellIDImmediately(t *testing.T) {
	env := execenv.New(t.TempDir())
	shells := agent.NewBackgroundShells()
	toolSet := tools.BackgroundShellTools(shells, env)
	bg := mustTool(t, toolSet, "bash_background")

	started := time.Now()
	result := runTool(t, bg, map[string]any{"command": "sleep 5"})
	if time.Since(started) > 500*time.Millisecond {
		t.Fatalf("bash_background blocked for %s", time.Since(started))
	}
	if result.IsError {
		t.Fatalf("unexpected error: %+v", result)
	}
	if !strings.Contains(text(result), "bash_1") {
		t.Fatalf("got %q, want it to mention bash_1", text(result))
	}
	shells.KillAll()
}

func TestBashBackgroundRejectsEmptyCommand(t *testing.T) {
	env := execenv.New(t.TempDir())
	shells := agent.NewBackgroundShells()
	bg := mustTool(t, tools.BackgroundShellTools(shells, env), "bash_background")

	result := runTool(t, bg, map[string]any{"command": "   "})
	if text(result) != "No command given." {
		t.Fatalf("got %q", text(result))
	}
}

func TestBashOutputReadsIncrementallyAndReportsMissed(t *testing.T) {
	env := execenv.New(t.TempDir())
	shells := agent.NewBackgroundShells()
	toolSet := tools.BackgroundShellTools(shells, env)
	bg := mustTool(t, toolSet, "bash_background")
	out := mustTool(t, toolSet, "bash_output")

	startResult := runTool(t, bg, map[string]any{"command": "for i in 1 2 3; do echo n$i; sleep 0.2; done"})
	id := shellIDFrom(t, text(startResult))

	time.Sleep(700 * time.Millisecond)
	first := runTool(t, out, map[string]any{"id": id})
	if first.IsError {
		t.Fatalf("unexpected error: %+v", first)
	}
	if !strings.Contains(text(first), "n1") {
		t.Fatalf("first read missing n1: %q", text(first))
	}

	second := runTool(t, out, map[string]any{"id": id})
	if strings.Contains(text(second), "n1") {
		t.Fatalf("second read repeated n1: %q", text(second))
	}
}

func TestBashOutputUnknownID(t *testing.T) {
	env := execenv.New(t.TempDir())
	shells := agent.NewBackgroundShells()
	out := mustTool(t, tools.BackgroundShellTools(shells, env), "bash_output")

	result := runTool(t, out, map[string]any{"id": "bash_99"})
	if !strings.Contains(text(result), `No shell "bash_99"`) {
		t.Fatalf("got %q", text(result))
	}
	if !strings.Contains(text(result), "none") {
		t.Fatalf("expected \"none\" for an empty registry, got %q", text(result))
	}
}

func TestKillShellVerbDistinguishesRunningFromFinished(t *testing.T) {
	env := execenv.New(t.TempDir())
	shells := agent.NewBackgroundShells()
	toolSet := tools.BackgroundShellTools(shells, env)
	bg := mustTool(t, toolSet, "bash_background")
	kill := mustTool(t, toolSet, "kill_shell")

	running := shellIDFrom(t, text(runTool(t, bg, map[string]any{"command": "sleep 5"})))
	time.Sleep(200 * time.Millisecond)
	killedResult := runTool(t, kill, map[string]any{"id": running})
	if !strings.HasPrefix(text(killedResult), "Killed ") {
		t.Fatalf("got %q, want it to start with \"Killed \"", text(killedResult))
	}
	if strings.Contains(text(killedResult), "killed") {
		t.Errorf("got %q, which repeats the status after \"Killed\"", text(killedResult))
	}

	finished := shellIDFrom(t, text(runTool(t, bg, map[string]any{"command": "echo done"})))
	time.Sleep(500 * time.Millisecond)
	finishedResult := runTool(t, kill, map[string]any{"id": finished})
	if !strings.HasPrefix(text(finishedResult), "Already finished:") {
		t.Fatalf("got %q, want it to start with \"Already finished:\"", text(finishedResult))
	}
}

func TestKillShellUnknownID(t *testing.T) {
	env := execenv.New(t.TempDir())
	shells := agent.NewBackgroundShells()
	kill := mustTool(t, tools.BackgroundShellTools(shells, env), "kill_shell")

	result := runTool(t, kill, map[string]any{"id": "bash_42"})
	if text(result) != `No shell "bash_42".` {
		t.Fatalf("got %q", text(result))
	}
}

// TestKillShellLeavesNoProcessBehind is the brief's specific check: killing
// a "sleep 30" leaves no matching process, verified through the actual
// kill_shell tool rather than the registry directly.
func TestKillShellLeavesNoProcessBehind(t *testing.T) {
	env := execenv.New(t.TempDir())
	shells := agent.NewBackgroundShells()
	toolSet := tools.BackgroundShellTools(shells, env)
	bg := mustTool(t, toolSet, "bash_background")
	kill := mustTool(t, toolSet, "kill_shell")

	marker := strconv.FormatInt(time.Now().UnixNano()%100000, 10)
	command := "sleep 30" + marker
	id := shellIDFrom(t, text(runTool(t, bg, map[string]any{"command": command})))

	time.Sleep(300 * time.Millisecond)
	if !processMatches(command) {
		t.Fatalf("process for %q never started", command)
	}

	runTool(t, kill, map[string]any{"id": id})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !processMatches(command) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("process for %q survived kill_shell", command)
}

func processMatches(pattern string) bool {
	return exec.Command("pgrep", "-f", pattern).Run() == nil
}

func mustTool(t *testing.T, set []*tool.Tool, name string) *tool.Tool {
	t.Helper()
	for _, tl := range set {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("no tool named %q in %v", name, set)
	return nil
}

// shellIDFrom extracts "bash_N" from the bash_background tool's
// "Started bash_N: ..." response text.
func shellIDFrom(t *testing.T, s string) string {
	t.Helper()
	const prefix = "Started "
	if !strings.HasPrefix(s, prefix) {
		t.Fatalf("unexpected bash_background text: %q", s)
	}
	rest := s[len(prefix):]
	if idx := strings.IndexAny(rest, ":"); idx >= 0 {
		return rest[:idx]
	}
	t.Fatalf("unexpected bash_background text: %q", s)
	return ""
}
