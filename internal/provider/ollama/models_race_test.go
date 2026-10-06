package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestModelsRefreshConcurrentWithReads runs RefreshModels (the /model
// switch, on the UI goroutine) against Models (the turn loop's
// Registry.GetModel, on the lane goroutine) at the same time. Run under
// -race it fails if the model list is shared without synchronization.
func TestModelsRefreshConcurrentWithReads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(tagsResponse{Models: []tag{{Name: "m1", Model: "m1"}}})
		case "/api/show":
			_ = json.NewEncoder(w).Encode(showResponse{Capabilities: []string{"completion", "tools"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	p := New(Options{URL: srv.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, m := range p.Models() {
				_ = m.ID
			}
		}
	}()
	for i := 0; i < 20; i++ {
		if err := p.RefreshModels(ctx); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("RefreshModels: %v", err)
		}
	}
	close(stop)
	wg.Wait()
	if got := p.Models(); len(got) != 1 || got[0].ID != "m1" {
		t.Fatalf("Models() = %+v, want one model m1", got)
	}
}
