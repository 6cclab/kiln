// Package login wires provider auth specs (internal/provider) to the
// terminal Interaction (internal/auth) and the OAuth flows
// (internal/auth/oauth) into the three verbs harness's CLI needs: Login,
// Logout, Status. Ported from harness/src/cli.ts's login()/listProviders()
// and pi-ai's dist/models.js login()/logout()/checkAuth().
package login

import (
	"context"
	"fmt"
	"strings"

	"github.com/andrepato/harness/internal/auth"
	"github.com/andrepato/harness/internal/auth/oauth"
	"github.com/andrepato/harness/internal/provider"
)

// anthropicLogin runs the Anthropic OAuth flow. A package var (rather than a
// direct call to oauth.LoginAnthropic) so tests can inject a fake
// implementation that binds the local callback server to an ephemeral port
// instead of the real 53692 -- see oauth.WithCallbackPort.
var anthropicLogin = func(ctx context.Context, ia oauth.Interaction) (oauth.AnthropicToken, error) {
	return oauth.LoginAnthropic(ctx, ia)
}

// The remaining OAuth flows are package vars for the same reason: tests
// inject fakes (or point the real flow's endpoints at an httptest server via
// each flow's SetXxxURLsForTesting/WithXxxCallbackPort helpers) rather than
// hitting the network.
var (
	openaiCodexLogin = func(ctx context.Context, ia oauth.FlowInteraction) (oauth.OpenAICodexToken, error) {
		return oauth.LoginOpenAICodex(ctx, ia)
	}
	githubCopilotLogin = func(ctx context.Context, ia oauth.FlowInteraction, domain string) (oauth.GitHubCopilotCredential, error) {
		return oauth.LoginGitHubCopilot(ctx, ia, domain)
	}
	kimiLogin = func(ctx context.Context, ia oauth.FlowInteraction) (oauth.Token, error) {
		return oauth.LoginKimi(ctx, ia)
	}
	openrouterLogin = func(ctx context.Context, ia oauth.FlowInteraction) (string, error) {
		return oauth.LoginOpenRouter(ctx, ia)
	}
	xaiLogin = func(ctx context.Context, ia oauth.FlowInteraction) (oauth.Token, error) {
		return oauth.LoginXai(ctx, ia)
	}
	radiusLogin = func(ctx context.Context, ia oauth.FlowInteraction, name, gateway string) (oauth.Token, error) {
		return oauth.LoginRadius(ctx, ia, name, gateway)
	}
)

// ProviderAuthStatus is one provider's auth kind and whether it is
// currently configured, mirroring cli.ts's listProviders row.
type ProviderAuthStatus struct {
	ProviderID string
	// Kind is "subscription" (OAuth backed by a plan), "oauth", or
	// "api key", matching cli.ts's `kind` column exactly.
	Kind string
	// Authed reports whether the provider currently has usable credentials
	// (env, stored api_key, or a stored OAuth credential -- expiry is not
	// checked here; refresh happens lazily at Stream time, see
	// builtin.catalogProvider.resolveAuth).
	Authed bool
}

// oauthInteractionAdapter adapts auth.Interaction (the harness's terminal
// prompt/notify surface) to oauth.Interaction (the narrower surface
// LoginAnthropic needs), matching how cli.ts's single
// createTerminalAuthInteraction serves both pi's AuthInteraction and the
// OAuth flow underneath it.
type oauthInteractionAdapter struct {
	ia auth.Interaction
}

func (a *oauthInteractionAdapter) NotifyAuthURL(url, instructions string) {
	a.ia.Notify(auth.Event{Type: auth.EventAuthURL, URL: url, Instructions: instructions})
}

func (a *oauthInteractionAdapter) NotifyProgress(message string) {
	a.ia.Notify(auth.Event{Type: auth.EventProgress, Message: message})
}

