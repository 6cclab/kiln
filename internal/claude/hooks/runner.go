package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/execenv"
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
	// Decision is the PreToolUse hooks' permission decision short of a
	// denial (a denial is Blocked): DecisionAllow, DecisionAsk or "" for
	// none. Several hooks combine as in Claude Code: ask over allow.
	Decision Decision
	// DecisionReason is the reason given with Decision, shown with the
	// prompt a hook's "ask" raises.
	DecisionReason string
}

// Decision is a PreToolUse hook's permissionDecision short of "deny".
type Decision string

const (
	// DecisionAllow skips the permission prompt; deny and ask rules, and
	// the checks no allow approves, still apply (permission.Gate).
	DecisionAllow Decision = "allow"
	// DecisionAsk forces the permission prompt.
	DecisionAsk Decision = "ask"
)

// merge applies one hook's decision with Claude Code's precedence: deny
// (Blocked, handled by the caller) over ask over allow.
func (o *Outcome) merge(d Decision, reason string) {
	switch d {
	case DecisionAsk:
		if o.Decision != DecisionAsk {
			o.Decision, o.DecisionReason = DecisionAsk, reason
		}
	case DecisionAllow:
		if o.Decision == "" {
			o.Decision, o.DecisionReason = DecisionAllow, reason
		}
	}
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
	// Decision and Reason are PreToolUse's deprecated top-level form:
	// "approve" is "allow" and "block" is "deny" (Claude Code's hooks
	// reference). Other events give "decision" other meanings, so it is
	// read for PreToolUse only.
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`
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
func runCommand(command, input string, timeoutSeconds int, cwd string, extraEnv map[string]string) runOutput {
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "CLAUDE_HOOK=1", "HARNESS_HOOK=1")
	if cwd != "" {
		// Claude Code gives hooks the project root this way, and project
		// hook configs name their scripts through it
		// ("$CLAUDE_PROJECT_DIR"/.claude/hooks/x.sh); unset, that path
		// resolves to /.claude/hooks/x.sh and the hook fails.
		cmd.Env = append(cmd.Env, "CLAUDE_PROJECT_DIR="+cwd)
	}
	for k, v := range extraEnv {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	execenv.SetProcGroup(cmd)

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
			if err := execenv.KillProcessGroup(cmd.Process.Pid, execenv.SignalKill); err != nil {
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
// (PreToolUse) the deprecated top-level decision, then
// hookSpecificOutput.permissionDecision: deny (block), ask or allow
// (Outcome.Decision); then updatedInput (merge), additionalContext
// (append).
func interpret(out runOutput, outcome *Outcome, label string, event Event) {
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

	// The decision this hook made, if any. A later hookSpecificOutput
	// decision overrides the deprecated top-level one, as in Claude Code.
	decision, reason := "", ""
	if event == PreToolUse {
		switch parsed.Decision {
		case "approve":
			decision, reason = "allow", parsed.Reason
		case "block":
			decision, reason = "deny", parsed.Reason
		}
	}
	specific := parsed.HookSpecificOutput
	if specific != nil && specific.PermissionDecision != "" {
		switch specific.PermissionDecision {
		case "deny":
			decision, reason = "deny", specific.PermissionDecisionReason
			if reason == "" {
				reason = parsed.Reason
			}
		case "allow", "ask":
			// Claude Code honours these only from a PreToolUse hook that
			// names its event; a deny is honoured whatever it names.
			if event == PreToolUse && specific.HookEventName == string(PreToolUse) {
				decision, reason = specific.PermissionDecision, specific.PermissionDecisionReason
			}
		case "defer":
			// "Let the normal permission flow apply": no decision.
		default:
			outcome.Notices = append(outcome.Notices, fmt.Sprintf("hook returned an unknown permissionDecision %q, ignored: %s", specific.PermissionDecision, label))
		}
	}
	switch decision {
	case "deny":
		if reason == "" {
			reason = fmt.Sprintf("denied by hook: %s", label)
		}
		outcome.Blocked = &Blocked{Reason: reason}
		return
	case "allow":
		outcome.merge(DecisionAllow, reason)
	case "ask":
		outcome.merge(DecisionAsk, reason)
	}
	if specific == nil {
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

		out := runCommand(h.Command, string(data), timeout, opts.Payload.Cwd, h.Env)

		before := len(outcome.Notices)
		label := h.Command
		if len(label) > 60 {
			label = label[:60]
		}
		// This hook's own decision, apart from the earlier hooks'.
		prevDecision, prevReason := outcome.Decision, outcome.DecisionReason
		outcome.Decision, outcome.DecisionReason = "", ""
		interpret(out, &outcome, label, opts.Event)
		decided, decidedReason := outcome.Decision, outcome.DecisionReason
		outcome.Decision, outcome.DecisionReason = prevDecision, prevReason
		// An earlier hook's "allow" judged the input it was shown. A hook
		// that then changes the input without deciding anything itself
		// leaves a call nobody allowed: the allow is dropped, and the
		// regular permission flow judges the rewrite. (Claude Code runs
		// hooks side by side on the same input and applies an allow to a
		// passthrough hook's rewrite; kiln's hooks run in turn, so it can
		// tell.) An "ask" stays.
		if decided == "" && outcome.Decision == DecisionAllow && changedInput(current.ToolInput, outcome.UpdatedInput) {
			outcome.Decision, outcome.DecisionReason = "", ""
		}
		outcome.merge(decided, decidedReason)
		if opts.OnNotice != nil {
			for _, n := range outcome.Notices[before:] {
				opts.OnNotice(n)
			}
		}

		// A block ends the chain: later hooks have nothing left to decide,
		// and it outranks an earlier hook's allow or ask.
		if outcome.Blocked != nil {
			outcome.Decision, outcome.DecisionReason = "", ""
			break
		}
	}

	return outcome
}

// changedInput reports whether updated (the rewrites so far, merged over
// the original by key) gives a key a value other than the one seen.
func changedInput(seen, updated map[string]any) bool {
	for k, v := range updated {
		if old, ok := seen[k]; !ok || !reflect.DeepEqual(old, v) {
			return true
		}
	}
	return false
}

// CheckFunc is the permission check, already bound to a gate. decision and
// reason are the PreToolUse hooks' permission decision short of a denial
// ("" when they made none); the gate applies it (permission.Request's
// HookDecision).
type CheckFunc func(toolName, primaryArg string, hasPrimaryArg bool, args map[string]any, decision Decision, reason string) (*Blocked, error)

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
	// CheckArgs, if set, vets hook-rewritten input before the gate judges
	// it (tool.CheckArgs, via harness.Harness.CheckToolArgs): a rewrite
	// whose keys the gate and the tool would read differently is refused
	// without asking the gate. The turn loop already checked the model's
	// own input before any hook ran.
	CheckArgs func(args map[string]any) error
}

// GuardResult is the outcome of GuardToolCall.
type GuardResult struct {
	Blocked *Blocked
	// ByHook reports that a PreToolUse hook, not the permission gate,
	// blocked the call.
	ByHook bool
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
		return GuardResult{Blocked: hookResult.Blocked, ByHook: true}, nil
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
		if opts.CheckArgs != nil {
			if err := opts.CheckArgs(args); err != nil {
				return GuardResult{Blocked: &Blocked{Reason: fmt.Sprintf("The call to %s did not run: a PreToolUse hook rewrote its input, and %s.", opts.ToolName, err.Error())}, ByHook: true}, nil
			}
		}
		if opts.OnNotice != nil {
			primary, _ := opts.PrimaryArgOf(args)
			opts.OnNotice(fmt.Sprintf("rewrote %s: %s", opts.ToolName, primary))
		}
	}

	// The gate judges the input as rewritten, and gets the hooks'
	// decision with it: an "allow" skips the prompt but not deny or ask
	// rules, and an "ask" forces the prompt (Claude Code's
	// resolveHookPermissionDecision).
	primary, hasPrimary := opts.PrimaryArgOf(args)
	blocked, err := opts.Check(opts.ToolName, primary, hasPrimary, args, hookResult.Decision, hookResult.DecisionReason)
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
