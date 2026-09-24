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

// Every provider in the vendored catalog is registered (RegisterAll), and
// all ten pi-ai API shapes have a streaming client. implementedAPIs is kept
// as the single gate so that an api value the catalog gains before a client
// exists is reported as "known, not yet streamable" by Stream and
// KnownNotStreamable rather than silently omitted.

// implementedAPIs are the api values this phase can stream. Every other
// provider id in the catalog is exposed as catalog data only (Models()
// still lists every catalog model regardless of api; Stream reports
// ErrNotYetStreamable for a model whose api is not in this set).
var implementedAPIs = map[provider.Api]bool{
	provider.ApiAnthropicMessages:     true,
	provider.ApiOpenAICompletions:     true,
	provider.ApiOpenAIResponses:       true,
	provider.ApiOpenAICodexResponses:  true,
	provider.ApiAzureOpenAIResponses:  true,
	provider.ApiMistralConversations:  true,
	provider.ApiGoogleGenerativeAI:    true,
	provider.ApiGoogleVertex:          true,
	provider.ApiBedrockConverseStream: true,
	provider.ApiPiMessages:            true,
}

// oauthRefresher refreshes a stored OAuth credential for one provider id,
// returning the rotated (refresh, access, expires) triple. Every provider
// in catalog.Providers with AuthKind==AuthOAuth and a refresh flow this
// phase implements has an entry here; OpenRouter is deliberately absent
// (its OAuth exchange yields a permanent API key stored as an api_key
// credential, never an OAuthCredential -- see login.go).
type oauthRefresher func(ctx context.Context, refreshToken string) (oauth.Token, error)

var oauthRefreshers = map[string]oauthRefresher{
	"anthropic": func(ctx context.Context, refreshToken string) (oauth.Token, error) {
		tok, err := oauth.RefreshAnthropicToken(ctx, refreshToken)
		if err != nil {
			return oauth.Token{}, err
		}
		return oauth.Token(tok), nil
	},
	"openai-codex": func(ctx context.Context, refreshToken string) (oauth.Token, error) {
		tok, err := oauth.RefreshOpenAICodexToken(ctx, refreshToken)
		if err != nil {
			return oauth.Token{}, err
		}
		return tok.Token, nil
	},
	"github-copilot": func(ctx context.Context, refreshToken string) (oauth.Token, error) {
		// Enterprise domain is not persisted this phase (auth.OAuthCredential
		// has no field for it); every stored GitHub Copilot credential
		// refreshes against github.com. See github_copilot.go's doc comment.
		cred, err := oauth.RefreshGitHubCopilotToken(ctx, refreshToken, "")
		if err != nil {
			return oauth.Token{}, err
		}
		return cred.Token, nil
	},
	"kimi-coding": oauth.RefreshKimiToken,
	"xai":         oauth.RefreshXaiToken,
	"radius": func(ctx context.Context, refreshToken string) (oauth.Token, error) {
		return oauth.RefreshRadiusToken(ctx, oauth.RadiusDefaultGateway, refreshToken)
	},
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
	responsesClient api.OpenAIResponsesClient
	codexClient     api.OpenAICodexResponsesClient
	azureClient     api.AzureOpenAIResponsesClient
	mistralClient   api.MistralConversationsClient
	googleClient    api.GoogleGenerativeAIClient
	vertexClient    api.GoogleVertexClient
	bedrockClient   api.BedrockConverseStreamClient
	piClient        api.PiMessagesClient
}

// newCatalogProvider builds a provider over every catalog model for id
// (unfiltered by api: Models() must list a provider's full catalog even
// where Stream cannot yet drive every model's api, so `harness models <id>`
// is complete). Stream itself reports ErrNotYetStreamable for a model whose
// api is not in implementedAPIs.
func newCatalogProvider(id, name string, envKeys []string, creds auth.CredentialStore) *catalogProvider {
	models := catalog.All()[id]
	return &catalogProvider{id: id, name: name, envKeys: envKeys, models: models, creds: creds}
}

// newProviderFromConfig builds a catalogProvider from a catalog.Providers
// entry, wiring its AuthKind/IsSubscription straight from the vendored
// pi-ai auth spec that entry was ported from.
func newProviderFromConfig(cfg catalog.ProviderConfig, creds auth.CredentialStore) *catalogProvider {
	p := newCatalogProvider(cfg.ID, cfg.Name, cfg.EnvVars, creds)
	p.oauth = cfg.AuthKind == catalog.AuthOAuth
	p.isSubscription = cfg.IsSubscription
	return p
}

// NewAnthropicProvider builds the built-in Anthropic provider.
func NewAnthropicProvider(creds auth.CredentialStore) provider.Provider {
	cfg, _ := catalog.ProviderConfigByID("anthropic")
	return newProviderFromConfig(cfg, creds)
}

