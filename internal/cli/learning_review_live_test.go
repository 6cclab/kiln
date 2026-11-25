package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/memory"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/review"
	"github.com/andrepato/harness/internal/tools"
)

// TestLiveLearningReview runs the learning review against a real model:
// KILN_REVIEW_LIVE=provider/model (e.g. ollama/qwen3.8:latest), repeated
// KILN_REVIEW_REPEAT times (default 1), optionally with KILN_REVIEW_THINKING
// set to a thinking level. Each case is a short session; the
// model answers its last message for real (the "last request"), then the
// review continues it exactly as attachLearningReview does. It reports,
// per run, whether the answer parsed, what was proposed and dropped, the
// tool calls, and how many prompt tokens the provider read from cache
// (Anthropic) or had to evaluate (Ollama, whose input count excludes its
// KV-cache hits). Nothing is written outside the test's temp directory.
func TestLiveLearningReview(t *testing.T) {
	target := os.Getenv("KILN_REVIEW_LIVE")
	if target == "" {
		t.Skip("set KILN_REVIEW_LIVE=provider/model to run")
	}
	repeat := 1
	if n, err := fmt.Sscan(os.Getenv("KILN_REVIEW_REPEAT"), &repeat); n != 1 || err != nil {
		repeat = 1
	}
	providerID, modelID, _ := strings.Cut(target, "/")
	reg := buildRegistry()
	p, ok := reg.Provider(providerID)
	if !ok {
		t.Fatalf("no provider %s", providerID)
	}
	ctx := context.Background()
	if r, ok := p.(interface{ RefreshModels(context.Context) error }); ok {
		if err := r.RefreshModels(ctx); err != nil {
			t.Fatalf("refresh %s: %v", providerID, err)
		}
	}
	model, ok := reg.GetModel(providerID, modelID)
	if !ok {
		t.Fatalf("no model %s", target)
	}
	toolDefs := []provider.ToolDef{}
	set := tools.Builtins(execenv.New(t.TempDir()))
	for _, name := range set.Names() {
		tl, _ := set.Get(name)
		toolDefs = append(toolDefs, provider.ToolDef{Name: tl.Name, Description: tl.Description, Parameters: tl.Parameters})
	}
	// The session's own requests set no max_tokens (the model's default),
	// as kiln's lane does; KILN_REVIEW_THINKING sets a thinking level.
	opts := provider.StreamOptions{SystemPrompt: liveSystemPrompt, Tools: toolDefs,
		ThinkingLevel: provider.ThinkingLevel(os.Getenv("KILN_REVIEW_THINKING"))}

	var rows []string
	for _, c := range liveCases() {
		for i := 0; i < repeat; i++ {
			dir := t.TempDir()
			skillsDir := filepath.Join(dir, "skills")
			for path, content := range c.skills {
				full := filepath.Join(skillsDir, path)
				_ = os.MkdirAll(filepath.Dir(full), 0o755)
				_ = os.WriteFile(full, []byte(content), 0o644)
			}
			// The session's last request, answered for real. A session
			// runs the tools an answer calls and asks again until the
			// answer is text; here each call gets a stub result.
			start := time.Now()
			messages := append([]msg.Message(nil), c.messages...)
			var answer *msg.AssistantMessage
			for step := 0; ; step++ {
				ch, wait := provider.Stream(ctx, p, model, messages, opts)
				for range ch {
				}
				var err error
				if answer, err = wait(); err != nil {
					t.Fatalf("%s: last request: %v", c.name, err)
				}
				messages = append(messages, *answer)
				calls := 0
				for _, b := range answer.Content {
					if call, ok := b.(msg.ToolCall); ok {
						calls++
						messages = append(messages, result(call.ID, call.Name, "(not run: the session ended here)", false))
					}
				}
				if calls == 0 || step == 3 {
					break
				}
			}
			lastInput := answer.Usage.Input + answer.Usage.CacheRead + answer.Usage.CacheWrite
			conv := review.Conversation{Streamer: p, Model: model, Options: opts, Messages: messages}
			in := review.Input{SkillsDir: skillsDir, MemoryScope: "/proj", Session: "live", Memory: []memory.Record{}}
			runner := reviewToolRunner(tools.Builtins(execenv.New(dir)), dir, skillsDir, func(string) bool { return false })
			reviewStart := time.Now()
			proposals, st, err := review.Run(ctx, conv, in, runner, nil)
			got := []string{}
			for _, pr := range proposals {
				got = append(got, pr.Kind+":"+pr.Target())
			}
			verdict := c.check(proposals)
			rows = append(rows, fmt.Sprintf("%-22s run %d | %-4s | proposals %v | err %v | req %d tools %d refused %d repaired %v | last-request prompt %d tok | review prompt: %d evaluated, %d cache-read | %.1fs + %.1fs",
				c.name, i+1, verdict, got, err, st.Requests, st.ToolCalls, st.Refused, st.Repaired, lastInput,
				st.InputTotal-st.CacheRead, st.CacheRead, reviewStart.Sub(start).Seconds(), time.Since(reviewStart).Seconds()))
			for _, pr := range proposals {
				body := pr.Content + pr.Text
				t.Logf("%s run %d proposal %s why=%q\n%s", c.name, i+1, pr.Target(), pr.Why, clipLive(body, 900))
			}
		}
	}
	t.Log("\n" + strings.Join(rows, "\n"))
}

