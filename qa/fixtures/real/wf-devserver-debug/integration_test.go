// Integration test against a already-running dev server (PORT env var,
// default 8089). Skipped automatically if nothing is listening, so plain
// `go test ./...` without a server up reports the skip, not a failure; run
// it with the server up to see the real (currently failing) assertion.
package main

import (
	"net/http"
	"os"
	"testing"
)

func baseURL() string {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8089"
	}
	return "http://127.0.0.1:" + port
}

func TestHealthContentType(t *testing.T) {
	resp, err := http.Get(baseURL() + "/health")
	if err != nil {
		t.Skipf("dev server not running on %s: %v", baseURL(), err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}
