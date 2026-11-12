package login

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/auth"
	"github.com/andrepato/harness/internal/auth/oauth"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/provider/builtin"
)

// fakeInteraction is a scriptable auth.Interaction: it captures the
// auth_url notification on a channel and answers secret prompts from a
// queue, blocking manual_code prompts until their context is cancelled
// (mirroring the real terminal's prompt racing against the OAuth callback
// server -- see oauth.LoginAnthropic).
type fakeInteraction struct {
	authURL       chan string
	secretAnswers []string
	selectAnswers []string
}

func (f *fakeInteraction) Notify(e auth.Event) {
	if e.Type == auth.EventAuthURL && f.authURL != nil {
		f.authURL <- e.URL
	}
}

func (f *fakeInteraction) Prompt(ctx context.Context, p auth.Prompt) (string, error) {
	switch p.Type {
	case auth.PromptManualCode:
		<-ctx.Done()
		return "", ctx.Err()
	case auth.PromptSecret:
		if len(f.secretAnswers) == 0 {
			return "", nil
		}
		v := f.secretAnswers[0]
		f.secretAnswers = f.secretAnswers[1:]
		return v, nil
	case auth.PromptSelect:
		if len(f.selectAnswers) == 0 {
			return "", nil
		}
		v := f.selectAnswers[0]
		f.selectAnswers = f.selectAnswers[1:]
		return v, nil
	default:
		return "", nil
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func newTestRegistry(t *testing.T) (*provider.Registry, auth.CredentialStore) {
	t.Helper()
	store := auth.NewFileCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	reg := provider.NewRegistry(store)
	reg.Register(builtin.NewAnthropicProvider(store))
	reg.Register(builtin.NewOpenAIProvider(store))
	reg.Register(builtin.NewOpenAICodexProvider(store))
	return reg, store
}

// TestLoginOAuthAnthropic drives the full OAuth login path: a fake
// authorization server (the callback listener LoginAnthropic itself starts,
// bound to an injected ephemeral port rather than the real 53692) and a fake
// token-exchange server, then asserts the exchanged token is persisted
// through store.Modify.
func TestLoginOAuthAnthropic(t *testing.T) {
	var gotExchangeBody map[string]any
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotExchangeBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"refresh_token": "rt-123",
			"access_token":  "at-456",
			"expires_in":    3600,
		})
	}))
	defer tokenSrv.Close()
	restoreTokenURL := oauth.SetTokenURLForTesting(tokenSrv.URL)
	defer restoreTokenURL()

	port := freePort(t)
	origLogin := anthropicLogin
	anthropicLogin = func(ctx context.Context, ia oauth.Interaction) (oauth.AnthropicToken, error) {
		return oauth.LoginAnthropic(ctx, ia, oauth.WithCallbackPort(port))
	}
	defer func() { anthropicLogin = origLogin }()

	reg, store := newTestRegistry(t)
	ia := &fakeInteraction{authURL: make(chan string, 1)}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- Login(ctx, reg, store, "anthropic", ia) }()

	var authURL string
	select {
	case authURL = <-ia.authURL:
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for auth_url notification")
	}

	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth URL %q: %v", authURL, err)
	}
	state := u.Query().Get("state")
	if state == "" {
		t.Fatalf("auth URL %q carried no state", authURL)
	}

	// Simulate the browser completing the OAuth redirect back to the local
	// callback server LoginAnthropic started on our injected port.
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/callback?code=authcode123&state=%s", port, url.QueryEscape(state))
	resp, err := http.Get(callbackURL)
	if err != nil {
		t.Fatalf("hit callback: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d, want 200", resp.StatusCode)
	}

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Login: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for Login to return")
	}

	if gotExchangeBody["code"] != "authcode123" {
		t.Errorf("token exchange code = %v, want authcode123", gotExchangeBody["code"])
	}

	cred, err := store.Read("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if cred == nil || cred.OAuth == nil {
		t.Fatalf("expected a persisted OAuth credential, got %+v", cred)
	}
	if cred.OAuth.Access != "at-456" || cred.OAuth.Refresh != "rt-123" {
		t.Fatalf("persisted OAuth credential = %+v, want access=at-456 refresh=rt-123", cred.OAuth)
	}
}

func TestLoginAPIKey(t *testing.T) {
	reg, store := newTestRegistry(t)
	ia := &fakeInteraction{secretAnswers: []string{"sk-test-openai-key"}}

	if err := Login(context.Background(), reg, store, "openai", ia); err != nil {
		t.Fatalf("Login: %v", err)
	}

	cred, err := store.Read("openai")
	if err != nil {
		t.Fatal(err)
	}
	if cred == nil || cred.APIKey == nil || cred.APIKey.Key != "sk-test-openai-key" {
		t.Fatalf("persisted credential = %+v, want api_key sk-test-openai-key", cred)
	}
}

