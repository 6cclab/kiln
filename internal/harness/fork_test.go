package harness

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	fauxprovider "github.com/andrepato/harness/internal/provider/faux"
)

// TestForkLastRequestSharesThePrefix: a side request built on
// ForkLastRequest sends the lane's last request unchanged (system prompt,
// tools, every message) followed by its answer, so a prompt cache serves
// the whole prefix. Anthropic's cache breakpoint moves to the new tail;
// that marker is the only difference allowed.
func TestForkLastRequestSharesThePrefix(t *testing.T) {
	for _, api := range []string{"openai-completions", "anthropic-messages"} {
		t.Run(api, func(t *testing.T) {
			script := "model: faux-1\nsteps:\n" +
				"  - tool_call: {name: bash, args: {command: \"echo hi\"}}\n" +
				"  - text: \"It printed hi.\"\n    end_turn: true\n" +
				"  - text: \"side answer\"\n    end_turn: true\n"
			rig := newTestRigWith(t, script, []string{"bash", "read"}, nil, func(_ *Options, reg *provider.Registry) {
				t.Setenv("HARNESS_FAUX_API", api)
				fp, ok := fauxprovider.New()
				if !ok {
					t.Fatal("fauxprovider.New failed")
				}
				reg.Register(fp)
			})
			lane := rig.mustLane("main")
			if _, err := lane.ForkLastRequest(); !errors.Is(err, ErrNoSideRequest) {
				t.Fatalf("before any request: err = %v, want ErrNoSideRequest", err)
			}
			if res, err := lane.Prompt(context.Background(), "run echo", nil); err != nil || res.Status != StatusCompleted {
				t.Fatalf("Prompt = %+v, %v", res, err)
			}
			side, err := lane.ForkLastRequest()
			if err != nil {
				t.Fatalf("ForkLastRequest: %v", err)
			}
			if got := msg.TextOf(side.Messages[len(side.Messages)-1].(msg.AssistantMessage).Content); got != "It printed hi." {
				t.Fatalf("fork ends with %q, want the last answer", got)
			}
			messages := append(side.Messages, msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("review this")}, Timestamp: time.Now().UnixMilli()})
			ch, wait := provider.Stream(context.Background(), side.Provider, side.Model, messages, side.Options)
			for range ch {
			}
			if _, err := wait(); err != nil {
				t.Fatalf("side request: %v", err)
			}

			reqs := rig.Faux.Requests()
			if len(reqs) != 3 {
				t.Fatalf("%d requests, want 3", len(reqs))
			}
			last, fork := reqs[1], reqs[2]
			if fork.System != last.System || !reflect.DeepEqual(fork.Tools, last.Tools) || fork.Model != last.Model {
				t.Errorf("fork changed the system prompt, tools or model:\n%+v\n%+v", last, fork)
			}
			var lastMsgs, forkMsgs []map[string]any
			_ = json.Unmarshal(last.Messages, &lastMsgs)
			_ = json.Unmarshal(fork.Messages, &forkMsgs)
			if len(forkMsgs) <= len(lastMsgs) {
				t.Fatalf("fork has %d messages, last request %d", len(forkMsgs), len(lastMsgs))
			}
			for i := range lastMsgs {
				a, b := stripCacheControl(lastMsgs[i]), stripCacheControl(forkMsgs[i])
				if !reflect.DeepEqual(a, b) {
					t.Fatalf("message %d differs:\nlast %v\nfork %v", i, a, b)
				}
			}
		})
	}
}

func stripCacheControl(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if k != "cache_control" {
				out[k] = stripCacheControl(val)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = stripCacheControl(val)
		}
		return out
	}
	return v
}

// TestForkLastRequestRefusesAMovedConversation: once the tip is no longer
// the last answer, there is nothing to continue.
func TestForkLastRequestRefusesAMovedConversation(t *testing.T) {
	rig := newTestRig(t, "model: faux-1\nsteps:\n  - text: \"hello\"\n    end_turn: true\n", []string{"bash"})
	lane := rig.mustLane("main")
	if res, err := lane.Prompt(context.Background(), "hi", nil); err != nil || res.Status != StatusCompleted {
		t.Fatalf("Prompt = %+v, %v", res, err)
	}
	if _, err := lane.ForkLastRequest(); err != nil {
		t.Fatalf("ForkLastRequest at the answer: %v", err)
	}
	entries, err := lane.FindEntries(context.Background())
	if err != nil || len(entries) == 0 {
		t.Fatalf("FindEntries = %d, %v", len(entries), err)
	}
	// Rewind to the prompt: the answer is no longer the tip.
	tip, _ := lane.GetTipID()
	first := entries[0].ID
	if first == tip {
		first = entries[len(entries)-1].ID
	}
	if err := lane.NavigateTree(context.Background(), &first); err != nil {
		t.Fatalf("NavigateTree: %v", err)
	}
	if _, err := lane.ForkLastRequest(); !errors.Is(err, ErrNoSideRequest) {
		t.Fatalf("after the tip moved: err = %v, want ErrNoSideRequest", err)
	}
}

// TestForkLastRequestRefusesUnrunToolCalls: while an answer's tool calls
// have not run, the answer is the tip, but a provider would refuse
// anything after it but their results.
func TestForkLastRequestRefusesUnrunToolCalls(t *testing.T) {
	script := "model: faux-1\nsteps:\n  - tool_call: {name: bash, args: {command: \"echo hi\"}}\n  - text: \"done\"\n    end_turn: true\n"
	rig := newTestRig(t, script, []string{"bash"})
	lane := rig.mustLane("main")
	var forkErr error
	var tipIsAnswer bool
	rig.H.Hooks().OnBeforeTool(func(context.Context, msg.ToolCall) (BeforeToolResult, error) {
		tip, _ := lane.GetTipID()
		lane.mu.Lock()
		tipIsAnswer = lane.lastReq != nil && lane.lastReq.entryID == tip
		lane.mu.Unlock()
		_, forkErr = lane.ForkLastRequest()
		return BeforeToolResult{}, nil
	})
	if res, err := lane.Prompt(context.Background(), "run echo", nil); err != nil || res.Status != StatusCompleted {
		t.Fatalf("Prompt = %+v, %v", res, err)
	}
	if !tipIsAnswer {
		t.Fatal("the answer with the tool call was not the tip while the tool ran")
	}
	if !errors.Is(forkErr, ErrNoSideRequest) {
		t.Fatalf("fork during the tool call: err = %v, want ErrNoSideRequest", forkErr)
	}
}
