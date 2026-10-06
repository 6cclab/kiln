package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/andrepato/harness/internal/crash"
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
	// Stop / SubagentStop: the text of the reply that ended the turn.
	LastAssistantMessage string `json:"last_assistant_message,omitempty"`
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
	// Cancelled is set when the chain's ctx was cancelled (Esc / Lane.Abort)
	// while a hook was running: that hook was killed mid-run, and the chain
	// stopped there, so nothing after it ran. Distinct from Blocked - a
	// cancelled hook made no decision at all, it just did not get to
	// finish; the caller decides what that means for the call it was
	// guarding (GuardToolCall turns it into an error, so the tool does not
	// run - see its doc comment).
	Cancelled bool
	// Stopped is set when a hook answered {"continue": false}: stop
	// everything, with StopReason (its stopReason) for the user. Blocked
	// is set too, so an event that reads only Blocked still refuses; a
	// Stop hook's caller reads Stopped first, because for Stop a block
	// means the opposite (keep going).
	Stopped    bool
	StopReason string
	// BlockReasons holds every hook's block reason, in the order the
	// hooks finished. RunHooks (sequential) sets at most one - a block
	// ends the chain, so nothing after it ever gets to contribute a
	// second reason. RunHooksConcurrently can set several: its hooks run
	// side by side, so one hook blocking does not stop another from
	// finishing and reporting its own. Blocked still carries the first
	// one, for callers that only check whether anything blocked at all.
	BlockReasons []string
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
	// read for PreToolUse, and for Stop and SubagentStop, where "block"
	// keeps the turn going.
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

type runOutput struct {
	code      int
	stdout    string
	stderr    string
	timedOut  bool
	cancelled bool
}

// MaxOutputBytes caps how many bytes of a hook's stdout, and separately its
// stderr, runCommand retains. Matches Claude Code's own bound on a hook's
// captured output: its hook runner buffers stdout and stderr together in
// memory up to 8MB before diverting the rest to a spill file. kiln has no
// spill path for hook output - a hook's stdout/stderr is read once, to
// decide the Outcome, not tailed live the way a backgrounded shell command
// is - so bytes past the cap are simply dropped rather than diverted.
const MaxOutputBytes = 8 * 1024 * 1024

// boundedWriter retains at most max bytes written to it and silently drops
// the rest, mirroring execenv's bounded capture (internal/execenv/capture.go)
// for the same reason: a hook that produces continuous output for its whole
// run must not grow kiln's memory without bound, timeout or no timeout.
//
// Write always reports the full count written and never returns an error,
// so a chatty hook never sees a short write or a broken pipe because kiln
// stopped remembering its output - it just stops being remembered.
type boundedWriter struct {
	max int
	buf bytes.Buffer
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if remaining := w.max - w.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			w.buf.Write(p[:remaining])
		} else {
			w.buf.Write(p)
		}
	}
	return len(p), nil
}

func (w *boundedWriter) String() string { return w.buf.String() }

