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
	for _, tc := range []struct {
		name   string
		apply  transform
		change func()
	}{
		{"memory", memoryContext(store, "project", func() bool { return true }), func() { putMemory("database after") }},
		{"capabilities", capabilitySuggestionContext(catalog, settings.SkillDiscovery{}, func() int { return 256 }), func() { description = "database after" }},
		{"skills", skillDiscoveryContext(records, settings.SkillDiscovery{}, func() int { return 256 }), func() { records[0].Description = "database after" }},
		{"task state", state.transform(256), func() {
			state.accept(savedTodos(t, "second", tools.TodoItem{Content: "database after", Status: tools.TodoInProgress}))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := []msg.Message{
				msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("database")}},
				msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{msg.NewToolCall("read-1", "read", map[string]any{"path": "schema.sql"})}},
				msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: "read-1", ToolName: "read", Content: msg.Blocks{msg.Text("existing schema")}},
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
