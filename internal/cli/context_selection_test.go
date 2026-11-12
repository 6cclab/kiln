package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	claudememory "github.com/andrepato/harness/internal/claude/memory"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/claude/skills"
	slashcommands "github.com/andrepato/harness/internal/commands"
	"github.com/andrepato/harness/internal/compaction"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/tools"
)

func selectionRows(rows []slashcommands.ContextSelection) string {
	var lines []string
	for _, r := range rows {
		lines = append(lines, fmt.Sprintf("%s|%s|%s|%s|%t", r.Kind, r.Material, r.Name, r.Reason, r.Included))
	}
	return strings.Join(lines, "\n")
}

func TestContextSelectionProjectedCompactionAndRestore(t *testing.T) {
	user := msg.UserMessage{Role: msg.RoleUser, Timestamp: 1, Content: msg.Blocks{msg.Text("review")}}
	call := msg.AssistantMessage{Role: msg.RoleAssistant, Content: msg.Blocks{
		msg.NewToolCall("skill-1", "skill", map[string]any{"skill": "review"}),
		msg.NewToolCall("task-1", "task", map[string]any{"subagent_type": "scout"}),
		msg.NewToolCall("search-1", "capability_search", map[string]any{"query": "database"}),
	}}
	entries := []session.Entry{
		{ID: "u", Type: session.EntryMessage, Message: user},
		{ID: "a", Type: session.EntryMessage, Message: call},
		{ID: "s", Type: session.EntryMessage, Message: msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: "skill-1", ToolName: "skill", Content: msg.Blocks{msg.Text("secret skill body")}}},
		{ID: "t", Type: session.EntryMessage, Message: msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: "task-1", ToolName: "task", Content: msg.Blocks{msg.Text("scout report")}}},
		{ID: "q", Type: session.EntryMessage, Message: msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: "search-1", ToolName: "capability_search", Content: msg.Blocks{msg.Text("- db [skill]: Database help. Match: database. Action: skill(...)")}}},
	}
	projected := compaction.ContextMessages(entries)
	ledger := newContextSelectionLedger()
	ledger.BeginRequest(projected)
	ledger.Record("skill", "review", "pinned", true)
	ledger.RecordSearch("search-1", "mcp", "mcp__fixture__long", "budget-dropped", false)
	for _, want := range []string{
		"skill|body|review|exact-name invocation|true", "agent|result|scout|exact-name invocation|true",
		"skill|metadata|db|search hit|true", "mcp|metadata|mcp__fixture__long|budget-dropped|false",
	} {
		if !strings.Contains(selectionRows(ledger.SnapshotForRequest(projected, "", nil, false)), want) {
			t.Errorf("missing %s", want)
		}
	}
	// A fresh process has no in-memory ledger; restored committed tool results
	// still yield the exact-name and search-hit rows.
	restored := newContextSelectionLedger().SnapshotForRequest(projected, "", nil, false)
	for _, want := range []string{"skill|body|review|exact-name invocation|true", "agent|result|scout|exact-name invocation|true", "skill|metadata|db|search hit|true"} {
		if !strings.Contains(selectionRows(restored), want) {
			t.Errorf("restore missing %s", want)
		}
	}
	if strings.Contains(selectionRows(restored), "budget-dropped") {
		t.Fatal("uncommitted drop appeared after restore")
	}

	// The real compaction projection drops old tool calls and results.
	compact := append(entries, session.Entry{ID: "c", Type: session.EntryCompaction, Summary: "prior work summarized"})
	after := compaction.ContextMessages(compact)
	rows := selectionRows(ledger.SnapshotForRequest(after, "", nil, false))
	for _, stale := range []string{"skill|body|review", "agent|result|scout", "skill|metadata|db", "mcp__fixture__long", "pinned"} {
		if strings.Contains(rows, stale) {
			t.Errorf("stale after compaction: %s in %s", stale, rows)
		}
	}
}

func TestContextSelectionCatalogAndClaudeMemoryCurrentPrompt(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(cwd, "CLAUDE.md"), []byte("Project instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	mem := claudememory.LoadMemory(cwd, 2048)
	autoDir := filepath.Join(t.TempDir(), "memory")
	if err := os.MkdirAll(autoDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(autoDir, "MEMORY.md"), []byte("Project notes"), 0600); err != nil {
		t.Fatal(err)
	}
	auto := claudememory.LoadAutoMemory(cwd, claudememory.AutoMemoryOptions{Directory: autoDir, BudgetTokens: 512})
	if auto.Text == "" {
		t.Fatal("auto-memory fixture did not load")
	}
	list := []skills.Skill{{Name: "review", Description: "Review code", FilePath: filepath.Join(cwd, ".claude", "skills", "review", "SKILL.md")}}
	prompt := strings.Join([]string{mem.Text, auto.Text, discoveryIndex(list, claudesettings.SkillDiscovery{Mode: "catalog"}, 1024)}, "\n\n")
	ledger := newContextSelectionLedger()
	rows := selectionRows(ledger.SnapshotForRequest(nil, prompt, []tools.SkillRecord{{Name: "review"}}, true))
	for _, want := range []string{"skill|metadata|review|catalog listing|true", "memory|body|" + filepath.Join(cwd, "CLAUDE.md") + "|Claude instructions|true", "memory|index|" + filepath.Join(autoDir, "MEMORY.md") + "|Claude auto memory|true"} {
		if !strings.Contains(rows, want) {
			t.Errorf("prompt row %s missing from %s", want, rows)
		}
	}
	if got := selectionRows(ledger.SnapshotForRequest(nil, "", []tools.SkillRecord{{Name: "review"}}, true)); got != "" {
		t.Fatalf("stale prompt content: %s", got)
	}
}

func TestContextSelectionExclusionRequiresLoadedSkill(t *testing.T) {
	ledger := newContextSelectionLedger()
	ledger.ExcludeConfiguredSkills([]tools.SkillRecord{{Name: "review"}}, []string{"review", "typo"})
	rows := selectionRows(ledger.SnapshotForRequest(nil, "", nil, false))
	if !strings.Contains(rows, "skill|metadata|review|excluded|false") || strings.Contains(rows, "typo") {
		t.Fatal(rows)
	}
}
