package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/search"
	"github.com/andrepato/harness/internal/tool"
)

const (
	largeFixture = "../../testdata/sessions/2026-09-23T11-37-47-498Z_01a0ce0e-bcea-7701-a97e-cc374e8c56d1.jsonl"
)

func openTestSearch(t *testing.T) *search.Search {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "search.db")
	s, err := search.Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSessionSearchToolResultFormat(t *testing.T) {
	sessDir := t.TempDir()
	data, err := os.ReadFile(largeFixture)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(sessDir, filepath.Base(largeFixture))
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}

	s := openTestSearch(t)

	sst := SessionSearchTool(s)
	if sst.Name != "session_search" {
		t.Fatalf("tool name = %q, want session_search", sst.Name)
	}

	// Pre-sync against just our temp fixtures dir. Execute's own internal
	// Sync call uses the default roots (~/.harness/sessions,
	// ~/.claude/projects); that's additive and harmless here even though
	// this test does not depend on it.
	ctx := context.Background()
	if err := s.Sync(ctx, []string{sessDir}); err != nil {
		t.Fatal(err)
	}

	args, _ := json.Marshal(map[string]any{"query": "codeword"})
	res, err := sst.Execute(ctx, args, nil, tool.Invocation{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute returned an error result: %+v", res)
	}
	text := msg.TextOf(res.Content)
	if !strings.Contains(text, "prior session(s):") {
		t.Errorf("result text missing summary line: %q", text)
	}
	if !strings.Contains(text, "session: 2026-09-23T11-37-47-498Z_01a0ce0e-bcea-7701-a97e-cc374e8c56d1") {
		t.Errorf("result text missing session id line: %q", text)
	}

	emptyArgs, _ := json.Marshal(map[string]any{"query": "zzzznonexistentzzzz"})
	res2, err := sst.Execute(ctx, emptyArgs, nil, tool.Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	text2 := msg.TextOf(res2.Content)
	if !strings.Contains(text2, "No past sessions mention") {
		t.Errorf("no-hit result text = %q", text2)
	}

	noQueryArgs, _ := json.Marshal(map[string]any{"query": "  "})
	res3, err := sst.Execute(ctx, noQueryArgs, nil, tool.Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	text3 := msg.TextOf(res3.Content)
	if text3 != "No query provided." {
		t.Errorf("blank-query result text = %q, want %q", text3, "No query provided.")
	}
}
