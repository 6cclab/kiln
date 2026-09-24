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

func loginOAuth(ctx context.Context, store auth.CredentialStore, providerID string, ia auth.Interaction) error {
	if providerID != "anthropic" {
		// No other provider's OAuth flow is implemented this phase
		// (github-copilot, openai-codex, kimi-coding, ... in pi-ai are out
		// of scope; see builtin.implementedAPIs).
		return fmt.Errorf("no OAuth login flow implemented for provider %q", providerID)
	}
	adapter := &oauthInteractionAdapter{ia: ia}
	tok, err := anthropicLogin(ctx, adapter)
	if err != nil {
		return err
	}
	return store.Modify(providerID, func(_ *auth.Credential) (*auth.Credential, error) {
		return &auth.Credential{OAuth: &auth.OAuthCredential{
			Refresh: tok.Refresh,
			Access:  tok.Access,
			Expires: tok.Expires,
		}}, nil
	})
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
