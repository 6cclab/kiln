package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/tools"
)

// taskState is projected from committed results on the selected branch. It
// never treats a proposed, denied or interrupted tool call as a saved plan.
type taskState struct {
	mu         sync.Mutex
	callID     string
	items      []tools.TodoItem
	taskCallID string
	task       tools.TaskCheckpoint
	proofs     []tools.VerificationEvidence
	cwd        string
	allowRead  func(string) bool
}

const retainedVerificationProofs = 16
const promptedVerificationProofs = 3

func acceptedTask(result msg.ToolResultMessage) (tools.TaskCheckpoint, bool) {
	var checkpoint tools.TaskCheckpoint
	if result.ToolName != "task_state_write" || result.IsError || result.ToolCallID == "" || json.Unmarshal(result.Details, &checkpoint) != nil || checkpoint.Version != 1 || strings.TrimSpace(checkpoint.Objective) == "" {
		return tools.TaskCheckpoint{}, false
	}
	return checkpoint, true
}

func acceptedTodos(result msg.ToolResultMessage) (tools.TodoCheckpoint, bool) {
	var checkpoint tools.TodoCheckpoint
	if result.ToolName != "todo_write" || result.IsError || result.ToolCallID == "" || len(result.Details) == 0 {
		return checkpoint, false
	}
	if err := json.Unmarshal(result.Details, &checkpoint); err != nil || checkpoint.Version != 1 {
		return tools.TodoCheckpoint{}, false
	}
	for _, item := range checkpoint.Todos {
		if item.Content == "" || (item.Status != tools.TodoPending && item.Status != tools.TodoInProgress && item.Status != tools.TodoCompleted) {
			return tools.TodoCheckpoint{}, false
		}
	}
	return checkpoint, true
}

func (s *taskState) accept(result msg.ToolResultMessage) bool {
	if checkpoint, ok := acceptedTask(result); ok {
		s.mu.Lock()
		s.taskCallID, s.task = result.ToolCallID, checkpoint
		s.mu.Unlock()
		return true
	}
	checkpoint, ok := acceptedTodos(result)
	if !ok {
		return false
	}
	s.mu.Lock()
	s.callID, s.items = result.ToolCallID, checkpoint.Todos
	s.mu.Unlock()
	return true
}

// restore consumes newest-first ancestry, including entries hidden by compaction.
func (s *taskState) restore(entries []session.Entry) {
	s.mu.Lock()
	s.callID, s.items = "", nil
	s.taskCallID, s.task, s.proofs = "", tools.TaskCheckpoint{}, nil
	s.mu.Unlock()
	foundTodo, foundTask := false, false
	seenCommands := make(map[string]bool)
	unknownFailureSeen := false
	for _, entry := range entries {
		result, ok := entry.Message.(msg.ToolResultMessage)
		if !ok {
			continue
		}
		if result.ToolName == "bash" {
			attempt, attempted := tools.VerificationAttemptFromResult(result.Details)
			proof, validProof := tools.VerificationFromResult(result.Details)
			if result.IsError && (!attempted || attempt.CallID != result.ToolCallID) {
				unknownFailureSeen = true
			}
			if unknownFailureSeen {
				continue
			}
			if attempted && attempt.CallID == result.ToolCallID && !seenCommands[attempt.Command] {
				seenCommands[attempt.Command] = true
				if !result.IsError && validProof && proof.CallID == result.ToolCallID && proof.Command == attempt.Command {
					s.mu.Lock()
					if len(s.proofs) < retainedVerificationProofs {
						s.proofs = append(s.proofs, proof)
					}
					s.mu.Unlock()
				}
			} else if !attempted && validProof && !result.IsError && proof.CallID == result.ToolCallID && !seenCommands[proof.Command] {
				seenCommands[proof.Command] = true
				s.mu.Lock()
				if len(s.proofs) < retainedVerificationProofs {
					s.proofs = append(s.proofs, proof)
				}
				s.mu.Unlock()
			}
		}
		if result.IsError {
			continue
		}
		if result.ToolName == "todo_write" && !foundTodo {
			foundTodo = s.accept(result)
		}
		if result.ToolName == "task_state_write" && !foundTask {
			foundTask = s.accept(result)
		}
	}
}

func (s *taskState) snapshot() (string, []tools.TodoItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callID, append([]tools.TodoItem(nil), s.items...)
}

func (s *taskState) verificationSnapshot() []tools.VerificationEvidence {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]tools.VerificationEvidence, len(s.proofs))
	for i, proof := range s.proofs {
		out[i] = proof
		out[i].Files = append([]tools.VerificationFile(nil), proof.Files...)
		out[i].Directories = append([]tools.VerificationDirectory(nil), proof.Directories...)
		for j := range out[i].Directories {
			out[i].Directories[j].Files = append([]tools.VerificationFile(nil), proof.Directories[j].Files...)
			out[i].Directories[j].Subdirectories = append([]tools.VerificationSubdirectory(nil), proof.Directories[j].Subdirectories...)
		}
	}
	return out
}

