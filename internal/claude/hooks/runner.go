package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Payload is what a hook receives on stdin as JSON.
type Payload struct {
	SessionID      string         `json:"session_id"`
	TranscriptPath string         `json:"transcript_path,omitempty"`
	Cwd            string         `json:"cwd"`
	HookEventName  Event          `json:"hook_event_name"`
	ToolName       string         `json:"tool_name,omitempty"`
	ToolInput      map[string]any `json:"tool_input,omitempty"`
	ToolResponse   any            `json:"tool_response,omitempty"`
	Prompt         string         `json:"prompt,omitempty"`
	Reason         string         `json:"reason,omitempty"`
	// Stop / SubagentStop: true when the run is already continuing because
	// of a Stop hook. Always sent for those events, so a pointer, not a
	// bool with omitempty.
	StopHookActive *bool `json:"stop_hook_active,omitempty"`
	// Notification: the text shown to the user and its kind
	// ("permission_prompt").
	Message          string `json:"message,omitempty"`
	NotificationType string `json:"notification_type,omitempty"`
	// PreCompact: "auto" or "manual", and the user's /compact instructions.
	Trigger            string `json:"trigger,omitempty"`
	CustomInstructions string `json:"custom_instructions,omitempty"`
}

// Blocked is set when a hook refused the call.
type Blocked struct {
	Reason string
}

// Outcome is the accumulated result of running a chain of hooks for one
// event.
type Outcome struct {
	// UpdatedInput carries replacement tool arguments, when a hook rewrote
	// them.
	UpdatedInput map[string]any
	// Blocked is set when a hook refused the call.
	Blocked *Blocked
	// Context is text to add to the model's context.
	Context []string
	// Notices are messages for the user, never for the model.
	Notices []string
}

