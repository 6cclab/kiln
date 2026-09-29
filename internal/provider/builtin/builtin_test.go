package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/auth"
	"github.com/andrepato/harness/internal/auth/oauth"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

const minimalAnthropicSSE = "" +
	"event: message_start\n" +
	"data: {\"message\":{\"id\":\"msg_1\",\"usage\":{}}}\n\n" +
	"event: content_block_start\n" +
	"data: {\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
	"event: content_block_delta\n" +
	"data: {\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
	"event: content_block_stop\n" +
	"data: {\"index\":0}\n\n" +
	"event: message_delta\n" +
	"data: {\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{}}\n\n" +
	"event: message_stop\n" +
	"data: {}\n\n"

func collect(ch <-chan msg.StreamEvent) {
	for range ch {
	}
}

// TestStreamRefreshesExpiredOAuthToken exercises the credential path Stream
// actually takes: a stored OAuth credential whose Expires has passed must be
// refreshed (through oauth.RefreshAnthropicToken against a fake token
// endpoint), the request must go out with the refreshed access token as a
// Bearer, and the rotated tokens must be persisted back to the store before
// Stream returns.
func TestStreamRefreshesExpiredOAuthToken(t *testing.T) {
	var gotAuthHeader string
	msgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, minimalAnthropicSSE)
	}))
	defer msgSrv.Close()

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"refresh_token": "rt-new",
			"access_token":  "at-new",
			"expires_in":    3600,
		})
	}))
	defer tokenSrv.Close()
	restore := oauth.SetTokenURLForTesting(tokenSrv.URL)
	defer restore()

	store := auth.NewFileCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	expired := time.Now().UnixMilli() - 1000
	if err := store.Modify("anthropic", func(_ *auth.Credential) (*auth.Credential, error) {
		return &auth.Credential{OAuth: &auth.OAuthCredential{Refresh: "rt-old", Access: "at-old", Expires: expired}}, nil
	}); err != nil {
		t.Fatal(err)
	}

	p := NewAnthropicProvider(store)
	model := provider.Model{
		ID:            "m",
		Provider:      "anthropic",
		Api:           provider.ApiAnthropicMessages,
		BaseURL:       msgSrv.URL,
		ContextWindow: 1000,
		MaxTokens:     100,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := p.Stream(ctx, model, []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}},
	}, provider.StreamOptions{})
	collect(events)
	if _, err := wait(); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if gotAuthHeader != "Bearer at-new" {
		t.Fatalf("Authorization header = %q, want %q", gotAuthHeader, "Bearer at-new")
	}

	cred, err := store.Read("anthropic")
	if err != nil {
		t.Fatal(err)
	}
	if cred == nil || cred.OAuth == nil {
		t.Fatalf("expected a persisted OAuth credential, got %+v", cred)
	}
	if cred.OAuth.Access != "at-new" || cred.OAuth.Refresh != "rt-new" {
		t.Fatalf("stored credential not updated: %+v, want access=at-new refresh=rt-new", cred.OAuth)
	}
}

// TestStreamUsesUnexpiredStoredOAuthTokenWithoutRefresh asserts a
// not-yet-expired stored OAuth token is used as-is, with no call to the
// token endpoint at all.
func TestStreamUsesUnexpiredStoredOAuthTokenWithoutRefresh(t *testing.T) {
	var gotAuthHeader string
	msgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, minimalAnthropicSSE)
	}))
	defer msgSrv.Close()

	tokenCalled := false
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalled = true
	}))
	defer tokenSrv.Close()
	restore := oauth.SetTokenURLForTesting(tokenSrv.URL)
	defer restore()

	store := auth.NewFileCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	notExpired := time.Now().UnixMilli() + 60*60*1000
	if err := store.Modify("anthropic", func(_ *auth.Credential) (*auth.Credential, error) {
		return &auth.Credential{OAuth: &auth.OAuthCredential{Refresh: "rt", Access: "at-still-valid", Expires: notExpired}}, nil
	}); err != nil {
		t.Fatal(err)
	}

	p := NewAnthropicProvider(store)
	model := provider.Model{
		ID:            "m",
		Provider:      "anthropic",
		Api:           provider.ApiAnthropicMessages,
		BaseURL:       msgSrv.URL,
		ContextWindow: 1000,
		MaxTokens:     100,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := p.Stream(ctx, model, []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}},
	}, provider.StreamOptions{})
	collect(events)
	if _, err := wait(); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if gotAuthHeader != "Bearer at-still-valid" {
		t.Fatalf("Authorization header = %q, want %q", gotAuthHeader, "Bearer at-still-valid")
	}
	if tokenCalled {
		t.Fatal("token endpoint should not have been called for an unexpired credential")
	}
}

// TestStreamPrefersStoredAPIKeyOverEnv asserts a stored api-key credential
// wins over an env var, matching pi's resolve.js: "A stored credential owns
// the provider: ambient/env is consulted only when nothing is stored."
func TestStreamPrefersStoredAPIKeyOverEnv(t *testing.T) {
	var gotAPIKeyHeader string
	msgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKeyHeader = r.Header.Get("x-api-key")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, minimalAnthropicSSE)
	}))
	defer msgSrv.Close()

	t.Setenv("ANTHROPIC_API_KEY", "sk-from-env")

	store := auth.NewFileCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	if err := store.Modify("anthropic", func(_ *auth.Credential) (*auth.Credential, error) {
		return &auth.Credential{APIKey: &auth.APIKeyCredential{Key: "sk-from-store"}}, nil
	}); err != nil {
		t.Fatal(err)
	}

	p := NewAnthropicProvider(store)
	model := provider.Model{
		ID:            "m",
		Provider:      "anthropic",
		Api:           provider.ApiAnthropicMessages,
		BaseURL:       msgSrv.URL,
		ContextWindow: 1000,
		MaxTokens:     100,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := p.Stream(ctx, model, []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}},
	}, provider.StreamOptions{})
	collect(events)
	if _, err := wait(); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if gotAPIKeyHeader != "sk-from-store" {
		t.Fatalf("x-api-key header = %q, want %q (stored credential should win over env)", gotAPIKeyHeader, "sk-from-store")
	}
}

// A refresh token the server no longer accepts (OAuth invalid_grant: it
// was rotated elsewhere, revoked, or expired) is a signed-out user, not a
// transport fault: the error says how to sign in again and carries no
// token-endpoint URL or response body.
func TestStreamRejectedRefreshAsksToSignInAgain(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error": "invalid_grant", "error_description": "Refresh token not found or invalid"}`)
	}))
	defer tokenSrv.Close()
	restore := oauth.SetTokenURLForTesting(tokenSrv.URL)
	defer restore()

	store := auth.NewFileCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	if err := store.Modify("anthropic", func(_ *auth.Credential) (*auth.Credential, error) {
		return &auth.Credential{OAuth: &auth.OAuthCredential{Refresh: "rt-dead", Access: "at-old", Expires: time.Now().UnixMilli() - 1000}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	p := NewAnthropicProvider(store)
	model := provider.Model{ID: "m", Provider: "anthropic", Api: provider.ApiAnthropicMessages, BaseURL: "http://127.0.0.1:1", ContextWindow: 1000, MaxTokens: 100}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, wait := p.Stream(ctx, model, []msg.Message{msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}}}, provider.StreamOptions{})
	collect(events)
	_, err := wait()
	if err == nil {
		t.Fatal("Stream succeeded with a rejected refresh token")
	}
	want := "Your anthropic login has expired or was revoked. Sign in again with /login anthropic."
	if err.Error() != want {
		t.Errorf("error = %q\nwant    %q", err.Error(), want)
	}
}
