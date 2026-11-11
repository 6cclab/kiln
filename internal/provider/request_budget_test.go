package provider

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/andrepato/harness/internal/msg"
)

type budgetTestStreamer struct{ calls atomic.Int32 }

func (p *budgetTestStreamer) Stream(context.Context, Model, []msg.Message, StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	p.calls.Add(1)
	ch := make(chan msg.StreamEvent)
	close(ch)
	return ch, func() (*msg.AssistantMessage, error) {
		return &msg.AssistantMessage{Usage: msg.Usage{Input: 10, Output: 5, Cost: msg.Cost{Total: 0.00002}}}, nil
	}
}

func TestRequestBudgetConcurrentReservations(t *testing.T) {
	b := &RequestBudget{MaxCalls: 2}
	ctx := WithRequestBudget(context.Background(), b)
	p := &budgetTestStreamer{}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ch, wait := Stream(ctx, p, Model{ContextWindow: 100, MaxTokens: 20}, nil, StreamOptions{})
			for range ch {
			}
			_, _ = wait()
		}()
	}
	wg.Wait()
	if p.calls.Load() != 2 || b.Snapshot().Calls != 2 {
		t.Fatalf("provider %d ledger %+v", p.calls.Load(), b.Snapshot())
	}
	if b.Snapshot().Tokens != 30 {
		t.Fatal(b.Snapshot())
	}
}

func TestRequestBudgetRefusesUnknownPricingAndRetainsReservations(t *testing.T) {
	model := Model{Provider: "test", ID: "model", ContextWindow: 100, MaxTokens: 20}
	b := &RequestBudget{MaxUSD: 1}
	if _, err := b.reserve(model, StreamOptions{}); err == nil {
		t.Fatal("unknown price treated as free")
	}
	b = &RequestBudget{MaxTokens: 120}
	finish, err := b.reserve(model, StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.reserve(model, StreamOptions{}); err == nil {
		t.Fatal("overcommitted concurrent reservation")
	}
	finish(nil, context.Canceled)
	if b.Snapshot().Tokens != 120 {
		t.Fatal("unknown usage refunded")
	}
}

func TestRequestBudgetSettlementOnce(t *testing.T) {
	b := &RequestBudget{MaxTokens: 1000}
	finish, err := b.reserve(Model{ContextWindow: 100, MaxTokens: 20}, StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	m := &msg.AssistantMessage{Usage: msg.Usage{Input: 10, Output: 5}}
	finish(m, nil)
	finish(m, nil)
	if b.Snapshot().Tokens != 15 {
		t.Fatal(b.Snapshot())
	}
}