// NewOpenAIProvider builds the built-in OpenAI provider.
func NewOpenAIProvider(creds auth.CredentialStore) provider.Provider {
	cfg, _ := catalog.ProviderConfigByID("openai")
	return newProviderFromConfig(cfg, creds)
}

// RegisterAll registers every provider id in catalog.Providers (which
// providers_test.go asserts covers exactly the vendored catalog's ids) into
// reg, backed by store. This is how `harness providers`/`harness models
// <id>` are meant to see all 41 catalog providers; internal/cli owns
// whether/where it calls this (out of this phase's file ownership).
func RegisterAll(reg *provider.Registry, store auth.CredentialStore) {
	for _, cfg := range catalog.Providers {
		reg.Register(newProviderFromConfig(cfg, store))
	}
}

// IsStreamable reports whether providerID has at least one model whose api
// this phase can actually drive (Stream), as opposed to catalog data it can
// only list.
func IsStreamable(providerID string) bool {
	for _, m := range catalog.All()[providerID] {
		if implementedAPIs[m.Api] {
			return true
		}
	}
	return false
}

// KnownNotStreamable lists every catalog provider id with zero models whose
// api this phase implements (anthropic-messages, openai-completions), for
// `harness providers` to list as known but not yet streamable. Computed
// from the vendored catalog data, not hardcoded to "everything but
// anthropic/openai": a provider like OpenRouter or Vercel AI Gateway, whose
// vendored models are anthropic-messages/openai-completions shaped, is
// streamable even though this phase did not write its api client.
func KnownNotStreamable() []string {
	var out []string
	for _, id := range catalog.ProviderIDs() {
		if !IsStreamable(id) {
			out = append(out, id)
		}
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

// refreshOAuthIfNeeded returns o unchanged if the provider has no refresh
// flow (oauthRefreshers has no entry for it) or it is not yet within its
// expiry margin (Expires already has each flow's own refresh margin baked
// in at store time), else refreshes it under the store's per-provider lock,
// double-checking expiry under that lock so two concurrent requests do not
// both refresh, and persists the rotated tokens via store.Modify before
// returning.
func (p *catalogProvider) refreshOAuthIfNeeded(ctx context.Context, o *auth.OAuthCredential) (*auth.OAuthCredential, error) {
	refresher, ok := oauthRefreshers[p.id]
	if !ok || o == nil || time.Now().UnixMilli() < o.Expires {
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
		tok, rerr := refresher(ctx, current.OAuth.Refresh)
		if rerr != nil {
			return nil, fmt.Errorf("%s OAuth refresh failed: %w", p.id, rerr)
		}
		refreshed = &auth.OAuthCredential{Refresh: tok.Refresh, Access: tok.Access, Expires: tok.Expires}
		return &auth.Credential{OAuth: refreshed}, nil
	})
	if err != nil {
		return nil, err
	}
	if refreshed == nil {
		return nil, fmt.Errorf("%s OAuth credential was removed during refresh", p.id)
	}
	return refreshed, nil
}

func (p *catalogProvider) Stream(ctx context.Context, model provider.Model, transcript []msg.Message, opts provider.StreamOptions) (<-chan msg.StreamEvent, func() (*msg.AssistantMessage, error)) {
	if !implementedAPIs[model.Api] {
		return authErrorStream(model, fmt.Errorf("%s: model %q speaks %q, which has no streaming client yet (known, not yet streamable)", p.id, model.ID, model.Api))
	}
	key, isBearer, err := p.resolveAuth(ctx)
	if err != nil {
		return authErrorStream(model, err)
	}
	isOAuth := isBearer || strings.Contains(key, "sk-ant-oat")
	a := api.Auth{APIKey: key, IsOAuth: isOAuth}
	switch model.Api {
	case provider.ApiOpenAICompletions:
		return p.openaiClient.Stream(ctx, model, transcript, opts, a)
	case provider.ApiOpenAIResponses:
		return p.responsesClient.Stream(ctx, model, transcript, opts, a)
	case provider.ApiOpenAICodexResponses:
		return p.codexClient.Stream(ctx, model, transcript, opts, a)
	case provider.ApiAzureOpenAIResponses:
		return p.azureClient.Stream(ctx, model, transcript, opts, a)
	case provider.ApiMistralConversations:
		return p.mistralClient.Stream(ctx, model, transcript, opts, a)
	case provider.ApiGoogleGenerativeAI:
		return p.googleClient.Stream(ctx, model, transcript, opts, a)
	case provider.ApiGoogleVertex:
		return p.vertexClient.Stream(ctx, model, transcript, opts, a)
	case provider.ApiBedrockConverseStream:
		return p.bedrockClient.Stream(ctx, model, transcript, opts, a)
	case provider.ApiPiMessages:
		return p.piClient.Stream(ctx, model, transcript, opts, a)
	default: // provider.ApiAnthropicMessages, guarded by implementedAPIs above
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
