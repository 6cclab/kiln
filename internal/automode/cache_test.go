package automode

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	fauxprovider "github.com/andrepato/harness/internal/provider/faux"
	"github.com/andrepato/harness/internal/testkit/faux"
	"github.com/andrepato/harness/internal/testkit/fauxtest"
)

// promptCache models Anthropic's prompt cache closely enough to measure a
// request layout: a request's blocks are a prefix sequence; each block
// with cache_control writes a cache entry for the prefix ending there; a
// breakpoint reads the longest cached prefix that ends at it or at one of
// the 20 block boundaries before it. Tokens are chars/4 (an estimate, not
// the API's count).
type promptCache struct{ entries map[[32]byte]bool }

type cachedBlock struct {
	text string
	brk  bool
}

func (c *promptCache) send(blocks []cachedBlock) (read, write int) {
	hashes := make([][32]byte, len(blocks))
	tokens := make([]int, len(blocks)+1) // tokens[i] = size of blocks[:i]
	h := sha256.New()
	for i, b := range blocks {
		h.Write([]byte(b.text))
		h.Write([]byte{0})
		copy(hashes[i][:], h.Sum(nil))
		tokens[i+1] = tokens[i] + (len(b.text)+3)/4
	}
	covered := 0 // blocks already read or written
	for i, b := range blocks {
		if !b.brk {
			continue
		}
		hit := -1
		for j := i; j >= 0 && j >= i-20; j-- {
			if c.entries[hashes[j]] {
				hit = j
				break
			}
		}
		if hit+1 > covered {
			read += tokens[hit+1] - tokens[covered]
			covered = hit + 1
		}
		if i+1 > covered {
			write += tokens[i+1] - tokens[covered]
			covered = i + 1
		}
		c.entries[hashes[i]] = true
	}
	return read, write
}

// blocksOf turns a recorded Anthropic request into the cache's view: the
// system prompt (one block, always a breakpoint) and every message block
// in order.
func blocksOf(t *testing.T, r faux.Request) []cachedBlock {
	t.Helper()
	out := []cachedBlock{{text: r.System, brk: true}}
	var messages []struct {
		Content []struct {
			Text         string          `json:"text"`
			CacheControl json.RawMessage `json:"cache_control"`
		} `json:"content"`
	}
	if err := json.Unmarshal(r.Messages, &messages); err != nil {
		t.Fatalf("messages: %v", err)
	}
	for _, m := range messages {
		for _, b := range m.Content {
			out = append(out, cachedBlock{text: b.Text, brk: len(b.CacheControl) > 0})
		}
	}
	return out
}

// Successive classifier calls in a growing session read the transcript
// they share from the prompt cache and write only what is new. Before,
// the whole user message was one block cached only at its end, after the
// action: every call wrote the full transcript again and read only the
// system prompt (cache_read fixed at 2414 while cache_write grew).
func TestClassifierCallsReuseTheCachedTranscript(t *testing.T) {
	const calls = 8
	var script strings.Builder
	script.WriteString("model: faux-1\nsteps:\n")
	for i := 0; i < calls; i++ {
		script.WriteString("  - text: '{\"decision\":\"allow\"}'\n    end_turn: true\n")
	}
	addr, srv := fauxtest.Start(t, script.String())
	t.Setenv("HARNESS_FAUX_ADDR", addr)
	t.Setenv("HARNESS_FAUX_API", "anthropic-messages")
	fp, ok := fauxprovider.New()
	if !ok {
		t.Fatal("faux provider not available")
	}
	model := fp.Models()[0]
	c := &Classifier{
		Resolve: func() (Streamer, provider.Model, error) { return fp, model, nil },
		Memory:  strings.Repeat("Project rule: keep the build green and test every change. ", 80),
	}

	var history []msg.Message
	for i := 0; i < calls; i++ {
		// Each step adds a typed prompt and a sizeable tool call.
		history = append(history,
			msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("step")}, KilnTyped: fmt.Sprintf("do step %d of the migration", i)},
			msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{msg.ToolCall{Type: "toolCall", ID: fmt.Sprintf("c%d", i), Name: "bash",
				Arguments: map[string]any{"command": fmt.Sprintf("go run ./migrate --step %d %s", i, strings.Repeat("--flag ", 150))}}}})
		req := bash(fmt.Sprintf("make check-%d", i))
		req.History = history
		if _, err := c.Classify(context.Background(), req); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	reqs := srv.Requests()
	if len(reqs) != calls {
		t.Fatalf("%d requests, want %d", len(reqs), calls)
	}
	cache := &promptCache{entries: map[[32]byte]bool{}}
	prevRead := 0
	for i, r := range reqs {
		blocks := blocksOf(t, r)
		read, write := cache.send(blocks)
		total := 0
		for _, b := range blocks {
			total += (len(b.text) + 3) / 4
		}
		t.Logf("call %d: %d tokens, cache read %d, write %d", i+1, total, read, write)
		if i == 0 {
			continue
		}
		if read <= prevRead {
			t.Errorf("call %d read %d from the cache, no more than call %d's %d: the shared transcript is not reused", i+1, read, i, prevRead)
		}
		if write*3 > total {
			t.Errorf("call %d wrote %d of its %d tokens to the cache: more than the new lines and the action", i+1, write, total)
		}
		prevRead = read
	}
}
