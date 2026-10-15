package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
)

type recordingHooks struct {
	deny     string
	prompt   string
	calls    []string
	attached *harness.Harness
	status   string
}

func (r *recordingHooks) BeforeSpawn(ctx context.Context, agentID, agentType string, req *DispatchRequest) string {
	r.calls = append(r.calls, "spawn:"+agentID+":"+agentType)
	if r.prompt != "" {
		req.Prompt = r.prompt
	}
	return r.deny
}
func (r *recordingHooks) Attach(agentID string, h *harness.Harness) {
	r.calls = append(r.calls, "attach:"+agentID)
	r.attached = h
}
func (r *recordingHooks) Done(agentID, status string, _ msg.Usage) {
	r.calls = append(r.calls, "done:"+agentID)
	r.status = status
}

// TestDispatchHooksSeeAndSteerSubagents: the caller's hooks run in order
// around a dispatch, keyed by the dispatching tool call's id, and a
// rewritten prompt is what the subagent actually receives (squad appends a
// progress-reporting note to every subagent's prompt this way).
func TestDispatchHooksSeeAndSteerSubagents(t *testing.T) {
	d, _, _ := newParentAndDispatcher(t, "model: faux-1\nsteps:\n  - text: \"done\"\n", nil)
	h := &recordingHooks{prompt: "go\n\n(report your steps)"}
	d.Hooks = h
	var firstUser string
	h2 := &attachProbe{recordingHooks: h, onAttach: func(hs *harness.Harness) {
		hs.Hooks().OnTransformContext(func(_ context.Context, transcript []msg.Message) ([]msg.Message, error) {
			if firstUser == "" {
				for _, m := range transcript {
					if um, ok := m.(msg.UserMessage); ok {
						firstUser = msg.TextOf(um.Content)
						break
					}
				}
			}
			return transcript, nil
		})
	}}
	d.Hooks = h2
	if _, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Description: "x", Prompt: "go", ToolCallID: "tc9"}); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got := strings.Join(h.calls, ","); got != "spawn:tc9:general-purpose,attach:tc9,done:tc9" {
		t.Fatalf("hook order = %s", got)
	}
	if h.status != harness.StatusCompleted {
		t.Errorf("Done status = %q, want completed", h.status)
	}
	if !strings.Contains(firstUser, "(report your steps)") {
		t.Errorf("subagent's prompt = %q, want the rewritten one", firstUser)
	}
}

type attachProbe struct {
	*recordingHooks
	onAttach func(*harness.Harness)
}

func (a *attachProbe) Attach(agentID string, h *harness.Harness) {
	a.recordingHooks.Attach(agentID, h)
	a.onAttach(h)
}

// TestDispatchHookDenyRefusesTheSpawn: a deny reason is the task's result
// and no subagent session starts.
func TestDispatchHookDenyRefusesTheSpawn(t *testing.T) {
	d, _, _ := newParentAndDispatcher(t, "model: faux-1\nsteps:\n  - text: \"should not run\"\n", nil)
	h := &recordingHooks{deny: "not now"}
	d.Hooks = h
	res, err := d.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "go", ToolCallID: "tc1"})
	if err != nil || res.Text != "not now" {
		t.Fatalf("Dispatch = %+v, %v; want the deny reason as the result", res, err)
	}
	if strings.Join(h.calls, ",") != "spawn:tc1:general-purpose" {
		t.Fatalf("hooks after a deny: %v", h.calls)
	}
}
