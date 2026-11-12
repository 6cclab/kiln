package builtin

import (
	"context"
	"encoding/base64"
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

// responsesTextSSE is a minimal OpenAI Responses SSE stream (shared wire
// shape between the openai-responses and openai-codex-responses clients):
// one text item and a terminal response.completed.
const responsesTextSSE = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1"}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","status":"in_progress"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","output_index":0,"delta":"hi"}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","status":"completed","content":[{"type":"output_text","text":"hi"}]}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}}

`

// codexJWT builds a minimal (unsigned) JWT whose payload carries the
// chatgpt_account_id claim the codex client extracts. Only the middle
// segment is decoded, so the header/signature segments are placeholders.
func codexJWT(accountID string) string {
	payload := base64.RawURLEncoding.EncodeToString(
		[]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"` + accountID + `"}}`))
	return "h." + payload + ".s"
}

func codexModel(baseURL string) provider.Model {
	return provider.Model{
		ID:            "gpt-5.5",
		Provider:      "openai-codex",
		Api:           provider.ApiOpenAICodexResponses,
		BaseURL:       baseURL,
		ContextWindow: 1000,
		MaxTokens:     100,
	}
}

func streamCodexOnce(t *testing.T, p provider.Provider, model provider.Model) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	events, wait := p.Stream(ctx, model, []msg.Message{
		msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text("hi")}},
	}, provider.StreamOptions{})
	collect(events)
	if _, err := wait(); err != nil {
		t.Fatalf("Stream: %v", err)
	}
}

// TestStreamCodexAPIKeyRoutesToResponses asserts that when openai-codex
// resolves to an api key (stored credential or OPENAI_API_KEY env), the
// request is routed to the standard OpenAI Responses endpoint (path
// /responses, Authorization: Bearer <key>) rather than the chatgpt.com codex
// backend, which only accepts the subscription token.
func TestStreamCodexAPIKeyRoutesToResponses(t *testing.T) {
	run := func(t *testing.T, stored bool) {
		var gotPath, gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, responsesTextSSE)
		}))
		defer srv.Close()
		defer SetOpenAICodexAPIKeyBaseURLForTesting(srv.URL)()

		store := auth.NewFileCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
		if stored {
			if err := store.Modify("openai-codex", func(_ *auth.Credential) (*auth.Credential, error) {
				return &auth.Credential{APIKey: &auth.APIKeyCredential{Key: "sk-key"}}, nil
			}); err != nil {
				t.Fatal(err)
			}
		} else {
			t.Setenv("OPENAI_API_KEY", "sk-key")
		}

		// A codex-backend BaseURL that must be ignored on the api-key path.
		streamCodexOnce(t, NewOpenAICodexProvider(store), codexModel("https://chatgpt.com/backend-api"))

		if gotPath != "/responses" {
			t.Fatalf("request path = %q, want /responses (standard OpenAI Responses endpoint)", gotPath)
		}
		if gotAuth != "Bearer sk-key" {
			t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer sk-key")
		}
	}
	t.Run("stored", func(t *testing.T) { run(t, true) })
	t.Run("env", func(t *testing.T) { run(t, false) })
}

// TestStreamCodexSubscriptionRoutesToCodexBackend asserts the regression
// path: a stored OAuth (ChatGPT subscription) credential still streams via
// the codex client (path /codex/responses, chatgpt-account-id header), and
// takes precedence over an OPENAI_API_KEY env var.
func TestStreamCodexSubscriptionRoutesToCodexBackend(t *testing.T) {
	var gotPath, gotAccountID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAccountID = r.Header.Get("chatgpt-account-id")
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, responsesTextSSE)
	}))
	defer srv.Close()

	// Point the api-key fallback at the same server so that, if routing were
	// wrong, the path assertion still distinguishes the two endpoints.
	defer SetOpenAICodexAPIKeyBaseURLForTesting(srv.URL)()
	t.Setenv("OPENAI_API_KEY", "sk-should-not-win")

	store := auth.NewFileCredentialStore(filepath.Join(t.TempDir(), "credentials.json"))
	future := time.Now().UnixMilli() + 3600_000
	if err := store.Modify("openai-codex", func(_ *auth.Credential) (*auth.Credential, error) {
		return &auth.Credential{OAuth: &auth.OAuthCredential{Refresh: "rt", Access: codexJWT("acc_1"), Expires: future}}, nil
	}); err != nil {
		t.Fatal(err)
	}

	streamCodexOnce(t, NewOpenAICodexProvider(store), codexModel(srv.URL))

	if gotPath != "/codex/responses" {
		t.Fatalf("request path = %q, want /codex/responses (subscription should route to the codex backend)", gotPath)
	}
	if gotAccountID != "acc_1" {
		t.Fatalf("chatgpt-account-id = %q, want acc_1", gotAccountID)
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
