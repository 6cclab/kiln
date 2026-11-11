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

func TestRequestBudgetUSDExactConservativeBound(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rates ModelCostRates
		tiers []ModelCostTier
		limit float64
	}{
		{"uncached input", ModelCostRates{Input: 17, Output: 30, CacheRead: 2, CacheWrite: 4}, nil, 20},
		{"cache read", ModelCostRates{Input: 5, Output: 30, CacheRead: 17, CacheWrite: 4}, nil, 20},
		{"long cache write", ModelCostRates{Input: 5, Output: 30, CacheRead: 2, CacheWrite: 8}, nil, 19},
		{"highest tier rates", ModelCostRates{Input: 5, Output: 25, CacheRead: 1, CacheWrite: 4}, []ModelCostTier{
			{ModelCostRates: ModelCostRates{Input: 7, Output: 30, CacheRead: 2, CacheWrite: 8}, InputTokensAbove: 100_000},
			{ModelCostRates: ModelCostRates{Input: 9, Output: 26, CacheRead: 3, CacheWrite: 6}, InputTokensAbove: 500_000},
		}, 19},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := Model{Provider: "test", ID: "priced", ContextWindow: 1_000_000, MaxTokens: 100_000,
				Cost: ModelCost{ModelCostRates: tc.rates, Tiers: tc.tiers}}
			b := &RequestBudget{MaxUSD: tc.limit}
			if _, err := b.reserve(model, StreamOptions{}); err != nil {
				t.Fatalf("exact conservative bound rejected: %v", err)
			}
			if got := b.Snapshot().USD; got != tc.limit {
				t.Fatalf("reservation = $%g, want $%g", got, tc.limit)
			}
			if _, err := (&RequestBudget{MaxUSD: tc.limit - 0.01}).reserve(model, StreamOptions{}); err == nil {
				t.Fatal("budget below conservative bound admitted")
			}
		})
	}
}

func TestRequestBudgetConcurrentUSDReservationsRetainUnknownUsage(t *testing.T) {
	model := Model{Provider: "test", ID: "priced", ContextWindow: 1_000_000, MaxTokens: 100_000,
		Cost: ModelCost{ModelCostRates: ModelCostRates{Input: 5, Output: 30, CacheWrite: 8}}}
	b := &RequestBudget{MaxUSD: 19}
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			finish, err := b.reserve(model, StreamOptions{})
			if err != nil {
				return
			}
			admitted.Add(1)
			finish(nil, context.Canceled)
		}()
	}
	wg.Wait()
	if admitted.Load() != 1 || b.Snapshot().Calls != 1 || b.Snapshot().USD != 19 {
		t.Fatalf("admitted %d, ledger %+v; want one retained $19 reservation", admitted.Load(), b.Snapshot())
	}
}