func clipLive(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

const liveSystemPrompt = `You are kiln, a coding agent running in the user's terminal. You help with software engineering tasks: reading and editing code, running commands, and explaining what you find. Use the tools to act; keep replies short and factual. Work in the user's current project. Ask before destructive actions. Never invent file contents; read files before changing them. When a command fails, read the error and fix the cause.`

type liveCase struct {
	name     string
	messages []msg.Message
	skills   map[string]string
	check    func([]review.Proposal) string
}

func user(s string) msg.Message {
	return msg.UserMessage{Role: msg.RoleUser, Content: msg.Blocks{msg.Text(s)}}
}

func agentSays(s string, calls ...msg.ToolCall) msg.Message {
	b := msg.Blocks{}
	if s != "" {
		b = append(b, msg.Text(s))
	}
	stop := msg.StopStop
	for _, c := range calls {
		b = append(b, c)
		stop = msg.StopToolUse
	}
	return msg.AssistantMessage{Role: msg.RoleAssistant, Content: b, StopReason: stop}
}

func result(id, name, text string, isErr bool) msg.Message {
	return msg.ToolResultMessage{Role: msg.RoleToolResult, ToolCallID: id, ToolName: name, Content: msg.Blocks{msg.Text(text)}, IsError: isErr}
}

func liveCases() []liveCase {
	releaseSkill := "---\nname: release-flow\ndescription: Tag and publish a release of this project.\n---\n# Release flow\n\n## Procedure\n1. Run `make check`.\n2. Tag with `git tag vX.Y.Z`.\n3. Push with `git push --tags`.\n\n## Verification\n`git ls-remote --tags origin` lists the tag.\n"
	ciSkill := "---\nname: ci-checks\ndescription: Run this project's checks before a commit.\n---\n# CI checks\n\nSee references/order.md for the order.\n"
	order := "# Check order\n\n1. go vet ./...\n2. go test ./...\n3. staticcheck ./...\n"
	none := func(ps []review.Proposal) string {
		if len(ps) == 0 {
			return "ok"
		}
		return "BAD"
	}
	return []liveCase{
		{
			name: "pnpm-preference",
			messages: []msg.Message{
				user("add lodash to the web app"),
				agentSays("", msg.NewToolCall("c1", "bash", map[string]any{"command": "cd web && npm install lodash"})),
				result("c1", "bash", "added 1 package, and audited 812 packages in 3s", false),
				agentSays("Added lodash to web/package.json with npm."),
				user("No. This repo uses pnpm, there's a pnpm-lock.yaml. Always use pnpm here, never npm. Undo that and redo it with pnpm."),
				agentSays("", msg.NewToolCall("c2", "bash", map[string]any{"command": "cd web && rm -f package-lock.json && git checkout package.json && pnpm add lodash"})),
				result("c2", "bash", "Packages: +1\nDone in 1.2s", false),
				agentSays("Done: removed npm's lockfile, restored package.json, and added lodash with pnpm."),
				user("good, thanks"),
			},
			check: func(ps []review.Proposal) string {
				for _, p := range ps {
					if strings.Contains(strings.ToLower(p.Content+p.Text), "pnpm") {
						return "ok"
					}
				}
				return "MISS"
			},
		},
		{
			name: "nothing-to-learn",
			messages: []msg.Message{
				user("what does git status -sb show?"),
				agentSays("A short status: the branch line with ahead/behind counts, then one line per changed file with a two-letter code."),
				user("thanks"),
			},
			check: none,
		},
		{
			name: "flaky-test-technique",
			messages: []msg.Message{
				user("TestCacheEvict fails about one run in five in CI, can you find out why"),
				agentSays("", msg.NewToolCall("c1", "bash", map[string]any{"command": "go test ./internal/cache -run TestCacheEvict -count=50"})),
				result("c1", "bash", "--- FAIL: TestCacheEvict (0.01s)\n    cache_test.go:88: len = 3, want 2\nFAIL", true),
				agentSays("It fails under repetition. Checking for a race.", msg.NewToolCall("c2", "bash", map[string]any{"command": "go test ./internal/cache -run TestCacheEvict -race -count=20"})),
				result("c2", "bash", "WARNING: DATA RACE\nWrite at 0x00c0001 by goroutine 9:\n  cache.(*LRU).evict()\n      lru.go:71\nPrevious read at 0x00c0001 by goroutine 8:\n  cache.(*LRU).Len()\n      lru.go:40", true),
				agentSays("evict runs on a timer goroutine without the mutex Len takes.", msg.NewToolCall("c3", "edit", map[string]any{"path": "internal/cache/lru.go", "edits": []map[string]any{{"oldText": "func (c *LRU) evict() {", "newText": "func (c *LRU) evict() {\n\tc.mu.Lock()\n\tdefer c.mu.Unlock()"}}})),
				result("c3", "edit", "Successfully replaced 1 block(s) in internal/cache/lru.go.", false),
				agentSays("", msg.NewToolCall("c4", "bash", map[string]any{"command": "go test ./internal/cache -run TestCacheEvict -race -count=200"})),
				result("c4", "bash", "ok  \tinternal/cache\t4.1s", false),
				agentSays("Fixed: evict now takes the mutex. 200 runs under -race pass."),
				user("nice. that -count plus -race approach is how I want flaky tests chased from now on"),
			},
			check: func(ps []review.Proposal) string {
				for _, p := range ps {
					if strings.Contains(p.Content+p.Text, "-race") {
						return "ok"
					}
				}
				return "MISS"
			},
		},
		{
			name:   "existing-skill-wrong",
			skills: map[string]string{"release-flow/SKILL.md": releaseSkill},
			messages: []msg.Message{
				user("cut release v1.4.0"),
				agentSays("", msg.NewToolCall("c1", "skill", map[string]any{"skill": "release-flow"})),
				result("c1", "skill", releaseSkill, false),
				agentSays("", msg.NewToolCall("c2", "bash", map[string]any{"command": "make check && git tag v1.4.0 && git push --tags"})),
				result("c2", "bash", "ok\nTo github.com:acme/app.git\n * [new tag] v1.4.0 -> v1.4.0\n ! [rejected] v1.2.0-rc1 -> v1.2.0-rc1 (already exists)", true),
				agentSays("v1.4.0 is pushed; git push --tags also tried to push an old local rc tag."),
				user("Right, never push --tags here, it drags stale local tags along. Push only the new tag: git push origin vX.Y.Z."),
				agentSays("Understood. Only v1.4.0 needed pushing and it is on origin."),
				user("ok"),
			},
			check: func(ps []review.Proposal) string {
				for _, p := range ps {
					if p.Kind == review.KindSkill && p.Skill == "release-flow" && strings.Contains(p.Content, "git push origin") {
						return "ok"
					}
				}
				return "MISS"
			},
		},
		{
			name:   "reference-needs-read",
			skills: map[string]string{"ci-checks/SKILL.md": ciSkill, "ci-checks/references/order.md": order},
			messages: []msg.Message{
				user("run the checks before I commit"),
				agentSays("", msg.NewToolCall("c1", "bash", map[string]any{"command": "go vet ./... && go test ./... && staticcheck ./..."})),
				result("c1", "bash", "ok (412s)", false),
				agentSays("All checks pass."),
				user("That took 7 minutes. Run staticcheck first, it's fast and catches most things; the tests go last. Keep that order from now on."),
				agentSays("Will do: staticcheck, then vet, then tests."),
				user("thanks"),
			},
			check: func(ps []review.Proposal) string {
				for _, p := range ps {
					if p.File == "references/order.md" && strings.Index(p.Content, "staticcheck") < strings.Index(p.Content, "go test") {
						return "ok"
					}
					if strings.Contains(strings.ToLower(p.Content+p.Text), "staticcheck") {
						return "ok~"
					}
				}
				return "MISS"
			},
		},
	}
}
