package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/commands"
)

func testRegistry() *commands.Registry {
	reg := commands.NewRegistry()
	reg.Add(commands.StaticSource(commands.OriginBuiltin, []commands.Command{
		{Name: "model", Description: "switch model", ArgumentHint: "<provider>",
			Run: func(ctx context.Context, args string) (commands.Result, error) { return commands.Result{}, nil },
			ArgumentCompletions: func(prefix string) []commands.Completion {
				all := []commands.Completion{
					{Value: "anthropic", Label: "anthropic", Description: "Claude"},
					{Value: "openai", Label: "openai", Description: "GPT"},
				}
				var out []commands.Completion
				for _, c := range all {
					if strings.HasPrefix(c.Value, prefix) {
						out = append(out, c)
					}
				}
				return out
			},
		},
		{Name: "modal-test", Description: "open a modal",
			Run: func(ctx context.Context, args string) (commands.Result, error) { return commands.Result{}, nil }},
		{Name: "memory", Description: "add memory",
			Run: func(ctx context.Context, args string) (commands.Result, error) { return commands.Result{}, nil }},
	}))
	return reg
}

func TestBuildPopup_SlashCommandMatching(t *testing.T) {
	reg := testRegistry()
	p := BuildPopup(reg, "/tmp", "/mod", 4)
	if p == nil {
		t.Fatal("expected a popup for /mod")
	}
	if p.Kind != KindSlashCommand {
		t.Fatalf("kind = %v, want KindSlashCommand", p.Kind)
	}
	if len(p.Items) == 0 {
		t.Fatal("expected at least one match")
	}
	// slashCommandSuggestions filters by prefix, not fuzzy score (finding
	// 5): "modal-test" and "model" both start with "mod", so both match,
	// in registry order (alphabetical-by-qualified-name) — "modal-test"
	// before "model".
	values := []string{p.Items[0].Value, p.Items[1].Value}
	if values[0] != "modal-test" || values[1] != "model" {
		t.Fatalf("order = %v, want [modal-test, model] (both are prefix matches for \"mod\", in registry order)", values)
	}
	if p.Start != 0 || p.End != 4 {
		t.Fatalf("range = [%d,%d), want [0,4) (the whole \"/mod\", leading slash included)", p.Start, p.End)
	}
}

// TestBuildPopup_SlashCommandIsPrefixNotFuzzy is finding 5's regression
// case: typing "/co" must list only commands whose name actually starts
// with "co" ("/compact", "/context"), never "/doctor" — under fuzzy
// matching, "doctor"'s letters happen to contain an out-of-order match
// for "c"/"o" ("d-o-c-t-o-r"), which is exactly the bug this pins.
func TestBuildPopup_SlashCommandIsPrefixNotFuzzy(t *testing.T) {
	reg := commands.NewRegistry()
	reg.Add(commands.StaticSource(commands.OriginBuiltin, []commands.Command{
		{Name: "compact", Description: "compact the conversation",
			Run: func(ctx context.Context, args string) (commands.Result, error) { return commands.Result{}, nil }},
		{Name: "context", Description: "show context usage",
			Run: func(ctx context.Context, args string) (commands.Result, error) { return commands.Result{}, nil }},
		{Name: "doctor", Description: "diagnose the install",
			Run: func(ctx context.Context, args string) (commands.Result, error) { return commands.Result{}, nil }},
	}))

	p := BuildPopup(reg, "/tmp", "/co", 3)
	if p == nil {
		t.Fatal("expected a popup for /co")
	}
	var values []string
	for _, it := range p.Items {
		values = append(values, it.Value)
	}
	for _, v := range values {
		if v == "doctor" {
			t.Fatalf("items = %v, want \"doctor\" excluded — it is not a prefix match for \"co\"", values)
		}
	}
	if len(values) != 2 || values[0] != "compact" || values[1] != "context" {
		t.Fatalf("items = %v, want exactly [compact, context] (alphabetical, both real prefix matches)", values)
	}
}

