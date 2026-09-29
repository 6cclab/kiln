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

func runWebFetch(t *testing.T, args map[string]any) (string, bool) {
	t.Helper()
	raw, _ := json.Marshal(args)
	res, err := WebFetchTool(nil).Execute(context.Background(), raw, func(tool.Result) {}, tool.Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	return resultText(res), res.IsError
}

// TestWebFetch_HTMLToReadableText: a docs page comes back as its main
// content — title, headings, list items, absolute links, fenced code —
// without scripts, styles or navigation.
func TestWebFetch_HTMLToReadableText(t *testing.T) {
	page := `<!doctype html><html><head><title>Go 1.23 Release Notes</title><style>.x{color:red}</style>
<script>track()</script></head><body><nav>Home | Docs | Blog</nav><main>
<h1>Go 1.23</h1><p>The <code>for</code>-<code>range</code> clause now accepts
iterator functions.</p><ul><li>func(func() bool)</li><li>func(func(K) bool)</li></ul>
<p>See <a href="/ref/spec#For_range">the spec</a>.</p><pre>for x := range seq {
	fmt.Println(x)
}</pre></main><footer>© Google</footer></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, page)
	}))
	defer srv.Close()
	out, isErr := runWebFetch(t, map[string]any{"url": srv.URL + "/doc/go1.23"})
	if isErr {
		t.Fatalf("error: %s", out)
	}
	for _, want := range []string{"Source: " + srv.URL + "/doc/go1.23", "# Go 1.23 Release Notes", "# Go 1.23", "The `for`-`range` clause now accepts iterator functions.",
		"- func(func() bool)", "the spec (" + srv.URL + "/ref/spec#For_range)", "```\nfor x := range seq {\n\tfmt.Println(x)\n}\n```"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	for _, gone := range []string{"track()", "color:red", "Home | Docs", "© Google"} {
		if strings.Contains(out, gone) {
			t.Errorf("kept page chrome %q:\n%s", gone, out)
		}
	}
}

// TestWebFetch_PagesLongText: a long page is returned in chunks with the
// offset to continue from; JSON and plain text come back as they are.
func TestWebFetch_PagesLongText(t *testing.T) {
	long := strings.Repeat("abcdefghij", 3000) // 30000 characters
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/long":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, long)
		case "/json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"ok":true}`)
		case "/missing":
			http.NotFound(w, r)
		case "/bin":
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte{0x89, 'P', 'N', 'G'})
		}
	}))
	defer srv.Close()
	first, _ := runWebFetch(t, map[string]any{"url": srv.URL + "/long"})
	if !strings.Contains(first, "call web_fetch again with offset 20000") {
		t.Errorf("no continuation footer:\n%s", first[len(first)-200:])
	}
	second, _ := runWebFetch(t, map[string]any{"url": srv.URL + "/long", "offset": 20000})
	if strings.Contains(second, "offset 40000") || !strings.HasSuffix(second, "abcdefghij") {
		t.Errorf("second chunk wrong: ...%s", second[len(second)-80:])
	}
	if out, _ := runWebFetch(t, map[string]any{"url": srv.URL + "/json"}); !strings.Contains(out, `{"ok":true}`) {
		t.Errorf("json: %s", out)
	}
	if out, isErr := runWebFetch(t, map[string]any{"url": srv.URL + "/missing"}); !isErr || !strings.Contains(out, "404") {
		t.Errorf("404: err=%v %s", isErr, out)
	}
	if out, isErr := runWebFetch(t, map[string]any{"url": srv.URL + "/bin"}); !isErr || !strings.Contains(out, "image/png") {
		t.Errorf("binary: err=%v %s", isErr, out)
	}
	if out, isErr := runWebFetch(t, map[string]any{"url": "file:///etc/passwd"}); !isErr || !strings.Contains(out, "not an http(s) URL") {
		t.Errorf("file url: err=%v %s", isErr, out)
	}
}
