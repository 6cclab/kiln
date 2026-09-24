// Package faux registers provider id "faux" against the scripted server in
// internal/testkit/faux, giving every later test one real, deterministic
// model to drive Stream through.
//
// It registers only when HARNESS_FAUX_ADDR is set (the address the test set
// up already started faux at); it is never part of the default provider
// list.
package faux

import (
	"context"
	"os"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/provider/api"
)

// ProviderID is the registry id.
const ProviderID = "faux"

// ModelID is the id of the one model faux serves.
const ModelID = "faux-1"

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
		ID:            ModelID,
		Name:          ModelID,
		Api:           apiShape,
		Provider:      ProviderID,
		BaseURL:       baseURL,
		Input:         []string{"text", "image"},
		ContextWindow: 32768,
		MaxTokens:     4096,
		Cost:          provider.ModelCost{},
	}

	return &Provider{model: model}, true
}

// Provider is the faux provider.Provider implementation.
type Provider struct {
	model     provider.Model
	anthropic api.AnthropicClient
	openai    api.OpenAICompletionsClient
}

func (p *Provider) ID() string   { return ProviderID }
func (p *Provider) Name() string { return "Faux (test)" }

func (p *Provider) Auth() provider.AuthSpec {
	return provider.AuthSpec{Kind: provider.AuthKindNone}
}

func (p *Provider) Models() []provider.Model { return []provider.Model{p.model} }

func (p *Provider) RefreshModels(ctx context.Context) error { return nil }

func (p *Provider) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	auth := api.Auth{APIKey: "faux"}
	if model.Api == provider.ApiOpenAICompletions {
		return p.openai.Stream(ctx, model, transcript, opts, auth)
	}
	return p.anthropic.Stream(ctx, model, transcript, opts, auth)
}
