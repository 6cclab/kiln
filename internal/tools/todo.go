package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/andrepato/harness/internal/tool"
)

// TodoStatus is one todo item's lifecycle state.
type TodoStatus string

const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoCompleted  TodoStatus = "completed"
)

// TodoItem is one row of the todo list the model reports via todo_write.
type TodoItem struct {
	Content string     `json:"content"`
	Status  TodoStatus `json:"status"`
}

var todoParameters = json.RawMessage(`{
	"type": "object",
	"properties": {
		"todos": {
			"type": "array",
			"items": {
				"type": "object",
				"required": ["content", "status"],
				"properties": {
					"content": {"type": "string", "description": "What needs doing."},
					"status": {"type": "string", "enum": ["pending", "in_progress", "completed"]}
				}
			}
		}
	},
	"required": ["todos"]
}`)

const todoDescription = "Track progress on a multi-step task. Replaces the whole list each call. " +
	"Use for work with 3+ distinct steps; skip it for single actions. " +
	"Exactly one item should be in_progress at a time."

type todoArgs struct {
	Todos []TodoItem `json:"todos"`
}

// TodoTool builds the todo_write built-in, mirroring todo.ts's
// createTodoTool: it replaces the whole list on every call and returns a
// count plus the current in-progress item, not the list itself — the
// model just sent it, so echoing it back would double its context cost
// for no new information.
//
// set is called with the filtered, valid items on every call; the
// integrator wires it to an *agent.TodoStore's Set method.
func TodoTool(set func(items []TodoItem)) *tool.Tool {
	return &tool.Tool{
		Name:        "todo_write",
		Label:       "Update todos",
		Description: todoDescription,
		Parameters:  todoParameters,
		Execute: func(ctx context.Context, args json.RawMessage, onUpdate tool.Update, inv tool.Invocation) (tool.Result, error) {
			var parsed todoArgs
			if err := json.Unmarshal(args, &parsed); err != nil {
				return tool.Errorf("todo_write: invalid arguments: %v", err), nil
			}

			var valid []TodoItem
			for _, t := range parsed.Todos {
				if t.Content == "" {
					continue
				}
				switch t.Status {
				case TodoPending, TodoInProgress, TodoCompleted:
				default:
					continue
				}
				valid = append(valid, t)
			}
			if set != nil {
				set(valid)
			}

			done := 0
			var active *TodoItem
			for i, t := range valid {
				if t.Status == TodoCompleted {
					done++
				}
				if t.Status == TodoInProgress && active == nil {
					active = &valid[i]
				}
			}
			text := fmt.Sprintf("%d todos, %d done.", len(valid), done)
			if active != nil {
				text += fmt.Sprintf(" Now: %s", active.Content)
			}
			return tool.Text(text), nil
		},
	}
}
