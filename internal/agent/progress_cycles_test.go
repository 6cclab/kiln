package agent

import (
	"context"
	"strings"
	"testing"
)

// The depth-one dispatcher created for S1 must pass the cycle limit into
// S2's harness. S2 has no final-text step: only the guard can end its run
// before it asks the model for a fifth response.
func TestDispatchForwardsCycleLimitToNestedWorker(t *testing.T) {
	script := `model: faux-1
steps:
  - tool_call: {name: task, args: {subagent_type: general-purpose, description: nested, prompt: look}, id: child}
  - tool_call: {name: read, args: {path: a-missing}, id: a1}
  - tool_call: {name: read, args: {path: b-missing}, id: b1}
  - tool_call: {name: read, args: {path: a-missing}, id: a2}
  - tool_call: {name: read, args: {path: b-missing}, id: b2}
  - on_tool_result: child
    then:
      - text: parent-done
`
	dispatcher, _, _ := newParentAndDispatcher(t, script, nil)
	dispatcher.CycleLimit = 2
	var childFailure bool
	dispatcher.OnEvent = func(event SubagentEvent) {
		if event.Depth == 2 && event.Kind == SubagentEventError && strings.Contains(event.Message, "failed tool-call cycle") {
			childFailure = true
		}
	}
	result, err := dispatcher.Dispatch(context.Background(), DispatchRequest{Agent: "general-purpose", Prompt: "start"})
	if err != nil {
		t.Fatalf("outer dispatch: %v", err)
	}
	if !childFailure || result.Text != "parent-done" {
		t.Fatalf("nested cycle guard did not fire: childFailure=%v result=%+v", childFailure, result)
	}
}
