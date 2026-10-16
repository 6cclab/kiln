package harness

import (
	"sync"
	"testing"
)

// The setters /model (agent.Retier), the MCP connect path and mods call
// run on goroutines of their own while a lane's turn loop builds requests,
// decides on compaction and runs tools. The loop must read the options
// those setters write under h.mu, never straight from h.opts: run with
// -race, this test fails on any unlocked read of a settable option.
func TestSettersRacingATurnLoop(t *testing.T) {
	rig := newTestRig(t, `
model: faux-1
steps:
  - tool_call: {name: bash, args: {command: "echo one"}, id: tc1}
  - on_tool_result: tc1
    then:
      - tool_call: {name: bash, args: {command: "echo two"}, id: tc2}
  - on_tool_result: tc2
    then:
      - tool_call: {name: read, args: {path: "missing.txt"}, id: tc3}
  - on_tool_result: tc3
    then:
      - text: "done"
`, nil)
	rig.H.SetCompactionSettings(CompactionSettings{Enabled: true, ReserveTokens: 1000, KeepRecentTokens: 1000})

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			rig.H.SetCompactionSettings(CompactionSettings{Enabled: true, ReserveTokens: 1000 + i%7, KeepRecentTokens: 1000})
			rig.H.SetToolOutputTokens(10000 + i%7)
			rig.H.SetSystemPrompt("test harness")
			rig.H.AddTools()
		}
	}()
	promptWithin(t, rig.mustLane("main"), "hi")
	close(stop)
	wg.Wait()

	if n := len(rig.Faux.Requests()); n != 4 {
		t.Fatalf("%d requests, want 4 (three tool steps and the answer)", n)
	}
}
