package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/memory"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tools"
)

// Request transforms must preserve the replayed conversation, not just avoid
// mutating the stored slice. Changing an old user message also changes every
// subsequent request prefix, even when the persisted history is untouched.
func TestRuntimeContextPreservesRequestPrefix(t *testing.T) {
	type transform = func(context.Context, []msg.Message) ([]msg.Message, error)
	store, err := memory.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx := context.Background()
	putMemory := func(text string) {
		t.Helper()
		if _, err := store.Put(ctx, memory.Record{Scope: "project", Key: "database", Kind: "fact", Text: text, Evidence: "user"}); err != nil {
			t.Fatal(err)
		}
	}
	putMemory("database before")
	state := &taskState{}
	state.accept(savedTodos(t, "first", tools.TodoItem{Content: "database before", Status: tools.TodoPending}))
	description := "database before"
	catalog := func() tools.CapabilityCatalog {
		return tools.CapabilityCatalog{Skills: []tools.SkillRecord{{Name: "database", Description: description}}}
	}
	records := []tools.SkillRecord{{Name: "database", Description: description}}
	// Suggestions go only into the request answering the user
	// (answeringUser), so their input ends at the user's message; the rest
	// refresh on every step of a tool loop.
	for _, tc := range []struct {
		name       string
		apply      transform
		change     func()
		answerOnly bool
	}{
		{"memory", memoryContext(store, "project", func() bool { return true }), func() { putMemory("database after") }, false},
		{"capabilities", capabilitySuggestionContext(catalog, settings.SkillDiscovery{}, func() int { return 256 }), func() { description = "database after" }, true},
		{"skills", skillDiscoveryContext(records, settings.SkillDiscovery{}, func() int { return 256 }), func() { records[0].Description = "database after" }, true},
		{"task state", state.transform(256), func() {
			state.accept(savedTodos(t, "second", tools.TodoItem{Content: "database after", Status: tools.TodoInProgress}))
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := []msg.Message{
				msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("database")}},
				msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{msg.NewToolCall("read-1", "read", map[string]any{"path": "schema.sql"})}},
				msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: "read-1", ToolName: "read", Content: msg.Blocks{msg.Text("existing schema")}},
			}
			if tc.answerOnly {
				input = input[:1]
			}
			encode := func(v any) string {
				t.Helper()
				raw, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				return string(raw)
			}
			before := encode(input)
			var previous string
			for iteration := 0; iteration < 2; iteration++ {
				out, err := tc.apply(ctx, input)
				if err != nil {
					t.Fatal(err)
				}
				if len(out) != len(input)+1 || encode(out[:len(input)]) != before {
					t.Fatalf("runtime context rewrote the replayed conversation instead of appending: %s", encode(out))
				}
				if encode(input) != before {
					t.Fatal("stored history mutated")
				}
				latest := out[len(input)].(msg.UserMessage)
				text := msg.TextOf(latest.Content)
				if latest.KilnHook == "" || latest.KilnTyped != "" || !strings.Contains(text, "database") {
					t.Fatalf("runtime context is not marked as synthetic: %+v", latest)
				}
				if iteration == 1 && text == previous {
					t.Fatal("context stopped refreshing between requests")
				}
				previous = text
				tc.change()
			}
		})
	}
}

func TestRuntimeContextDoesNotBecomeDiscoveryQuery(t *testing.T) {
	input := []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("database")}}}
	input = appendRuntimeContext(input, "unrelated weather forecast memory")
	weather := []tools.SkillRecord{{Name: "weather", Description: "weather forecast"}}
	for _, tc := range []struct {
		name  string
		apply func(context.Context, []msg.Message) ([]msg.Message, error)
	}{
		{"skills", skillDiscoveryContext(weather, settings.SkillDiscovery{}, func() int { return 256 })},
		{"capabilities", capabilitySuggestionContext(func() tools.CapabilityCatalog {
			return tools.CapabilityCatalog{Skills: weather}
		}, settings.SkillDiscovery{}, func() int { return 256 })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.apply(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(input) {
				t.Fatal("derived memory was searched as if it were the user's request")
			}
		})
	}
}

// Skill and capability suggestions go into the request answering the user,
// not into every step of the tool loop that follows: appended after each
// tool result, they were the newest thing the model saw, so it answered them
// on every step ("these suggestions only matched on 'ok, go'").
func TestSuggestionsOnlyAnswerTheUser(t *testing.T) {
	records := []tools.SkillRecord{{Name: "database", Description: "database work"}}
	catalog := func() tools.CapabilityCatalog { return tools.CapabilityCatalog{Skills: records} }
	user := msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("database")}}
	call := msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{msg.NewToolCall("read-1", "read", map[string]any{"path": "schema.sql"})}}
	result := msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: "read-1", ToolName: "read", Content: msg.Blocks{msg.Text("existing schema")}}
	memory := msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("<memory>…</memory>")}, KilnHook: "RuntimeContext"}
	steer := msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("database again")}}
	for _, tc := range []struct {
		name  string
		apply func(context.Context, []msg.Message) ([]msg.Message, error)
	}{
		{"skills", skillDiscoveryContext(records, settings.SkillDiscovery{}, func() int { return 256 })},
		{"capabilities", capabilitySuggestionContext(catalog, settings.SkillDiscovery{}, func() int { return 256 })},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, step := range []struct {
				name  string
				input []msg.Message
				want  bool
			}{
				{"answering the user", []msg.Message{user}, true},
				{"after derived memory", []msg.Message{user, memory}, true},
				{"a tool step", []msg.Message{user, call, result}, false},
				{"a steered message", []msg.Message{user, call, result, steer}, true},
			} {
				got, err := tc.apply(context.Background(), step.input)
				if err != nil {
					t.Fatal(err)
				}
				if added := len(got) > len(step.input); added != step.want {
					t.Errorf("%s: suggestions added = %v, want %v", step.name, added, step.want)
				}
			}
		})
	}
}
