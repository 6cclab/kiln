package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func withTestTokenURL(t *testing.T, url string) {
	t.Helper()
	orig := anthropicTokenURL
	anthropicTokenURL = url
	t.Cleanup(func() { anthropicTokenURL = orig })
}

func TestExchangeAnthropicCode(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"refresh_token": "rt-123",
			"access_token":  "at-456",
			"expires_in":    3600,
		})
	}))
	defer srv.Close()
	withTestTokenURL(t, srv.URL)

	before := time.Now().UnixMilli()
	tok, err := exchangeAnthropicCode(context.Background(), "code123", "state123", "verifier123", "http://localhost:53692/callback")
	if err != nil {
		t.Fatal(err)
	}
	if tok.Refresh != "rt-123" || tok.Access != "at-456" {
		t.Fatalf("unexpected token: %+v", tok)
	}
	// expires = now + 3600*1000 - 5*60*1000 (5-minute margin baked in).
	wantApprox := before + 3600*1000 - fiveMinuteMarginMs
	if diff := tok.Expires - wantApprox; diff < -2000 || diff > 2000 {
		t.Errorf("expires = %d, want ~%d (5-minute margin applied)", tok.Expires, wantApprox)
	}

	if gotBody["grant_type"] != "authorization_code" {
		t.Errorf("grant_type = %v", gotBody["grant_type"])
	}
	if gotBody["client_id"] != anthropicClientID {
		t.Errorf("client_id = %v, want %s", gotBody["client_id"], anthropicClientID)
	}
	if gotBody["code"] != "code123" || gotBody["code_verifier"] != "verifier123" {
		t.Errorf("unexpected code/verifier in request body: %+v", gotBody)
	}
}

func TestRefreshAnthropicToken(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"refresh_token": "rt-new",
			"access_token":  "at-new",
			"expires_in":    7200,
		})
	}))
	defer srv.Close()
	withTestTokenURL(t, srv.URL)

	tok, err := RefreshAnthropicToken(context.Background(), "rt-old")
	if err != nil {
		t.Fatal(err)
	}
	if tok.Refresh != "rt-new" || tok.Access != "at-new" {
		t.Fatalf("unexpected token: %+v", tok)
	}
	if gotBody["grant_type"] != "refresh_token" || gotBody["refresh_token"] != "rt-old" {
		t.Errorf("unexpected request body: %+v", gotBody)
	}
}

func TestExchangeAnthropicCodeHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	withTestTokenURL(t, srv.URL)

	_, err := exchangeAnthropicCode(context.Background(), "bad", "s", "v", "http://localhost:53692/callback")
	if err == nil {
		t.Fatal("expected an error for a 400 response")
	}
}

func TestParseAuthorizationInput(t *testing.T) {
	cases := []struct {
		in, wantCode, wantState string
	}{
		{"http://localhost:53692/callback?code=abc&state=xyz", "abc", "xyz"},
		{"abc#xyz", "abc", "xyz"},
		{"code=abc&state=xyz", "abc", "xyz"},
		{"just-a-code", "just-a-code", ""},
	}
	for _, c := range cases {
		code, state := parseAuthorizationInput(c.in)
		if code != c.wantCode || state != c.wantState {
			t.Errorf("parseAuthorizationInput(%q) = (%q, %q), want (%q, %q)", c.in, code, state, c.wantCode, c.wantState)
		}
	}
}
