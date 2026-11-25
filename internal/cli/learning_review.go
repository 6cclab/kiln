package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/claude/paths"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	claudeskills "github.com/andrepato/harness/internal/claude/skills"
	"github.com/andrepato/harness/internal/crash"
	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/memory"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/plural"
	"github.com/andrepato/harness/internal/review"
	"github.com/andrepato/harness/internal/tool"
	"github.com/andrepato/harness/internal/tools"
)

// reviewIdleDelay is how long the session must sit idle after a due run
// before the review starts, so it never competes with the person's next
// prompt for a local model's GPU (hermes-agent defers its review on a
// local runtime for the same reason). HARNESS_REVIEW_IDLE_MS overrides it
// (QA scenarios).
func reviewIdleDelay() time.Duration {
	if v := os.Getenv("HARNESS_REVIEW_IDLE_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms >= 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return 20 * time.Second
}

// reviewScheduler decides when the automatic learning review runs: once a
// run ends with everyPrompts prompts or everyToolCalls tool calls since the
// last review, after the session has been idle for idle. A new prompt
// cancels a review that is waiting or running; the counts stay, so it is
// tried again after a later run.
type reviewScheduler struct {
	everyPrompts, everyToolCalls int
	idle                         time.Duration
	// run is one review; done is false when it could not run (nothing to
	// continue), so the counts stay and a later run tries again.
	run func(ctx context.Context) (done bool)

	mu                 sync.Mutex
	prompts, toolCalls int
	timer              *time.Timer
	cancel             context.CancelFunc // the running review's
}

func (s *reviewScheduler) observe(ev harness.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch ev.Type {
	case harness.EventRunStart:
		s.prompts++
		s.stopLocked()
	case harness.EventToolEnd:
		s.toolCalls++
	case harness.EventRunEnd:
		if s.cancel == nil && (s.prompts >= s.everyPrompts || s.toolCalls >= s.everyToolCalls) {
			if s.timer != nil {
				s.timer.Stop()
			}
			s.timer = time.AfterFunc(s.idle, s.start)
		}
	}
}

// stopLocked drops a waiting review and cancels a running one.
func (s *reviewScheduler) stopLocked() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *reviewScheduler) start() {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.timer = cancel, nil
	s.mu.Unlock()
	crash.Go(func() {
		done := s.run(ctx)
		s.mu.Lock()
		if done && ctx.Err() == nil {
			s.prompts, s.toolCalls = 0, 0
		}
		s.cancel = nil
		s.mu.Unlock()
		cancel()
	})
}

// close stops the scheduler for good, at session end.
func (s *reviewScheduler) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}

// attachLearningReview starts the automatic review for an interactive
// session unless settings turn it off. notify posts a line in the transcript;
// it is read each time, so the TUI can bind it after start. readAllowed
// says whether the session may read a path without asking; the review
// reads kiln's own skills freely and anything else only when allowed.
//
// The review continues the session's last request (harness.SideRequest):
// the same model, system prompt, tools and messages, with its instruction
// added at the end, so the provider's prompt cache serves the whole
// conversation and only the instruction is new.
func attachLearningReview(started *agent.Started, cwd string, settings claudesettings.Settings, store *memory.Store, scope string, readAllowed func(string) bool, notify func() func(string)) (cleanup func()) {
	if !reviewOn(settings) {
		return func() {}
	}
	queue := review.DefaultQueue()
	skillsDir := paths.KilnSkillsDir()
	s := &reviewScheduler{
		everyPrompts:   claudesettings.PositiveOr(settings.LearningReview.EveryPrompts, claudesettings.DefaultReviewEveryPrompts),
		everyToolCalls: claudesettings.PositiveOr(settings.LearningReview.EveryToolCalls, claudesettings.DefaultReviewEveryToolCalls),
		idle:           reviewIdleDelay(),
	}
	s.run = func(ctx context.Context) bool {
		side, err := started.Lane.ForkLastRequest()
		if err != nil {
			diag.L().Info("learning review skipped", "reason", err.Error())
			return false
		}
		model := side.Model
		in := review.Input{SkillsDir: skillsDir, MemoryScope: scope, Session: started.SessionID,
			Usage: review.Activity(skillsDir, review.DefaultUsage().Load())}
		if store != nil {
			recent, err := store.Recent(ctx, scope, 50)
			if err != nil {
				diag.L().Warn("learning review: memory notes", "err", err)
			}
			in.Memory = append([]memory.Record{}, recent...)
		}
		ctx, cancel := context.WithTimeout(ctx, review.DefaultRunTimeout)
		defer cancel()
		conv := review.Conversation{Streamer: side.Provider, Model: model, Messages: side.Messages, Options: side.Options}
		proposals, st, err := review.Run(ctx, conv, in, reviewToolRunner(started.Harness.Tools(), cwd, skillsDir, readAllowed), func(u msg.Usage) {
			if err := started.Harness.RecordSideUsage("learning review · "+model.Provider+"/"+model.ID, u); err != nil {
				diag.L().Warn("learning review usage", "err", err)
			}
		})
		diag.L().Info("learning review", "model", model.Provider+"/"+model.ID, "requests", st.Requests,
			"tool_calls", st.ToolCalls, "captured", st.Captured, "refused", strings.Join(st.Refused, ","), "repaired", st.Repaired,
			"cache_read", st.CacheRead, "input_total", st.InputTotal, "kept", len(proposals), "err", fmt.Sprint(err))
		added, err := queue.Add(proposals)
		if err != nil {
			diag.L().Warn("learning review: queue", "err", err)
		}
		if len(added) > 0 {
			if fn := notify(); fn != nil {
				fn(fmt.Sprintf("Learning review proposed %s · /pending to review", plural.Count(len(added), "change")))
			}
		}
		return true
	}
	events := started.Harness.Events()
	unsubs := []func(){
		events.On(harness.EventRunStart, s.observe),
		events.On(harness.EventToolEnd, s.observe),
		events.On(harness.EventRunEnd, s.observe),
	}
	return func() {
		for _, u := range unsubs {
			u()
		}
		s.close()
	}
}

