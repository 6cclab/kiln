package cli

import (
	"context"

	settings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/provider"
)

func withRequestBudget(ctx context.Context, config settings.RequestBudget) context.Context {
	ledger := &provider.RequestBudget{FreeModels: map[string]bool{}}
	if config.MaxCalls != nil {
		ledger.MaxCalls = *config.MaxCalls
	}
	if config.MaxTokens != nil {
		ledger.MaxTokens = *config.MaxTokens
	}
	if config.MaxUSD != nil {
		ledger.MaxUSD = *config.MaxUSD
	}
	for _, model := range config.FreeModels {
		ledger.FreeModels[model] = true
	}
	if ledger.MaxCalls == 0 && ledger.MaxTokens == 0 && ledger.MaxUSD == 0 {
		return ctx
	}
	return provider.WithRequestBudget(ctx, ledger)
}
