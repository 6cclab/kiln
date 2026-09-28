package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// incident is one canned open incident.
type incident struct {
	ID        string `json:"id"`
	Service   string `json:"service"`
	Title     string `json:"title"`
	StartedAt string `json:"started_at"`
	Severity  string `json:"severity"`
}

// incidents is the fixed set of currently open incidents.
var incidents = []incident{
	{
		ID:        "INC-482",
		Service:   "orders-svc",
		Title:     "orders-svc returning 500s on paginated order listings",
		StartedAt: "2026-09-28T14:06:11Z",
		Severity:  "high",
	},
	{
		ID:        "INC-479",
		Service:   "checkout-svc",
		Title:     "checkout-svc p99 latency elevated after cache warm-up job",
		StartedAt: "2026-09-27T09:20:00Z",
		Severity:  "low",
	},
}

// timelines maps incident ID to its canned event timeline, oldest first.
var timelines = map[string][]string{
	"INC-482": {
		"2026-09-28T13:58:02Z  deploy   orders-svc v1.4.2 — pagination rewrite",
		"2026-09-28T14:02:47Z  alert    error budget burn rate 14x on orders-svc (5xx)",
		"2026-09-28T14:06:11Z  incident INC-482 opened: orders-svc returning 500s on paginated order listings",
		"2026-09-28T14:07:30Z  note     on-call: 5xxs correlate with page/per_page query params, not with any single customer",
		"2026-09-28T14:09:15Z  note     on-call: reverting v1.4.2 would also drop an unrelated fix customers are waiting on; investigating root cause instead",
	},
	"INC-479": {
		"2026-09-27T09:15:00Z  deploy   checkout-svc v2.1.0 — warm cache on boot",
		"2026-09-27T09:20:00Z  alert    checkout-svc p99 latency > 800ms",
		"2026-09-27T09:20:00Z  incident INC-479 opened: checkout-svc p99 latency elevated after cache warm-up job",
	},
}

// logLines is a canned corpus of orders-svc log lines, including the panic
// and stack trace that a search for "panic" or "500" should surface. The
// panic matches qa/fixtures/real/mcp-orders-svc/handlers.go's real bug: end
// is clamped to len(orders) but start is not, so a page past the end of the
// 17-row store panics with a low>high slice bounds error.
var logLines = []string{
	"2026-09-28T14:02:41Z INFO  orders-svc: GET /orders?page=1&per_page=10 200 8ms",
	"2026-09-28T14:02:44Z INFO  orders-svc: GET /orders?page=2&per_page=10 200 6ms",
	"2026-09-28T14:02:47Z ERROR orders-svc: GET /orders?page=3&per_page=10 500 2ms",
	"2026-09-28T14:02:47Z ERROR orders-svc: http: panic serving 10.0.4.12:51322: runtime error: slice bounds out of range [20:17]",
	"2026-09-28T14:02:47Z ERROR orders-svc: goroutine 812 [running]:",
	"2026-09-28T14:02:47Z ERROR orders-svc: main.listOrders({0x1400010e000, 0x14000122000}, 0x14000104000)",
	"2026-09-28T14:02:47Z ERROR orders-svc: \t/app/handlers.go:40 +0x1a4",
	"2026-09-28T14:02:47Z ERROR orders-svc: net/http.HandlerFunc.ServeHTTP(...)",
	"2026-09-28T14:02:47Z ERROR orders-svc: \t/usr/local/go/src/net/http/server.go:2136",
	"2026-09-28T14:02:47Z ERROR orders-svc: net/http.(*ServeMux).ServeHTTP(0x1400012c000, {0x104e2a1e0, 0x140001160c0}, 0x14000104000)",
	"2026-09-28T14:02:47Z ERROR orders-svc: \t/usr/local/go/src/net/http/server.go:2514",
	"2026-09-28T14:03:02Z ERROR orders-svc: GET /orders?page=4&per_page=10 500 1ms",
	"2026-09-28T14:03:02Z ERROR orders-svc: http: panic serving 10.0.4.19:51410: runtime error: slice bounds out of range [30:17]",
	"2026-09-28T14:03:18Z INFO  orders-svc: GET /orders?page=1&per_page=25 200 9ms",
	"2026-09-28T14:03:22Z ERROR orders-svc: GET /orders?page=2&per_page=17 500 2ms",
	"2026-09-28T14:03:22Z ERROR orders-svc: http: panic serving 10.0.4.7:51488: runtime error: slice bounds out of range [17:17]",
}

