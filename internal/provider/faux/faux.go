// Package faux registers provider id "faux" against the scripted server in
// internal/testkit/faux, giving every later test real, deterministic
// models (faux-1, faux-2) to drive Stream through.
//
// It registers only when HARNESS_FAUX_ADDR is set (the address the test set
// up already started faux at); it is never part of the default provider
// list.
package faux

import (
	"context"
	"os"
	"strings"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/provider/api"
)

// ProviderID is the registry id.
const ProviderID = "faux"

// ModelID is the id of faux's default (large-tier) model.
const ModelID = "faux-1"

// ModelID2 is the id of faux's second, small-tier model, used to exercise
// multi-model behavior (independent scripts, per-model tiers/cost) without
// a second provider.
const ModelID2 = "faux-2"

// New builds the faux provider, reading its address and API shape from
// HARNESS_FAUX_ADDR / HARNESS_FAUX_API. Returns nil, false when
// HARNESS_FAUX_ADDR is unset, matching the "registers only when configured"
// contract -- callers should skip Registry.Register in that case.
func New() (*Provider, bool) {
	addr := os.Getenv("HARNESS_FAUX_ADDR")
	if addr == "" {
		return nil, false
	}
	apiShape := provider.Api(os.Getenv("HARNESS_FAUX_API"))
	if apiShape == "" {
		apiShape = provider.ApiAnthropicMessages
	}

	baseURL := "http://" + addr
	if apiShape == provider.ApiOpenAICompletions {
		baseURL += "/v1"
	}

	model := provider.Model{
		ID:       ModelID,
		Name:     ModelID,
		Api:      apiShape,
		Provider: ProviderID,
		BaseURL:  baseURL,
		Input:    []string{"text", "image"},
		// pi-ai's fauxProvider defaults (providers/faux.js: contextWindow
		// 128000, maxTokens 16384), so the TS oracle and this port compute
		// the same tier and footer for faux-1.
		ContextWindow: 128000,
		MaxTokens:     16384,
		Cost:          provider.ModelCost{},
	}

	model2 := provider.Model{
		ID:       ModelID2,
		Name:     ModelID2,
		Api:      apiShape,
		Provider: ProviderID,
		BaseURL:  baseURL,
		Input:    []string{"text", "image"},
		// A small-tier sibling to faux-1: a smaller context window/max
		// tokens and a non-zero cost, so tests can exercise a second
		// scripted model with a different tier and cost footer.
		ContextWindow: 32768,
		MaxTokens:     8192,
		Cost: provider.ModelCost{
			ModelCostRates: provider.ModelCostRates{
				Input:  3.0,
				Output: 15.0,
			},
		},
	}

	models := []provider.Model{model, model2}
	// HARNESS_FAUX_EXTRA_MODELS ("faux-3,faux-4") adds more faux-1-shaped
	// models, for scripts that give each of several concurrent subagents
	// its own queue. Opt-in, so the default model list stays two long.
	for _, id := range strings.Split(os.Getenv("HARNESS_FAUX_EXTRA_MODELS"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			extra := model
			extra.ID, extra.Name = id, id
			models = append(models, extra)
		}
	}
	return &Provider{models: models}, true
}

// Provider is the faux provider.Provider implementation.
type Provider struct {
	models    []provider.Model
	anthropic api.AnthropicClient
	openai    api.OpenAICompletionsClient
}

func (p *Provider) ID() string   { return ProviderID }
func (p *Provider) Name() string { return "Faux (test)" }

func (p *Provider) Auth() provider.AuthSpec {
	return provider.AuthSpec{Kind: provider.AuthKindNone}
}

func (p *Provider) Models() []provider.Model { return p.models }

func (p *Provider) RefreshModels(ctx context.Context) error { return nil }

func (p *Provider) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	auth := api.Auth{APIKey: "faux"}
	if model.Api == provider.ApiOpenAICompletions {
		return p.openai.Stream(ctx, model, transcript, opts, auth)
	}
	return p.anthropic.Stream(ctx, model, transcript, opts, auth)
}
