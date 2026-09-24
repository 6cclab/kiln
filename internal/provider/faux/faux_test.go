package faux

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	tkfaux "github.com/andrepato/harness/internal/testkit/faux"
)

func TestNewUnsetEnv(t *testing.T) {
	os.Unsetenv("HARNESS_FAUX_ADDR")
	if _, ok := New(); ok {
		t.Fatal("expected New() to report false when HARNESS_FAUX_ADDR is unset")
	}
}

func TestNewAndStreamAnthropic(t *testing.T) {
	s, err := tkfaux.New(tkfaux.Options{ScriptYAML: `
model: faux-1
steps:
  - text: "hello from faux"
`})
	if err != nil {
		t.Fatal(err)
	}
	addr, err := s.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	t.Setenv("HARNESS_FAUX_ADDR", addr)
	t.Setenv("HARNESS_FAUX_API", "anthropic-messages")

	p, ok := New()
	if !ok {
		t.Fatal("expected New() to report true when HARNESS_FAUX_ADDR is set")
	}
	if p.ID() != "faux" {
		t.Fatalf("ID() = %q", p.ID())
	}
	models := p.Models()
	if len(models) != 1 || models[0].ID != "faux-1" {
		t.Fatalf("Models() = %+v", models)
	}
	if models[0].Api != provider.ApiAnthropicMessages {
		t.Fatalf("Api = %q", models[0].Api)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := p.Stream(ctx, models[0], []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}},
	}, provider.StreamOptions{})
	for range events {
	}
	final, err := wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if msg.TextOf(final.Content) != "hello from faux" {
		t.Fatalf("text = %q", msg.TextOf(final.Content))
	}
}
