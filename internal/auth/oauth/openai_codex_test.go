package oauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeFlowInteraction is a minimal FlowInteraction for tests: notifications
// are no-ops, PromptSelect returns SelectID (or the first option if unset),
// and PromptManualCode always reports "no manual code" so device-code and
// polling-only tests never race a real prompt.
type fakeFlowInteraction struct {
	SelectID string
}

func (f *fakeFlowInteraction) NotifyAuthURL(url, instructions string) {}
func (f *fakeFlowInteraction) NotifyProgress(message string)          {}
func (f *fakeFlowInteraction) NotifyDeviceCode(userCode, verificationURI string, intervalSeconds, expiresInSeconds int) {
}
func (f *fakeFlowInteraction) PromptManualCode(ctx context.Context, message, placeholder string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}
func (f *fakeFlowInteraction) PromptSelect(ctx context.Context, message string, options []SelectOption) (string, error) {
	if f.SelectID != "" {
		return f.SelectID, nil
	}
	if len(options) > 0 {
		return options[0].ID, nil
	}
	return "", fmt.Errorf("no options")
}

// fakeJWT builds a minimal unsigned JWT whose payload carries the OpenAI
// Codex account-id claim shape, for tests that exercise accountId
// extraction without a real ChatGPT token.
func fakeJWT(t *testing.T, accountID string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(
		`{"https://api.openai.com/auth":{"chatgpt_account_id":%q}}`, accountID)))
	return header + "." + payload + ".sig"
}

func TestOpenAICodexDeviceCodeLoginAndRefresh(t *testing.T) {
	// A fixed sequence: usercode -> pending once -> token -> exchange ->
	// refresh, all against one httptest server whose routes mirror pi's
	// deviceauth endpoints and token endpoint exactly.
	pollCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/accounts/deviceauth/usercode", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_auth_id": "dev-1",
			"user_code":      "ABCD-1234",
			"interval":       0.01,
		})
	})
	mux.HandleFunc("/api/accounts/deviceauth/token", func(w http.ResponseWriter, r *http.Request) {
		pollCount++
		if pollCount == 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"authorization_code": "auth-code-1",
			"code_verifier":      "verifier-1",
		})
	})
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			if r.Form.Get("code") != "auth-code-1" || r.Form.Get("code_verifier") != "verifier-1" {
				t.Fatalf("unexpected exchange params: %v", r.Form)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  fakeJWT(t, "acct-abc"),
				"refresh_token": "refresh-1",
				"expires_in":    3600,
			})
		case "refresh_token":
			if r.Form.Get("refresh_token") != "refresh-1" {
				t.Fatalf("unexpected refresh token: %v", r.Form)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  fakeJWT(t, "acct-abc"),
				"refresh_token": "refresh-2",
				"expires_in":    3600,
			})
		default:
			t.Fatalf("unexpected grant_type: %v", r.Form)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	restore := SetOpenAICodexURLsForTesting(srv.URL)
	defer restore()

	tok, err := loginOpenAICodexDeviceCode(context.Background(), &fakeFlowInteraction{})
	if err != nil {
		t.Fatalf("loginOpenAICodexDeviceCode: %v", err)
	}
	if tok.Refresh != "refresh-1" || tok.AccountID != "acct-abc" {
		t.Fatalf("token = %+v, want refresh=refresh-1 accountID=acct-abc", tok)
	}

	refreshed, err := RefreshOpenAICodexToken(context.Background(), tok.Refresh)
	if err != nil {
		t.Fatalf("RefreshOpenAICodexToken: %v", err)
	}
	if refreshed.Refresh != "refresh-2" || refreshed.AccountID != "acct-abc" {
		t.Fatalf("refreshed = %+v, want refresh=refresh-2 accountID=acct-abc", refreshed)
	}
}

func TestOpenAICodexAccountIDExtractionFailure(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "not-a-jwt",
			"refresh_token": "refresh-1",
			"expires_in":    3600,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	restore := SetOpenAICodexURLsForTesting(srv.URL)
	defer restore()

	_, err := exchangeOpenAICodexCode(context.Background(), "code", "verifier", "redirect")
	if err == nil {
		t.Fatal("expected an error extracting accountId from a non-JWT access token")
	}
}