type hookSpecificOutput struct {
	HookEventName            string         `json:"hookEventName,omitempty"`
	PermissionDecision       string         `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string         `json:"permissionDecisionReason,omitempty"`
	UpdatedInput             map[string]any `json:"updatedInput,omitempty"`
	AdditionalContext        string         `json:"additionalContext,omitempty"`
}

type jsonOutput struct {
	HookSpecificOutput *hookSpecificOutput `json:"hookSpecificOutput,omitempty"`
	Continue           *bool               `json:"continue,omitempty"`
	StopReason         string              `json:"stopReason,omitempty"`
}

type runOutput struct {
	code     int
	stdout   string
	stderr   string
	timedOut bool
}

// runCommand runs command through /bin/sh -c, feeding input on stdin and
// enforcing timeoutSeconds. It is spawned in its own process group so a
// timeout can kill the whole tree: a plain child.Kill only kills the shell
// itself, leaving grandchildren alive holding the stdout pipe open, which
// means Wait never returns and the timeout never actually times out.
func runCommand(command, input string, timeoutSeconds int, cwd string) runOutput {
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "CLAUDE_HOOK=1", "HARNESS_HOOK=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = strings.NewReader(input)

	if err := cmd.Start(); err != nil {
		// A missing interpreter or unreadable script is a failed hook, not
		// a failed session.
		return runOutput{code: 1, stderr: err.Error()}
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timedOut := false
	select {
	case err := <-done:
		code := 0
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				code = exitErr.ExitCode()
			} else {
				code = 1
			}
		}
		return runOutput{code: code, stdout: stdout.String(), stderr: stderr.String(), timedOut: false}
	case <-time.After(time.Duration(timeoutSeconds) * time.Second):
		timedOut = true
		if cmd.Process != nil {
			// Negative pid targets the group, which is the point of
			// Setpgid.
			if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
				_ = cmd.Process.Kill()
			}
		}
		<-done // reap
		return runOutput{code: -1, stdout: stdout.String(), stderr: stderr.String(), timedOut: timedOut}
	}
}

// interpret parses stdout, which may be JSON, plain text, or nothing, and
// applies it to outcome. Order matters and mirrors hook-runner.ts exactly:
// timedOut, then exit 2 (block), then other non-zero (notice), then
// stderr-on-zero-exit (notice), then empty stdout (nothing), then
// non-JSON stdout (context), then JSON continue:false (block), then
// hookSpecificOutput.permissionDecision deny (block), updatedInput
// (merge), additionalContext (append).
func interpret(out runOutput, outcome *Outcome, label string) {
	text := strings.TrimSpace(out.stdout)

	if out.timedOut {
		outcome.Notices = append(outcome.Notices, fmt.Sprintf("hook timed out: %s", label))
		return
	}

	// Exit 2 is Claude Code's "block", with the reason on stderr. Checked
	// before stdout is parsed: a blocking hook's stdout is not a decision
	// document.
	if out.code == 2 {
		reason := strings.TrimSpace(out.stderr)
		if reason == "" {
			reason = fmt.Sprintf("blocked by hook: %s", label)
		}
		outcome.Blocked = &Blocked{Reason: reason}
		return
	}

	if out.code != 0 {
		// Non-blocking failure. Surfaced rather than swallowed - a hook
		// that stopped working should not look like a hook that chose to
		// do nothing.
		if strings.TrimSpace(out.stderr) != "" {
			outcome.Notices = append(outcome.Notices, fmt.Sprintf("hook failed (%d): %s", out.code, strings.TrimSpace(out.stderr)))
		}
		return
	}

	if strings.TrimSpace(out.stderr) != "" {
		outcome.Notices = append(outcome.Notices, strings.TrimSpace(out.stderr))
	}
	if text == "" {
		return
	}

	var parsed jsonOutput
	isObject := false
	var raw any
	if err := json.Unmarshal([]byte(text), &raw); err == nil {
		if _, ok := raw.(map[string]any); ok {
			isObject = true
			_ = json.Unmarshal([]byte(text), &parsed)
		}
	}

	if !isObject {
		// Not JSON. This is the relay-inbox case: raw text is context.
		outcome.Context = append(outcome.Context, text)
		return
	}

	if parsed.Continue != nil && !*parsed.Continue {
		reason := parsed.StopReason
		if reason == "" {
			reason = fmt.Sprintf("stopped by hook: %s", label)
		}
		outcome.Blocked = &Blocked{Reason: reason}
		return
	}

	specific := parsed.HookSpecificOutput
	if specific == nil {
		return
	}

	if specific.PermissionDecision == "deny" {
		reason := specific.PermissionDecisionReason
		if reason == "" {
			reason = fmt.Sprintf("denied by hook: %s", label)
		}
		outcome.Blocked = &Blocked{Reason: reason}
		return
	}
	if specific.UpdatedInput != nil {
		// Merged into whatever an earlier hook already rewrote, so two
		// hooks touching different fields compose instead of the last one
		// winning.
		if outcome.UpdatedInput == nil {
			outcome.UpdatedInput = map[string]any{}
		}
		for k, v := range specific.UpdatedInput {
			outcome.UpdatedInput[k] = v
		}
	}
	if specific.AdditionalContext != "" {
		outcome.Context = append(outcome.Context, specific.AdditionalContext)
	}
}

// RunOptions configures RunHooks.
type RunOptions struct {
	Config   Config
	Event    Event
	Payload  Payload // HookEventName is overwritten with Event.
	ToolName string
	// HasToolName distinguishes "no tool" from an empty tool name.
	HasToolName bool
	OnNotice    func(message string)
}

// RunHooks runs every hook registered for an event, in configured order.
//
// Sequential on purpose. Hooks rewrite the same tool_input, so running
// them concurrently would make the result depend on which finished first.
func RunHooks(opts RunOptions) Outcome {
	outcome := Outcome{}
	commands := HooksFor(opts.Config, opts.Event, opts.ToolName, opts.HasToolName)
	if len(commands) == 0 {
		return outcome
	}

	payload := opts.Payload
	payload.HookEventName = opts.Event

	for _, h := range commands {
		// Each hook sees the input as rewritten by the ones before it,
		// which is what makes a chain of rewrites compose rather than
		// conflict.
		current := payload
		if outcome.UpdatedInput != nil {
			merged := map[string]any{}
			for k, v := range payload.ToolInput {
				merged[k] = v
			}
			for k, v := range outcome.UpdatedInput {
				merged[k] = v
			}
			current.ToolInput = merged
		}

		data, _ := json.Marshal(current)

		timeout := h.Timeout
		if timeout == 0 {
			timeout = DefaultTimeoutSeconds
		}

		out := runCommand(h.Command, string(data), timeout, opts.Payload.Cwd)

		before := len(outcome.Notices)
		label := h.Command
		if len(label) > 60 {
			label = label[:60]
		}
		interpret(out, &outcome, label)
		if opts.OnNotice != nil {
			for _, n := range outcome.Notices[before:] {
				opts.OnNotice(n)
			}
		}

		// A block ends the chain: later hooks have nothing left to decide.
		if outcome.Blocked != nil {
			break
		}
	}

	return outcome
}

// CheckFunc is the permission check, already bound to a gate.
type CheckFunc func(toolName, primaryArg string, hasPrimaryArg bool, args map[string]any) (*Blocked, error)

// PrimaryArgOfFunc extracts the identifying argument from tool args.
type PrimaryArgOfFunc func(args map[string]any) (string, bool)

// GuardOptions configures GuardToolCall.
type GuardOptions struct {
	Config         Config
	ToolName       string
	Args           map[string]any
	SessionID      string
	TranscriptPath string
	Cwd            string
	Check          CheckFunc
	PrimaryArgOf   PrimaryArgOfFunc
	OnNotice       func(message string)
}

// GuardResult is the outcome of GuardToolCall.
type GuardResult struct {
	Blocked *Blocked
	// Args is present only when a hook rewrote the call.
	Args map[string]any
}

// GuardToolCall composes PreToolUse hooks with the permission gate.
//
// Hooks run first, then the gate. That order is a safety property, not a
// preference: a hook may rewrite the command, and the gate has to judge
// what will actually execute, not what the model proposed. Gate-then-hooks
// would let any rewrite escape every rule, because the gate would only
// ever have seen the original.
func GuardToolCall(opts GuardOptions) (GuardResult, error) {
	args := opts.Args

	hookResult := RunHooks(RunOptions{
		Config:      opts.Config,
		Event:       PreToolUse,
		ToolName:    opts.ToolName,
		HasToolName: true,
		Payload: Payload{
			SessionID:      opts.SessionID,
			TranscriptPath: opts.TranscriptPath,
			Cwd:            opts.Cwd,
			ToolName:       opts.ToolName,
			ToolInput:      args,
		},
		OnNotice: opts.OnNotice,
	})

	if hookResult.Blocked != nil {
		return GuardResult{Blocked: hookResult.Blocked}, nil
	}

	if hookResult.UpdatedInput != nil {
		merged := map[string]any{}
		for k, v := range args {
			merged[k] = v
		}
		for k, v := range hookResult.UpdatedInput {
			merged[k] = v
		}
		args = merged
		if opts.OnNotice != nil {
			primary, _ := opts.PrimaryArgOf(args)
			opts.OnNotice(fmt.Sprintf("rewrote %s: %s", opts.ToolName, primary))
		}
	}

	primary, hasPrimary := opts.PrimaryArgOf(args)
	blocked, err := opts.Check(opts.ToolName, primary, hasPrimary, args)
	if err != nil {
		return GuardResult{}, err
	}
	if blocked != nil {
		return GuardResult{Blocked: blocked}, nil
	}

	if hookResult.UpdatedInput != nil {
		return GuardResult{Args: args}, nil
	}
	return GuardResult{}, nil
}
