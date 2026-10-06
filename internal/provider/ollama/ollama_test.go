package ollama

import "testing"

// TestResolveContextWindowPriority covers the new ordering:
// Modelfile num_ctx > OLLAMA_CONTEXT_LENGTH (ServerDefault) >
// min(training context, 32768) > fallbackContext (8192).
//
// Case 1 is the defect this fix closes: origin/main's ResolveContextWindow
// had no TrainingContext input at all (its top priority was /api/ps's
// PSContextLength, a field this struct no longer carries) and would return
// ServerDefault here, not the pinned num_ctx -- this assertion documents
// num_ctx still winning over everything else now that /api/ps is gone.
func TestResolveContextWindowPriority(t *testing.T) {
	// 1. num_ctx from /api/show parameters wins over everything else.
	got := ResolveContextWindow(ResolveContextWindowArgs{
		ShowParameters:  "num_ctx 4096",
		ServerDefault:   16384,
		TrainingContext: 131072,
	})
	if got != 4096 {
		t.Fatalf("num_ctx priority: got %d, want 4096", got)
	}

	// 2. Server default (OLLAMA_CONTEXT_LENGTH) wins over training context
	// when no num_ctx is pinned.
	got = ResolveContextWindow(ResolveContextWindowArgs{
		ServerDefault:   16384,
		TrainingContext: 131072,
	})
	if got != 16384 {
		t.Fatalf("server default priority: got %d, want 16384", got)
	}

	// 3. Training context applies when nothing else pins a window, capped
	// at maxAutoContext (32768) -- qwen3:8b's measured training context is
	// 40960, larger than the cap.
	got = ResolveContextWindow(ResolveContextWindowArgs{TrainingContext: 40960})
	if got != 32768 {
		t.Fatalf("training-context cap: got %d, want 32768", got)
	}

	// 4. A training context at or under the cap is used as-is.
	got = ResolveContextWindow(ResolveContextWindowArgs{TrainingContext: 8000})
	if got != 8000 {
		t.Fatalf("training-context under cap: got %d, want 8000", got)
	}

	// 5. Fallback when nothing is known at all.
	got = ResolveContextWindow(ResolveContextWindowArgs{})
	if got != 8192 {
		t.Fatalf("fallback: got %d, want 8192", got)
	}
}

func TestTrainingContextFromModelInfo(t *testing.T) {
	n, ok := trainingContextFromModelInfo(map[string]any{
		"general.architecture":   "qwen3",
		"qwen3.context_length":   float64(40960),
		"qwen3.embedding_length": float64(4096),
	})
	if !ok || n != 40960 {
		t.Fatalf("trainingContextFromModelInfo = (%d, %v), want (40960, true)", n, ok)
	}

	if n, ok := trainingContextFromModelInfo(map[string]any{"general.architecture": "qwen3"}); ok {
		t.Fatalf("expected no training context, got %d", n)
	}
	if n, ok := trainingContextFromModelInfo(nil); ok {
		t.Fatalf("expected no training context for nil map, got %d", n)
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
