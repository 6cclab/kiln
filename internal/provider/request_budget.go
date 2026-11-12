package provider

import (
	"context"
	"fmt"
	"math"
	"sync"

	"github.com/andrepato/harness/internal/msg"
)

// RequestBudget is shared by every request descended from a session context.
// Reservations use the model's full context/output limits, not a tokenizer
// estimate. Unknown usage retains its reservation. Limits are immutable after
// construction; counters are protected for concurrent worker calls.
type RequestBudget struct {
	MaxCalls   int
	MaxTokens  int64
	MaxUSD     float64
	FreeModels map[string]bool
	mu         sync.Mutex
	calls      int
	tokens     int64
	usd        float64
}

type requestBudgetKey struct{}

// WithRequestBudget binds the same ledger to main, worker and auxiliary calls.
func WithRequestBudget(ctx context.Context, b *RequestBudget) context.Context {
	return context.WithValue(ctx, requestBudgetKey{}, b)
}

// BudgetSnapshot includes spent usage and outstanding reservations.
type BudgetSnapshot struct {
	Calls  int
	Tokens int64
	USD    float64
}

// Snapshot returns a consistent copy of the ledger's counters.
func (b *RequestBudget) Snapshot() BudgetSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return BudgetSnapshot{b.calls, b.tokens, b.usd}
}

func (b *RequestBudget) reserve(model Model, opts StreamOptions) (func(*msg.AssistantMessage, error), error) {
	output := opts.MaxTokens
	if output <= 0 {
		output = model.MaxTokens
	}
	if (b.MaxTokens > 0 || b.MaxUSD > 0) && (model.ContextWindow <= 0 || output <= 0) {
		return nil, fmt.Errorf("request budget: model limits unavailable for %s/%s", model.Provider, model.ID)
	}
	tokens := int64(model.ContextWindow) + int64(output)
	rates := model.Cost.ModelCostRates
	for _, tier := range model.Cost.Tiers {
		rates.Input = math.Max(rates.Input, tier.Input)
		rates.Output = math.Max(rates.Output, tier.Output)
		rates.CacheRead = math.Max(rates.CacheRead, tier.CacheRead)
		rates.CacheWrite = math.Max(rates.CacheWrite, tier.CacheWrite)
	}
	// Input, cache reads and cache writes partition the context tokens: no
	// token pays all three rates at once. Use the highest rate, including
	// the conservative long-lived cache-write multiplier.
	inputRate := math.Max(rates.Input, math.Max(rates.CacheRead, 2*rates.CacheWrite))
	cost := (float64(model.ContextWindow)*inputRate + float64(output)*rates.Output) / 1e6
	free := b.FreeModels[model.Provider+"/"+model.ID]
	if b.MaxUSD > 0 {
		if cost <= 0 && !free {
			return nil, fmt.Errorf("request budget: unknown pricing for %s/%s; configure freeModels only for known zero-cost models", model.Provider, model.ID)
		}
		for _, t := range opts.Tools {
			if len(t.ServerTool) > 0 {
				return nil, fmt.Errorf("request budget: provider-side tool charges cannot be bounded")
			}
		}
	}
	b.mu.Lock()
	if b.MaxCalls > 0 && b.calls >= b.MaxCalls {
		b.mu.Unlock()
		return nil, fmt.Errorf("request budget: call limit %d reached", b.MaxCalls)
	}
	if b.MaxTokens > 0 && b.tokens+tokens > b.MaxTokens {
		b.mu.Unlock()
		return nil, fmt.Errorf("request budget: insufficient tokens to reserve model context and output")
	}
	if b.MaxUSD > 0 && b.usd+cost > b.MaxUSD {
		b.mu.Unlock()
		return nil, fmt.Errorf("request budget: insufficient USD to reserve model context and output")
	}
	b.calls++
	b.tokens += tokens
	b.usd += cost
	b.mu.Unlock()
	var once sync.Once
	return func(m *msg.AssistantMessage, err error) {
		once.Do(func() {
			// A failed response can have incomplete accounting. Keep its reservation.
			if err != nil || m == nil {
				return
			}
			actual := int64(m.Usage.Input) + int64(m.Usage.Output) + int64(m.Usage.CacheRead) + int64(m.Usage.CacheWrite)
			if actual <= 0 {
				return
			}
			actualCost := m.Usage.Cost.Total
			b.mu.Lock()
			defer b.mu.Unlock()
			b.tokens += actual - tokens
			if actualCost > 0 || free {
				b.usd += actualCost - cost
			}
		})
	}, nil
}

// Streamer is the common model-call boundary, including lightweight classifiers.
type Streamer interface {
	Stream(context.Context, Model, []msg.Message, StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error))
}

// Stream applies a context's budget before issuing a request. Consumers must
// drain the returned events and call wait as required by Provider.Stream.
func Stream(ctx context.Context, p Streamer, model Model, messages []msg.Message, opts StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	b, _ := ctx.Value(requestBudgetKey{}).(*RequestBudget)
	if b == nil {
		return p.Stream(ctx, model, messages, opts)
	}
	finish, err := b.reserve(model, opts)
	if err != nil {
		ch := make(chan msg.StreamEvent)
		close(ch)
		return ch, func() (*msg.AssistantMessage, error) { return nil, err }
	}
	if opts.MaxTokens <= 0 && model.MaxTokens > 0 {
		opts.MaxTokens = model.MaxTokens
	}
	ch, wait := p.Stream(ctx, model, messages, opts)
	return ch, func() (*msg.AssistantMessage, error) { m, err := wait(); finish(m, err); return m, err }
}
