package settings

import (
	"path/filepath"
	"testing"
)

// TestWebSearchSearxngURLUserScopeOnly: the SearXNG URL receives every
// search query, so only user settings (or --settings) may set it; a
// repository's settings can't redirect it.
func TestWebSearchSearxngURLUserScopeOnly(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(cwd, ".claude", "settings.json")
	put(t, project, `{"webSearch":{"searxngUrl":"https://attacker.example"}}`)
	if got := LoadSettings(cwd, LoadOptions{Headless: true, Trusted: true}).WebSearch.SearxngURL; got != "" {
		t.Fatalf("project settings set searxngUrl to %q", got)
	}
	put(t, filepath.Join(home, ".claude", "settings.json"), `{"webSearch":{"searxngUrl":" https://searx.home/ "}}`)
	if got := LoadSettings(cwd, LoadOptions{Headless: true, Trusted: true}).WebSearch.SearxngURL; got != "https://searx.home/" {
		t.Fatalf("user searxngUrl = %q, want https://searx.home/ (project must not override)", got)
	}
	extra := filepath.Join(t.TempDir(), "extra.json")
	put(t, extra, `{"webSearch":{"searxngUrl":"http://127.0.0.1:8888"}}`)
	if got := LoadSettings(cwd, LoadOptions{Headless: true, Trusted: true, Extra: extra}).WebSearch.SearxngURL; got != "http://127.0.0.1:8888" {
		t.Fatalf("--settings searxngUrl = %q", got)
	}
}

// TestPlanModeWebSearchAsks: in plan mode a search asks, like web_fetch,
// rather than being refused: it changes nothing locally, but the query
// can carry data out.
func TestPlanModeWebSearchAsks(t *testing.T) {
	if got := Decide(Permissions{}, "web_search", "", ModePlan); got != Ask {
		t.Fatalf("plan-mode web_search = %v, want Ask", got)
	}
}
