package mods

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestSessionMeta drives $.session.version / .repo / .turns: the engine returns
// the Options Version as { version }, the Repo closure's map (or null), and the
// Turns closure's count.
func TestSessionMeta(t *testing.T) {
	repo := func(_ context.Context) (map[string]any, error) {
		return map[string]any{"root": "/work/x", "remote": "git@example.com:o/x.git", "internal": false, "name": nil}, nil
	}
	h := newHarness(t, Options{
		Cwd:     t.TempDir(),
		Version: "943dbfa",
		Repo:    repo,
		Turns:   func(context.Context) (int, error) { return 3, nil },
	})
	h.load(t, "sessionmeta")
	h.e.SessionStart(ctxT(t))
	got := runCommand(t, h, "meta", "")

	var out struct {
		Version string `json:"version"`
		Repo    struct {
			Root     string  `json:"root"`
			Remote   string  `json:"remote"`
			Internal bool    `json:"internal"`
			Name     *string `json:"name"`
		} `json:"repo"`
		Turns int `json:"turns"`
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("meta = %q: %v", got, err)
	}
	if out.Version != "943dbfa" {
		t.Errorf("session.version = %q, want 943dbfa", out.Version)
	}
	if out.Repo.Root != "/work/x" || out.Repo.Remote != "git@example.com:o/x.git" || out.Repo.Internal || out.Repo.Name != nil {
		t.Errorf("session.repo = %+v, want {root:/work/x remote:…:x.git internal:false name:nil}", out.Repo)
	}
	if out.Turns != 3 {
		t.Errorf("session.turns = %d, want 3", out.Turns)
	}
	if strings.Contains(h.logs.String(), "is not available in kiln") {
		t.Errorf("a $ call was rejected:\n%s", h.logs)
	}
}

// TestSessionRepoNull: a Repo closure that returns (nil, nil) — the cwd is not a
// repo — surfaces to the mod as null, not an error.
func TestSessionRepoNull(t *testing.T) {
	h := newHarness(t, Options{
		Cwd:     t.TempDir(),
		Version: "dev",
		Repo:    func(context.Context) (map[string]any, error) { return nil, nil },
		Turns:   func(context.Context) (int, error) { return 0, nil },
	})
	h.load(t, "sessionmeta")
	h.e.SessionStart(ctxT(t))
	got := runCommand(t, h, "meta", "")

	var out struct {
		Repo any `json:"repo"`
	}
	if err := json.Unmarshal([]byte(got), &out); err != nil {
		t.Fatalf("meta = %q: %v", got, err)
	}
	if out.Repo != "NULL" {
		t.Errorf("session.repo = %v, want NULL (no repo -> null)", out.Repo)
	}
}

// TestModelComplete drives $.model.complete: the request reaches the Complete
// closure (prompt, system and maxTokens parsed), and the ModelCompleteResult it
// returns comes back whole.
func TestModelComplete(t *testing.T) {
	var got ModelRequest
	complete := func(_ context.Context, req ModelRequest) (map[string]any, error) {
		got = req
		return map[string]any{"isAnswered": true, "text": "done", "usage": map[string]any{
			"input_tokens": 10, "output_tokens": 2, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0,
		}}, nil
	}
	h := newHarness(t, Options{Cwd: t.TempDir(), Complete: complete})
	h.load(t, "modeltier")
	h.e.SessionStart(ctxT(t))
	res := runCommand(t, h, "complete", "summarize this")

	var out struct {
		IsAnswered bool   `json:"isAnswered"`
		Text       string `json:"text"`
		Usage      struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(res), &out); err != nil {
		t.Fatalf("complete = %q: %v", res, err)
	}
	if !out.IsAnswered || out.Text != "done" || out.Usage.Input != 10 || out.Usage.Output != 2 {
		t.Errorf("complete result = %+v, want answered 'done' usage 10/2", out)
	}
	if len(got.Prompt) != 1 || got.Prompt[0].Text != "summarize this" {
		t.Errorf("Complete prompt = %+v, want [{summarize this}]", got.Prompt)
	}
	if len(got.System) != 1 || got.System[0].Text != "be terse" {
		t.Errorf("Complete system = %+v, want [{be terse}]", got.System)
	}
	if got.MaxTokens != 50 {
		t.Errorf("Complete maxTokens = %d, want 50", got.MaxTokens)
	}
	if strings.Contains(h.logs.String(), "is not available in kiln") {
		t.Errorf("a $ call was rejected:\n%s", h.logs)
	}
}

// TestModelFork drives $.model.fork: the prompt reaches the Fork closure, and a
// 'nothing-to-fork' result (no main thread) comes back as a resolved result.
func TestModelFork(t *testing.T) {
	var gotPrompt string
	fork := func(_ context.Context, prompt string) (map[string]any, error) {
		gotPrompt = prompt
		if prompt == "empty" {
			return map[string]any{"isAnswered": false, "reason": "nothing-to-fork"}, nil
		}
		return map[string]any{"isAnswered": true, "text": "forked:" + prompt, "usage": map[string]any{
			"input_tokens": 1, "output_tokens": 1, "cache_read_input_tokens": 0, "cache_creation_input_tokens": 0,
		}}, nil
	}
	h := newHarness(t, Options{Cwd: t.TempDir(), Fork: fork})
	h.load(t, "modeltier")
	h.e.SessionStart(ctxT(t))

	res := runCommand(t, h, "fork", "keep going")
	var ok struct {
		IsAnswered bool   `json:"isAnswered"`
		Text       string `json:"text"`
	}
	if err := json.Unmarshal([]byte(res), &ok); err != nil {
		t.Fatalf("fork = %q: %v", res, err)
	}
	if !ok.IsAnswered || ok.Text != "forked:keep going" {
		t.Errorf("fork result = %+v, want answered 'forked:keep going'", ok)
	}
	if gotPrompt != "keep going" {
		t.Errorf("Fork prompt = %q, want 'keep going'", gotPrompt)
	}

	res = runCommand(t, h, "fork", "empty")
	var none struct {
		IsAnswered bool   `json:"isAnswered"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(res), &none); err != nil {
		t.Fatalf("fork(empty) = %q: %v", res, err)
	}
	if none.IsAnswered || none.Reason != "nothing-to-fork" {
		t.Errorf("fork(empty) = %+v, want {isAnswered:false reason:nothing-to-fork}", none)
	}
	if strings.Contains(h.logs.String(), "is not available in kiln") {
		t.Errorf("a $ call was rejected:\n%s", h.logs)
	}
}