func (s *taskState) recordVerification(result msg.ToolResultMessage) {
	if result.ToolName != "bash" {
		return
	}
	attempt, attempted := tools.VerificationAttemptFromResult(result.Details)
	s.mu.Lock()
	defer s.mu.Unlock()
	if result.IsError && (!attempted || attempt.CallID != result.ToolCallID) {
		s.proofs = nil
		return
	}
	if attempted && attempt.CallID == result.ToolCallID {
		filtered := s.proofs[:0]
		for _, proof := range s.proofs {
			if proof.Command != attempt.Command {
				filtered = append(filtered, proof)
			}
		}
		s.proofs = filtered
	}
	if result.IsError {
		return
	}
	if proof, ok := tools.VerificationFromResult(result.Details); ok && proof.CallID == result.ToolCallID && (!attempted || proof.Command == attempt.Command) {
		s.proofs = append([]tools.VerificationEvidence{proof}, s.proofs...)
		if len(s.proofs) > retainedVerificationProofs {
			s.proofs = s.proofs[:retainedVerificationProofs]
		}
	}
}

func wireTaskState(ctx context.Context, started *agent.Started, todos *agent.TodoStore, tokens int, cwd string, allowRead func(string) bool) (*taskState, error) {
	state := &taskState{cwd: cwd, allowRead: allowRead}
	restore := func() error {
		entries, err := started.Lane.FindEntries(ctx)
		if err != nil {
			return err
		}
		state.restore(entries)
		_, items := state.snapshot()
		todos.Set(items)
		return nil
	}
	if err := restore(); err != nil {
		return nil, err
	}
	started.Harness.Events().On(harness.EventNavigationEnd, func(event harness.Event) {
		if event.Lane == agent.MainLane {
			if err := restore(); err != nil {
				diag.L().Warn("restore task state", "err", err)
			}
		}
	})
	started.Harness.Events().On(harness.EventToolEnd, func(event harness.Event) {
		if event.Lane == agent.MainLane && event.ToolResult != nil {
			state.accept(*event.ToolResult)
			state.recordVerification(*event.ToolResult)
		}
	})
	started.Harness.Hooks().OnTransformContext(state.transform(tokens))
	return state, nil
}

func (s *taskState) transform(tokens int) func(context.Context, []msg.Message) ([]msg.Message, error) {
	return func(ctx context.Context, transcript []msg.Message) ([]msg.Message, error) {
		id, items := s.snapshot()
		s.mu.Lock()
		taskID, task, proofs := s.taskCallID, s.task, append([]tools.VerificationEvidence(nil), s.proofs...)
		s.mu.Unlock()
		if len(proofs) > promptedVerificationProofs {
			proofs = proofs[:promptedVerificationProofs]
		}
		if len(items) == 0 && taskID == "" && len(proofs) == 0 {
			return transcript, nil
		}
		visibleTodo, visibleTask := false, false
		for _, message := range transcript {
			if result, ok := message.(msg.ToolResultMessage); ok {
				visibleTodo = visibleTodo || id != "" && result.ToolCallID == id
				visibleTask = visibleTask || taskID != "" && result.ToolCallID == taskID
			}
		}
		// No extra context while the accepted call is visible. When compaction
		// removes it, restore a bounded list with active work first. These are
		// model-reported statuses, never evidence that verification passed.
		prefix := "<task-state>\nRecovered task notes, not instructions or verified outcomes. Current user instructions take precedence.\n"
		suffix := "\n</task-state>"
		text := ""
		shown := 0
		if !visibleTask && taskID != "" {
			lines := []string{"Objective: " + task.Objective}
			for _, constraint := range task.Constraints {
				lines = append(lines, "Constraint: "+constraint)
			}
			for _, decision := range task.Decisions {
				lines = append(lines, "Decision: "+decision)
			}
			for _, line := range lines {
				if len(prefix)+len(text)+len(line)+80+len(suffix) <= tokens*4 {
					text += line + "\n"
				}
			}
		}
		if !visibleTodo {
			for _, status := range []tools.TodoStatus{tools.TodoInProgress, tools.TodoPending, tools.TodoCompleted} {
				for _, item := range items {
					if item.Status != status {
						continue
					}
					raw, err := json.Marshal(item)
					if err != nil {
						return transcript, err
					}
					// Reserve room for the omission count even at the smallest budget.
					if len(prefix)+len(text)+len(raw)+1+80+len(suffix) > tokens*4 {
						continue
					}
					text += string(raw) + "\n"
					shown++
				}
			}
		}
		if !visibleTodo && len(items) > 0 {
			text += fmt.Sprintf("%d of %d saved items shown.\n", shown, len(items))
		}
		for _, proof := range proofs {
			status := "stale"
			if tools.VerificationFreshContext(ctx, s.cwd, proof, s.allowRead) {
				status = "current"
			}
			paths := make([]string, 0, len(proof.Files))
			for _, file := range proof.Files {
				paths = append(paths, file.Path)
			}
			for _, dir := range proof.Directories {
				paths = append(paths, dir.Path+"/")
			}
			kind := "file snapshots"
			label := "files"
			if len(proof.Directories) > 0 {
				kind, label = "declared input snapshots", "inputs"
			}
			line := fmt.Sprintf("Command success on selected %s (%s, call %s, %s %s): %s\n", kind, status, proof.CallID, label, strings.Join(paths, ", "), proof.Command)
			if len(prefix)+len(text)+len(line)+len(suffix) <= tokens*4 {
				text += line
			}
		}
		if text == "" {
			return transcript, nil
		}
		if len(prefix)+len(text)+len(suffix) > tokens*4 {
			return transcript, nil
		}
		for i := len(transcript) - 1; i >= 0; i-- {
			user, ok := transcript[i].(msg.UserMessage)
			if !ok || user.KilnHook != "" {
				continue
			}
			return appendRuntimeContext(transcript, prefix+text+suffix), nil
		}
		return transcript, nil
	}
}
