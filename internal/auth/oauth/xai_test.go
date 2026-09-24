package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestXaiDeviceLoginAndRefresh(t *testing.T) {
	pollCount := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth2/device/code", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               "xai-dev-1",
			"user_code":                 "XAI-4242",
			"verification_uri":          "https://auth.x.ai/device",
			"verification_uri_complete": "https://auth.x.ai/device?code=XAI-4242",
			"interval":                  0.01,
			"expires_in":                60,
		})
	})
	mux.HandleFunc("/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		switch r.Form.Get("grant_type") {
		case "urn:ietf:params:oauth:grant-type:device_code":
			pollCount++
			if pollCount == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "authorization_pending"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "xai-at-1", "refresh_token": "xai-rt-1", "expires_in": 3600,
			})
		case "refresh_token":
			if r.Form.Get("refresh_token") != "xai-rt-1" {
				t.Fatalf("unexpected refresh_token: %v", r.Form)
			}
			// xAI may omit refresh_token on refresh; the previous one must
			// be carried forward.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "xai-at-2", "expires_in": 3600,
			})
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	restore := SetXaiURLsForTesting(srv.URL+"/oauth2/device/code", srv.URL+"/oauth2/token")
	defer restore()

	tok, err := LoginXai(context.Background(), &fakeFlowInteraction{})
	if err != nil {
		t.Fatalf("LoginXai: %v", err)
	}
	if tok.Access != "xai-at-1" || tok.Refresh != "xai-rt-1" {
		t.Fatalf("tok = %+v", tok)
	}
	if pollCount != 2 {
		t.Fatalf("polled %d times, want 2", pollCount)
	}

	refreshed, err := RefreshXaiToken(context.Background(), tok.Refresh)
	if err != nil {
		t.Fatalf("RefreshXaiToken: %v", err)
	}
	if refreshed.Access != "xai-at-2" {
		t.Fatalf("refreshed.Access = %q, want xai-at-2", refreshed.Access)
	}
	if refreshed.Refresh != "xai-rt-1" {
		t.Fatalf("refreshed.Refresh = %q, want xai-rt-1 (carried forward when omitted)", refreshed.Refresh)
	}
}

func TestXaiUntrustedVerificationURIRejected(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth2/device/code", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code": "d", "user_code": "u",
			"verification_uri": "javascript:alert(1)",
			"expires_in":       60,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	restore := SetXaiURLsForTesting(srv.URL+"/oauth2/device/code", srv.URL+"/oauth2/token")
	defer restore()

	_, err := requestXaiDeviceCode(context.Background())
	if err == nil {
		t.Fatal("expected an error for a non-https verification URI")
	}
}
