package cli

import (
	"bytes"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/agent"
)

// overlapWriter records whether two Write calls were ever in flight at once.
type overlapWriter struct {
	inFlight atomic.Int32
	overlap  atomic.Bool
	mu       sync.Mutex
	buf      bytes.Buffer
}

func (w *overlapWriter) Write(p []byte) (int, error) {
	if w.inFlight.Add(1) > 1 {
		w.overlap.Store(true)
	}
	defer w.inFlight.Add(-1)
	time.Sleep(time.Millisecond)
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

// Concurrent workers call Dispatcher.OnEvent from their own goroutines; the
// stderr sink must not write to the shared writer concurrently (CI race in
// TestRunWorkerBatchAutomaticIntegration, run 43220371085).
func TestSubagentEventSinkSerialisesConcurrentWorkers(t *testing.T) {
	w := &overlapWriter{}
	sink := subagentEventSink(w)
	const workers = 8
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sink(agent.SubagentEvent{Kind: agent.SubagentEventStart, Agent: "general-purpose", ModelID: "faux-1"})
			sink(agent.SubagentEvent{Kind: agent.SubagentEventDone, Agent: "general-purpose"})
		}()
	}
	wg.Wait()
	if w.overlap.Load() {
		t.Fatal("subagent event sink wrote to stderr from two goroutines at once")
	}
	if got := strings.Count(w.buf.String(), "\n"); got != 2*workers {
		t.Fatalf("stderr lines = %d, want %d:\n%s", got, 2*workers, w.buf.String())
	}
}
