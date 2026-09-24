package tui

import "testing"

func TestFormatTokens(t *testing.T) {
	cases := map[int]string{
		450:       "450",
		999:       "999",
		3400:      "3.4k",
		1_000_000: "1.0m",
		2_500_000: "2.5m",
	}
	for n, want := range cases {
		if got := FormatTokens(n); got != want {
			t.Errorf("FormatTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestPickLabelDeterministic(t *testing.T) {
	if PickLabel(0) != Labels[0] {
		t.Errorf("PickLabel(0) = %q, want %q", PickLabel(0), Labels[0])
	}
	if PickLabel(len(Labels)) != Labels[0] {
		t.Error("PickLabel should wrap around")
	}
}

func TestRenderToolCallMarkerColorByStatus(t *testing.T) {
	SetColorEnabled(true)
	defer SetColorEnabled(colorEnabled())

	ok := RenderToolCall(ToolCallView{Name: "Read", PrimaryArg: "f.go", Status: CallOK})[0]
	errLine := RenderToolCall(ToolCallView{Name: "Read", PrimaryArg: "f.go", Status: CallError})[0]
	running := RenderToolCall(ToolCallView{Name: "Read", PrimaryArg: "f.go", Status: CallRunning})[0]
	if ok == errLine || ok == running || errLine == running {
		t.Error("each status should render a distinctly colored marker")
	}
}

func TestRenderToolCallExpansionHint(t *testing.T) {
	view := ToolCallView{
		Name: "Read", PrimaryArg: "f.go", Status: CallOK,
		ResultLines: []string{"line one"}, TotalLines: 240, HasTotalLines: true,
	}
	out := RenderToolCall(view)
	if len(out) != 3 {
		t.Fatalf("got %d lines, want head + result + hint", len(out))
	}
}

func TestRenderTodosStatuses(t *testing.T) {
	out := RenderTodos([]TodoView{
		{Content: "a", Status: TodoCompletedStatus},
		{Content: "b", Status: TodoInProgressStatus},
		{Content: "c", Status: TodoPendingStatus},
	})
	if len(out) != 4 { // header + 3 items
		t.Fatalf("got %d lines, want 4", len(out))
	}
}

func TestRenderDiffLineNumbers(t *testing.T) {
	patch := "@@ -10,2 +10,3 @@\n-old line\n+new line one\n+new line two\n context line"
	out := RenderDiff(patch, 0)
	if len(out) != 5 {
		t.Fatalf("got %d lines, want 5", len(out))
	}
}

func TestRenderThinkingCollapsedVsExpanded(t *testing.T) {
	collapsed := RenderThinking(ThinkingView{Text: "reasoning here", Active: false, Expanded: false})
	if len(collapsed) != 1 {
		t.Fatalf("collapsed thinking should be one line, got %d", len(collapsed))
	}
	expanded := RenderThinking(ThinkingView{Text: "reasoning here", Active: false, Expanded: true})
	if len(expanded) != 2 {
		t.Fatalf("expanded thinking should be header + body, got %d", len(expanded))
	}
	empty := RenderThinking(ThinkingView{Text: "   "})
	if len(empty) != 0 {
		t.Error("blank thinking text should render nothing")
	}
}

func TestPrimaryArgPicksIdentifyingKey(t *testing.T) {
	cases := []struct {
		args any
		want string
	}{
		{"bare string", "bare string"},
		{map[string]any{"path": "a.go"}, "a.go"},
		{map[string]any{"command": "ls -la"}, "ls -la"},
		{map[string]any{"unrelated": 1}, ""},
	}
	for _, c := range cases {
		if got := PrimaryArg(c.args); got != c.want {
			t.Errorf("PrimaryArg(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}