// reviewOn reports whether the learning review (and the skill curator
// that comes with it) runs: by default, unless the user's settings turn it
// or learning off. It only proposes; nothing is written without /pending.
func reviewOn(settings claudesettings.Settings) bool {
	r := settings.LearningReview
	return (r.Enabled == nil || *r.Enabled) && (settings.LearningEnabled == nil || *settings.LearningEnabled)
}

// reviewToolRunner runs the review's read-only tool calls with the
// session's own tools: read for a kiln skill file, or any file the
// session may read without asking; skill to load a skill. The review
// never prompts the person, so a read that would ask is refused.
func reviewToolRunner(set *tool.Set, cwd, skillsDir string, readAllowed func(string) bool) review.ToolRunner {
	return func(ctx context.Context, call msg.ToolCall) (string, bool) {
		if call.Name == "read" {
			path, _ := call.Arguments["path"].(string)
			abs := path
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(cwd, abs)
			}
			inSkills := strings.HasPrefix(filepath.Clean(abs), skillsDir+string(os.PathSeparator))
			if !inSkills && (readAllowed == nil || !readAllowed(abs)) {
				return "Reading " + path + " needs the person's approval, which the learning review cannot ask for.", true
			}
		}
		t, ok := set.Get(call.Name)
		if !ok {
			return "No " + call.Name + " tool in this session.", true
		}
		raw, err := json.Marshal(call.Arguments)
		if err != nil {
			return err.Error(), true
		}
		res, err := t.Execute(ctx, raw, nil, tool.Invocation{ToolCallID: call.ID, ToolName: call.Name, Cwd: cwd})
		if err != nil {
			return err.Error(), true
		}
		return msg.TextOf(res.Content), res.IsError
	}
}

// curateSkills queues an archive proposal for each kiln-made skill that
// has gone unused for review.ArchiveAfter (no model call). It runs at
// startup with the learning review on.
func curateSkills(queue review.Queue, h review.History, usage review.Usage, skillsDir string, now time.Time) int {
	changes, err := h.List()
	if err != nil {
		diag.L().Warn("skill curator: history", "err", err)
		return 0
	}
	pending, err := queue.List()
	if err != nil {
		return 0
	}
	proposals := review.CuratorProposals(review.Activity(skillsDir, usage.Load()), review.Provenance(changes), pending, now)
	added, err := queue.Add(proposals)
	if err != nil {
		diag.L().Warn("skill curator: queue", "err", err)
	}
	return len(added)
}

// countToolSkillUse wraps the skill tool's rewrite to count each use of a
// kiln skill, which the curator and the review read.
func countToolSkillUse(usage review.Usage, skillsDir string, next func(context.Context, tools.SkillRecord, string) string) func(context.Context, tools.SkillRecord, string) string {
	return func(ctx context.Context, r tools.SkillRecord, text string) string {
		if err := usage.RecordUse(skillsDir, r.Dir); err != nil {
			diag.L().Warn("skill usage", "err", err)
		}
		return next(ctx, r, text)
	}
}

// countSlashSkillUse is countToolSkillUse for a skill the person runs
// from the slash palette.
func countSlashSkillUse(usage review.Usage, skillsDir string, next func(context.Context, claudeskills.Skill, string) string) func(context.Context, claudeskills.Skill, string) string {
	return func(ctx context.Context, s claudeskills.Skill, text string) string {
		if err := usage.RecordUse(skillsDir, s.FilePath); err != nil {
			diag.L().Warn("skill usage", "err", err)
		}
		return next(ctx, s, text)
	}
}

// watchSkills logs every change under ~/.kiln/skills in the skill history:
// after each tool call as the agent's (its write or edit, a bash command,
// a subagent), and before each run as made outside kiln's tools (by hand,
// or a background command). It returns the watch, for commands that
// change the directory themselves to rebase it.
func watchSkills(started *agent.Started, h review.History, skillsDir string) *review.Watch {
	w := review.NewWatch(skillsDir, h, started.SessionID)
	events := started.Harness.Events()
	events.On(harness.EventToolEnd, func(harness.Event) { w.Sync(review.SourceAgent) })
	events.On(harness.EventRunStart, func(harness.Event) { w.Sync(review.SourceOutside) })
	return w
}

// pendingNotice is the line shown at startup when proposals are waiting,
// "" when there are none.
func pendingNotice(queue review.Queue) string {
	ps, err := queue.List()
	if err != nil || len(ps) == 0 {
		return ""
	}
	return fmt.Sprintf("%s from earlier learning reviews · /pending to review", plural.Count(len(ps), "proposed change"))
}

// previewDiff shows the lines that change between old and new (the common
// leading and trailing lines dropped), at most limit of them.
func previewDiff(old, new string, limit int) []string {
	a, b := splitLines(old), splitLines(new)
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	var rows []string
	for _, l := range a[pre : len(a)-suf] {
		rows = append(rows, "    - "+l)
	}
	for _, l := range b[pre : len(b)-suf] {
		rows = append(rows, "    + "+l)
	}
	if len(rows) > limit {
		rows = append(rows[:limit], fmt.Sprintf("    … %s more", plural.Count(len(rows)-limit, "line")))
	}
	return rows
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + p[len(home):]
	}
	return p
}
