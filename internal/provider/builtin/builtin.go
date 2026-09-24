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
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/auth"
	"github.com/andrepato/harness/internal/auth/oauth"
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

	// oauth marks a provider that declares an OAuth login flow in addition
	// to (or instead of) api-key auth, e.g. Anthropic's Claude Pro/Max
	// subscription auth. Only "anthropic" sets this this phase.
	oauth          bool
	isSubscription bool

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
	p := newCatalogProvider("anthropic", "Anthropic", anthropicEnvKeys, creds)
	// cli.ts's login() prefers a provider's OAuth flow over its api-key
	// flow whenever both exist, since a Claude Pro/Max plan is what most
	// people have; IsSubscription drives cli.ts's "subscription" vs
	// "oauth" auth-kind column.
	p.oauth = true
	p.isSubscription = true
	return p
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
	if p.oauth {
		// cli.ts's login() treats a provider with an OAuth flow as
		// OAuth-first regardless of whether it also accepts an api key;
		// CheckAuth still recognizes either stored credential type.
		return provider.AuthSpec{Kind: provider.AuthKindOAuth, EnvVars: p.envKeys, IsSubscription: p.isSubscription}
	}
	return provider.AuthSpec{Kind: provider.AuthKindAPIKey, EnvVars: p.envKeys}
}

func (p *catalogProvider) Models() []provider.Model { return p.models }

func (p *catalogProvider) RefreshModels(ctx context.Context) error { return nil }

// resolveAuth follows pi's actual per-request auth resolution
// (pi-ai/dist/auth/resolve.js's resolveProviderAuthWithSignal +
// providers/anthropic.js's anthropicApiKeyAuth().resolve()):
//
//   - A stored credential owns the provider; env is consulted only when
//     nothing is stored. This is the opposite of "env wins" -- resolve.js's
//     own comment is explicit: "A stored credential owns the provider:
//     ambient/env is consulted only when nothing is stored."
//   - A stored OAuth credential is refreshed through oauth.Refresh (for
//     "anthropic", the only provider with a refresh implementation this
//     phase) when Expires has passed, under the credential store's
//     serialized Modify, and the rotated tokens are persisted before use --
//     matching resolve.js's resolveStoredOAuth double-checked locking.
//   - A stored api-key credential's Key is used as-is.
//   - Only with no stored credential at all does resolution fall through to
//     env vars, in provider order (ANTHROPIC_AUTH_TOKEN as Bearer, then
//     ANTHROPIC_OAUTH_TOKEN, then ANTHROPIC_API_KEY for anthropic).
func (p *catalogProvider) resolveAuth(ctx context.Context) (key string, isBearer bool, err error) {
	if p.creds != nil {
		cred, rerr := p.creds.Read(p.id)
		if rerr == nil && cred != nil {
			if cred.OAuth != nil {
				refreshed, rerr := p.refreshOAuthIfNeeded(ctx, cred.OAuth)
				if rerr != nil {
					return "", false, rerr
				}
				return refreshed.Access, true, nil
			}
			if cred.APIKey != nil && cred.APIKey.Key != "" {
				return cred.APIKey.Key, false, nil
			}
		}
	}
	for i, envVar := range p.envKeys {
		if v := os.Getenv(envVar); v != "" {
			bearer := p.id == "anthropic" && i == 0 && envVar == "ANTHROPIC_AUTH_TOKEN"
			return v, bearer, nil
		}
	}
	return "", false, nil
}

// refreshOAuthIfNeeded returns o unchanged if it is not yet within its
// expiry margin (Expires already has pi's 5-minute margin baked in at
// store time), else refreshes it under the store's per-provider lock,
// double-checking expiry under that lock so two concurrent requests do not
// both refresh, and persists the rotated tokens via store.Modify before
// returning.
func (p *catalogProvider) refreshOAuthIfNeeded(ctx context.Context, o *auth.OAuthCredential) (*auth.OAuthCredential, error) {
	if p.id != "anthropic" || o == nil || time.Now().UnixMilli() < o.Expires {
		return o, nil
	}
	var refreshed *auth.OAuthCredential
	err := p.creds.Modify(p.id, func(current *auth.Credential) (*auth.Credential, error) {
		if current == nil || current.OAuth == nil {
			return nil, nil // logged out meanwhile; nothing to persist
		}
		if time.Now().UnixMilli() < current.OAuth.Expires {
			refreshed = current.OAuth // another request already refreshed it
			return nil, nil
		}
		tok, rerr := oauth.RefreshAnthropicToken(ctx, current.OAuth.Refresh)
		if rerr != nil {
			return nil, fmt.Errorf("anthropic OAuth refresh failed: %w", rerr)
		}
		refreshed = &auth.OAuthCredential{Refresh: tok.Refresh, Access: tok.Access, Expires: tok.Expires}
		return &auth.Credential{OAuth: refreshed}, nil
	})
	if err != nil {
		return nil, err
	}
	if refreshed == nil {
		return nil, fmt.Errorf("anthropic OAuth credential was removed during refresh")
	}
	return refreshed, nil
}

func (p *catalogProvider) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	key, isBearer, err := p.resolveAuth(ctx)
	if err != nil {
		return authErrorStream(model, err)
	}
	isOAuth := isBearer || strings.Contains(key, "sk-ant-oat")
	a := api.Auth{APIKey: key, IsOAuth: isOAuth}
	switch model.Api {
	case provider.ApiOpenAICompletions:
		return p.openaiClient.Stream(ctx, model, transcript, opts, a)
	default:
		return p.anthropicClient.Stream(ctx, model, transcript, opts, a)
	}
}

// authErrorStream reports an auth-resolution failure (an OAuth refresh that
// failed, or a credential store read/write error) through the same
// start/error event shape a client's own errorOut path uses, since Stream
// never got as far as opening an HTTP request.
func authErrorStream(model provider.Model, err error) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	partial := &msg.AssistantMessage{
		API:          string(model.Api),
		Provider:     model.Provider,
		Model:        model.ID,
		Role:         msg.RoleAssistant,
		StopReason:   msg.StopError,
		ErrorMessage: err.Error(),
		Content:      msg.Blocks{},
	}
	events := make(chan msg.StreamEvent, 1)
	events <- msg.StreamEvent{Type: msg.EventError, Reason: msg.StopError, Error: partial}
	close(events)
	return events, func() (*msg.AssistantMessage, error) { return nil, err }
}
