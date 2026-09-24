package provider

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"

	"github.com/andrepato/harness/internal/auth"
	"github.com/andrepato/harness/internal/budget"
)

// Suppression is how to stop a model reasoning past its budget, mirroring
// harness/src/provider/reasoning.ts's ReasoningSuppression.
type Suppression struct {
	// Suffix, when non-empty, is appended to the last user message's text
	// (e.g. "/no_think") to suppress prompt-triggered reasoning on models
	// that respect it but expose no API-level switch.
	Suffix string
}

// Resolved is a chosen model plus everything the harness needs to run it.
type Resolved struct {
	Model       Model
	Tier        budget.Tier
	Suppression Suppression
}

// Registry is the harness-authored composition of every model provider it
// can drive, behind one type. Model-agnosticism is inherited rather than
// built: nothing downstream branches on which provider a model came from,
// the only thing that varies is the Tier, derived from a number every
// provider populates (ContextWindow).
//
// This is a Go port of harness/src/provider/registry.ts.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
	order     []string
	creds     auth.CredentialStore
}

// NewRegistry builds an empty registry backed by the given credential store.
// If store is nil, a FileCredentialStore at the default path is used.
func NewRegistry(store auth.CredentialStore) *Registry {
	if store == nil {
		store = auth.NewFileCredentialStore("")
	}
	return &Registry{
		providers: make(map[string]Provider),
		creds:     store,
	}
}

// Credentials returns the registry's credential store.
func (r *Registry) Credentials() auth.CredentialStore { return r.creds }

// Register upserts a provider by id, matching pi's models.setProvider: a
// later Register with the same id replaces the earlier one, which also lets
// harness-specific providers (Ollama, faux) override a built-in.
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := p.ID()
	if _, exists := r.providers[id]; !exists {
		r.order = append(r.order, id)
	}
	r.providers[id] = p
}

// Providers returns every registered provider in registration order.
func (r *Registry) Providers() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Provider, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.providers[id])
	}
	return out
}

// Provider returns the provider registered under id, if any.
func (r *Registry) Provider(id string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[id]
	return p, ok
}

// GetModel returns the model with the given id on the given provider.
func (r *Registry) GetModel(providerID, modelID string) (Model, bool) {
	p, ok := r.Provider(providerID)
	if !ok {
		return Model{}, false
	}
	for _, m := range p.Models() {
		if m.ID == modelID {
			return m, true
		}
	}
	return Model{}, false
}

// CheckAuth reports whether providerID currently has usable credentials: an
// API key from env or the credential store, or a non-expired OAuth token.
func (r *Registry) CheckAuth(ctx context.Context, providerID string) (bool, error) {
	p, ok := r.Provider(providerID)
	if !ok {
		return false, fmt.Errorf("unknown provider %q", providerID)
	}
	spec := p.Auth()
	if spec.Kind == AuthKindNone {
		return true, nil
	}
	cred, err := r.creds.Read(providerID)
	if err != nil {
		return false, err
	}
	if cred != nil {
		switch spec.Kind {
		case AuthKindAPIKey:
			if cred.APIKey != nil {
				return true, nil
			}
		case AuthKindOAuth:
			if cred.OAuth != nil {
				return true, nil
			}
		}
	}
	for _, envVar := range spec.EnvVars {
		if v := os.Getenv(envVar); v != "" {
			return true, nil
		}
	}
	return false, nil
}

// Available returns every provider with usable auth, for a model picker.
func (r *Registry) Available(ctx context.Context) ([]Provider, error) {
	var out []Provider
	for _, p := range r.Providers() {
		ok, err := r.CheckAuth(ctx, p.ID())
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID() < out[j].ID() })
	return out, nil
}

// Login stores a credential for a provider. For API-key providers, cred is
// the key. OAuth providers are logged in via their own oauth.* flow, which
// calls Logout/store directly; Login here only handles the API-key case.
func (r *Registry) Login(providerID, apiKey string) error {
	return r.creds.Modify(providerID, func(c *auth.Credential) (*auth.Credential, error) {
		return &auth.Credential{APIKey: &auth.APIKeyCredential{Key: apiKey}}, nil
	})
}

// Logout removes providerID's stored credential.
func (r *Registry) Logout(providerID string) error {
	return r.creds.Delete(providerID)
}

// Resolve looks up a model and computes everything the harness needs to run
// it: the model, its Tier (throws-equivalent: returns an error rather than an
// unrunnable tier, matching registry.ts's `requireTierForWindow`), and its
// reasoning Suppression.
func (r *Registry) Resolve(providerID, modelID string) (Resolved, error) {
	model, ok := r.GetModel(providerID, modelID)
	if !ok {
		return Resolved{}, fmt.Errorf(
			"unknown model %q on provider %q: run a refresh first if the provider is dynamic",
			modelID, providerID)
	}
	tier, err := budget.RequireTierForWindow(model.ContextWindow)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{
		Model:       model,
		Tier:        tier,
		Suppression: SuppressionFor(model),
	}, nil
}
