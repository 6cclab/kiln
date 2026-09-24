package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGitHubCopilotTwoStageExchange exercises the flow's two stages against
// one httptest server standing in for github.com and api.github.com:
// device code -> GitHub access token (via a slow_down then a pending, then
// success) -> Copilot session token via copilot_internal/v2/token.
func TestGitHubCopilotTwoStageExchange(t *testing.T) {
	pollCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/login/device/code", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":      "dev-code-1",
			"user_code":        "WXYZ-9876",
			"verification_uri": "https://github.com/login/device",
			"interval":         0.01,
			"expires_in":       60,
		})
	})
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		pollCount++
		switch pollCount {
		case 1:
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "slow_down", "interval": 0.02})
		case 2:
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "gh-token-1"})
		}
	})
	mux.HandleFunc("/copilot_internal/v2/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gh-token-1" {
			t.Fatalf("Authorization = %q, want Bearer gh-token-1", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Editor-Version") != "vscode/1.107.0" {
			t.Fatalf("Editor-Version header missing/wrong: %q", r.Header.Get("Editor-Version"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "copilot-session-1;proxy-ep=proxy.individual.githubcopilot.com;",
			"expires_at": 9999999999.0,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	restore := SetGitHubCopilotSchemeForTesting("http")
	defer restore()

	domain := srv.Listener.Addr().String() // host:port, no scheme
	device, err := startGitHubDeviceFlow(context.Background(), domain)
	if err != nil {
		t.Fatalf("startGitHubDeviceFlow: %v", err)
	}
	if device.userCode != "WXYZ-9876" {
		t.Fatalf("userCode = %q, want WXYZ-9876", device.userCode)
	}

	ghToken, err := pollForGitHubAccessToken(context.Background(), domain, device)
	if err != nil {
		t.Fatalf("pollForGitHubAccessToken: %v", err)
	}
	if ghToken != "gh-token-1" {
		t.Fatalf("ghToken = %q, want gh-token-1", ghToken)
	}
	if pollCount != 3 {
		t.Fatalf("polled %d times, want 3 (slow_down, pending, success)", pollCount)
	}

	// Passed as the enterprise domain so refreshGitHubCopilotAccessToken
	// targets the test server rather than defaulting to github.com; the
	// real default-domain path is exercised by using domain=="" in
	// production, which this test cannot do against a fake server.
	cred, err := refreshGitHubCopilotAccessToken(context.Background(), ghToken, domain)
	if err != nil {
		t.Fatalf("refreshGitHubCopilotAccessToken: %v", err)
	}
	if cred.Refresh != "gh-token-1" {
		t.Fatalf("cred.Refresh = %q, want gh-token-1 (the permanent GitHub token)", cred.Refresh)
	}
	if cred.Access != "copilot-session-1;proxy-ep=proxy.individual.githubcopilot.com;" {
		t.Fatalf("cred.Access = %q", cred.Access)
	}

	// The Copilot session token's proxy-ep claim should resolve to an api.*
	// base URL, matching pi's getBaseUrlFromToken.
	if got := GitHubCopilotBaseURL(cred.Access, ""); got != "https://api.individual.githubcopilot.com" {
		t.Fatalf("GitHubCopilotBaseURL = %q, want https://api.individual.githubcopilot.com", got)
	}
}

func TestGitHubCopilotStaticHeadersHelper(t *testing.T) {
	h := GitHubCopilotHeaders()
	if h["User-Agent"] != "GitHubCopilotChat/0.35.0" {
		t.Fatalf("User-Agent = %q", h["User-Agent"])
	}
	h["User-Agent"] = "mutated"
	if GitHubCopilotHeaders()["User-Agent"] == "mutated" {
		t.Fatal("GitHubCopilotHeaders() must return a fresh copy, not the shared map")
	}
}

func TestNormalizeGitHubDomain(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"company.ghe.com", "company.ghe.com"},
		{"https://company.ghe.com/", "company.ghe.com"},
	}
	for _, c := range cases {
		got, err := NormalizeGitHubDomain(c.in)
		if err != nil {
			t.Fatalf("NormalizeGitHubDomain(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("NormalizeGitHubDomain(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
