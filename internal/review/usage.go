package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/andrepato/harness/internal/claude/paths"
	"github.com/andrepato/harness/internal/plural"
)

// How long a kiln skill may sit unused: after StaleAfter the review is
// told so (it may fold the skill into another or fix it); after
// ArchiveAfter the curator proposes archiving it. Hermes-agent's curator
// uses the same 14 and 30 days (NousResearch, MIT); here archiving waits
// for approval, and a "keep" is remembered.
const (
	StaleAfter   = 14 * 24 * time.Hour
	ArchiveAfter = 30 * 24 * time.Hour
)

// SkillUse is one kiln skill's use record.
type SkillUse struct {
	Uses     int   `json:"uses"`
	LastUsed int64 `json:"lastUsed,omitempty"` // unix ms
	// KeptAt is when the person rejected archiving it; the curator does
	// not ask again for ArchiveAfter.
	KeptAt int64 `json:"keptAt,omitempty"`
	// Active is the later of LastUsed and the last change to its
	// SKILL.md (Activity fills it; not stored).
	Active int64 `json:"-"`
}

// Idle is how long the skill has gone unused and unchanged.
func (u SkillUse) Idle(now time.Time) time.Duration {
	if u.Active == 0 {
		return 0
	}
	return now.Sub(time.UnixMilli(u.Active))
}

// attrs renders the record as XML attributes for the review.
func (u SkillUse) attrs() string {
	now := time.Now()
	s := fmt.Sprintf(" uses=%q", fmt.Sprint(u.Uses))
	if u.LastUsed > 0 {
		s += fmt.Sprintf(" last_used=%q", ago(now.Sub(time.UnixMilli(u.LastUsed))))
	}
	if u.Idle(now) >= StaleAfter {
		s += ` stale="true"`
	}
	return s
}

func ago(d time.Duration) string {
	days := int(d / (24 * time.Hour))
	if days < 1 {
		return "today"
	}
	return plural.Count(days, "day") + " ago"
}

// Usage is the use counts of kiln's skills, kept outside the skills
// directory.
type Usage struct{ Path string }

// DefaultUsage is ~/.kiln/skill-usage.json.
func DefaultUsage() Usage { return Usage{Path: filepath.Join(paths.KilnUserDir(), "skill-usage.json")} }

var usageMu sync.Mutex

// Load returns the stored records; a missing or broken file is empty.
func (u Usage) Load() map[string]SkillUse {
	usageMu.Lock()
	defer usageMu.Unlock()
	return u.load()
}

func (u Usage) load() map[string]SkillUse {
	out := map[string]SkillUse{}
	data, err := os.ReadFile(u.Path)
	if err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

func (u Usage) update(name string, fn func(*SkillUse)) error {
	usageMu.Lock()
	defer usageMu.Unlock()
	all := u.load()
	rec := all[name]
	fn(&rec)
	all[name] = rec
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(u.Path), 0o700); err != nil {
		return err
	}
	return writeAtomic(u.Path, data, 0o600)
}

// RecordUse counts one use of the kiln skill at skillPath (its SKILL.md
// or directory). A skill outside skillsDir is not kiln's and is ignored.
func (u Usage) RecordUse(skillsDir, skillPath string) error {
	rel, ok := underDir(skillsDir, skillPath)
	if !ok {
		return nil
	}
	name, _, _ := strings.Cut(rel, "/")
	if !skillName(name) {
		return nil
	}
	return u.update(name, func(r *SkillUse) {
		r.Uses++
		r.LastUsed = time.Now().UnixMilli()
	})
}

// Keep records that the person chose to keep name when the curator
// proposed archiving it.
func (u Usage) Keep(name string) error {
	return u.update(name, func(r *SkillUse) { r.KeptAt = time.Now().UnixMilli() })
}

// Activity returns a record for every skill in skillsDir, with Active
// set: the later of its last use and its SKILL.md's last change.
func Activity(skillsDir string, stored map[string]SkillUse) map[string]SkillUse {
	out := map[string]SkillUse{}
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() || !skillName(e.Name()) {
			continue
		}
		info, err := os.Stat(filepath.Join(skillsDir, e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		r := stored[e.Name()]
		r.Active = max(r.LastUsed, info.ModTime().UnixMilli())
		out[e.Name()] = r
	}
	return out
}

// CuratorProposals proposes archiving each kiln-made skill (one with a
// creation in the skill history; a skill made by hand is the person's to
// manage) that has gone ArchiveAfter unused and unchanged, unless one is
// already pending or the person kept it within ArchiveAfter.
func CuratorProposals(activity map[string]SkillUse, provenance map[string]string, pending []Proposal, now time.Time) []Proposal {
	queued := map[string]bool{}
	for _, p := range pending {
		if p.Kind == KindArchive {
			queued[p.Skill] = true
		}
	}
	var out []Proposal
	for _, name := range sortedKeys(activity) {
		r := activity[name]
		if _, kilnMade := provenance[name]; !kilnMade || queued[name] || r.Idle(now) < ArchiveAfter {
			continue
		}
		if r.KeptAt > 0 && now.Sub(time.UnixMilli(r.KeptAt)) < ArchiveAfter {
			continue
		}
		why := "unused for " + plural.Count(int(r.Idle(now)/(24*time.Hour)), "day")
		if r.Uses == 0 {
			why += ", never used"
		} else {
			why += ", used " + plural.Count(r.Uses, "time")
		}
		out = append(out, Proposal{Kind: KindArchive, Skill: name, Why: why + " · /learned revert brings it back"})
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
