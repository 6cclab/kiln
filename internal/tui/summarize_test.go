package tui

import (
	"reflect"
	"strings"
	"testing"
)

// Rendering a tool result. The bug this guards against: `content` is an
// ARRAY of blocks, not a string, so a naive string check falls through to
// JSON and the transcript shows the envelope instead of the output.
func TestSummarizeBlockArray(t *testing.T) {
	captured := map[string]any{"content": []any{map[string]any{"type": "text", "text": "one\ntwo\nthree\n"}}}
	got := Summarize(captured)
	want := []string{"one", "two", "three"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSummarizeNeverLeaksEnvelope(t *testing.T) {
	rendered := ""
	for _, l := range Summarize(map[string]any{"content": []any{map[string]any{"type": "text", "text": "hello"}}}) {
		rendered += l + "\n"
	}
	if strings.Contains(rendered, `"type"`) || strings.Contains(rendered, "content") {
		t.Errorf("envelope leaked: %q", rendered)
	}
}

func TestSummarizeDropsTrailingNewline(t *testing.T) {
	got := Summarize(map[string]any{"content": []any{map[string]any{"type": "text", "text": "x\n"}}})
	want := []string{"x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSummarizeKeepsInteriorBlankLines(t *testing.T) {
	got := Summarize(map[string]any{"content": []any{map[string]any{"type": "text", "text": "a\n\nb\n"}}})
	want := []string{"a", "", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSummarizePlainShapes(t *testing.T) {
	cases := []struct {
		in   any
		want []string
	}{
		{"a\nb", []string{"a", "b"}},
		{map[string]any{"output": "x\ny\n"}, []string{"x", "y"}},
		{map[string]any{"text": "p"}, []string{"p"}},
		{map[string]any{"content": "q\n"}, []string{"q"}},
	}
	for _, c := range cases {
		got := Summarize(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Summarize(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSummarizeJoinsMultipleTextBlocks(t *testing.T) {
	got := Summarize(map[string]any{"content": []any{
		map[string]any{"type": "text", "text": "a"},
		map[string]any{"type": "text", "text": "b"},
	}})
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSummarizeSkipsUnrenderableBlocks(t *testing.T) {
	got := Summarize(map[string]any{"content": []any{map[string]any{"type": "image", "data": "base64..."}}})
	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
	got2 := Summarize(map[string]any{"content": []any{
		map[string]any{"type": "image", "data": "x"},
		map[string]any{"type": "text", "text": "ok"},
	}})
	want := []string{"ok"}
	if !reflect.DeepEqual(got2, want) {
		t.Errorf("got %v, want %v", got2, want)
	}
}

func TestSummarizeFallsBackToJSON(t *testing.T) {
	got := Summarize(map[string]any{"weird": 1})
	want := []string{`{"weird":1}`}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSummarizeEmptyResult(t *testing.T) {
	for _, in := range []any{nil, map[string]any{"content": []any{}}} {
		if got := Summarize(in); len(got) != 0 {
			t.Errorf("Summarize(%v) = %v, want empty", in, got)
		}
	}
}
