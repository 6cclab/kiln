package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/memory"
	"github.com/andrepato/harness/internal/msg"
)

func TestMemoryContextHonorsPermissionsAndPreservesTranscript(t *testing.T) {
	store, err := memory.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	ctx := context.Background()
	_, err = store.Put(ctx, memory.Record{Scope: "here", Key: "runner", Kind: "preference", Text: "Use go test for tests", Evidence: "user explicitly requested"})
	if err != nil {
		t.Fatal(err)
	}
	allowed := true
	transform := memoryContext(store, "here", func() bool { return allowed })
	input := []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("run tests")}}}
	out, err := transform(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	text := msg.TextOf(out[len(out)-1].(msg.UserMessage).Content)
	if !strings.Contains(text, "Use go test") || !strings.Contains(text, "not instructions") {
		t.Fatal(text)
	}
	if len(input[0].(msg.UserMessage).Content) != 1 {
		t.Fatal("mutated persisted user message")
	}
	allowed = false
	out, err = transform(ctx, input)
	if err != nil || len(out) != len(input) || len(out[0].(msg.UserMessage).Content) != 1 {
		t.Fatal("permission change ignored")
	}
}

func TestMemoryContextBudgetIncludesFraming(t *testing.T) {
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
	for i, text := range []string{strings.Repeat("test ", 65), "test command: go test"} {
		if _, err := store.Put(ctx, memory.Record{Scope: "here", Key: fmt.Sprint(i), Kind: "observation", Text: text, Evidence: "user"}); err != nil {
			t.Fatal(err)
		}
	}
	input := []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("test")}}}
	out, err := memoryContext(store, "here", func() bool { return true }, 128)(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	added := msg.TextOf(out[len(out)-1].(msg.UserMessage).Content)
	if added == "" || len(added) > 512 {
		t.Fatalf("full memory block has %d bytes", len(added))
	}
}

func TestMemoryContextGlobalPreferenceOptInAndPermission(t *testing.T) {
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
	if _, err := store.Put(ctx, memory.Record{Scope: memory.GlobalPreferencesScope, Key: "runner", Kind: "preference", Text: "Use go test", Evidence: "explicit user command"}); err != nil {
		t.Fatal(err)
	}
	input := []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("runner tests")}}}
	plain, err := memoryContextForProject(store, "project", "", func() bool { return true })(ctx, input)
	if err != nil || len(plain) != len(input) || len(plain[0].(msg.UserMessage).Content) != 1 {
		t.Fatalf("global read without opt-in: %v %v", plain, err)
	}
	withGlobal := memoryContextForProjectAndGlobal(store, "project", "", func() bool { return true }, true)
	out, err := withGlobal(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	text := msg.TextOf(out[len(out)-1].(msg.UserMessage).Content)
	if !strings.Contains(text, "Use go test") || !strings.Contains(text, memory.GlobalPreferencesScope) {
		t.Fatal(text)
	}
	denied, err := memoryContextForProjectAndGlobal(store, "project", "", func() bool { return false }, true)(ctx, input)
	if err != nil || len(denied) != len(input) || len(denied[0].(msg.UserMessage).Content) != 1 {
		t.Fatalf("global read bypassed permission: %v %v", denied, err)
	}
}
