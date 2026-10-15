package cli

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/andrepato/harness/internal/agent"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/msg"
)

// modeReminders tells the model about plan mode as the gate's mode moves,
// the way Claude Code does: a <system-reminder> ahead of the next prompt,
// never a change to the system prompt (which would cost the whole prompt
// cache on every switch, and which went stale: a session started in plan
// kept "You are in PLAN MODE" after approval, and one that entered plan
// later was never told).
//
// announcedPlan is what the model was last told in a stored prompt. A
// switch made mid-turn is not stored yet, so TransformContext adds it to
// each request, unstored, until the next prompt carries it.
type modeReminders struct {
	mode func() claudesettings.PermissionMode
	// planExtra is appended to the full plan-mode text (the plan ledger
	// experiment's paragraph, HARNESS_EXP_LEDGER); "" when off.
	planExtra string

	mu            sync.Mutex
	announcedPlan bool
}

func newModeReminders(mode func() claudesettings.PermissionMode, planExtra string) *modeReminders {
	return &modeReminders{mode: mode, planExtra: planExtra}
}

// reminder is the text for the current mode given what was announced,
// and whether it changes what the model knows (entering or leaving plan).
// Caller holds mu.
func (r *modeReminders) reminder() (text string, changes bool) {
	cur := r.mode()
	inPlan := cur == claudesettings.ModePlan
	switch {
	case inPlan && !r.announcedPlan:
		full := agent.PlanModePrompt
		if r.planExtra != "" {
			full += "\n\n" + r.planExtra
		}
		return full, true
	case inPlan:
		return agent.PlanModeSparseReminder, false
	case r.announcedPlan:
		return agent.PlanModeExitReminder(string(cur)), true
	}
	return "", false
}

// TakeForPrompt returns the <system-reminder> preamble for the prompt
// being submitted ("" when there is nothing to say) and records it as
// announced, since the prompt is stored with it.
func (r *modeReminders) TakeForPrompt() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	text, _ := r.reminder()
	r.announcedPlan = r.mode() == claudesettings.ModePlan
	if text == "" {
		return ""
	}
	return wrapReminder(text)
}

// Prefix puts TakeForPrompt's preamble ahead of prompt.
func (r *modeReminders) Prefix(prompt string) string {
	if pre := r.TakeForPrompt(); pre != "" {
		return pre + "\n\n" + prompt
	}
	return prompt
}

// TransformContext adds, unstored, a mode change made since the last
// prompt, so the model's next request in this turn already follows it.
func (r *modeReminders) TransformContext(_ context.Context, transcript []msg.Message) ([]msg.Message, error) {
	r.mu.Lock()
	text, changes := r.reminder()
	r.mu.Unlock()
	if !changes {
		return transcript, nil
	}
	return append(transcript, msg.UserMessage{
		Role:      msg.RoleUser,
		Content:   msg.Blocks{msg.Text(wrapReminder(text))},
		Timestamp: time.Now().UnixMilli(),
	}), nil
}

// LeftPlanByApproval records that the model already knows plan mode is
// over: the approval's tool result told it the new mode.
func (r *modeReminders) LeftPlanByApproval() {
	r.mu.Lock()
	r.announcedPlan = false
	r.mu.Unlock()
}

// Forget is for compaction: the summary may drop the stored reminder, so
// a session still in plan is told again in full.
func (r *modeReminders) Forget() {
	r.mu.Lock()
	r.announcedPlan = false
	r.mu.Unlock()
}

func wrapReminder(text string) string {
	return "<system-reminder>\n" + strings.TrimSpace(text) + "\n</system-reminder>"
}
