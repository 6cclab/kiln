package harness

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
)

// An OnBeforeStop handler that answers Continue keeps the run going: its
// message is committed to the branch as a user message the model reads,
// marked with its source, and the model gets one more turn in the same
// operation. StopHookActive is false on the first call and true on the one
// after the continuation.
func TestBeforeStopContinuesTheRun(t *testing.T) {
	rig := newTestRig(t, `
model: faux-1
steps:
  - text: "first reply"
    end_turn: true
  - text: "second reply"
    end_turn: true
`, []string{"bash"})
	lane := rig.mustLane("main")

	var infos []StopInfo
	rig.H.Hooks().OnBeforeStop(func(ctx context.Context, info StopInfo) (StopVerdict, error) {
		infos = append(infos, info)
		if info.StopHookActive {
			return StopVerdict{}, nil
		}
		return StopVerdict{Continue: true, Message: "Stop hook asked to continue:\nrun the tests", Source: "Stop"}, nil
	})
	runs := 0
	rig.H.Events().On(EventRunStart, func(Event) { runs++ })

	res, err := lane.Prompt(context.Background(), "do the thing", nil)
	if err != nil || res.Status != StatusCompleted {
		t.Fatalf("Prompt = %+v, %v; want completed", res, err)
	}
	if runs != 1 {
		t.Errorf("%d runs started, want 1: the continuation belongs to the same run", runs)
	}
	if len(infos) != 2 {
		t.Fatalf("OnBeforeStop called %d times, want 2", len(infos))
	}
	if infos[0].StopHookActive || !infos[1].StopHookActive {
		t.Errorf("StopHookActive = %v then %v, want false then true", infos[0].StopHookActive, infos[1].StopHookActive)
	}
	if infos[0].Last == nil || msg.TextOf(infos[0].Last.Content) != "first reply" {
		t.Errorf("first call's Last = %+v, want the first reply", infos[0].Last)
	}

	reqs := rig.Faux.Requests()
	if len(reqs) != 2 {
		t.Fatalf("%d model requests, want 2", len(reqs))
	}
	var second []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(reqs[1].Messages, &second); err != nil {
		t.Fatal(err)
	}
	last := second[len(second)-1]
	if last.Role != "user" || !strings.Contains(string(last.Content), "run the tests") {
		t.Fatalf("second request ends with %s %s, want the hook's message as a user message", last.Role, last.Content)
	}
	if strings.Contains(string(reqs[1].Messages), "kilnHook") {
		t.Errorf("the provider was sent kiln's own field: %s", reqs[1].Messages)
	}

	// Persisted like any other input: on the branch, between the two
	// replies, recorded as the hook's.
	entries, _ := lane.FindEntries(context.Background())
	var texts []string
	var hookMsg *msg.UserMessage
	for _, e := range entries {
		switch m := e.Message.(type) {
		case msg.UserMessage:
			texts = append(texts, "user:"+msg.TextOf(m.Content))
			if m.KilnHook != "" {
				hm := m
				hookMsg = &hm
			}
		case msg.AssistantMessage:
			texts = append(texts, "assistant:"+msg.TextOf(m.Content))
		}
	}
	// FindEntries lists the branch from its tip back.
	want := []string{"assistant:second reply", "user:Stop hook asked to continue:\nrun the tests", "assistant:first reply", "user:do the thing"}
	if strings.Join(texts, "|") != strings.Join(want, "|") {
		t.Fatalf("branch = %q, want %q", texts, want)
	}
	if hookMsg == nil || hookMsg.KilnHook != "Stop" || hookMsg.KilnTyped != "" {
		t.Fatalf("continuation message = %+v, want KilnHook Stop and no typed line", hookMsg)
	}
}

// stop_hook_active stays true for the rest of the run, also across tool
// turns after the continuation, and starts false again on the next prompt.
func TestBeforeStopActiveLastsTheRun(t *testing.T) {
	rig := newTestRig(t, `
model: faux-1
steps:
  - text: "first"
    end_turn: true
  - tool_call: {name: bash, args: {command: "true"}, id: c1}
  - on_tool_result: c1
    then:
      - text: "after the tool"
        end_turn: true
  - text: "next prompt"
    end_turn: true
`, []string{"bash"})
	lane := rig.mustLane("main")

	var active []bool
	rig.H.Hooks().OnBeforeStop(func(ctx context.Context, info StopInfo) (StopVerdict, error) {
		active = append(active, info.StopHookActive)
		if len(active) == 1 {
			return StopVerdict{Continue: true, Message: "keep going", Source: "Stop"}, nil
		}
		return StopVerdict{}, nil
	})
	if res, _ := lane.Prompt(context.Background(), "one", nil); res.Status != StatusCompleted {
		t.Fatalf("first run %q", res.Status)
	}
	if res, _ := lane.Prompt(context.Background(), "two", nil); res.Status != StatusCompleted {
		t.Fatalf("second run %q", res.Status)
	}
	if want := []bool{false, true, false}; len(active) != 3 || active[0] != want[0] || active[1] != want[1] || active[2] != want[2] {
		t.Fatalf("StopHookActive per call = %v, want %v", active, want)
	}
}

// A handler interrupted by Esc decided nothing: the run ends as aborted,
// and nothing continues it.
func TestBeforeStopInterruptedEndsTheRunAborted(t *testing.T) {
	rig := newTestRig(t, `
model: faux-1
steps:
  - text: "reply"
    end_turn: true
  - text: "must not be requested"
    end_turn: true
`, []string{"bash"})
	lane := rig.mustLane("main")
	rig.H.Hooks().OnBeforeStop(func(ctx context.Context, info StopInfo) (StopVerdict, error) {
		_ = lane.Abort()
		<-ctx.Done()
		// What chat.go's handler returns for a hook Esc killed.
		return StopVerdict{Interrupted: true}, nil
	})
	res, _ := lane.Prompt(context.Background(), "go", nil)
	if res.Status != StatusAborted {
		t.Fatalf("status %q, want aborted", res.Status)
	}
	if n := len(rig.Faux.Requests()); n != 1 {
		t.Fatalf("%d model requests, want 1", n)
	}
}

// Esc during the continued turn interrupts it like any other: the lane
// stays busy and abortable through the continuation.
func TestBeforeStopContinuedTurnIsAbortable(t *testing.T) {
	rig := newTestRig(t, `
model: faux-1
steps:
  - text: "first"
    end_turn: true
  - chunk_delay: 30ms
    text: "`+strings.Repeat("a slow continued reply ", 20)+`"
    end_turn: true
`, []string{"bash"})
	lane := rig.mustLane("main")
	rig.H.Hooks().OnBeforeStop(func(ctx context.Context, info StopInfo) (StopVerdict, error) {
		return StopVerdict{Continue: true, Message: "again", Source: "Stop"}, nil
	})
	var once sync.Once
	var mu sync.Mutex
	deltas := 0
	rig.H.Events().On(EventMessageUpdate, func(ev Event) {
		if ev.StreamEvent == nil || ev.StreamEvent.Type != msg.EventTextDelta {
			return
		}
		mu.Lock()
		deltas++
		n := deltas
		mu.Unlock()
		if n > 3 && strings.Contains(ev.StreamEvent.Delta, "slow") {
			once.Do(func() { _ = lane.Abort() })
		}
	})
	done := make(chan RunResult, 1)
	go func() {
		res, _ := lane.Prompt(context.Background(), "go", nil)
		done <- res
	}()
	select {
	case res := <-done:
		if res.Status != StatusAborted {
			t.Fatalf("status %q, want aborted", res.Status)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the continued turn never ended after Abort")
	}
}
