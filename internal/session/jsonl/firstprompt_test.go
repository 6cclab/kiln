package jsonl

import (
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

func TestFirstPromptReadsTheFirstUserMessage(t *testing.T) {
	repo, err := NewRepo(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	storage, meta, err := repo.Create(CreateOptions{Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if got := FirstPrompt(meta.Path); got != "" {
		t.Errorf("FirstPrompt on an empty session = %q, want empty", got)
	}
	writes := []session.Write{
		session.EntryWrite{Entry: session.Entry{ID: "a", Type: session.EntryMessage, Message: msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("fix the\n  pagination panic")}}}},
		session.EntryWrite{Entry: session.Entry{ID: "b", Type: session.EntryMessage, Message: msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("second")}}}},
	}
	if _, err := storage.Commit(writes); err != nil {
		t.Fatal(err)
	}
	if got := FirstPrompt(meta.Path); got != "fix the pagination panic" {
		t.Errorf("FirstPrompt = %q, want %q", got, "fix the pagination panic")
	}
}
