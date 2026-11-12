package faux

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const holdScript = `
model: faux-1
steps:
  - text: "before the hold"
  - hold: gate
  - text: " after the hold"
`

func holdReq(stream bool) map[string]any {
	return map[string]any{"model": "faux-1", "stream": stream,
		"messages": []map[string]any{{"role": "user", "content": "hi"}}}
}

func fauxState(t *testing.T, base string) MultiState {
	t.Helper()
	resp, err := http.Get(base + "/_faux/state")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st MultiState
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

// waitHolds polls /_faux/state until n requests are held at name. The
// deadline is generous: the poll returns as soon as the condition holds.
func waitHolds(t *testing.T, base, name string, n int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for fauxState(t, base).Holds[name] != n {
		if time.Now().After(deadline) {
			t.Fatalf("hold %q never reached %d waiters", name, n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func release(t *testing.T, base, name string) *http.Response {
	t.Helper()
	resp, err := http.Post(base+"/_faux/release?name="+name, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func requestInBackground(ctx context.Context, base string, stream bool) <-chan string {
	out := make(chan string, 1)
	body, _ := json.Marshal(holdReq(stream))
	req, _ := http.NewRequestWithContext(ctx, "POST", base+"/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			out <- "ERR " + err.Error()
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			out <- "ERR " + err.Error()
			return
		}
		out <- string(b)
	}()
	return out
}

func awaitBody(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case b := <-ch:
		return b
	case <-time.After(30 * time.Second):
		t.Fatal("request never finished")
		return ""
	}
}

// finishedReply reports whether a reply (SSE or JSON) carries the text after
// the hold. Streamed text is chunked, so look for the tail of it.
func finishedReply(b string) bool {
	return !strings.HasPrefix(b, "ERR") && (strings.Contains(b, "message_stop") || strings.Contains(b, "after the hold"))
}

func TestHoldBlocksUntilReleased(t *testing.T) {
	for _, stream := range []bool{true, false} {
		_, base := startTestServer(t, holdScript)
		done := requestInBackground(context.Background(), base, stream)
		waitHolds(t, base, "gate", 1)
		select {
		case b := <-done:
			t.Fatalf("stream=%v: request finished while held: %s", stream, b)
		default:
		}
		resp := release(t, base, "gate")
		if resp.StatusCode != 200 {
			t.Fatalf("release status = %d", resp.StatusCode)
		}
		b := awaitBody(t, done)
		if !finishedReply(b) {
			t.Errorf("stream=%v: body after release = %q", stream, b)
		}
		if got := fauxState(t, base).Holds["gate"]; got != 0 {
			t.Errorf("waiters after release = %d", got)
		}
	}
}

func TestHoldCancelledByClient(t *testing.T) {
	_, base := startTestServer(t, holdScript)
	ctx, cancel := context.WithCancel(context.Background())
	done := requestInBackground(ctx, base, true)
	waitHolds(t, base, "gate", 1)
	cancel()
	if b := awaitBody(t, done); !strings.HasPrefix(b, "ERR") {
		t.Errorf("cancelled request returned a body: %q", b)
	}
	waitHolds(t, base, "gate", 0) // the handler left the hold
}

// A release is latched: releasing before the request arrives, or a name no
// script uses, is a 200 and the hold is open when reached.
func TestHoldReleaseBeforeReachedAndUnknownName(t *testing.T) {
	_, base := startTestServer(t, holdScript)
	if resp := release(t, base, "nobody-holds-this"); resp.StatusCode != 200 {
		t.Errorf("unknown name status = %d, want 200", resp.StatusCode)
	}
	release(t, base, "gate")
	b := awaitBody(t, requestInBackground(context.Background(), base, true))
	if !finishedReply(b) {
		t.Errorf("pre-released hold blocked or cut the reply: %q", b)
	}
	resp, err := http.Post(base+"/_faux/release", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("missing name status = %d, want 400", resp.StatusCode)
	}
}

// Reset forgets a latched release, so the hold blocks again.
func TestHoldResetForgetsRelease(t *testing.T) {
	s, base := startTestServer(t, holdScript)
	release(t, base, "gate")
	s.Reset()
	done := requestInBackground(context.Background(), base, true)
	waitHolds(t, base, "gate", 1)
	release(t, base, "gate")
	awaitBody(t, done)
}

func TestHoldMustBeAStepOfItsOwn(t *testing.T) {
	if _, err := New(Options{ScriptYAML: "steps:\n  - text: x\n    hold: g\n"}); err == nil {
		t.Error("text+hold step loaded, want an error")
	}
}
