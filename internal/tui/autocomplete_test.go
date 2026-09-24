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
	// fuzzyMatch scores where the query's characters were found, not how
	// much text follows them, so "model" and "modal-test" score exactly
	// the same for query "mod" (both match at indices 0,1,2). fuzzyFilter
	// sorts with a stable sort, so a genuine tie keeps the candidates'
	// original (registry, i.e. alphabetical-by-qualified-name) order —
	// "modal-test" before "model" — matching Array.prototype.sort's
	// documented stability in the oracle.
	values := []string{p.Items[0].Value, p.Items[1].Value}
	if values[0] != "modal-test" || values[1] != "model" {
		t.Fatalf("order = %v, want [modal-test, model] (a genuine fuzzy-score tie, broken by stable-sort registry order)", values)
	}
	if p.Start != 0 || p.End != 4 {
		t.Fatalf("range = [%d,%d), want [0,4) (the whole \"/mod\", leading slash included)", p.Start, p.End)
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
	want := Suggestion("x") // just to get the escape prefix/suffix shape
	_ = want
	if !strings.Contains(lines[1], "\x1b[") {
		t.Fatalf("selected row %q does not appear styled", lines[1])
	}
	if strings.Contains(lines[0], "\x1b[") {
		t.Fatalf("unselected row %q is unexpectedly styled", lines[0])
	}
}

func TestPopup_RenderScrollIndicator(t *testing.T) {
	items := make([]AutocompleteItem, 10)
	for i := range items {
		items[i] = AutocompleteItem{Value: string(rune('a' + i))}
	}
	p := &Popup{Items: items, Selected: 0}
	lines := p.Render(40, 3)
	if len(lines) != 4 { // 3 rows + scroll indicator
		t.Fatalf("got %d lines, want 4 (3 rows + scroll indicator)", len(lines))
	}
	if !strings.Contains(lines[3], "(1/10)") {
		t.Fatalf("scroll indicator line = %q, want it to contain (1/10)", lines[3])
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
