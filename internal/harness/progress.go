package harness

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/andrepato/harness/internal/msg"
)

// progressGuard bounds consecutive identical failed tool calls within one
// operation. Successful calls and changed arguments or results reset it.
// It stores only one fingerprint, regardless of operation length.
type progressGuard struct {
	limit int
	last  [sha256.Size]byte
	count int
}

func (g *progressGuard) observe(call msg.ToolCall, result msg.ToolResultMessage) error {
	if g.limit < 2 || !result.IsError {
		g.count = 0
		return nil
	}
	fingerprint, err := failedToolFingerprint(call, result)
	if err != nil {
		g.count = 0
		return nil
	}
	return g.observeFingerprint(call.Name, fingerprint)
}

func (g *progressGuard) observeFingerprint(name string, fingerprint [sha256.Size]byte) error {
	if g.limit < 2 {
		return nil
	}
	if fingerprint == g.last {
		g.count++
	} else {
		g.last = fingerprint
		g.count = 1
	}
	if g.count >= g.limit {
		return fmt.Errorf("the agent repeated the same failed %s call %d times without a changed result; stop and revise the request or tool arguments", name, g.count)
	}
	return nil
}

// cycleGuard retains at most 40 digests: four calls per cycle across the
// largest supported ten-cycle limit. No arguments or output text is kept.
type cycleGuard struct {
	limit int
	ring  [40][sha256.Size]byte
	next  int
	count int
}

func (g *cycleGuard) reset() {
	g.next = 0
	g.count = 0
}

func (g *cycleGuard) observeFingerprint(fingerprint [sha256.Size]byte) error {
	if g.limit < 2 {
		return nil
	}
	g.ring[g.next] = fingerprint
	g.next = (g.next + 1) % len(g.ring)
	if g.count < len(g.ring) {
		g.count++
	}
	for period := 2; period <= 4; period++ {
		if period*g.limit > g.count || !g.distinctPeriod(period) {
			continue
		}
		matches := true
		for repetition := 1; repetition < g.limit && matches; repetition++ {
			for i := 0; i < period; i++ {
				if g.fromEnd(i) != g.fromEnd(repetition*period+i) {
					matches = false
					break
				}
			}
		}
		if matches {
			return fmt.Errorf("the agent repeated the same failed tool-call cycle of %d calls %d times; stop and revise the request or tool arguments", period, g.limit)
		}
	}
	return nil
}

func (g *cycleGuard) distinctPeriod(period int) bool {
	first := g.fromEnd(0)
	for i := 1; i < period; i++ {
		if g.fromEnd(i) != first {
			return true
		}
	}
	return false
}

func (g *cycleGuard) fromEnd(offset int) [sha256.Size]byte {
	index := (g.next - 1 - offset + len(g.ring)) % len(g.ring)
	return g.ring[index]
}

// progressTracker computes one fingerprint for both enabled guards. It is
// constructed per operation, so a new user prompt starts with fresh state.
type progressTracker struct {
	same  progressGuard
	cycle cycleGuard
}

func (p *progressTracker) enabled() bool {
	return p.same.limit >= 2 || p.cycle.limit >= 2
}

func (p *progressTracker) observe(call msg.ToolCall, result msg.ToolResultMessage) error {
	if !p.enabled() || !result.IsError {
		p.same.count = 0
		p.cycle.reset()
		return nil
	}
	fingerprint, err := failedToolFingerprint(call, result)
	if err != nil {
		p.same.count = 0
		p.cycle.reset()
		return nil
	}
	if err := p.same.observeFingerprint(call.Name, fingerprint); err != nil {
		return err
	}
	return p.cycle.observeFingerprint(fingerprint)
}

func failedToolFingerprint(call msg.ToolCall, result msg.ToolResultMessage) ([sha256.Size]byte, error) {
	// JSON marshalling canonicalizes map key order. Tool call IDs and result
	// timestamps and internal Details are intentionally excluded: per-call
	// bookkeeping (such as verification attempt IDs) does not indicate progress.
	payload, err := json.Marshal(struct {
		Name    string         `json:"name"`
		Args    map[string]any `json:"args"`
		Content msg.Blocks     `json:"content"`
	}{call.Name, call.Arguments, result.Content})
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(payload), nil
}
