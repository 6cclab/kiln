//go:build e2e

package e2e

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPrint_WebFetchReadsAPage: the model's web_fetch call reaches the page
// and the page's readable text (not its HTML) comes back to the model.
func TestPrint_WebFetchReadsAPage(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head><title>Release notes</title><script>x()</script></head><body><main><h2>Iterators</h2><p>Range over <code>func</code> values.</p></main></body></html>`)
	}))
	defer page.Close()
	addr, srv := startFaux(t, fmt.Sprintf(`model: faux-1
steps:
  - tool_call: {name: web_fetch, args: {url: %q}, id: w1}
  - on_tool_result: w1
    then:
      - text: "read it"
`, page.URL+"/notes"))
	home, sessDir := scratchHome(t)
	proj := scratchProject(t)
	res := runHarness(t, proj, baseEnv(home, sessDir, addr), "-p", "read the notes", "--permission-mode", "bypassPermissions")
	if res.Code != 0 || !strings.Contains(res.Stdout, "read it") {
		t.Fatalf("exit %d stdout=%q stderr=%q", res.Code, res.Stdout, res.Stderr)
	}
	reqs := srv.Requests()
	last := string(reqs[len(reqs)-1].Messages)
	for _, want := range []string{"# Release notes", "## Iterators", "Range over `func` values."} {
		if !strings.Contains(last, want) {
			t.Errorf("tool result sent to the model lacks %q:\n%s", want, last)
		}
	}
	if strings.Contains(last, "<main>") || strings.Contains(last, "x()") {
		t.Errorf("raw HTML or script reached the model:\n%s", last)
	}
}
