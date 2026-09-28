package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
)

func providerBlock(raw string) msg.ProviderBlock {
	return msg.ProviderBlock{Provider: "anthropic", Type: "providerBlock", Raw: json.RawMessage(raw)}
}

// TestSearchQueriesByToolUseID guards that the query is read from a
// server_tool_use block and keyed by its id, so the paired result block
// (matched on tool_use_id) can show what was searched for.
func TestSearchQueriesByToolUseID(t *testing.T) {
	content := msg.Blocks{
		msg.Text("before"),
		providerBlock(`{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"go generics"}}`),
	}
	queries := searchQueriesByToolUseID(content)
	if queries["srvtoolu_1"] != "go generics" {
		t.Fatalf("queries = %+v, want srvtoolu_1 -> \"go generics\"", queries)
	}
}

// TestSearchResultView_Success guards the committed block's shape for a
// successful search: label, the query as PrimaryArg, and a "→ N results ·
// domain, domain" result row with deduplicated hostnames.
func TestSearchResultView_Success(t *testing.T) {
	pb := providerBlock(`{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[` +
		`{"type":"web_search_result","url":"https://go.dev/blog/generics","title":"Generics"},` +
		`{"type":"web_search_result","url":"https://go.dev/doc/go1.18","title":"Go 1.18"},` +
		`{"type":"web_search_result","url":"https://pkg.go.dev/x","title":"pkg"}` +
		`]}`)
	view, ok := searchResultView(pb, map[string]string{"srvtoolu_1": "go generics"})
	if !ok {
		t.Fatal("searchResultView returned ok=false for a web_search_tool_result block")
	}
	if view.PrimaryArg != "go generics" {
		t.Fatalf("PrimaryArg = %q, want %q", view.PrimaryArg, "go generics")
	}
	if view.Status != CallOK {
		t.Fatalf("Status = %q, want CallOK", view.Status)
	}
	if len(view.ResultLines) != 1 {
		t.Fatalf("ResultLines = %v, want exactly one line", view.ResultLines)
	}
	line := view.ResultLines[0]
	if !strings.Contains(line, "3 results") {
		t.Fatalf("result line = %q, want it to report 3 results", line)
	}
	if !strings.Contains(line, "go.dev") || !strings.Contains(line, "pkg.go.dev") {
		t.Fatalf("result line = %q, want it to list the result domains", line)
	}
}

// TestSearchResultView_SingularResult guards "1 result", not "1 results".
func TestSearchResultView_SingularResult(t *testing.T) {
	pb := providerBlock(`{"type":"web_search_tool_result","tool_use_id":"t1","content":[{"type":"web_search_result","url":"https://go.dev","title":"Go"}]}`)
	view, ok := searchResultView(pb, nil)
	if !ok {
		t.Fatal("searchResultView returned ok=false")
	}
	if len(view.ResultLines) != 1 || !strings.Contains(view.ResultLines[0], "1 result") || strings.Contains(view.ResultLines[0], "1 results") {
		t.Fatalf("result line = %v, want \"1 result\" not \"1 results\"", view.ResultLines)
	}
}

// TestSearchResultView_Error guards the error-code row and CallError status
// for a web_search_tool_result_error content payload.
func TestSearchResultView_Error(t *testing.T) {
	pb := providerBlock(`{"type":"web_search_tool_result","tool_use_id":"t1","content":{"type":"web_search_tool_result_error","error_code":"max_uses_exceeded"}}`)
	view, ok := searchResultView(pb, map[string]string{"t1": "too many searches"})
	if !ok {
		t.Fatal("searchResultView returned ok=false for an error block")
	}
	if view.Status != CallError {
		t.Fatalf("Status = %q, want CallError", view.Status)
	}
	if len(view.ResultLines) != 1 || !strings.Contains(view.ResultLines[0], "max_uses_exceeded") {
		t.Fatalf("result line = %v, want it to carry the error code", view.ResultLines)
	}
}

// TestSearchResultView_NotAResultBlock guards that a server_tool_use block
// (or any non-web_search_tool_result block) gets no row of its own -- its
// query is folded into the paired result's view instead (see
// searchQueriesByToolUseID), not rendered separately.
func TestSearchResultView_NotAResultBlock(t *testing.T) {
	pb := providerBlock(`{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"x"}}`)
	if _, ok := searchResultView(pb, nil); ok {
		t.Fatal("searchResultView returned ok=true for a server_tool_use block, want false")
	}
	other := msg.ProviderBlock{Provider: "openai", Type: "providerBlock", Raw: json.RawMessage(`{"type":"web_search_tool_result"}`)}
	if _, ok := searchResultView(other, nil); ok {
		t.Fatal("searchResultView returned ok=true for a non-anthropic ProviderBlock, want false")
	}
}
