package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/andrepato/harness/internal/plural"
	"regexp"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/search"
	"github.com/andrepato/harness/internal/tool"
)

// session_search — recall across past sessions, ported from
// src/search/tool.ts's createSessionSearchTool.
//
// This is the harness's entire "learning" story for v1, chosen deliberately
// over self-written skills and agent-authored memory: it needs no judgment
// about what is worth remembering, because everything is already on disk.
// The only question is retrieval.
//
// The hard constraint is cost. A recall tool that returns whole entries
// spends more context than it saves, which is exactly the trap on a small
// window. Hits are therefore capped and snippet-only, and the cap is
// enforced here rather than trusted to the caller.

const (
	sessionSearchDefaultLimit = 5
	sessionSearchMaxLimit     = 10
)

var sessionSearchParameters = json.RawMessage(`{
	"type": "object",
	"properties": {
		"query": {"type": "string", "description": "Keywords to search for."},
		"limit": {"type": "number", "description": "Max results (default 5, max 10)."}
	},
	"required": ["query"]
}`)

const sessionSearchDescription = "Search your past conversations for prior work on a topic. Use this when the user refers to " +
	"something done before, or when you suspect a problem has been solved already. Returns short " +
	"snippets with dates, not full transcripts."

type sessionSearchArgs struct {
	Query string `json:"query"`
	Limit *int   `json:"limit"`
}

var whitespaceRun = regexp.MustCompile(`\s+`)

// SessionSearchTool builds the session_search tool against search, mirroring
// tool.ts's createSessionSearchTool: sync before searching (so work from
// earlier in the current session is findable — Sync is incremental, so this
// stays cheap even with hundreds of sessions), cap limit to
// sessionSearchMaxLimit, and render each hit as one line with an ISO date,
// a whitespace-collapsed snippet, and the session id.
func SessionSearchTool(s *search.Search) *tool.Tool {
	return &tool.Tool{
		Name:        "session_search",
		Label:       "Search past sessions",
		Description: sessionSearchDescription,
		Parameters:  sessionSearchParameters,
		Execute: func(ctx context.Context, args json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
			var in sessionSearchArgs
			if err := json.Unmarshal(args, &in); err != nil {
				return tool.Errorf("invalid arguments: %s", err), nil
			}
			text := strings.TrimSpace(in.Query)
			if text == "" {
				return tool.Text("No query provided."), nil
			}

			if err := s.Sync(ctx, nil); err != nil {
				return tool.Errorf("session search sync failed: %s", err), nil
			}

			limit := sessionSearchDefaultLimit
			if in.Limit != nil {
				limit = *in.Limit
			}
			if limit > sessionSearchMaxLimit {
				limit = sessionSearchMaxLimit
			}

			hits, err := s.SearchSessions(ctx, text, limit)
			if err != nil {
				return tool.Errorf("session search failed: %s", err), nil
			}

			if len(hits) == 0 {
				return tool.Text(fmt.Sprintf("No past sessions mention %q.", text)), nil
			}

			lines := make([]string, 0, len(hits))
			for _, hit := range hits {
				when := "unknown"
				if hit.Top.Timestamp != 0 {
					when = time.UnixMilli(hit.Top.Timestamp).UTC().Format("2006-01-02")
				}
				snippet := strings.TrimSpace(whitespaceRun.ReplaceAllString(hit.Top.Snippet, " "))
				lines = append(lines, fmt.Sprintf("[%s] %s\n  session: %s", when, snippet, hit.SessionID))
			}

			result := fmt.Sprintf("%s:\n\n%s", plural.Count(len(hits), "prior session"), strings.Join(lines, "\n\n"))
			return tool.Text(result), nil
		},
	}
}