// TestBuildPopup_SlashCommandPrefixIsCaseInsensitive covers matching
// "/CO" (or any other casing) the same as "/co".
func TestBuildPopup_SlashCommandPrefixIsCaseInsensitive(t *testing.T) {
	reg := commands.NewRegistry()
	reg.Add(commands.StaticSource(commands.OriginBuiltin, []commands.Command{
		{Name: "context", Description: "show context usage",
			Run: func(ctx context.Context, args string) (commands.Result, error) { return commands.Result{}, nil }},
	}))
	p := BuildPopup(reg, "/tmp", "/CO", 3)
	if p == nil || len(p.Items) != 1 || p.Items[0].Value != "context" {
		t.Fatalf("expected /CO to match \"context\" case-insensitively, got %+v", p)
	}
}

func TestBuildPopup_SlashCommandEmptyPrefixListsAll(t *testing.T) {
	reg := testRegistry()
	p := BuildPopup(reg, "/tmp", "/", 1)
	if p == nil {
		t.Fatal("expected a popup for bare /")
	}
	if len(p.Items) != len(reg.List()) {
		t.Fatalf("got %d items, want %d (every command)", len(p.Items), len(reg.List()))
	}
}

func TestBuildPopup_NoSlashNoPopup(t *testing.T) {
	reg := testRegistry()
	if p := BuildPopup(reg, "/tmp", "hello /mod", 10); p != nil {
		t.Fatalf("expected nil popup for a line not starting with /, got %+v", p)
	}
}

func TestBuildPopup_ArgumentCompletion(t *testing.T) {
	reg := testRegistry()
	line := "/model anth"
	p := BuildPopup(reg, "/tmp", line, len(line))
	if p == nil {
		t.Fatal("expected a popup for /model anth")
	}
	if p.Kind != KindArgument {
		t.Fatalf("kind = %v, want KindArgument", p.Kind)
	}
	if len(p.Items) != 1 || p.Items[0].Value != "anthropic" {
		t.Fatalf("items = %+v, want just anthropic", p.Items)
	}
	wantStart := len([]rune("/model "))
	if p.Start != wantStart || p.End != len([]rune(line)) {
		t.Fatalf("range = [%d,%d), want [%d,%d)", p.Start, p.End, wantStart, len([]rune(line)))
	}
}

func TestBuildPopup_UnknownCommandArgsNoPopup(t *testing.T) {
	reg := testRegistry()
	if p := BuildPopup(reg, "/tmp", "/nope some args", 15); p != nil {
		t.Fatalf("expected nil popup for an unknown command's arguments, got %+v", p)
	}
}

func TestBuildPopup_CommandWithoutArgumentCompletionsNoPopup(t *testing.T) {
	reg := testRegistry()
	if p := BuildPopup(reg, "/tmp", "/memory some text", 18); p != nil {
		t.Fatalf("expected nil popup for a command with no ArgumentCompletions, got %+v", p)
	}
}

func TestBuildPopup_AtFileCompletion(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "math.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	line := "look at @src/ma"
	p := BuildPopup(nil, dir, line, len([]rune(line)))
	if p == nil {
		t.Fatal("expected a popup for @src/ma")
	}
	if p.Kind != KindFile {
		t.Fatalf("kind = %v, want KindFile", p.Kind)
	}
	if len(p.Items) == 0 {
		t.Fatal("expected at least one file match")
	}
	if p.Items[0].Value != "src/math.js" {
		t.Fatalf("best match = %q, want %q (items: %+v)", p.Items[0].Value, "src/math.js", p.Items)
	}
	replacement, _ := p.Accept()
	if replacement != "@src/math.js " {
		t.Fatalf("Accept() = %q, want %q", replacement, "@src/math.js ")
	}
}

