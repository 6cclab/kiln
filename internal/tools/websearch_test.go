package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/tool"
)

func runWebSearch(t *testing.T, searxng string, args map[string]any) (string, bool) {
	t.Helper()
	raw, _ := json.Marshal(args)
	res, err := WebSearchTool(searxng).Execute(context.Background(), raw, func(tool.Result) {}, tool.Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	return resultText(res), res.IsError
}

const searxngJSON = `{"query":"go iterators","results":[
 {"title":"Go 1.23 Release Notes","url":"https://go.dev/doc/go1.23","content":"Range over\n  function types."},
 {"title":"Iterators in Go","url":"https://blog.example.com/iter","content":"A tutorial."},
 {"title":"pkg iter","url":"https://pkg.go.dev/iter","content":""}]}`

// TestWebSearchSearxng: with a SearXNG URL, web_search queries the
// instance's JSON API and returns each result's title, URL and snippet.
func TestWebSearchSearxng(t *testing.T) {
	var gotPath, gotQ, gotFormat string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQ, gotFormat = r.URL.Path, r.URL.Query().Get("q"), r.URL.Query().Get("format")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, searxngJSON)
	}))
	defer srv.Close()

	if !WebSearchTool(srv.URL).ServerToolFallback {
		t.Fatal("a SearXNG-backed web_search is not marked ServerToolFallback")
	}
	out, isErr := runWebSearch(t, srv.URL+"/", map[string]any{"query": "go iterators"})
	if isErr {
		t.Fatalf("error: %s", out)
	}
	if gotPath != "/search" || gotQ != "go iterators" || gotFormat != "json" {
		t.Fatalf("request = %s q=%q format=%q", gotPath, gotQ, gotFormat)
	}
	for _, want := range []string{"1. Go 1.23 Release Notes", "https://go.dev/doc/go1.23", "Range over function types.", "3. pkg iter"} {
		if !strings.Contains(out, want) {
			t.Errorf("result lacks %q:\n%s", want, out)
		}
	}

	out, _ = runWebSearch(t, srv.URL, map[string]any{"query": "go iterators", "allowed_domains": []string{"go.dev"}})
	if !strings.Contains(out, "go.dev/doc/go1.23") || !strings.Contains(out, "pkg.go.dev/iter") || strings.Contains(out, "blog.example.com") {
		t.Errorf("allowed_domains go.dev (with subdomains):\n%s", out)
	}
	out, _ = runWebSearch(t, srv.URL, map[string]any{"query": "go iterators", "blocked_domains": []string{"go.dev"}})
	if strings.Contains(out, "go.dev/") || !strings.Contains(out, "blog.example.com") {
		t.Errorf("blocked_domains go.dev:\n%s", out)
	}
	out, isErr = runWebSearch(t, srv.URL, map[string]any{"query": "x", "allowed_domains": []string{"a.com"}, "blocked_domains": []string{"b.com"}})
	if !isErr || !strings.Contains(out, "together") {
		t.Errorf("both domain lists: isErr=%v %s", isErr, out)
	}
}

// TestWebSearchSearxngJSONDisabled: an instance without the JSON format
// enabled answers 403; the error says how to enable it.
func TestWebSearchSearxngJSONDisabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	out, isErr := runWebSearch(t, srv.URL, map[string]any{"query": "x"})
	if !isErr || !strings.Contains(out, "search.formats") {
		t.Fatalf("isErr=%v %s", isErr, out)
	}
}

// TestWebSearchWithoutSearxngIsServerOnly: with no SearXNG URL the tool
// stays Anthropic's server tool and never searches client-side.
func TestWebSearchWithoutSearxngIsServerOnly(t *testing.T) {
	ws := WebSearchTool("")
	if ws.ServerToolFallback || ws.ServerTool == nil {
		t.Fatalf("fallback=%v serverTool=%s", ws.ServerToolFallback, ws.ServerTool)
	}
	if out, isErr := runWebSearch(t, "", map[string]any{"query": "x"}); !isErr || !strings.Contains(out, "server-side") {
		t.Fatalf("isErr=%v %s", isErr, out)
	}
}
