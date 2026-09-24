package compaction

import (
	"testing"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/session/jsonl"
)

const largeFixture = "../../testdata/sessions/2026-09-23T11-37-47-498Z_01a0ce0e-bcea-7701-a97e-cc374e8c56d1.jsonl"

// loadMainBranch opens a testdata fixture and returns its "main" branch,
// oldest first.
func loadMainBranch(t *testing.T, path string) []session.Entry {
	t.Helper()
	st, err := jsonl.Open(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	tipRaw, _, ok := st.GetValue(session.NamespaceBranchTip, "main")
	if !ok {
		t.Fatal("missing pi.branch.tip/main")
	}
	tip, err := session.GetTypedValue[*string](tipRaw)
	if err != nil {
		t.Fatal(err)
	}
	if tip == nil {
		t.Fatal("branch tip is nil")
	}
	entries, err := st.ScanBranch(session.BranchScan{Start: *tip, Order: "oldestFirst"})
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// TestCalculateContextTokensAgreesWithReportedUsage compares
// CalculateContextTokens against the real fixture's own `usage` rows: since
// the fixture's branch ends on the assistant message that reported the
// usage, and nothing follows it, CalculateContextTokens must reproduce that
// message's totalTokens exactly (this is not a heuristic band in this one
// case — no trailing message exists to estimate).
func TestCalculateContextTokensAgreesWithReportedUsage(t *testing.T) {
	entries := loadMainBranch(t, largeFixture)

	last := entries[len(entries)-1]
	am, ok := last.Message.(msg.AssistantMessage)
	if !ok {
		t.Fatalf("last entry is not an assistant message: %+v", last)
	}
	wantTotal := am.Usage.TotalTokens
	if wantTotal == 0 {
		t.Fatal("fixture's last assistant message has no usage.totalTokens to compare against")
	}

	got := CalculateContextTokens(entries)
	if got.Tokens != wantTotal {
		t.Fatalf("CalculateContextTokens(entries).Tokens = %d, want %d (fixture's reported usage.totalTokens)", got.Tokens, wantTotal)
	}
	if got.TrailingTokens != 0 {
		t.Fatalf("TrailingTokens = %d, want 0 (nothing follows the usage-bearing message)", got.TrailingTokens)
	}
	if got.LastUsageIndex == nil || *got.LastUsageIndex != len(entries)-1 {
		t.Fatalf("LastUsageIndex = %v, want %d", got.LastUsageIndex, len(entries)-1)
	}
}

// TestCalculateContextTokensFallsBackToEstimate drops the branch's final
// assistant message. The most recent remaining usage now belongs to an
// earlier assistant turn, with one toolResult entry trailing it; the
// result must be that usage plus a character-based estimate of that one
// trailing message (an exact, checkable arithmetic fact here, not just a
// sanity band, because the trailing message's own EstimateTokens is
// asserted independently in TestEstimateTokensRoles).
func TestCalculateContextTokensFallsBackToEstimate(t *testing.T) {
	entries := loadMainBranch(t, largeFixture)
	withoutLast := entries[:len(entries)-1]

	var wantUsage int
	var wantUsageIdx int
	for i, e := range withoutLast {
		if am, ok := e.Message.(msg.AssistantMessage); ok && am.Usage.TotalTokens > 0 {
			wantUsage = am.Usage.TotalTokens
			wantUsageIdx = i
		}
	}
	if wantUsage == 0 {
		t.Fatal("no usage-bearing assistant message left to compare against")
	}
	wantTrailing := 0
	for i := wantUsageIdx + 1; i < len(withoutLast); i++ {
		wantTrailing += EstimateTokens(withoutLast[i].Message)
	}

	got := CalculateContextTokens(withoutLast)
	if got.LastUsageIndex == nil || *got.LastUsageIndex != wantUsageIdx {
		t.Fatalf("LastUsageIndex = %v, want %d", got.LastUsageIndex, wantUsageIdx)
	}
	if got.Tokens != wantUsage+wantTrailing {
		t.Fatalf("Tokens = %d, want %d (usage %d + trailing %d)", got.Tokens, wantUsage+wantTrailing, wantUsage, wantTrailing)
	}
}

func TestEstimateTokensRoles(t *testing.T) {
	cases := []struct {
		name string
		m    msg.Message
		want int
	}{
		{"system message estimates zero", msg.SystemMessage{Content: msg.Blocks{msg.Text("ignored")}}, 0},
		{"user text", msg.UserMessage{Content: msg.Blocks{msg.Text("abcd")}}, 1},               // 4 chars -> ceil(4/4)=1
		{"user image", msg.UserMessage{Content: msg.Blocks{msg.Image("image/png", "")}}, 1200}, // 4800/4
		{
			"assistant text+thinking+toolcall",
			msg.AssistantMessage{Content: msg.Blocks{
				msg.Text("12345678"), // 8 chars
				msg.Thinking("1234"), // 4 chars
				msg.NewToolCall("id1", "read", map[string]any{"path": "a.go"}), // "read" (4) + `{"path":"a.go"}` (16) = 20
			}},
			8, // ceil((8+4+20)/4) = ceil(32/4) = 8
		},
		{"tool result text", msg.ToolResultMessage{Content: msg.Blocks{msg.Text("12345678")}}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EstimateTokens(tc.m)
			if got != tc.want {
				t.Fatalf("EstimateTokens = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestShouldCompact(t *testing.T) {
	settings := Settings{Enabled: true, ReserveTokens: 1000, KeepRecentTokens: 2000}
	cases := []struct {
		name          string
		contextTokens int
		contextWindow int
		settings      Settings
		want          bool
	}{
		{"well under budget", 5000, 100000, settings, false},
		{"exactly at threshold does not compact", 99000, 100000, settings, false}, // 100000-1000=99000, needs strictly greater
		{"one token over threshold compacts", 99001, 100000, settings, true},
		{"far over budget", 500000, 100000, settings, true},
		{"disabled never compacts even when far over", 500000, 100000, Settings{Enabled: false, ReserveTokens: 1000}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ShouldCompact(tc.contextTokens, tc.contextWindow, tc.settings)
			if got != tc.want {
				t.Fatalf("ShouldCompact(%d, %d, %+v) = %v, want %v", tc.contextTokens, tc.contextWindow, tc.settings, got, tc.want)
			}
		})
	}
}
