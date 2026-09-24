package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestKimiDeviceLoginAndRefresh(t *testing.T) {
	pollCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/oauth/device_authorization", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "dev-1",
			"user_code":                 "KIMI-1234",
			"verification_uri":          "https://auth.kimi.com/device",
			"verification_uri_complete": "https://auth.kimi.com/device?code=KIMI-1234",
			"interval":                  0.01,
			"expires_in":                60,
		})
	})
	mux.HandleFunc("/api/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		switch r.Form.Get("grant_type") {
		case "urn:ietf:params:oauth:grant-type:device_code":
			pollCount++
			if pollCount == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "at-1", "refresh_token": "rt-1", "expires_in": 3600,
			})
		case "refresh_token":
			if r.Form.Get("refresh_token") != "rt-1" {
				t.Fatalf("unexpected refresh_token: %v", r.Form)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "at-2", "refresh_token": "rt-2", "expires_in": 3600,
			})
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("KIMI_CODE_OAUTH_HOST", srv.URL)

	tok, err := LoginKimi(context.Background(), &fakeFlowInteraction{})
	if err != nil {
		t.Fatalf("LoginKimi: %v", err)
	}
	if tok.Access != "at-1" || tok.Refresh != "rt-1" {
		t.Fatalf("tok = %+v", tok)
	}
	if pollCount != 2 {
		t.Fatalf("polled %d times, want 2 (pending then success)", pollCount)
	}

	refreshed, err := RefreshKimiToken(context.Background(), tok.Refresh)
	if err != nil {
		t.Fatalf("RefreshKimiToken: %v", err)
	}
	if refreshed.Access != "at-2" || refreshed.Refresh != "rt-2" {
		t.Fatalf("refreshed = %+v", refreshed)
	}
}

func TestKimiRefreshUnauthorizedNotRetried(t *testing.T) {
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("KIMI_CODE_OAUTH_HOST", srv.URL)

	_, err := RefreshKimiToken(context.Background(), "dead-token")
	if err == nil {
		t.Fatal("expected an error")
	}
	if calls != 1 {
		t.Fatalf("called token endpoint %d times, want 1 (401 must not be retried)", calls)
	}
}

func TestKimiRefreshRetriesTransientFailures(t *testing.T) {
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at-ok", "refresh_token": "rt-ok", "expires_in": 3600,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("KIMI_CODE_OAUTH_HOST", srv.URL)

	tok, err := RefreshKimiToken(context.Background(), "rt")
	if err != nil {
		t.Fatalf("RefreshKimiToken: %v", err)
	}
	if tok.Access != "at-ok" {
		t.Fatalf("tok = %+v", tok)
	}
	if calls != 2 {
		t.Fatalf("called token endpoint %d times, want 2 (one 500, then success)", calls)
	}
}