// errorRatePoints is a canned per-minute error-rate series for orders-svc,
// spiking right after the v1.4.2 deploy noted in the incident timeline.
var errorRatePoints = []struct {
	TS   string  `json:"ts"`
	Rate float64 `json:"error_rate"`
}{
	{"2026-09-28T13:55:00Z", 0.001},
	{"2026-09-28T14:00:00Z", 0.001},
	{"2026-09-28T14:05:00Z", 0.412},
	{"2026-09-28T14:10:00Z", 0.487},
	{"2026-09-28T14:15:00Z", 0.451},
	{"2026-09-28T14:20:00Z", 0.439},
}

func registerTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_incidents",
		Description: "List currently open incidents (id, service, title, started_at, severity).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, any, error) {
		return textResult(formatIncidents(incidents)), incidents, nil
	})

	type getIncidentArgs struct {
		ID string `json:"id" jsonschema:"incident id, e.g. INC-482"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_incident",
		Description: "Get full detail and event timeline for one incident by id.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args getIncidentArgs) (*mcp.CallToolResult, any, error) {
		var inc *incident
		for i := range incidents {
			if incidents[i].ID == args.ID {
				inc = &incidents[i]
				break
			}
		}
		if inc == nil {
			return nil, nil, fmt.Errorf("no such incident: %s", args.ID)
		}
		events := timelines[args.ID]
		out := fmt.Sprintf("%s  %s  %s  severity=%s  started=%s\n\ntimeline:\n", inc.ID, inc.Service, inc.Title, inc.Severity, inc.StartedAt)
		for _, e := range events {
			out += "  " + e + "\n"
		}
		return textResult(out), map[string]any{"incident": inc, "timeline": events}, nil
	})

	type searchLogsArgs struct {
		Service string `json:"service" jsonschema:"service to search logs for, e.g. orders-svc"`
		Query   string `json:"query" jsonschema:"substring to search for, e.g. panic, 500, page=3"`
		Limit   int    `json:"limit,omitempty" jsonschema:"max number of matching lines to return (default 20)"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_logs",
		Description: "Search recent logs for a service by substring and return matching lines, most recent last.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args searchLogsArgs) (*mcp.CallToolResult, any, error) {
		limit := args.Limit
		if limit <= 0 {
			limit = 20
		}
		var matches []string
		for _, line := range logLines {
			if args.Service != "" && args.Service != "orders-svc" {
				continue // canned corpus only covers orders-svc
			}
			if args.Query == "" || containsFold(line, args.Query) {
				matches = append(matches, line)
			}
			if len(matches) >= limit {
				break
			}
		}
		if len(matches) == 0 {
			return textResult("no matching log lines"), []string{}, nil
		}
		out := ""
		for _, m := range matches {
			out += m + "\n"
		}
		return textResult(out), matches, nil
	})

	type errorRateArgs struct {
		Service string `json:"service" jsonschema:"service to get the error rate series for, e.g. orders-svc"`
	}
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_error_rate",
		Description: "Get a recent per-minute error-rate series (0..1) for a service.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args errorRateArgs) (*mcp.CallToolResult, any, error) {
		out := fmt.Sprintf("error rate for %s:\n", args.Service)
		for _, p := range errorRatePoints {
			out += fmt.Sprintf("  %s  %.3f\n", p.TS, p.Rate)
		}
		return textResult(out), errorRatePoints, nil
	})
}

func formatIncidents(list []incident) string {
	out := ""
	for _, inc := range list {
		out += fmt.Sprintf("%s  %-12s  %-8s  %s  (started %s)\n", inc.ID, inc.Service, inc.Severity, inc.Title, inc.StartedAt)
	}
	return out
}

func textResult(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func containsFold(s, substr string) bool {
	return substr == "" || strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
