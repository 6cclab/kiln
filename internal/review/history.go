package review

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/andrepato/harness/internal/claude/paths"
)

// Change sources: who made a change to a kiln skill.
const (
	SourceReview  = "review"  // the learning review, approved with /pending
	SourceAgent   = "agent"   // kiln's agent with the write or edit tool (/learn, /refine, or asked to)
	SourceCurator = "curator" // archived after going unused, approved with /pending
	SourceRevert  = "revert"  // undone with /learned revert
)

// Change operations.
const (
	OpWrite   = "write"   // a file written (created or replaced)
	OpArchive = "archive" // a skill moved out of the skills directory
	OpRestore = "restore" // an archived skill moved back
)

// maxBeforeBytes is the largest previous content kept for a revert.
const maxBeforeBytes = 256 << 10

// Change is one entry in the skill history: an append-only log of every
// change kiln made under ~/.kiln/skills, with what each file held before,
// so any one can be undone. A skill with no entries was made by hand.
type Change struct {
	N       int    `json:"n"`
	Time    int64  `json:"time"`
	Skill   string `json:"skill"`
	File    string `json:"file,omitempty"` // relative to the skill's directory; "" for a whole-skill op
	Op      string `json:"op"`
	Source  string `json:"source"`
	Session string `json:"session,omitempty"`
	// Proposal is the /pending id an approval came from.
	Proposal int `json:"proposal,omitempty"`
	// Before is the file's content before a write; nil when it did not
	// exist. BeforeTooLarge marks a file too big to keep.
	Before         *string `json:"before,omitempty"`
	BeforeTooLarge bool    `json:"beforeTooLarge,omitempty"`
	// After is the sha256 of the file after a write, so a revert refuses
	// a file changed since.
	After string `json:"after,omitempty"`
	// Archive is where an archived skill went (OpArchive, OpRestore).
	Archive string `json:"archive,omitempty"`
	// Reverts is the change a revert undid.
	Reverts int `json:"reverts,omitempty"`
}

// History is the skill history, a JSONL file outside the skills directory
// so it never loads as a skill.
type History struct{ Path string }

// DefaultHistory is ~/.kiln/skill-history.jsonl.
func DefaultHistory() History {
	return History{Path: filepath.Join(paths.KilnUserDir(), "skill-history.jsonl")}
}

var historyMu sync.Mutex

