package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/tool"
)

// web_search has two implementations behind one tool.
//
// On a model served over the Anthropic Messages API it is a provider
// server tool. Anthropic runs the search on its own infrastructure: the
// request declares the tool with webSearchServerToolJSON below, and the
// response stream carries the query and results back as server_tool_use /
// web_search_tool_result content blocks, which the Anthropic client
// (internal/provider/api/anthropic_messages.go) turns into
// msg.ProviderBlock entries rather than a msg.ToolCall. Execute never runs
// for it there.
//
// Every other provider's client skips a server tool. When the user has
// configured a SearXNG instance (settings "webSearch.searxngUrl"), the tool
// is marked ServerToolFallback: the harness then declares it to those
// models as an ordinary function tool (internal/harness/turn.go,
// buildStreamOptions) and Execute queries SearXNG's JSON API. Without a
// SearXNG URL the tool stays Anthropic-only, as before.
//
// Registered as a resident tool (internal/cli/mcp.go's residentAll);
// chat.go leaves it out of the active tool set for a session whose
// permission settings carry a deny rule for "WebSearch" (Claude Code's
// name for it).

// webSearchServerToolJSON is the Anthropic tool-declaration block sent
// verbatim in place of a function schema. max_uses caps how many searches
// one turn may run, matching Anthropic's own documented default.
var webSearchServerToolJSON = json.RawMessage(`{"type":"web_search_20250305","name":"web_search","max_uses":5}`)

const webSearchDescription = "Search the web. Executed server-side by Anthropic; only available when the " +
	"active model is served over the Anthropic Messages API."

const searxngDescription = "Search the web and return the top results' titles, URLs and snippets. It does not " +
	"read the pages: use web_fetch on a result URL to read it. allowed_domains keeps only results from those hosts; " +
	"blocked_domains drops results from them. Use one or the other, not both."

var searxngParameters = json.RawMessage(`{
	"type": "object",
	"properties": {
		"query": {"type": "string", "description": "The search query"},
		"allowed_domains": {"type": "array", "items": {"type": "string"}, "description": "Only return results from these domains"},
		"blocked_domains": {"type": "array", "items": {"type": "string"}, "description": "Never return results from these domains"}
	},
	"required": ["query"]
}`)

const (
	searxngTimeout    = 20 * time.Second
	searxngMaxBytes   = 2 << 20
	searxngMaxResults = 10
)

// WebSearchTool builds the web_search resident tool. searxngURL is the
// base URL of a SearXNG instance (settings "webSearch.searxngUrl"); empty
// leaves the tool Anthropic-only.
func WebSearchTool(searxngURL string) *tool.Tool {
	t := &tool.Tool{
		Name:        "web_search",
		Label:       "web search",
		Description: webSearchDescription,
		Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`),
		ServerTool:  webSearchServerToolJSON,
		Execute: func(ctx context.Context, args json.RawMessage, onUpdate tool.Update, inv tool.Invocation) (tool.Result, error) {
			// Never legitimately reached: Anthropic resolves the call
			// itself, and other providers are never offered the tool.
			// Refuse rather than pretend to search.
			return tool.Errorf("web_search is executed server-side by Anthropic and cannot be run directly"), nil
		},
	}
	base := strings.TrimRight(strings.TrimSpace(searxngURL), "/")
	if base == "" {
		return t
	}
	client := &http.Client{Timeout: searxngTimeout}
	t.Description = searxngDescription
	t.Parameters = searxngParameters
	t.ServerToolFallback = true
	t.Execute = func(ctx context.Context, args json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
		var in struct {
			Query          string   `json:"query"`
			AllowedDomains []string `json:"allowed_domains"`
			BlockedDomains []string `json:"blocked_domains"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return tool.Errorf("invalid arguments: %s", err), nil
		}
		in.Query = strings.TrimSpace(in.Query)
		if in.Query == "" {
			return tool.Errorf("query is empty"), nil
		}
		if len(in.AllowedDomains) > 0 && len(in.BlockedDomains) > 0 {
			return tool.Errorf("allowed_domains and blocked_domains can't be used together"), nil
		}
		results, err := searxngSearch(ctx, client, base, in.Query)
		if err != nil {
			return tool.Errorf("%s", err), nil
		}
		return tool.Text(formatSearchResults(in.Query, filterResults(results, in.AllowedDomains, in.BlockedDomains))), nil
	}
	return t
}

type searxngResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

// searxngSearch runs query against the SearXNG instance at base through
// its JSON API (GET /search?format=json), which an instance serves only
// when "json" is listed under search.formats in its settings.yml.
func searxngSearch(ctx context.Context, client *http.Client, base, query string) ([]searxngResult, error) {
	u, err := url.Parse(base + "/search")
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("webSearch.searxngUrl is not an http(s) URL: %q", base)
	}
	u.RawQuery = url.Values{"q": {query}, "format": {"json"}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("SearXNG request failed: %s", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, searxngMaxBytes))
	if err != nil {
		return nil, fmt.Errorf("reading the SearXNG response: %s", err)
	}
	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("SearXNG refused the JSON API (HTTP 403): add json to search.formats in the instance's settings.yml")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SearXNG returned HTTP %d", resp.StatusCode)
	}
	var out struct {
		Results []searxngResult `json:"results"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("SearXNG did not return JSON: %s", err)
	}
	return out.Results, nil
}

// filterResults applies allowed_domains / blocked_domains to results. A
// domain matches its own host and every subdomain of it.
func filterResults(results []searxngResult, allowed, blocked []string) []searxngResult {
	var out []searxngResult
	for _, r := range results {
		u, err := url.Parse(r.URL)
		if err != nil || u.Hostname() == "" {
			continue
		}
		host := strings.ToLower(u.Hostname())
		if len(allowed) > 0 && !hostInDomains(host, allowed) {
			continue
		}
		if hostInDomains(host, blocked) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func hostInDomains(host string, domains []string) bool {
	for _, d := range domains {
		d = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(d), "."))
		if d != "" && (host == d || strings.HasSuffix(host, "."+d)) {
			return true
		}
	}
	return false
}

func formatSearchResults(query string, results []searxngResult) string {
	if len(results) == 0 {
		return fmt.Sprintf("No results for %q.", query)
	}
	if len(results) > searxngMaxResults {
		results = results[:searxngMaxResults]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Results for %q:\n", query)
	for i, r := range results {
		title := strings.TrimSpace(r.Title)
		if title == "" {
			title = r.URL
		}
		fmt.Fprintf(&b, "\n%d. %s\n   %s\n", i+1, title, r.URL)
		if s := strings.Join(strings.Fields(r.Content), " "); s != "" {
			fmt.Fprintf(&b, "   %s\n", s)
		}
	}
	return b.String()
}
