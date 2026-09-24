// Package builtin wires the vendored model catalog (internal/provider/catalog)
// together with the two streaming API clients this phase implements
// (internal/provider/api) into provider.Provider implementations for
// Anthropic and OpenAI.
//
// It is a separate package from internal/provider because
// internal/provider/catalog already imports internal/provider (for
// provider.Model); this package depends on both without creating a cycle.
package builtin

import (
	"context"
	"os"
	"strings"

	"github.com/andrepato/harness/internal/auth"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/provider/api"
	"github.com/andrepato/harness/internal/provider/catalog"
)

// This phase registers Provider implementations for exactly the two API
// shapes with a working streaming client: anthropic-messages and
// openai-completions. Every other api value present in the vendored catalog
// (openai-responses, google-generative-ai, bedrock-converse-stream, ...) is
// catalog data the harness can read (models, cost, context windows) but
// cannot yet stream; KnownNotStreamable lists those provider ids so
// `harness providers` can show them as "known, not yet streamable" rather
// than silently omitting them.

// anthropicEnvKeys mirrors pi-ai's env-api-keys.js getApiKeyEnvVars("anthropic"):
// ANTHROPIC_AUTH_TOKEN participates in env discovery but is sent as
// Authorization: Bearer rather than an api key.
var anthropicEnvKeys = []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_OAUTH_TOKEN", "ANTHROPIC_API_KEY"}

// openaiEnvKeys mirrors env-api-keys.js's envMap["openai"].
var openaiEnvKeys = []string{"OPENAI_API_KEY"}

// implementedAPIs are the api values this phase can stream. Every other
// provider id in the catalog is exposed as catalog data only.
var implementedAPIs = map[provider.Api]bool{
	provider.ApiAnthropicMessages: true,
	provider.ApiOpenAICompletions: true,
}

// catalogProvider is a provider.Provider backed by catalog.All()'s static
// models for one provider id, streaming through one of the two implemented
// API clients.
type catalogProvider struct {
	id      string
	name    string
	envKeys []string
	models  []provider.Model
	creds   auth.CredentialStore

	anthropicClient api.AnthropicClient
	openaiClient    api.OpenAICompletionsClient
}

func newCatalogProvider(id, name string, envKeys []string, creds auth.CredentialStore) *catalogProvider {
	var models []provider.Model
	for _, m := range catalog.All()[id] {
		if implementedAPIs[m.Api] {
			models = append(models, m)
		}
	}
	return &catalogProvider{id: id, name: name, envKeys: envKeys, models: models, creds: creds}
}

// NewAnthropicProvider builds the built-in Anthropic provider (models with
// api=="anthropic-messages" from the vendored catalog).
func NewAnthropicProvider(creds auth.CredentialStore) provider.Provider {
	return newCatalogProvider("anthropic", "Anthropic", anthropicEnvKeys, creds)
}

// NewOpenAIProvider builds the built-in OpenAI provider. pi's own "openai"
// provider id is primarily openai-responses; this phase only streams
// openai-completions, so only the catalog's openai-completions models under
// the "openai" provider id are exposed here.
func NewOpenAIProvider(creds auth.CredentialStore) provider.Provider {
	return newCatalogProvider("openai", "OpenAI", openaiEnvKeys, creds)
}

// KnownNotStreamable lists every catalog provider id besides "anthropic" and
// "openai" (this phase's two implemented shapes), for `harness providers` to
// list as known but not yet streamable.
func KnownNotStreamable() []string {
	var out []string
	for _, id := range catalog.ProviderIDs() {
		if id == "anthropic" || id == "openai" {
			continue
		}
		out = append(out, id)
	}
	return out
}

func (p *catalogProvider) ID() string   { return p.id }
func (p *catalogProvider) Name() string { return p.name }

func (p *catalogProvider) Auth() provider.AuthSpec {
	return provider.AuthSpec{Kind: provider.AuthKindAPIKey, EnvVars: p.envKeys}
}

func (p *catalogProvider) Models() []provider.Model { return p.models }

func (p *catalogProvider) RefreshModels(ctx context.Context) error { return nil }

// resolveAPIKey follows pi's anthropicApiKeyAuth().resolve() order: stored
// api-key credential, then stored OAuth credential, then each env var in
// order (with ANTHROPIC_AUTH_TOKEN sent as Bearer rather than x-api-key).
func (p *catalogProvider) resolveAPIKey() (key string, isBearer bool) {
	if p.creds != nil {
		if cred, err := p.creds.Read(p.id); err == nil && cred != nil {
			if cred.APIKey != nil && cred.APIKey.Key != "" {
				return cred.APIKey.Key, false
			}
			if cred.OAuth != nil && cred.OAuth.Access != "" {
				return cred.OAuth.Access, true
			}
		}
	}
	for i, envVar := range p.envKeys {
		if v := os.Getenv(envVar); v != "" {
			bearer := p.id == "anthropic" && i == 0 && envVar == "ANTHROPIC_AUTH_TOKEN"
			return v, bearer
		}
	}
	return "", false
}

func (p *catalogProvider) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	key, isBearer := p.resolveAPIKey()
	isOAuth := isBearer || strings.Contains(key, "sk-ant-oat")
	a := api.Auth{APIKey: key, IsOAuth: isOAuth}
	switch model.Api {
	case provider.ApiOpenAICompletions:
		return p.openaiClient.Stream(ctx, model, transcript, opts, a)
	default:
		return p.anthropicClient.Stream(ctx, model, transcript, opts, a)
	}
}
