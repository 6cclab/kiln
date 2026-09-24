package ollama

import "testing"

func TestResolveContextWindowPriority(t *testing.T) {
	// 1. /api/ps context_length wins over everything else.
	got := ResolveContextWindow(ResolveContextWindowArgs{
		PSContextLength: 32768,
		ShowParameters:  "num_ctx 4096",
		ServerDefault:   16384,
	})
	if got != 32768 {
		t.Fatalf("ps.context_length priority: got %d, want 32768", got)
	}

	// 2. num_ctx from /api/show parameters wins over server default.
	got = ResolveContextWindow(ResolveContextWindowArgs{
		ShowParameters: "num_ctx 4096",
		ServerDefault:  16384,
	})
	if got != 4096 {
		t.Fatalf("num_ctx priority: got %d, want 4096", got)
	}

	// 3. Server default applies when nothing else pins a window.
	got = ResolveContextWindow(ResolveContextWindowArgs{ServerDefault: 16384})
	if got != 16384 {
		t.Fatalf("server default: got %d, want 16384", got)
	}

	// 4. Fallback when nothing is known.
	got = ResolveContextWindow(ResolveContextWindowArgs{})
	if got != 8192 {
		t.Fatalf("fallback: got %d, want 8192", got)
	}
}

func TestNumCtxFromParameters(t *testing.T) {
	cases := []struct {
		params string
		want   int
		ok     bool
	}{
		{"num_ctx 32768", 32768, true},
		{"  num_ctx    8192  \n", 8192, true},
		{"temperature 0.7\nnum_ctx 4096\ntop_p 0.9", 4096, true},
		{"temperature 0.7", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		n, ok := numCtxFromParameters(c.params)
		if ok != c.ok || (ok && n != c.want) {
			t.Errorf("numCtxFromParameters(%q) = (%d, %v), want (%d, %v)", c.params, n, ok, c.want, c.ok)
		}
	}
}

func TestToModelReasoningCapability(t *testing.T) {
	m := toModel("qwen3-cc:latest", "http://x/v1", 32768, []string{"completion", "tools", "thinking"})
	if !m.Reasoning {
		t.Fatal("expected reasoning=true for a model with the thinking capability")
	}
	if m.ThinkingLevelMap == nil {
		t.Fatal("expected a thinkingLevelMap for a reasoning model")
	}
	off, ok := m.ThinkingLevelMap["off"]
	if !ok || off == nil || *off != "off" {
		t.Fatalf("thinkingLevelMap[off] = %v, want \"off\"", off)
	}
	if v, ok := m.ThinkingLevelMap["minimal"]; !ok || v != nil {
		t.Fatalf("thinkingLevelMap[minimal] should be present and nil (unsupported), got %v", v)
	}
	if m.Cost.Input != 0 || m.Cost.Output != 0 {
		t.Fatalf("expected zero cost for a self-hosted model, got %+v", m.Cost)
	}
	if m.MaxTokensField() != "max_tokens" {
		t.Fatalf("MaxTokensField() = %q, want max_tokens", m.MaxTokensField())
	}
}

func TestToModelNoVisionNoReasoning(t *testing.T) {
	m := toModel("llama3:8b", "http://x/v1", 8192, []string{"completion", "tools"})
	if m.Reasoning {
		t.Fatal("expected reasoning=false")
	}
	if len(m.Input) != 1 || m.Input[0] != "text" {
		t.Fatalf("Input = %v, want [text]", m.Input)
	}
}
