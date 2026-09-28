// Background shells: bash_background, bash_output, kill_shell.
//
// A dev server, a watch build, a long test run: commands that are useful
// *while* they run and never "finish". Run in the foreground they occupy
// the turn until they time out; the model waits for output that only
// arrives when the process dies.
//
// So a background shell is started, given an id, and left alone. The model
// polls it when it wants to, kills it when it is done, and spends nothing
// on it in between. This mirrors src/agent/background-shell.ts's three
// AgentHarnessTool exports (createBackgroundBashTool, createBashOutputTool,
// createKillShellTool); the registry those tools call into
// (BackgroundShells, the ring buffer, renderShellList) lives in
// internal/agent/shells.go — see the deviation note on ShellManager below
// for why.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/tool"
)

// MaxBackgroundShellLines bounds the ring buffer background-shell.ts calls
// MAX_LINES: bounded so a watch process cannot grow the buffer without
// limit. Declared here (rather than in internal/agent, which owns the
// buffer itself) so the bash_output "dropped lines" message and the
// buffer's own cap read the same number.
const MaxBackgroundShellLines = 500

// ShellStatus is one background shell's lifecycle state, mirroring
// background-shell.ts's ShellStatus.
type ShellStatus string

const (
	ShellRunning ShellStatus = "running"
	ShellExited  ShellStatus = "exited"
	ShellKilled  ShellStatus = "killed"
	ShellFailed  ShellStatus = "failed"
)

// Shell is a snapshot of one background shell's state, mirroring
// background-shell.ts's BackgroundShell (the parts a snapshot exposes;
// Entry's internal buffer fields stay in internal/agent).
type Shell struct {
	ID        string
	Command   string
	Status    ShellStatus
	ExitCode  *int
	StartedAt time.Time
	EndedAt   *time.Time
	// Dropped is lines lost off the front of the buffer, so a truncated
	// log says so.
	Dropped int
}

// ShellManager is what the three tools need from a shell registry.
//
// Deviation from the brief: the brief's constructor signature is
// BackgroundShellTools(shells *agent.BackgroundShells, env *execenv.Env).
// internal/agent already imports internal/tools (session.go, todo.go use
// tools.TodoItem/TodoStatus the same way), so internal/tools importing
// internal/agent back would be an import cycle - agent.BackgroundShells
// cannot appear as a parameter type here. ShellManager is the structural
// interface internal/agent.BackgroundShells satisfies instead, using only
// types this package defines; BackgroundShellTools takes that interface.
// No behavior differs from the brief, only how the two packages refer to
// each other.
type ShellManager interface {
	// Start begins running command in the background and returns
	// immediately with its snapshot.
	Start(ctx context.Context, command string, env *execenv.Env) (*Shell, error)
	// Read returns everything new since the caller's last read, advancing
	// the cursor. ok is false for an unknown id.
	Read(id string) (lines []string, missed int, shell Shell, ok bool)
	// Kill stops a running shell (a no-op status-wise if it already
	// finished). ok is false for an unknown id.
	Kill(id string) (Shell, bool)
	// Get returns the current snapshot without reading output. ok is
	// false for an unknown id.
	Get(id string) (Shell, bool)
	// List returns every shell, running or finished.
	List() []Shell
}

// describe renders one shell as background-shell.ts's describe() does:
// "bash_1  running (exit 0)  3s  sleep 5".
// shellRuntime is how long s ran (or has run so far), in whole seconds.
func shellRuntime(s Shell) string {
	end := time.Now()
	if s.EndedAt != nil {
		end = *s.EndedAt
	}
	return fmt.Sprintf("%ds", int(math.Round(end.Sub(s.StartedAt).Seconds())))
}

func describe(s Shell) string {
	code := ""
	if s.ExitCode != nil {
		code = fmt.Sprintf(" (exit %d)", *s.ExitCode)
	}
	return fmt.Sprintf("%s  %s%s  %s  %s", s.ID, s.Status, code, shellRuntime(s), s.Command)
}

func idParam(args json.RawMessage) (string, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	return strings.TrimSpace(in.ID), nil
}

var shellIDParameters = json.RawMessage(`{
	"type": "object",
	"properties": {
		"id": {"type": "string", "description": "The shell id, e.g. bash_1."}
	},
	"required": ["id"]
}`)