// TestLoginOpenAICodexAPIKey asserts that choosing "OpenAI API key" at the
// openai-codex login prompt stores a plain api_key credential (the path that
// Stream later routes to the standard OpenAI Responses endpoint), without
// running the OAuth flow.
func TestLoginOpenAICodexAPIKey(t *testing.T) {
	reg, store := newTestRegistry(t)
	ia := &fakeInteraction{
		selectAnswers: []string{"api_key"},
		secretAnswers: []string{"sk-codex-key"},
	}

	if err := Login(context.Background(), reg, store, "openai-codex", ia); err != nil {
		t.Fatalf("Login: %v", err)
	}

	cred, err := store.Read("openai-codex")
	if err != nil {
		t.Fatal(err)
	}
	if cred == nil || cred.APIKey == nil || cred.APIKey.Key != "sk-codex-key" {
		t.Fatalf("persisted credential = %+v, want api_key sk-codex-key", cred)
	}
	if cred.OAuth != nil {
		t.Fatalf("api-key login must not store an OAuth credential: %+v", cred.OAuth)
	}
}

// TestLoginOpenAICodexSubscription asserts that choosing the subscription
// option runs the OAuth flow and stores an OAuth credential. The flow itself
// is stubbed (the full browser/callback path is covered by the Anthropic
// test); this pins the login dispatch.
func TestLoginOpenAICodexSubscription(t *testing.T) {
	orig := openaiCodexLogin
	openaiCodexLogin = func(ctx context.Context, ia oauth.FlowInteraction) (oauth.OpenAICodexToken, error) {
		return oauth.OpenAICodexToken{
			Token:     oauth.Token{Refresh: "rt-codex", Access: "at-codex", Expires: 123},
			AccountID: "acc_1",
		}, nil
	}
	defer func() { openaiCodexLogin = orig }()

	reg, store := newTestRegistry(t)
	ia := &fakeInteraction{selectAnswers: []string{"subscription"}}

	if err := Login(context.Background(), reg, store, "openai-codex", ia); err != nil {
		t.Fatalf("Login: %v", err)
	}

	cred, err := store.Read("openai-codex")
	if err != nil {
		t.Fatal(err)
	}
	if cred == nil || cred.OAuth == nil || cred.OAuth.Access != "at-codex" {
		t.Fatalf("persisted credential = %+v, want OAuth access=at-codex", cred)
	}
}

func TestLoginUnknownProvider(t *testing.T) {
	reg, store := newTestRegistry(t)
	ia := &fakeInteraction{}
	if err := Login(context.Background(), reg, store, "does-not-exist", ia); err == nil {
		t.Fatal("expected an error for an unknown provider")
	}
}

func TestLogout(t *testing.T) {
	reg, store := newTestRegistry(t)
	if err := store.Modify("openai", func(_ *auth.Credential) (*auth.Credential, error) {
		return &auth.Credential{APIKey: &auth.APIKeyCredential{Key: "sk-existing"}}, nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := Logout(context.Background(), reg, store, "openai"); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	cred, err := store.Read("openai")
	if err != nil {
		t.Fatal(err)
	}
	if cred != nil {
		t.Fatalf("expected credential removed, got %+v", cred)
	}
}

func TestStatusKinds(t *testing.T) {
	reg, store := newTestRegistry(t)

	statuses := Status(context.Background(), reg, store)
	byID := map[string]ProviderAuthStatus{}
	for _, s := range statuses {
		byID[s.ProviderID] = s
	}

	anthropicStatus, ok := byID["anthropic"]
	if !ok {
		t.Fatal("missing anthropic status")
	}
	if anthropicStatus.Kind != "subscription" {
		t.Errorf("anthropic kind = %q, want subscription", anthropicStatus.Kind)
	}
	if anthropicStatus.Authed {
		t.Errorf("anthropic should not be authed before any login")
	}

	openaiStatus, ok := byID["openai"]
	if !ok {
		t.Fatal("missing openai status")
	}
	if openaiStatus.Kind != "api key" {
		t.Errorf("openai kind = %q, want %q", openaiStatus.Kind, "api key")
	}
	if openaiStatus.Authed {
		t.Errorf("openai should not be authed before any login")
	}

	if err := store.Modify("openai", func(_ *auth.Credential) (*auth.Credential, error) {
		return &auth.Credential{APIKey: &auth.APIKeyCredential{Key: "sk-x"}}, nil
	}); err != nil {
		t.Fatal(err)
	}

	statuses = Status(context.Background(), reg, store)
	for _, s := range statuses {
		if s.ProviderID == "openai" && !s.Authed {
			t.Errorf("openai should be authed after storing an api key")
		}
	}
}
