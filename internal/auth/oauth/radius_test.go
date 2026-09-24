package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestNormalizeRadiusGatewayURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"radius.pi.dev", "https://radius.pi.dev"},
		{"https://radius.pi.dev/", "https://radius.pi.dev"},
		{"http://localhost:8080///", "http://localhost:8080"},
	}
	for _, c := range cases {
		if got := NormalizeRadiusGatewayURL(c.in); got != c.want {
			t.Errorf("NormalizeRadiusGatewayURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRadiusDeviceCodeLoginAndRefresh(t *testing.T) {
	pollCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/oauth/device", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":      "rad-dev-1",
			"user_code":        "RADIUS-1",
			"verification_uri": "https://radius.pi.dev/device",
			"interval":         0.01,
			"expires_in":       60,
		})
	})
	mux.HandleFunc("/v1/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		switch r.Form.Get("grant_type") {
		case radiusDeviceGrantType:
			pollCount++
			if pollCount == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "rad-at-1", "refresh_token": "rad-rt-1", "expires_in": 3600,
			})
		case "refresh_token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "rad-at-2", "refresh_token": "rad-rt-2", "expires_in": 3600,
			})
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tok, err := loginRadiusWithDeviceCode(context.Background(), srv.URL, &fakeFlowInteraction{})
	if err != nil {
		t.Fatalf("loginRadiusWithDeviceCode: %v", err)
	}
	if tok.Access != "rad-at-1" || tok.Refresh != "rad-rt-1" {
		t.Fatalf("tok = %+v", tok)
	}
	if pollCount != 2 {
		t.Fatalf("polled %d times, want 2", pollCount)
	}

	refreshed, err := RefreshRadiusToken(context.Background(), srv.URL, tok.Refresh)
	if err != nil {
		t.Fatalf("RefreshRadiusToken: %v", err)
	}
	if refreshed.Access != "rad-at-2" {
		t.Fatalf("refreshed = %+v", refreshed)
	}
}

func TestRadiusBrowserLoginViaDiscoveryAndCallback(t *testing.T) {
	restorePort := SetRadiusCallbackPortForTesting(19456) // avoid binding the real 1456
	defer restorePort()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/oauth", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"authorizationEndpoint": "https://radius.pi.dev/authorize"})
	})
	mux.HandleFunc("/v1/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "browser-code-1" {
			t.Fatalf("unexpected token request: %v", r.Form)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "browser-at-1", "refresh_token": "browser-rt-1", "expires_in": 3600,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	discovery, err := LoadRadiusOAuthDiscovery(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("LoadRadiusOAuthDiscovery: %v", err)
	}
	if discovery.AuthorizationEndpoint != "https://radius.pi.dev/authorize" {
		t.Fatalf("discovery = %+v", discovery)
	}

	ia := &radiusFakeInteraction{}
	tok, err := loginRadiusWithBrowser(context.Background(), srv.URL, discovery.AuthorizationEndpoint, ia)
	if err != nil {
		t.Fatalf("loginRadiusWithBrowser: %v", err)
	}
	if tok.Access != "browser-at-1" || tok.Refresh != "browser-rt-1" {
		t.Fatalf("tok = %+v", tok)
	}
}

// radiusFakeInteraction hits the local callback server's redirect URI with
// the expected state as soon as NotifyAuthURL fires, simulating the
// browser completing the authorize redirect.
type radiusFakeInteraction struct{}

func (f *radiusFakeInteraction) NotifyAuthURL(authURL, instructions string) {
	go func() {
		u, err := url.Parse(authURL)
		if err != nil {
			return
		}
		state := u.Query().Get("state")
		redirectURI := radiusRedirectURI()
		resp, err := http.Get(redirectURI + "?code=browser-code-1&state=" + state)
		if err == nil {
			resp.Body.Close()
		}
	}()
}
func (f *radiusFakeInteraction) NotifyProgress(message string) {}
func (f *radiusFakeInteraction) NotifyDeviceCode(userCode, verificationURI string, intervalSeconds, expiresInSeconds int) {
}
func (f *radiusFakeInteraction) PromptManualCode(ctx context.Context, message, placeholder string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}
func (f *radiusFakeInteraction) PromptSelect(ctx context.Context, message string, options []SelectOption) (string, error) {
	return "", nil
}
