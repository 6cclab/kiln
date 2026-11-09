package mods

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestHTTPFetch drives $.http.fetch against a local server: method, headers
// and body go out, and { status, ok, headers (lowercased), text } come back.
// Not parallel: it passes the server URL through the process environment,
// which $.env.get reads.
func TestHTTPFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "%s %s %s", r.Method, string(b), r.Header.Get("X-Mod"))
	}))
	defer srv.Close()

	t.Setenv("KILN_MODS_TEST_URL", srv.URL)

	h := newHarness(t, Options{Cwd: t.TempDir(), LookupEnv: os.LookupEnv})
	h.load(t, "httptier")
	h.e.SessionStart(ctxT(t))
	got := runCommand(t, h, "fetch", "")

	var out struct {
		Status float64 `json:"status"`
		OK     bool    `json:"ok"`
		CType  string  `json:"ctype"`
		Body   string  `json:"body"`
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("fetch = %q: %v", got, err)
	}
	if out.Status != 200 || !out.OK {
		t.Errorf("status = %v, ok = %v", out.Status, out.OK)
	}
	if out.CType != "text/plain" {
		t.Errorf("headers['content-type'] = %q (header keys must be lowercased)", out.CType)
	}
	if out.Body != "POST ping kiln" {
		t.Errorf("body = %q, want %q (method/body/header round trip)", out.Body, "POST ping kiln")
	}
	if strings.Contains(h.logs.String(), "is not available in kiln") {
		t.Errorf("a $ call was rejected:\n%s", h.logs)
	}
}