// Record appends c with the next number and the current time, and returns
// it as stored.
func (h History) Record(c Change) (Change, error) {
	historyMu.Lock()
	defer historyMu.Unlock()
	all, err := h.list()
	if err != nil {
		return c, err
	}
	c.N = 1
	if len(all) > 0 {
		c.N = all[len(all)-1].N + 1
	}
	if c.Time == 0 {
		c.Time = time.Now().UnixMilli()
	}
	data, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	if err := os.MkdirAll(filepath.Dir(h.Path), 0o700); err != nil {
		return c, err
	}
	f, err := os.OpenFile(h.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return c, err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return c, err
}

// RecordWrite logs a write to path (inside skillsDir) given what it held
// before. Paths outside skillsDir are ignored, so it can watch every
// write the agent makes.
func (h History) RecordWrite(skillsDir, path string, before []byte, existed bool, after []byte, source, session string, proposal int) error {
	rel, ok := underDir(skillsDir, path)
	if !ok {
		return nil
	}
	skill, file, _ := strings.Cut(rel, "/")
	if file == "" || strings.HasPrefix(skill, ".") {
		return nil
	}
	c := Change{Skill: skill, File: file, Op: OpWrite, Source: source, Session: session, Proposal: proposal, After: hashBytes(after)}
	if existed {
		if len(before) > maxBeforeBytes {
			c.BeforeTooLarge = true
		} else {
			s := string(before)
			c.Before = &s
		}
	}
	_, err := h.Record(c)
	return err
}

// List returns every change, oldest first. A line that does not parse is
// skipped.
func (h History) List() ([]Change, error) {
	historyMu.Lock()
	defer historyMu.Unlock()
	return h.list()
}

func (h History) list() ([]Change, error) {
	f, err := os.Open(h.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Change
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4*maxBeforeBytes)
	for sc.Scan() {
		var c Change
		if json.Unmarshal(sc.Bytes(), &c) == nil && c.N > 0 {
			out = append(out, c)
		}
	}
	return out, sc.Err()
}

// Provenance says who made each skill: the source of its first recorded
// creation by kiln. A skill missing from the map was made by hand
// (including while kiln ran: SourceOutside).
func Provenance(changes []Change) map[string]string {
	out := map[string]string{}
	seen := map[string]bool{}
	for _, c := range changes {
		if seen[c.Skill] || c.Op != OpWrite || c.Before != nil || c.BeforeTooLarge || c.After == "" {
			continue
		}
		seen[c.Skill] = true
		if c.Source != SourceOutside {
			out[c.Skill] = c.Source
		}
	}
	return out
}

// Revert undoes change n: a write is put back to what the file held
// before (removed when it was new), an archive is restored. It refuses
// when the file or skill changed since, so it never overwrites later
// work. The revert is itself a change, which can be reverted in turn.
func (h History) Revert(skillsDir string, n int, session string) (Change, error) {
	all, err := h.List()
	if err != nil {
		return Change{}, err
	}
	var c *Change
	for i := range all {
		if all[i].N == n {
			c = &all[i]
		}
	}
	if c == nil {
		return Change{}, fmt.Errorf("no change #%d in the skill history", n)
	}
	switch c.Op {
	case OpWrite:
		if err := validSkillTarget(c.Skill, c.File); err != nil && c.File != "SKILL.md" {
			return Change{}, err
		}
		path := filepath.Join(skillsDir, c.Skill, filepath.FromSlash(c.File))
		current, readErr := os.ReadFile(path)
		existed := readErr == nil
		if (existed && hashBytes(current) != c.After) || (!existed && c.After != "") {
			return Change{}, fmt.Errorf("%s/%s changed after #%d, so reverting it would lose that change", c.Skill, c.File, n)
		}
		if c.BeforeTooLarge {
			return Change{}, fmt.Errorf("#%d replaced a file too large to keep a copy of", n)
		}
		undo := Change{Skill: c.Skill, File: c.File, Op: OpWrite, Source: SourceRevert, Session: session, Reverts: n}
		if existed {
			s := string(current)
			undo.Before = &s
		}
		if c.Before == nil {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return Change{}, err
			}
			removeEmptyDirs(filepath.Dir(path), skillsDir)
		} else {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return Change{}, err
			}
			if err := writeAtomic(path, []byte(*c.Before), 0o644); err != nil {
				return Change{}, err
			}
			undo.After = hashBytes([]byte(*c.Before))
		}
		return h.Record(undo)
	case OpArchive:
		dst := filepath.Join(skillsDir, c.Skill)
		if _, err := os.Stat(dst); err == nil {
			return Change{}, fmt.Errorf("a skill named %s exists again; move it away to restore the archived one", c.Skill)
		}
		if err := os.MkdirAll(skillsDir, 0o755); err != nil {
			return Change{}, err
		}
		if err := os.Rename(c.Archive, dst); err != nil {
			return Change{}, err
		}
		return h.Record(Change{Skill: c.Skill, Op: OpRestore, Source: SourceRevert, Session: session, Archive: c.Archive, Reverts: n})
	case OpRestore:
		return Archive(h, skillsDir, c.Skill, SourceRevert, session, 0)
	}
	return Change{}, fmt.Errorf("#%d cannot be reverted", n)
}

// Archive moves a skill out of skillsDir into skills-archive next to the
// history file (~/.kiln/skills-archive), where it no longer loads, and logs it so /learned revert brings it back.
func Archive(h History, skillsDir, skill, source, session string, proposal int) (Change, error) {
	if !skillName(skill) {
		return Change{}, fmt.Errorf("invalid skill name %q", skill)
	}
	src := filepath.Join(skillsDir, skill)
	if _, err := os.Stat(src); err != nil {
		return Change{}, fmt.Errorf("no kiln skill %s", skill)
	}
	dir := filepath.Join(filepath.Dir(h.Path), "skills-archive")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Change{}, err
	}
	dst := filepath.Join(dir, skill+"-"+time.Now().Format("20060102-150405"))
	if err := os.Rename(src, dst); err != nil {
		return Change{}, err
	}
	return h.Record(Change{Skill: skill, Op: OpArchive, Source: source, Session: session, Proposal: proposal, Archive: dst})
}

func removeEmptyDirs(dir, stop string) {
	for dir != stop && strings.HasPrefix(dir, stop) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
