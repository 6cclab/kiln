package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/claude/paths"
	slashcommands "github.com/andrepato/harness/internal/commands"
	"github.com/andrepato/harness/internal/memory"
	"github.com/andrepato/harness/internal/plural"
	"github.com/andrepato/harness/internal/review"
)

// learningStores is where kiln keeps what it learns: the review's
// proposals, the history of every change to a kiln skill, the skills' use
// counts, the skills themselves and project memory.
type learningStores struct {
	queue     review.Queue
	history   review.History
	usage     review.Usage
	skillsDir string
	memory    *memory.Store
	session   string
}

func defaultLearningStores(store *memory.Store, session string) learningStores {
	return learningStores{queue: review.DefaultQueue(), history: review.DefaultHistory(), usage: review.DefaultUsage(),
		skillsDir: paths.KilnSkillsDir(), memory: store, session: session}
}

// pendingSource is /pending: list the learning review's proposals, or
// approve or reject them. Approval is the only way a proposal is written.
func pendingSource(ls learningStores) slashcommands.Source {
	return slashcommands.StaticSource(slashcommands.OriginBuiltin, []slashcommands.Command{{
		Name:         "pending",
		Description:  "Review the learning review's proposed skill and memory changes",
		ArgumentHint: "[approve|reject <id…|all>]",
		Run: func(ctx context.Context, args string) (slashcommands.Result, error) {
			return slashcommands.Result{Output: pendingRun(ctx, ls, args)}, nil
		},
	}})
}

func pendingRun(ctx context.Context, ls learningStores, args string) []string {
	ps, err := ls.queue.List()
	if err != nil {
		return []string{"Could not read the proposals: " + err.Error()}
	}
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return pendingList(ps, ls.skillsDir)
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
			if err := review.Apply(ctx, p, ls.skillsDir, ls.memory, ls.history, ls.session); err != nil {
				out = append(out, fmt.Sprintf("#%d not applied: %v", p.ID, err))
				continue
			}
		} else if p.Kind == review.KindArchive {
			if err := ls.usage.Keep(p.Skill); err != nil {
				out = append(out, fmt.Sprintf("#%d: %v", p.ID, err))
			}
		}
		if err := ls.queue.Remove(p.ID); err != nil {
			out = append(out, fmt.Sprintf("#%d: %v", p.ID, err))
			continue
		}
		switch {
		case verb == "reject" && p.Kind == review.KindArchive:
			out = append(out, fmt.Sprintf("Kept %s; kiln won't suggest archiving it for %d days.", p.Skill, int(review.ArchiveAfter.Hours()/24)))
		case verb == "reject":
			out = append(out, fmt.Sprintf("Rejected #%d %s.", p.ID, p.Target()))
		case p.Kind == review.KindSkill:
			out = append(out, fmt.Sprintf("Approved #%d: wrote %s (loads in the next session).", p.ID, tildePath(p.SkillPath(ls.skillsDir))))
		case p.Kind == review.KindArchive:
			out = append(out, fmt.Sprintf("Approved #%d: archived %s · /learned revert brings it back.", p.ID, p.Skill))
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
		case review.KindArchive:
			out = append(out, fmt.Sprintf("#%d archive skill %s — %s", p.ID, p.Skill, p.Why))
		default:
			out = append(out, fmt.Sprintf("#%d memory %s (%s) — %s", p.ID, p.Key, p.Type, p.Why))
			out = append(out, "    "+p.Text, "    evidence: "+p.Evidence)
		}
	}
	return append(out, "", "/pending approve <id…|all> · /pending reject <id…|all>")
}

// learnedSource is /learned: kiln's own skills with who made each and how
// much it is used, the history of changes to them, and revert.
func learnedSource(ls learningStores) slashcommands.Source {
	return slashcommands.StaticSource(slashcommands.OriginBuiltin, []slashcommands.Command{{
		Name:         "learned",
		Description:  "List kiln's learned skills and their history, or revert a change",
		ArgumentHint: "[history [skill] | revert <n>]",
		Run: func(_ context.Context, args string) (slashcommands.Result, error) {
			return slashcommands.Result{Output: learnedRun(ls, args, time.Now())}, nil
		},
	}})
}

