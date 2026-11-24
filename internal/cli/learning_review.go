package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/automode"
	"github.com/andrepato/harness/internal/claude/paths"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	slashcommands "github.com/andrepato/harness/internal/commands"
	"github.com/andrepato/harness/internal/crash"
	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/memory"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/plural"
	"github.com/andrepato/harness/internal/provider"
	"github.com/andrepato/harness/internal/review"
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
	run                          func(ctx context.Context) // one review

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
		s.run(ctx)
		s.mu.Lock()
		if ctx.Err() == nil {
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
// session when settings enable it. notify posts a line in the transcript;
// it is read each time, so the TUI can bind it after start.
func attachLearningReview(started *agent.Started, reg *provider.Registry, settings claudesettings.Settings, store *memory.Store, scope string, virtualDirect func() (provider.Model, bool), notify func() func(string)) (cleanup func()) {
	if r := settings.LearningReview; r.Enabled == nil || !*r.Enabled || (settings.LearningEnabled != nil && !*settings.LearningEnabled) {
		return func() {}
	}
	queue := review.DefaultQueue()
	s := &reviewScheduler{
		everyPrompts:   claudesettings.PositiveOr(settings.LearningReview.EveryPrompts, claudesettings.DefaultReviewEveryPrompts),
		everyToolCalls: claudesettings.PositiveOr(settings.LearningReview.EveryToolCalls, claudesettings.DefaultReviewEveryToolCalls),
		idle:           reviewIdleDelay(),
	}
	s.run = func(ctx context.Context) {
		model := classifierSessionModel(started.Model, virtualDirect)
		p, ok := reg.Provider(model.Provider)
		if !ok {
			diag.L().Warn("learning review", "err", "unknown provider "+model.Provider)
			return
		}
		in := review.Input{Messages: automode.BranchMessages(ctx, started.Lane), SkillsDir: paths.KilnSkillsDir(), MemoryScope: scope, Session: started.SessionID}
		if store != nil {
			recent, err := store.Recent(ctx, scope, 50)
			if err != nil {
				diag.L().Warn("learning review: memory notes", "err", err)
			}
			in.Memory = append([]memory.Record{}, recent...)
		}
		ctx, cancel := context.WithTimeout(ctx, review.DefaultRunTimeout)
		defer cancel()
		proposals, err := review.Run(ctx, p, model, in, func(u msg.Usage) {
			if err := started.Harness.RecordSideUsage("learning review · "+model.Provider+"/"+model.ID, u); err != nil {
				diag.L().Warn("learning review usage", "err", err)
			}
		})
		if err != nil {
			diag.L().Info("learning review", "model", model.Provider+"/"+model.ID, "err", err.Error(), "kept", len(proposals))
		}
		added, err := queue.Add(proposals)
		if err != nil {
			diag.L().Warn("learning review: queue", "err", err)
		}
		diag.L().Info("learning review done", "proposed", len(added))
		if len(added) > 0 {
			if fn := notify(); fn != nil {
				fn(fmt.Sprintf("Learning review proposed %s · /pending to review", plural.Count(len(added), "change")))
			}
		}
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

// pendingNotice is the line shown at startup when proposals are waiting,
// "" when there are none.
func pendingNotice(queue review.Queue) string {
	ps, err := queue.List()
	if err != nil || len(ps) == 0 {
		return ""
	}
	return fmt.Sprintf("%s from earlier learning reviews · /pending to review", plural.Count(len(ps), "proposed change"))
}

// pendingSource is /pending: list the learning review's proposals, or
// approve or reject them. Approval is the only way a proposal is written.
func pendingSource(queue review.Queue, skillsDir string, store *memory.Store) slashcommands.Source {
	return slashcommands.StaticSource(slashcommands.OriginBuiltin, []slashcommands.Command{{
		Name:         "pending",
		Description:  "Review the learning review's proposed skill and memory changes",
		ArgumentHint: "[approve|reject <id…|all>]",
		Run: func(ctx context.Context, args string) (slashcommands.Result, error) {
			return slashcommands.Result{Output: pendingRun(ctx, queue, skillsDir, store, args)}, nil
		},
	}})
}

func pendingRun(ctx context.Context, queue review.Queue, skillsDir string, store *memory.Store, args string) []string {
	ps, err := queue.List()
	if err != nil {
		return []string{"Could not read the proposals: " + err.Error()}
	}
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return pendingList(ps, skillsDir)
	}
	verb := fields[0]
	if (verb != "approve" && verb != "reject") || len(fields) < 2 {
		return []string{"Usage: /pending, /pending approve <id…|all>, /pending reject <id…|all>"}
	}
	byID := map[int]review.Proposal{}
	for _, p := range ps {
		byID[p.ID] = p
	}
	var chosen []review.Proposal
	var out []string
	if len(fields) == 2 && fields[1] == "all" {
		chosen = ps
	} else {
		for _, f := range fields[1:] {
			id, err := strconv.Atoi(strings.TrimPrefix(f, "#"))
			p, ok := byID[id]
			if err != nil || !ok {
				out = append(out, "No pending proposal "+f+".")
				continue
			}
			chosen = append(chosen, p)
		}
	}
	for _, p := range chosen {
		if verb == "approve" {
			if err := review.Apply(ctx, p, skillsDir, store); err != nil {
				out = append(out, fmt.Sprintf("#%d not applied: %v", p.ID, err))
				continue
			}
		}
		if err := queue.Remove(p.ID); err != nil {
			out = append(out, fmt.Sprintf("#%d: %v", p.ID, err))
			continue
		}
		switch {
		case verb == "reject":
			out = append(out, fmt.Sprintf("Rejected #%d %s.", p.ID, p.Target()))
		case p.Kind == review.KindSkill:
			out = append(out, fmt.Sprintf("Approved #%d: wrote %s (loads in the next session).", p.ID, tildePath(p.SkillPath(skillsDir))))
		default:
			out = append(out, fmt.Sprintf("Approved #%d: saved memory %s.", p.ID, p.Key))
		}
	}
	if len(out) == 0 {
		out = []string{"No proposals are pending."}
	}
	return out
}

// pendingList renders every proposal with a preview of its change.
func pendingList(ps []review.Proposal, skillsDir string) []string {
	if len(ps) == 0 {
		return []string{"No proposals are pending. With learningReview enabled, kiln proposes skill and memory changes as you work."}
	}
	var out []string
	for _, p := range ps {
		out = append(out, "")
		switch p.Kind {
		case review.KindSkill:
			verb := "update"
			current, err := os.ReadFile(p.SkillPath(skillsDir))
			if p.Base == "" {
				verb = "new"
			}
			out = append(out, fmt.Sprintf("#%d %s skill %s — %s", p.ID, verb, p.Target(), p.Why))
			if err != nil {
				current = nil
			}
			out = append(out, previewDiff(string(current), p.Content, 12)...)
		default:
			out = append(out, fmt.Sprintf("#%d memory %s (%s) — %s", p.ID, p.Key, p.Type, p.Why))
			out = append(out, "    "+p.Text, "    evidence: "+p.Evidence)
		}
	}
	return append(out, "", "/pending approve <id…|all> · /pending reject <id…|all>")
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
