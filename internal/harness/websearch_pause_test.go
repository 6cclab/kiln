package harness

import (
	"context"
	"testing"

	"github.com/andrepato/harness/internal/msg"
)

// TestPauseTurnReRequests guards Anthropic's pause_turn handling in
// drive(): a turn that ends with no tool calls but stop_reason pause_turn
// (a long server-tool turn cut for interim delivery -- see
// mapAnthropicStopReason) must not finish the operation. The partial
// assistant message is committed to the branch as usual, and the loop
// re-requests immediately, picking it up as history; a second scripted
// turn then completes the operation normally. Break to verify: drop the
// "if final.StopReason == msg.StopPause { continue }" branch in drive()
// and this test fails by finishing the operation after only one request,
// with the search's own text as the final answer instead of "done
// searching".
func TestPauseTurnReRequests(t *testing.T) {
	script := `
model: faux-1
steps:
  - raw_blocks:
      - {type: server_tool_use, id: srvtoolu_1, name: web_search, input: {query: "go generics"}}
      - {type: web_search_tool_result, tool_use_id: srvtoolu_1, content: [{type: web_search_result, url: "https://go.dev", title: "Go"}]}
    stop_reason: pause_turn
  - text: "done searching"
`
	rig := newTestRig(t, script, []string{"bash"})
	lane := rig.mustLane("main")

	result, err := lane.Prompt(context.Background(), "search for go generics", nil)
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if result.Status != StatusCompleted {
		t.Fatalf("status = %q, err = %v", result.Status, result.Error)
	}

	reqs := rig.Faux.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2 (the pause_turn cut, then the re-request)", len(reqs))
	}

	entries, err := lane.FindEntries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var assistantTexts []string
	var sawProviderBlock bool
	for _, e := range entries {
		am, ok := e.Message.(msg.AssistantMessage)
		if !ok {
			continue
		}
		if text := msg.TextOf(am.Content); text != "" {
			assistantTexts = append(assistantTexts, text)
		}
		for _, c := range am.Content {
			if _, ok := c.(msg.ProviderBlock); ok {
				sawProviderBlock = true
			}
		}
	}
	if !sawProviderBlock {
		t.Fatal("no ProviderBlock committed from the pause_turn message")
	}
	if len(assistantTexts) != 1 || assistantTexts[0] != "done searching" {
		t.Fatalf("assistant texts = %v, want just [\"done searching\"] (the pause_turn message carried no text)", assistantTexts)
	}
}
