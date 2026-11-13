//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPrint_WebSearchSearxngOnOpenAI: with webSearch.searxngUrl in user
// settings, a model served over the OpenAI chat-completions API is offered
// web_search as a function tool, its call reaches SearXNG's JSON API, and
// the results come back to the model.
func TestPrint_WebSearchSearxngOnOpenAI(t *testing.T) {
	var gotQ string
	searx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQ = r.URL.Query().Get("q")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[{"title":"Go 1.23 Release Notes","url":"https://go.dev/doc/go1.23","content":"Range over function types."}]}`)
	}))
	defer searx.Close()
	addr, srv := startFaux(t, `model: faux-1
steps:
  - tool_call: {name: web_search, args: {query: "go 1.23 iterators"}, id: s1}
  - on_tool_result: s1
    then:
      - text: "searched"
`)
	home, sessDir := scratchHome(t)
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(fmt.Sprintf(`{"webSearch":{"searxngUrl":%q}}`, searx.URL)), 0o644); err != nil {
		t.Fatal(err)
	}
	env := baseEnv(home, sessDir, addr)
	env["HARNESS_FAUX_API"] = "openai-completions"
	res := runHarness(t, scratchProject(t), env, "-p", "search for it", "--permission-mode", "bypassPermissions")
	if res.Code != 0 || !strings.Contains(res.Stdout, "searched") {
		t.Fatalf("exit %d stdout=%q stderr=%q", res.Code, res.Stdout, res.Stderr)
	}
	reqs := srv.Requests()
	var names []string
	for _, tool := range reqs[0].Tools {
		names = append(names, tool.Name)
	}
	if !strings.Contains(" "+strings.Join(names, " ")+" ", " web_search ") {
		t.Fatalf("web_search was not offered to the openai-completions model; tools: %v", names)
	}
	if gotQ != "go 1.23 iterators" {
		t.Fatalf("SearXNG query = %q", gotQ)
	}
	last := string(reqs[len(reqs)-1].Messages)
	if !strings.Contains(last, "Go 1.23 Release Notes") || !strings.Contains(last, "https://go.dev/doc/go1.23") {
		t.Fatalf("search results did not reach the model:\n%s", last)
	}
}