// BashOutputTool builds bash_output, mirroring createBashOutputTool.
func BashOutputTool(shells ShellManager) *tool.Tool {
	return &tool.Tool{
		Name:  "bash_output",
		Label: "Read background shell",
		Description: "Read new output from a background shell started with run_in_background. " +
			"Returns only what has appeared since your last read, so polling is cheap.",
		Parameters: shellIDParameters,
		Execute: func(_ context.Context, args json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
			id, err := idParam(args)
			if err != nil {
				return tool.Errorf("invalid arguments: %s", err), nil
			}
			lines, missed, shell, ok := shells.Read(id)
			if !ok {
				known := []string{}
				for _, s := range shells.List() {
					known = append(known, s.ID)
				}
				list := strings.Join(known, ", ")
				if list == "" {
					list = "none"
				}
				return tool.Text(fmt.Sprintf(`No shell "%s". Running or finished: %s.`, id, list)), nil
			}

			header := describe(shell)
			missedNote := ""
			if missed > 0 {
				missedNote = fmt.Sprintf("\n[%d earlier lines dropped - the buffer holds the last %d]", missed, MaxBackgroundShellLines)
			}
			body := "(no new output)"
			if len(lines) > 0 {
				body = strings.Join(lines, "\n")
			}
			return tool.Text(fmt.Sprintf("%s%s\n\n%s", header, missedNote, body)), nil
		},
	}
}

// KillShellTool builds kill_shell, mirroring createKillShellTool.
func KillShellTool(shells ShellManager) *tool.Tool {
	return &tool.Tool{
		Name:        "kill_shell",
		Label:       "Kill background shell",
		Description: "Stop a background shell. Do this when you are finished with a dev server or watch process.",
		Parameters:  shellIDParameters,
		Execute: func(_ context.Context, args json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
			id, err := idParam(args)
			if err != nil {
				return tool.Errorf("invalid arguments: %s", err), nil
			}
			before, hadBefore := shells.Get(id)
			shell, ok := shells.Kill(id)
			if !ok {
				return tool.Text(fmt.Sprintf(`No shell "%s".`, id)), nil
			}
			// "Killed" a shell that had already exited is a small lie that
			// sends the model looking for a cause it will not find.
			if hadBefore && before.Status == ShellRunning {
				// describe would repeat the status ("Killed bash_1  killed").
				return tool.Text(fmt.Sprintf("Killed %s after %s  %s", shell.ID, shellRuntime(shell), shell.Command)), nil
			}
			return tool.Text("Already finished: " + describe(shell)), nil
		},
	}
}

// backgroundBashParameters mirrors createBackgroundBashTool's schema.
var backgroundBashParameters = json.RawMessage(`{
	"type": "object",
	"properties": {
		"command": {"type": "string", "description": "The command to run."}
	},
	"required": ["command"]
}`)

// BackgroundBashTool builds bash_background, mirroring
// createBackgroundBashTool.
//
// A separate tool rather than a run_in_background flag on bash: bash's own
// tool owns its schema and execution path, and adding a parameter to it
// would mean wrapping and re-implementing that path - which is how the
// foreground case quietly breaks. A distinct tool leaves BashTool
// untouched.
func BackgroundBashTool(shells ShellManager, env *execenv.Env) *tool.Tool {
	return &tool.Tool{
		Name:  "bash_background",
		Label: "Background command",
		Description: "Start a long-running command in the background and return immediately with a shell id. " +
			"Use for dev servers, watch builds, and anything that does not exit on its own. " +
			"Read its output with bash_output and stop it with kill_shell. " +
			"For a command that finishes, use bash instead - this one never returns its output directly.",
		Parameters: backgroundBashParameters,
		Execute: func(ctx context.Context, args json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
			var in struct {
				Command string `json:"command"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return tool.Errorf("invalid arguments: %s", err), nil
			}
			command := strings.TrimSpace(in.Command)
			if command == "" {
				return tool.Text("No command given."), nil
			}
			shell, err := shells.Start(ctx, command, env)
			if err != nil {
				return tool.Errorf("%s", err), nil
			}
			return tool.Text(fmt.Sprintf("Started %s: %s\nRead it with bash_output({id: \"%s\"}).", shell.ID, command, shell.ID)), nil
		},
	}
}

// BackgroundShellTools builds the three background-shell tools bound to
// one registry and one execution environment.
func BackgroundShellTools(shells ShellManager, env *execenv.Env) []*tool.Tool {
	return []*tool.Tool{
		BackgroundBashTool(shells, env),
		BashOutputTool(shells),
		KillShellTool(shells),
	}
}