// TestBuildPopup_AtFileCompletionTieBreak matches scoreEntry's own tie
// order (autocomplete.js's getFuzzyFileSuggestions sort): same score, same
// depth, same length -> alphabetical by relative path. "main.js" sorts
// before "math.js" ('i' < 't'), even though both are equally good prefix
// matches for "ma".
func TestBuildPopup_AtFileCompletionTieBreak(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"math.js", "main.js"} {
		if err := os.WriteFile(filepath.Join(dir, "src", name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	line := "@src/ma"
	p := BuildPopup(nil, dir, line, len([]rune(line)))
	if p == nil || len(p.Items) < 2 {
		t.Fatalf("expected two tied matches, got %+v", p)
	}
	if p.Items[0].Value != "src/main.js" || p.Items[1].Value != "src/math.js" {
		t.Fatalf("order = [%s, %s], want [src/main.js, src/math.js] (alphabetical tie-break)", p.Items[0].Value, p.Items[1].Value)
	}
}

func TestBuildPopup_AtEmptyPrefixListsCwd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	line := "@"
	p := BuildPopup(nil, dir, line, 1)
	if p == nil {
		t.Fatal("expected a popup for bare @")
	}
	found := false
	for _, it := range p.Items {
		if it.Value == "a.txt" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a.txt among items, got %+v", p.Items)
	}
}

func TestPopup_MoveWraps(t *testing.T) {
	p := &Popup{Items: []AutocompleteItem{{Value: "a"}, {Value: "b"}, {Value: "c"}}}
	p.Move(-1)
	if p.Selected != 2 {
		t.Fatalf("Move(-1) from 0 = %d, want 2 (wrap to bottom)", p.Selected)
	}
	p.Move(1)
	if p.Selected != 0 {
		t.Fatalf("Move(1) from 2 = %d, want 0 (wrap to top)", p.Selected)
	}
}

func TestPopup_AcceptSlashCommand(t *testing.T) {
	p := &Popup{Kind: KindSlashCommand, Items: []AutocompleteItem{{Value: "model"}}, Start: 1, End: 4}
	replacement, col := p.Accept()
	if replacement != "/model " {
		t.Fatalf("replacement = %q, want %q", replacement, "/model ")
	}
	if want := 1 + len("/model "); col != want {
		t.Fatalf("col = %d, want %d", col, want)
	}
}

func TestPopup_AcceptArgument(t *testing.T) {
	p := &Popup{Kind: KindArgument, Items: []AutocompleteItem{{Value: "anthropic"}}, Start: 7, End: 11}
	replacement, _ := p.Accept()
	if replacement != "anthropic" {
		t.Fatalf("replacement = %q, want %q (no forced trailing space)", replacement, "anthropic")
	}
}

func TestPopup_AcceptFileDirectoryNoTrailingSpace(t *testing.T) {
	p := &Popup{Kind: KindFile, Items: []AutocompleteItem{{Value: "src/"}}, Start: 0, End: 4}
	replacement, _ := p.Accept()
	if replacement != "@src/" {
		t.Fatalf("replacement = %q, want %q (no trailing space so completion can continue)", replacement, "@src/")
	}
}

func TestPopup_RenderWidths(t *testing.T) {
	p := &Popup{
		Kind: KindSlashCommand,
		Items: []AutocompleteItem{
			{Value: "model", Label: "/model <provider>", Description: "switch model"},
			{Value: "memory", Label: "/memory", Description: "add memory"},
			{Value: "cost", Label: "/cost", Description: "show cost"},
		},
	}
	for _, width := range []int{20, 40, 80} {
		lines := p.Render(width, 5)
		if len(lines) == 0 {
			t.Fatalf("width %d: expected some rendered lines", width)
		}
		for _, l := range lines {
			if w := VisibleWidth(l); w != width {
				t.Errorf("width %d: line %q has visible width %d", width, l, w)
			}
		}
	}
}

func TestPopup_RenderSelectedRowUsesSuggestionColour(t *testing.T) {
	SetColorEnabled(true)
	defer SetColorEnabled(false)
	p := &Popup{
		Kind:     KindSlashCommand,
		Selected: 1,
		Items: []AutocompleteItem{
			{Value: "model", Label: "/model"},
			{Value: "memory", Label: "/memory"},
		},
	}
	lines := p.Render(40, 5)
	if len(lines) < 2 {
		t.Fatalf("expected at least two rows, got %d", len(lines))
	}
	// kiln: the selected row's raised background wraps the whole row, and
	// its command is amber; unselected rows are still styled (command in
	// ink, description dim), just without the raised background.
	if !strings.Contains(lines[1], "\x1b[") {
		t.Fatalf("selected row %q does not appear styled", lines[1])
	}
	if !strings.Contains(lines[0], "\x1b[") {
		t.Fatalf("unselected row %q should still carry ink/dim styling", lines[0])
	}
	if strings.Contains(lines[0], "48;2;36;31;24") {
		t.Fatalf("unselected row %q should not carry the raised background", lines[0])
	}
}

// TestPopup_RenderClampsToMaxRows: Claude Code shows a window of the list
// with no scroll indicator (autocomplete-at.txt lists five rows of many).
func TestPopup_RenderClampsToMaxRows(t *testing.T) {
	items := make([]AutocompleteItem, 10)
	for i := range items {
		items[i] = AutocompleteItem{Value: string(rune('a' + i))}
	}
	p := &Popup{Items: items, Selected: 0}
	lines := p.Render(40, 3)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
}

// TestPopup_RenderMatchesReferenceColumns pins the slash layout to the
// kiln design handoff's palette: two-space indent, command padded to 10
// columns (%-10s; a longer command overflows by one space), then the
// description on the same row, truncated (not wrapped) to fit.
func TestPopup_RenderMatchesReferenceColumns(t *testing.T) {
	SetColorEnabled(false)
	defer SetColorEnabled(true)
	p := &Popup{Kind: KindSlashCommand, Items: []AutocompleteItem{
		{Value: "model", Description: "Set the AI model for Claude Code (currently Opus 5 (1M context))"},
		{Value: "track-work", Description: "Track work as epics and stories in the self-hosted Task Tracker."},
	}}
	got := p.Render(100, 5)
	want := []string{
		"  /model    Set the AI model for Claude Code (currently Opus 5 (1M context))",
		"  /track-work Track work as epics and stories in the self-hosted Task Tracker.",
	}
	if len(got) != len(want) {
		t.Fatalf("rows = %d, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if strings.TrimRight(got[i], " ") != want[i] {
			t.Errorf("row %d:\n got: %q\nwant: %q", i, strings.TrimRight(got[i], " "), want[i])
		}
	}
	files := &Popup{Kind: KindFile, Items: []AutocompleteItem{{Value: "math.js"}}}
	if row := strings.TrimRight(files.Render(100, 5)[0], " "); row != "  + math.js" {
		t.Errorf("file row = %q, want %q", row, "  + math.js")
	}
}

func TestFuzzyMatch_PrefixBeatsSubstring(t *testing.T) {
	prefixScore, ok := fuzzyMatch("mod", "model")
	if !ok {
		t.Fatal("expected model to match mod")
	}
	substrScore, ok := fuzzyMatch("mod", "commodore")
	if !ok {
		t.Fatal("expected commodore to match mod")
	}
	if !(prefixScore < substrScore) {
		t.Fatalf("prefix score %v should beat (be lower than) substring score %v", prefixScore, substrScore)
	}
}

func TestFuzzyMatch_NoMatch(t *testing.T) {
	if _, ok := fuzzyMatch("xyz", "model"); ok {
		t.Fatal("expected no match")
	}
}

// TestPopup_ClippedListCountsHiddenItems: a list longer than the rows it
// is given ends in a count of the hidden items, and stays within maxRows.
func TestPopup_ClippedListCountsHiddenItems(t *testing.T) {
	var items []AutocompleteItem
	for _, name := range []string{"agents", "bashes", "clear", "compact", "config", "context", "cost", "doctor"} {
		items = append(items, AutocompleteItem{Value: name, Label: name})
	}
	p := &Popup{Kind: KindSlashCommand, Items: items}
	lines := p.Render(80, 5)
	if len(lines) != 5 {
		t.Fatalf("got %d rows, want 5:\n%s", len(lines), stripANSI(strings.Join(lines, "\n")))
	}
	if last := stripANSI(lines[4]); !strings.Contains(last, "4 more · keep typing to narrow") {
		t.Errorf("last row = %q, want the hidden count", last)
	}
	if short := (&Popup{Kind: KindSlashCommand, Items: items[:3]}).Render(80, 5); strings.Contains(stripANSI(strings.Join(short, "\n")), "more") {
		t.Errorf("an unclipped list shows a count:\n%s", stripANSI(strings.Join(short, "\n")))
	}
}

// TestSlashItemMarksTruncatedDescription: a description cut to fit ends
// in "…" rather than stopping mid-word.
func TestSlashItemMarksTruncatedDescription(t *testing.T) {
	item := AutocompleteItem{Value: "review", Description: "Uses Chrome DevTools MCP for accessibility debugging and auditing based on the page"}
	rows := renderSlashCommandItem(item, false, 40, func(s string) string { return s }, func(s string) string { return s })
	if got := stripANSI(rows[0]); !strings.HasSuffix(got, "…") || VisibleWidth(got) > 40 {
		t.Errorf("row = %q (width %d), want it cut to 40 columns ending in …", got, VisibleWidth(got))
	}
}
