package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestOpenRouterLoginExchangesCodeForPermanentKey drives LoginOpenRouter
// almost end to end: it fetches the callback URL LoginOpenRouter notifies
// (via a fake NotifyAuthURL that hits it in a goroutine, simulating the
// browser redirect), and asserts the returned value is the permanent API
// key from POST api/v1/auth/keys -- not an access/refresh/expiry triple.
func TestOpenRouterLoginExchangesCodeForPermanentKey(t *testing.T) {
	var gotVerifier string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/keys", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["code"] != "code-from-redirect" {
			t.Fatalf("code = %v, want code-from-redirect", body["code"])
		}
		if v, _ := body["code_verifier"].(string); v == "" {
			t.Fatal("expected a non-empty code_verifier")
		} else {
			gotVerifier = v
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"key": "sk-or-permanent-key"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	restore := SetOpenRouterURLsForTesting("https://openrouter.ai/auth", srv.URL+"/api/v1/auth/keys")
	defer restore()

	ia := &openrouterFakeInteraction{}
	key, err := LoginOpenRouter(context.Background(), ia)
	if err != nil {
		t.Fatalf("LoginOpenRouter: %v", err)
	}
	if key != "sk-or-permanent-key" {
		t.Fatalf("key = %q, want sk-or-permanent-key", key)
	}
	if gotVerifier == "" {
		t.Fatal("PKCE verifier never reached the exchange")
	}
}

// openrouterFakeInteraction hits the callback URL LoginOpenRouter notifies
// (simulating the browser completing the redirect) as soon as
// NotifyAuthURL fires, and never answers PromptManualCode.
type openrouterFakeInteraction struct{}

func (f *openrouterFakeInteraction) NotifyAuthURL(authURL, instructions string) {
	go func() {
		u, err := parseCallbackURLFromAuthorize(authURL)
		if err != nil {
			return
		}
		resp, err := http.Get(u + "?code=code-from-redirect")
		if err == nil {
			resp.Body.Close()
		}
	}()
}
func (f *openrouterFakeInteraction) NotifyProgress(message string) {}
func (f *openrouterFakeInteraction) NotifyDeviceCode(userCode, verificationURI string, intervalSeconds, expiresInSeconds int) {
}
func (f *openrouterFakeInteraction) PromptManualCode(ctx context.Context, message, placeholder string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}
func (f *openrouterFakeInteraction) PromptSelect(ctx context.Context, message string, options []SelectOption) (string, error) {
	return "", nil
}

func parseCallbackURLFromAuthorize(authURL string) (string, error) {
	u, err := url.Parse(authURL)
	if err != nil {
		return "", err
	}
	return u.Query().Get("callback_url"), nil
}