func (a *oauthInteractionAdapter) PromptManualCode(ctx context.Context, message, placeholder string) (string, error) {
	return a.ia.Prompt(ctx, auth.Prompt{Type: auth.PromptManualCode, Message: message, Placeholder: placeholder})
}

// NotifyDeviceCode and PromptSelect implement oauth.FlowInteraction, the
// wider surface the device-code and multi-method flows (OpenAI Codex,
// GitHub Copilot, Kimi, xAI, Radius) need beyond the narrower oauth.
// Interaction LoginAnthropic uses.
func (a *oauthInteractionAdapter) NotifyDeviceCode(userCode, verificationURI string, intervalSeconds, expiresInSeconds int) {
	a.ia.Notify(auth.Event{
		Type:             auth.EventDeviceCode,
		UserCode:         userCode,
		VerificationURI:  verificationURI,
		IntervalSeconds:  intervalSeconds,
		ExpiresInSeconds: expiresInSeconds,
	})
}

func (a *oauthInteractionAdapter) PromptSelect(ctx context.Context, message string, options []oauth.SelectOption) (string, error) {
	opts := make([]auth.SelectOption, len(options))
	for i, o := range options {
		opts[i] = auth.SelectOption{ID: o.ID, Label: o.Label}
	}
	return a.ia.Prompt(ctx, auth.Prompt{Type: auth.PromptSelect, Message: message, Options: opts})
}

// Login logs providerID in: the OAuth flow if the provider declares one
// (only "anthropic" has a flow implemented this phase), else an API-key
// prompt via ia.Prompt(secret). The resulting credential is persisted
// through store.Modify. Confirmation/failure text matches cli.ts's login():
// "Logging in to <label>...", then "Logged in to <providerId>." or
// "Login failed: <message>".
func Login(ctx context.Context, reg *provider.Registry, store auth.CredentialStore, providerID string, ia auth.Interaction) error {
	p, ok := reg.Provider(providerID)
	if !ok {
		return fmt.Errorf(`unknown provider %q. See: harness providers`, providerID)
	}
	spec := p.Auth()

	// cli.ts: "Prefer the subscription/OAuth flow when the provider has
	// one; an API key is the fallback, not the default, since a plan is
	// what most people have."
	label := p.Name()
	ia.Notify(auth.Event{Type: auth.EventInfo, Message: fmt.Sprintf("Logging in to %s...", label)})

	var err error
	if spec.Kind == provider.AuthKindOAuth {
		err = loginOAuth(ctx, store, providerID, ia)
	} else {
		err = loginAPIKey(ctx, store, providerID, label, ia)
	}
	if err != nil {
		ia.Notify(auth.Event{Type: auth.EventInfo, Message: fmt.Sprintf("Login failed: %s", err.Error())})
		return err
	}
	ia.Notify(auth.Event{Type: auth.EventInfo, Message: fmt.Sprintf("Logged in to %s.", providerID)})
	return nil
}

func storeOAuthCredential(store auth.CredentialStore, providerID string, refresh, access string, expires int64) error {
	return store.Modify(providerID, func(_ *auth.Credential) (*auth.Credential, error) {
		return &auth.Credential{OAuth: &auth.OAuthCredential{Refresh: refresh, Access: access, Expires: expires}}, nil
	})
}

