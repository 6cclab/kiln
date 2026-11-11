package eval

import (
	"encoding/json"
	"testing"

	"github.com/andrepato/harness/internal/testkit/faux"
	"github.com/andrepato/harness/internal/testkit/scenario"
)

func TestRequestBudgetIncludesTools(t *testing.T) {
	art := Artifacts{FauxRequests: []faux.Request{{System: "short", Body: json.RawMessage(`{"system":"short","tools":[{"description":"This substantial tool description must count toward the context budget","input_schema":{"type":"object"}}],"messages":[]}`)}}}
	check := scenario.Check{Type: "request_tokens", Max: 10}
	if got := checkRequestTokens(check, art); got.Pass || got.Skipped {
		t.Fatalf("%+v", got)
	}
	check.Max = 1000
	if got := checkRequestTokens(check, art); !got.Pass {
		t.Fatalf("%+v", got)
	}
	if got := checkRequestTokens(check, Artifacts{}); !got.Skipped {
		t.Fatalf("%+v", got)
	}
}