// runCommand runs command through /bin/sh -c, feeding input on stdin and
// enforcing timeoutSeconds. It is spawned in its own process group so a
// timeout can kill the whole tree: a plain child.Kill only kills the shell
// itself, leaving grandchildren alive holding the stdout pipe open, which
// means Wait never returns and the timeout never actually times out.
//
// ctx is raced against the timeout and the command's own completion: Esc
// (Lane.Abort) cancels the ctx a running PreToolUse/PostToolUse hook was
// started with, and that must kill the hook the same way a timeout does -
// process group and all - rather than leaving the user watching a hook run
// out its full timeout after they already asked to stop. ctx must not be
// nil; RunHooks defaults it to context.Background() so a caller that has no
// live per-operation ctx to offer (Stop, SessionStart, ...) still runs
// hooks to completion/timeout exactly as before this existed.
func runCommand(ctx context.Context, command, input string, timeoutSeconds int, cwd string, extraEnv map[string]string, maxOutputBytes int) runOutput {
	if maxOutputBytes <= 0 {
		maxOutputBytes = MaxOutputBytes
	}
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

	stdout := &boundedWriter{max: maxOutputBytes}
	stderr := &boundedWriter{max: maxOutputBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.Stdin = strings.NewReader(input)

	if err := cmd.Start(); err != nil {
		// A missing interpreter or unreadable script is a failed hook, not
		// a failed session.
		return runOutput{code: 1, stderr: err.Error()}
	}

	done := make(chan error, 1)
	crash.Go(func() { done <- cmd.Wait() })

	timer := time.NewTimer(time.Duration(timeoutSeconds) * time.Second)
	defer timer.Stop()

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
		return runOutput{code: code, stdout: stdout.String(), stderr: stderr.String()}
	case <-timer.C:
		if cmd.Process != nil {
			if err := execenv.KillProcessGroup(cmd.Process.Pid, execenv.SignalKill); err != nil {
				_ = cmd.Process.Kill()
			}
		}
		<-done // reap
		return runOutput{code: -1, stdout: stdout.String(), stderr: stderr.String(), timedOut: true}
	case <-ctx.Done():
		if cmd.Process != nil {
			if err := execenv.KillProcessGroup(cmd.Process.Pid, execenv.SignalKill); err != nil {
				_ = cmd.Process.Kill()
			}
		}
		<-done // reap
		return runOutput{code: -1, stdout: stdout.String(), stderr: stderr.String(), cancelled: true}
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
//
// command is the hook's full, untruncated configured command, used only
// for Stop and SubagentStop's exit-2 block reason, which names the
// command the way Claude Code does ("[command]: stderr", with "No
// stderr output" standing in for empty stderr) - the other events keep
// kiln's own shorter fallback ("blocked by hook: <label>", label already
// truncated by the caller) since nothing asked for parity there.
func interpret(out runOutput, outcome *Outcome, label, command string, event Event) {
	text := strings.TrimSpace(out.stdout)

	if out.cancelled {
		// No stdout/stderr parsing: a killed hook made no decision, and
		// whatever partial output it produced before dying is not a
		// decision document. RunHooks stops the chain here (Outcome.Cancelled).
		outcome.Cancelled = true
		return
	}

	if out.timedOut {
		outcome.Notices = append(outcome.Notices, fmt.Sprintf("hook timed out: %s", label))
		return
	}

	// Exit 2 is Claude Code's "block", with the reason on stderr. Checked
	// before stdout is parsed: a blocking hook's stdout is not a decision
	// document.
	if out.code == 2 {
		reason := strings.TrimSpace(out.stderr)
		if event == Stop || event == SubagentStop {
			if reason == "" {
				reason = "No stderr output"
			}
			reason = fmt.Sprintf("[%s]: %s", command, reason)
		} else if reason == "" {
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
		outcome.Stopped, outcome.StopReason = true, parsed.StopReason
		return
	}

	// Stop and SubagentStop give "decision": "block" its own meaning: do
	// not stop, keep working, with reason telling the model why.
	if (event == Stop || event == SubagentStop) && parsed.Decision == "block" {
		reason := strings.TrimSpace(parsed.Reason)
		if reason == "" {
			reason = fmt.Sprintf("blocked by hook: %s", label)
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
	// Stop and SubagentStop accept hookSpecificOutput.additionalContext on
	// the wire but it has no effect: nothing downstream (stopVerdict,
	// internal/cli/stophooks.go) reads Outcome.Context for either event.
	// That matches Claude Code's observed behaviour: a Stop or
	// SubagentStop hook's additionalContext never reaches the model there
	// either. Its public hooks docs describe the field as continuing the
	// conversation for these events too; the shipped behaviour does not,
	// and kiln follows what ships.
	if specific.AdditionalContext != "" && event != Stop && event != SubagentStop {
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
	// Ctx bounds how long a hook in this chain gets to run: cancelling it
	// kills whatever hook is currently executing, process group and all,
	// the same way its own per-hook timeout does, and stops the chain
	// there (Outcome.Cancelled). Nil behaves as context.Background() - no
	// cancellation - which is what a caller with no live per-operation ctx
	// to offer (Stop, SessionStart, PreCompact, ...) gets by leaving this
	// unset; only the two hooks that run inside an active, abortable tool
	// call (PreToolUse via GuardToolCall, PostToolUse) are wired to a real
	// one today.
	Ctx context.Context
	// MaxOutputBytes overrides MaxOutputBytes (the default stdout/stderr
	// cap per hook) for this call. Zero uses the default; only tests
	// should need to set this, to exercise the cap without producing
	// megabytes of real output.
	MaxOutputBytes int
	// OnHookStart, if set, is called before each hook in the chain runs,
	// with the hook's index and the whole matching chain, so a caller can
	// show which hook is running (the busy row's "running stop hooks… 1/2").
	// RunHooks only - RunHooksConcurrently has no "about to run" moment
	// since every hook starts together, so it calls OnHookDone instead.
	OnHookStart func(index int, commands []Command)
	// OnHookDone, if set, is called by RunHooksConcurrently once before
	// any hook starts (done=0, so a caller can show the total right
	// away) and again each time a hook finishes, with how many have
	// finished so far and the whole matching chain. RunHooks never calls
	// it - its hooks finish one at a time in configuration order, which
	// OnHookStart's index already reports.
	OnHookDone func(done int, commands []Command)
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

	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	maxOutputBytes := opts.MaxOutputBytes
	if maxOutputBytes <= 0 {
		maxOutputBytes = MaxOutputBytes
	}

	payload := opts.Payload
	payload.HookEventName = opts.Event

	for i, h := range commands {
		if opts.OnHookStart != nil {
			opts.OnHookStart(i, commands)
		}
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

		out := runCommand(ctx, h.Command, string(data), timeout, opts.Payload.Cwd, h.Env, maxOutputBytes)

		before := len(outcome.Notices)
		label := h.Command
		if len(label) > 60 {
			label = label[:60]
		}
		// This hook's own decision, apart from the earlier hooks'.
		prevDecision, prevReason := outcome.Decision, outcome.DecisionReason
		outcome.Decision, outcome.DecisionReason = "", ""
		interpret(out, &outcome, label, h.Command, opts.Event)
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
			if len(outcome.BlockReasons) == 0 {
				outcome.BlockReasons = []string{outcome.Blocked.Reason}
			}
			break
		}
		// A cancelled hook decided nothing, and ctx is now done for the
		// rest of this chain too - no point starting another hook just to
		// kill it immediately.
		if outcome.Cancelled {
			outcome.Decision, outcome.DecisionReason = "", ""
			break
		}
	}

	return outcome
}

// RunHooksConcurrently runs every hook registered for an event at once,
// each racing its own timeout, instead of RunHooks' one-after-another
// chain. Wired for Stop and SubagentStop only (internal/cli/chat.go, via
// stopVerdict): those are the only events where nothing rewrites what
// another hook sees, so starting them together changes what a chain of
// blocks does without changing what any one hook is handed.
//
// Combining, matching Claude Code: every hook that blocks contributes
// its own reason (Outcome.BlockReasons, Blocked holding the first one);
// a hook answering {"continue": false} always wins over every block,
// because Stopped and StopReason are set independently of BlockReasons
// and a Stop/SubagentStop caller checks Stopped first (see Outcome's
// doc comment) - when more than one hook stops it, the last one to
// finish names StopReason, there being no configuration order left to
// prefer once hooks run side by side. Notices and the block/stop
// results are combined in the order hooks finish, not configuration
// order.
//
// Cancellation: ctx being cancelled kills every hook still running,
// process group and all (each one's own runCommand call already reacts
// to ctx.Done the same way a timeout does), and the combined Outcome is
// simply Cancelled - whatever any hook decided before being killed is
// discarded, since the caller asked to stop, not to hear verdicts from
// whichever hooks happened to finish first.
//
// Every goroutine this starts has sent its result and returned before
// RunHooksConcurrently does: none outlives the call.
func RunHooksConcurrently(opts RunOptions) Outcome {
	commands := HooksFor(opts.Config, opts.Event, opts.ToolName, opts.HasToolName)
	if len(commands) == 0 {
		return Outcome{}
	}

	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	maxOutputBytes := opts.MaxOutputBytes
	if maxOutputBytes <= 0 {
		maxOutputBytes = MaxOutputBytes
	}

	payload := opts.Payload
	payload.HookEventName = opts.Event
	data, _ := json.Marshal(payload)
	input := string(data)

	if opts.OnHookDone != nil {
		opts.OnHookDone(0, commands)
	}

	results := make(chan Outcome, len(commands))
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		finished int
	)
	wg.Add(len(commands))
	for _, h := range commands {
		h := h
		crash.Go(func() {
			defer wg.Done()
			timeout := h.Timeout
			if timeout == 0 {
				timeout = DefaultTimeoutSeconds
			}
			out := runCommand(ctx, h.Command, input, timeout, opts.Payload.Cwd, h.Env, maxOutputBytes)

			label := h.Command
			if len(label) > 60 {
				label = label[:60]
			}
			var single Outcome
			interpret(out, &single, label, h.Command, opts.Event)

			mu.Lock()
			finished++
			n := finished
			mu.Unlock()
			if opts.OnHookDone != nil {
				opts.OnHookDone(n, commands)
			}
			results <- single
		})
	}

	combined := Outcome{}
	for range commands {
		r := <-results
		if opts.OnNotice != nil {
			for _, n := range r.Notices {
				opts.OnNotice(n)
			}
		}
		combined.Notices = append(combined.Notices, r.Notices...)
		combined.Context = append(combined.Context, r.Context...)
		if r.Cancelled {
			combined.Cancelled = true
		}
		if r.Stopped {
			combined.Stopped, combined.StopReason = true, r.StopReason
		}
		if r.Blocked != nil {
			combined.BlockReasons = append(combined.BlockReasons, r.Blocked.Reason)
			if combined.Blocked == nil {
				combined.Blocked = r.Blocked
			}
		}
	}
	wg.Wait()

	if ctx.Err() != nil {
		// The caller asked every hook to stop: whatever any of them
		// decided before being killed does not matter any more, only
		// that the chain is done.
		return Outcome{Cancelled: true}
	}
	return combined
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
	// Ctx is the tool call's own ctx (Lane.beginTool's), forwarded to the
	// PreToolUse hooks so Esc (Lane.Abort) kills one that is still running
	// instead of waiting out its timeout. Nil behaves as
	// context.Background() (see RunOptions.Ctx).
	Ctx context.Context
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
//
// A PreToolUse hook killed mid-run by opts.Ctx being cancelled (Esc) is
// reported as an error, not a Blocked: the caller (Lane.invokeBeforeTool)
// already treats a before_tool error as "the call did not run", which is
// exactly what a cancelled hook means here - it is the same path an
// erroring hook or a failed permission check already takes, not a new one.
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
		Ctx:      opts.Ctx,
	})

	if hookResult.Cancelled {
		ctx := opts.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		if err := ctx.Err(); err != nil {
			return GuardResult{}, err
		}
		return GuardResult{}, context.Canceled
	}

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