func loginOAuth(ctx context.Context, store auth.CredentialStore, providerID string, ia auth.Interaction) error {
	adapter := &oauthInteractionAdapter{ia: ia}
	switch providerID {
	case "anthropic":
		tok, err := anthropicLogin(ctx, adapter)
		if err != nil {
			return err
		}
		return storeOAuthCredential(store, providerID, tok.Refresh, tok.Access, tok.Expires)

	case "openai-codex":
		tok, err := openaiCodexLogin(ctx, adapter)
		if err != nil {
			return err
		}
		ia.Notify(auth.Event{Type: auth.EventInfo, Message: fmt.Sprintf("Signed in as ChatGPT account %s.", tok.AccountID)})
		return storeOAuthCredential(store, providerID, tok.Refresh, tok.Access, tok.Expires)

	case "github-copilot":
		domain, err := ia.Prompt(ctx, auth.Prompt{
			Type:        auth.PromptText,
			Message:     "GitHub Enterprise URL/domain (blank for github.com)",
			Placeholder: "company.ghe.com",
		})
		if err != nil {
			return err
		}
		cred, err := githubCopilotLogin(ctx, adapter, domain)
		if err != nil {
			return err
		}
		return storeOAuthCredential(store, providerID, cred.Refresh, cred.Access, cred.Expires)

	case "kimi-coding":
		tok, err := kimiLogin(ctx, adapter)
		if err != nil {
			return err
		}
		return storeOAuthCredential(store, providerID, tok.Refresh, tok.Access, tok.Expires)

	case "openrouter":
		// OpenRouter's OAuth exchange yields a permanent, user-controlled
		// API key, not a refresh/access/expiry triple -- stored as a plain
		// api_key credential, matching this phase's brief (pi itself boxes
		// it as `{ type: "oauth", refresh: "", expires: MAX_SAFE_INTEGER }`;
		// storing it as what it actually is avoids a bogus refresh cycle).
		key, err := openrouterLogin(ctx, adapter)
		if err != nil {
			return err
		}
		return store.Modify(providerID, func(_ *auth.Credential) (*auth.Credential, error) {
			return &auth.Credential{APIKey: &auth.APIKeyCredential{Key: key}}, nil
		})

	case "xai":
		tok, err := xaiLogin(ctx, adapter)
		if err != nil {
			return err
		}
		return storeOAuthCredential(store, providerID, tok.Refresh, tok.Access, tok.Expires)

	case "radius":
		tok, err := radiusLogin(ctx, adapter, "Radius", oauth.RadiusDefaultGateway)
		if err != nil {
			return err
		}
		return storeOAuthCredential(store, providerID, tok.Refresh, tok.Access, tok.Expires)

	default:
		return fmt.Errorf("no OAuth login flow implemented for provider %q", providerID)
	}
}

func loginAPIKey(ctx context.Context, store auth.CredentialStore, providerID, label string, ia auth.Interaction) error {
	key, err := ia.Prompt(ctx, auth.Prompt{
		Type:    auth.PromptSecret,
		Message: fmt.Sprintf("Enter %s API key", label),
	})
	if err != nil {
		return err
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("no API key entered")
	}
	return store.Modify(providerID, func(_ *auth.Credential) (*auth.Credential, error) {
		return &auth.Credential{APIKey: &auth.APIKeyCredential{Key: key}}, nil
	})
}

// Logout removes providerID's stored credential, matching cli.ts's
// `harness logout <provider>` / pi's models.logout().
func Logout(ctx context.Context, reg *provider.Registry, store auth.CredentialStore, providerID string) error {
	if _, ok := reg.Provider(providerID); !ok {
		return fmt.Errorf(`unknown provider %q. See: harness providers`, providerID)
	}
	return store.Delete(providerID)
}

// Status returns every registered provider's auth kind and whether it is
// currently configured, mirroring cli.ts's listProviders(): kind is
// "subscription" when the provider's OAuth is plan-backed
// (auth.oauth.isSubscription), "oauth" when it has OAuth but is not a
// subscription, else "api key".
func Status(ctx context.Context, reg *provider.Registry, store auth.CredentialStore) []ProviderAuthStatus {
	providers := reg.Providers()
	out := make([]ProviderAuthStatus, 0, len(providers))
	for _, p := range providers {
		spec := p.Auth()
		kind := "api key"
		if spec.Kind == provider.AuthKindOAuth {
			kind = "oauth"
		}
		if spec.IsSubscription {
			kind = "subscription"
		}
		authed, _ := reg.CheckAuth(ctx, p.ID())
		out = append(out, ProviderAuthStatus{ProviderID: p.ID(), Kind: kind, Authed: authed})
	}
	return out
}