func learnedRun(ls learningStores, args string, now time.Time) []string {
	fields := strings.Fields(args)
	changes, err := ls.history.List()
	if err != nil {
		return []string{"Could not read the skill history: " + err.Error()}
	}
	switch {
	case len(fields) == 0:
		return learnedList(ls, changes, now)
	case fields[0] == "history" && len(fields) <= 2:
		skill := ""
		if len(fields) == 2 {
			skill = fields[1]
		}
		return learnedHistory(changes, skill, now)
	case fields[0] == "revert" && len(fields) == 2:
		n, err := strconv.Atoi(strings.TrimPrefix(fields[1], "#"))
		if err != nil {
			return []string{"Usage: /learned revert <n>, with n from /learned history"}
		}
		undo, err := ls.history.Revert(ls.skillsDir, n, ls.session)
		if err != nil {
			return []string{"Not reverted: " + err.Error() + "."}
		}
		return []string{fmt.Sprintf("Reverted #%d (%s) as #%d; it applies from the next session. /learned revert %d undoes it.", n, describeChange(changes, n), undo.N, undo.N)}
	}
	return []string{"Usage: /learned, /learned history [skill], /learned revert <n>"}
}

// learnedList shows each kiln skill: who made it, its use, and whether it
// has gone stale.
func learnedList(ls learningStores, changes []review.Change, now time.Time) []string {
	activity := review.Activity(ls.skillsDir, ls.usage.Load())
	if len(activity) == 0 {
		return []string{"No learned skills yet. /learn saves one; with learningReview enabled, kiln proposes them as you work."}
	}
	provenance := review.Provenance(changes)
	out := []string{"Skills in " + tildePath(ls.skillsDir) + ":"}
	for _, name := range sortedNames(activity) {
		r := activity[name]
		made := "made by hand"
		switch provenance[name] {
		case review.SourceReview:
			made = "learning review"
		case review.SourceAgent:
			made = "kiln (/learn, /refine or asked)"
		}
		use := "never used"
		if r.Uses > 0 {
			use = "used " + plural.Count(r.Uses, "time") + ", last " + agoText(now.Sub(time.UnixMilli(r.LastUsed)))
		}
		line := fmt.Sprintf("  %s · %s · %s", name, made, use)
		if r.Idle(now) >= review.StaleAfter {
			line += " · stale"
		}
		out = append(out, line)
	}
	return append(out, "", "/learned history [skill] · /learned revert <n>")
}

// learnedHistory lists the changes, newest first, at most 30.
func learnedHistory(changes []review.Change, skill string, now time.Time) []string {
	var out []string
	for i := len(changes) - 1; i >= 0 && len(out) < 30; i-- {
		c := changes[i]
		if skill != "" && c.Skill != skill {
			continue
		}
		out = append(out, fmt.Sprintf("  #%d %s · %s · %s", c.N, agoText(now.Sub(time.UnixMilli(c.Time))), describe(c), sourceText(c)))
	}
	if len(out) == 0 {
		return []string{"No changes recorded."}
	}
	return append(append([]string{"Changes to learned skills, newest first:"}, out...), "", "/learned revert <n> undoes one")
}

func describe(c review.Change) string {
	switch c.Op {
	case review.OpArchive:
		return "archived " + c.Skill
	case review.OpRestore:
		return "restored " + c.Skill
	}
	verb := "updated"
	if c.Before == nil && !c.BeforeTooLarge {
		verb = "created"
	}
	if c.Reverts > 0 {
		verb = fmt.Sprintf("reverted #%d:", c.Reverts)
	}
	return verb + " " + c.Skill + "/" + c.File
}

func describeChange(changes []review.Change, n int) string {
	for _, c := range changes {
		if c.N == n {
			return describe(c)
		}
	}
	return ""
}

func sourceText(c review.Change) string {
	switch c.Source {
	case review.SourceReview:
		return fmt.Sprintf("learning review, /pending #%d", c.Proposal)
	case review.SourceCurator:
		return fmt.Sprintf("unused, /pending #%d", c.Proposal)
	case review.SourceRevert:
		return "/learned revert"
	}
	return "kiln"
}

func agoText(d time.Duration) string {
	switch {
	case d < time.Hour:
		return "just now"
	case d < 24*time.Hour:
		return plural.Count(int(d/time.Hour), "hour") + " ago"
	}
	return plural.Count(int(d/(24*time.Hour)), "day") + " ago"
}

func sortedNames[V any](m map[string]V) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names
}
