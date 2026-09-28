//go:build e2e

package e2e

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestTUI_WebSearch drives testdata/faux/web-search.yaml through the real
// kiln binary end to end: the model searches (a server_tool_use /
// web_search_tool_result pair, via faux's raw_blocks) and answers, all
// within one assistant message. It asserts the three things the web_search
// feature promises:
//
//  1. the first request declares web_search as an Anthropic server tool
//     ({"type":"web_search_20250305",...}), not a function schema.
//  2. the TUI shows a committed "web search" block with the query and a
//     result count, in scrollback alongside the surrounding text.
//  3. a later turn's request replays the server_tool_use and
//     web_search_tool_result blocks verbatim (Anthropic requires the
//     conversation, including these blocks, sent back on any later turn
//     that needs the context -- the same contract pause_turn relies on).
func TestTUI_WebSearch(t *testing.T) {
	script, err := os.ReadFile("../../testdata/faux/web-search.yaml")
	if err != nil {
		t.Fatal(err)
	}
	proj := scratchProject(t)
	home, sessDir := scratchHome(t)
	addr, srv := startFaux(t, string(script))

	s := startTUI(t, 100, 30, proj, home, sessDir, addr, "--permission-mode", "bypassPermissions")
	waitReady(t, s)

	s.Send("what's new in go generics")
	s.SendKey("enter")
	waitTurnSettled(t, s)

	// (1) the first request declared the server tool, not a function
	// schema.
	reqs := srv.Requests()
	if len(reqs) == 0 {
		t.Fatal("no requests recorded")
	}
	firstBody := string(reqs[0].Body)
	if !strings.Contains(firstBody, `"type":"web_search_20250305"`) {
		t.Fatalf("first request missing the web_search server tool declaration:\n%s", firstBody)
	}
	if !strings.Contains(firstBody, `"max_uses":5`) {
		t.Fatalf("first request's web_search declaration missing max_uses:\n%s", firstBody)
	}

	// (2) the committed block shows in scrollback: label, query, result
	// count and domain, alongside the surrounding text.
	joined := strings.Join(s.Rows(), "\n")
	if !regexp.MustCompile(`(?i)web search`).MatchString(joined) {
		t.Fatalf("transcript missing the web search block label:\n%s", joined)
	}
	if !strings.Contains(joined, "go generics release date") {
		t.Fatalf("transcript missing the search query:\n%s", joined)
	}
	if !regexp.MustCompile(`2 results?`).MatchString(joined) {
		t.Fatalf("transcript missing the result count:\n%s", joined)
	}
	if !strings.Contains(joined, "go.dev") {
		t.Fatalf("transcript missing the result domain:\n%s", joined)
	}
	if !strings.Contains(joined, "Let me search for that.") || !strings.Contains(joined, "Go generics shipped in Go 1.18") {
		t.Fatalf("transcript missing the surrounding text:\n%s", joined)
	}

	// A second turn triggers the next scripted step and a fresh request
	// that must replay the search verbatim.
	s.Send("thanks")
	s.SendKey("enter")
	waitTurnSettled(t, s)

	reqs = srv.Requests()
	if len(reqs) < 2 {
		t.Fatalf("requests = %d, want at least 2", len(reqs))
	}
	lastBody := string(reqs[len(reqs)-1].Body)
	if !strings.Contains(lastBody, `"server_tool_use"`) || !strings.Contains(lastBody, "go generics release date") {
		t.Fatalf("second turn's request missing replayed server_tool_use:\n%s", lastBody)
	}
	if !strings.Contains(lastBody, `"web_search_tool_result"`) || !strings.Contains(lastBody, "go.dev") {
		t.Fatalf("second turn's request missing replayed web_search_tool_result:\n%s", lastBody)
	}
}
