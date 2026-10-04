package ollama

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// Generation requests have no whole-request deadline: http.Client.Timeout
// covers reading the body, so any value there cuts off a slow local model
// mid-answer (a 3-minute one killed compactions on qwen at exactly 180s).
// Discovery calls keep their per-call bound.
func TestStreamClientHasNoTotalDeadline(t *testing.T) {
	for name, opts := range map[string]Options{
		"default":          {},
		"custom transport": {HTTPClient: &http.Client{Transport: http.DefaultTransport}},
	} {
		p := New(opts)
		if got := p.client.HTTPClient.Timeout; got != 0 {
			t.Errorf("%s: stream client Timeout = %s, want 0 (no total deadline)", name, got)
		}
		if got := opts.client().Timeout; got != discoveryTimeout {
			t.Errorf("%s: discovery client Timeout = %s, want %s", name, got, discoveryTimeout)
		}
	}
}

// A slow server, whose headers come only after it has read the prompt and
// whose tokens trickle in, streams to the end through the default client.
func TestStreamSlowServerCompletes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond) // reading the prompt
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		for i := 0; i < 5; i++ {
			fmt.Fprintf(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"t%d \"}}]}\n\n", i)
			fl.Flush()
			time.Sleep(100 * time.Millisecond)
		}
		fmt.Fprint(w, "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		fl.Flush()
	}))
	defer srv.Close()

	p := New(Options{URL: srv.URL})
	model := toModel("slow:latest", srv.URL+"/v1", 8192, []string{"completion", "tools"})
	user := msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}
	events, wait := p.Stream(context.Background(), model, []msg.Message{user}, provider.StreamOptions{})
	for range events {
	}
	final, err := wait()
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	text := ""
	for _, b := range final.Content {
		if tc, ok := b.(msg.TextContent); ok {
			text += tc.Text
		}
	}
	if text != "t0 t1 t2 t3 t4 " {
		t.Fatalf("text = %q", text)
	}
}
