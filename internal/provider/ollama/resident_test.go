package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/provider"
)

// TestResidentListsLoadedModels: a GET /api/ps returning two entries gives
// their ids, normalising a ":latest" suffix the same way other Ollama
// endpoints' ids are normalised elsewhere in this package.
func TestResidentListsLoadedModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ps" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[{"model":"qwen3.8:latest"},{"model":"gpt-oss:20b"}]}`))
	}))
	defer srv.Close()

	p := New(Options{URL: srv.URL})
	ids, err := p.Resident(context.Background())
	if err != nil {
		t.Fatalf("Resident() error = %v", err)
	}
	want := []string{"qwen3.8", "gpt-oss:20b"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Errorf("ids[%d] = %q, want %q", i, ids[i], want[i])
		}
	}
}

// TestResidentHTTPError: a 500 from /api/ps gives an error, not an empty
// (and so falsely "nothing resident") list.
func TestResidentHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := New(Options{URL: srv.URL})
	if _, err := p.Resident(context.Background()); err == nil {
		t.Fatal("Resident() error = nil, want an error for HTTP 500")
	}
}

// TestResidentTimesOut: a handler that never answers must not hang
// Resident -- its own 2s bound (residentTimeout) applies regardless of
// the caller's context.
func TestResidentTimesOut(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer func() { close(block); srv.Close() }()

	p := New(Options{URL: srv.URL})
	start := time.Now()
	_, err := p.Resident(context.Background())
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Resident() error = nil, want a timeout error")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("Resident() took %s, want it bounded near residentTimeout (%s)", elapsed, residentTimeout)
	}
}

// TestHostNormalisesURL: Host is the provider id plus the normalised base
// URL (trailing slash trimmed), regardless of the model given -- every
// model on this provider shares one host.
func TestHostNormalisesURL(t *testing.T) {
	p := New(Options{URL: "http://192.168.3.168:11434/"})
	host := p.Host(provider.Model{ID: "qwen3.8"})
	if host != "ollama http://192.168.3.168:11434" {
		t.Errorf("Host() = %q, want %q", host, "ollama http://192.168.3.168:11434")
	}
	// A different model on the same provider gets the same Host value.
	if other := p.Host(provider.Model{ID: "gpt-oss:20b"}); other != host {
		t.Errorf("Host() differs per model: %q vs %q", host, other)
	}
}

// TestHostDefaultsBareURL confirms Host uses baseURL()'s own default
// (DefaultURL) when Options.URL is empty, same as every other endpoint.
func TestHostDefaultsBareURL(t *testing.T) {
	p := New(Options{})
	if got := p.Host(provider.Model{}); got != "ollama "+DefaultURL {
		t.Errorf("Host() = %q, want %q", got, "ollama "+DefaultURL)
	}
}
