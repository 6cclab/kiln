//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/session/jsonl"
)

func TestTaskState_ResumeAfterCompaction(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		name := "enabled"
		if !enabled {
			name = "disabled"
		}
		t.Run(name, func(t *testing.T) {
			home, sessDir := scratchHome(t)
			proj := scratchProject(t)
			addr, _ := startFaux(t, `model: faux-1
steps:
  - tool_call: {name: todo_write, args: {todos: [{content: "Verify upload retries", status: "in_progress"}]}, id: plan1}
  - on_tool_result: plan1
    then:
      - text: "Plan saved."
`)
			res := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "start task", "--permission-mode", "bypassPermissions")
			if res.Code != 0 {
				t.Fatalf("save: %s", res.Stderr)
			}
			path := sessionFile(t, sessDir, proj)
			store, err := jsonl.Open(path, nil)
			if err != nil {
				t.Fatal(err)
			}
			raw, _, _ := store.GetValue(session.NamespaceBranchTip, agent.MainLane)
			tip, err := session.GetTypedValue[*string](raw)
			if err != nil {
				t.Fatal(err)
			}
			// Install a compaction with no retained todo call. The committed
			// ancestry remains on disk exactly as real compaction leaves it.
			id := "compacted-task-state"
			write, err := session.SetValue(session.BranchTip(agent.MainLane), &id)
			if err != nil {
				t.Fatal(err)
			}
			_, commitErr := store.Commit([]session.Write{session.EntryWrite{Entry: session.Entry{ID: id, ParentID: tip, Type: session.EntryCompaction, Summary: "Earlier conversation summarized."}}, write})
			closeErr := store.Close()
			if commitErr != nil {
				t.Fatal(commitErr)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
			addr2, server := startFaux(t, "model: faux-1\nsteps:\n  - text: Resumed.\n")
			args := []string{"-c", "-p", "continue", "--permission-mode", "bypassPermissions"}
			if !enabled {
				config := filepath.Join(t.TempDir(), "settings.json")
				if err := os.WriteFile(config, []byte(`{"taskStateEnabled":false}`), 0600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--settings", config)
			}
			res = runHarness(t, proj, baseEnv(home, sessDir, addr2), args...)
			if res.Code != 0 {
				t.Fatalf("resume: %s", res.Stderr)
			}
			requests := server.Requests()
			if len(requests) == 0 {
				t.Fatal("no resumed request")
			}
			var messages []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			}
			if err := json.Unmarshal(requests[0].Messages, &messages); err != nil {
				t.Fatal(err)
			}
			text := ""
			for _, message := range messages {
				for _, block := range message.Content {
					text += block.Text
				}
			}
			if strings.Contains(text, "Verify upload retries") != enabled || strings.Contains(text, "<task-state>") != enabled {
				t.Fatalf("task state enabled=%v, request=%s", enabled, text)
			}
		})
	}
}
