// Package review is kiln's automatic learning review: after enough work in
// a session, one model request (no tools) reads the conversation and
// proposes skill files under ~/.kiln/skills and project memory notes. Each
// proposal waits in a queue (~/.kiln/proposals) until the person approves
// or rejects it with /pending; nothing is written before that.
//
// The review's rules come from hermes-agent's background review
// (agent/background_review.py, NousResearch, MIT); the queue replaces its
// write-freely default with kiln's approval-first stance.
package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/claude/paths"
	"github.com/andrepato/harness/internal/claude/skills"
	"github.com/andrepato/harness/internal/memory"
)

// Proposal kinds.
const (
	KindSkill  = "skill"
	KindMemory = "memory"
)

// Proposal is one change the review suggests.
type Proposal struct {
	ID      int    `json:"id"`
	Created int64  `json:"created"`
	Session string `json:"session,omitempty"`
	Kind    string `json:"kind"`
	// Why is the review's one-line reason, with what in the conversation
	// supports it.
	Why string `json:"why"`

	// Skill proposals: File is relative to ~/.kiln/skills/<Skill>/, and
	// Content replaces it whole. Base is the sha256 of the file when the
	// proposal was made ("" when it did not exist), so approval refuses a
	// file that changed since.
	Skill   string `json:"skill,omitempty"`
	File    string `json:"file,omitempty"`
	Content string `json:"content,omitempty"`
	Base    string `json:"base,omitempty"`

	// Memory proposals, stored in Scope (the project's memory scope).
	Scope    string `json:"scope,omitempty"`
	Key      string `json:"key,omitempty"`
	Type     string `json:"type,omitempty"` // preference, fact, procedure, observation
	Text     string `json:"text,omitempty"`
	Evidence string `json:"evidence,omitempty"`
}

// Target names what a proposal changes, for listings.
func (p Proposal) Target() string {
	if p.Kind == KindSkill {
		return p.Skill + "/" + p.File
	}
	return p.Key
}

// SkillPath is the absolute file a skill proposal writes.
func (p Proposal) SkillPath(skillsDir string) string {
	return filepath.Join(skillsDir, p.Skill, filepath.FromSlash(p.File))
}

// Queue is the directory holding one JSON file per pending proposal.
type Queue struct{ Dir string }

// DefaultQueue is ~/.kiln/proposals.
func DefaultQueue() Queue { return Queue{Dir: filepath.Join(paths.KilnUserDir(), "proposals")} }

// List returns the pending proposals, oldest first. A file that does not
// parse is skipped, not fatal.
func (q Queue) List() ([]Proposal, error) {
	entries, err := os.ReadDir(q.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Proposal
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(q.Dir, e.Name()))
		if err != nil {
			continue
		}
		var p Proposal
		if json.Unmarshal(data, &p) == nil && p.ID > 0 {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Add stores proposals with fresh ids and returns them as stored.
func (q Queue) Add(ps []Proposal) ([]Proposal, error) {
	if len(ps) == 0 {
		return nil, nil
	}
	existing, err := q.List()
	if err != nil {
		return nil, err
	}
	next := 1
	for _, p := range existing {
		if p.ID >= next {
			next = p.ID + 1
		}
	}
	if err := os.MkdirAll(q.Dir, 0o700); err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	out := make([]Proposal, 0, len(ps))
	for _, p := range ps {
		p.ID, p.Created = next, now
		next++
		data, err := json.MarshalIndent(p, "", "  ")
		if err != nil {
			return out, err
		}
		if err := writeAtomic(q.path(p.ID), data, 0o600); err != nil {
			return out, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Remove drops a proposal from the queue.
func (q Queue) Remove(id int) error {
	err := os.Remove(q.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("no pending proposal %d", id)
	}
	return err
}

func (q Queue) path(id int) string { return filepath.Join(q.Dir, strconv.Itoa(id)+".json") }

// Hash is the Base value for a file's current content: "" when it does not
// exist.
func Hash(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Apply makes an approved proposal's change: a skill file written under
// skillsDir, or a memory note put in store. A skill file that changed
// since the proposal was made is refused, so approval never overwrites an
// edit the person (or another approval) made in between.
func Apply(ctx context.Context, p Proposal, skillsDir string, store *memory.Store) error {
	switch p.Kind {
	case KindSkill:
		if err := validSkillTarget(p.Skill, p.File); err != nil {
			return err
		}
		path := p.SkillPath(skillsDir)
		if Hash(path) != p.Base {
			return fmt.Errorf("%s changed since this was proposed; reject it and run /refine for a fresh one", p.Target())
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return writeAtomic(path, []byte(p.Content), 0o644)
	case KindMemory:
		if store == nil {
			return errors.New("personal memory is off in this session")
		}
		_, err := store.Put(ctx, memory.Record{Scope: p.Scope, Key: p.Key, Kind: p.Type, Text: p.Text, Evidence: "kiln review: " + p.Evidence})
		return err
	}
	return fmt.Errorf("unknown proposal kind %q", p.Kind)
}

// validSkillTarget keeps a skill proposal inside its own skill directory:
// a lowercase-hyphenated skill name, and a file that is SKILL.md or a
// relative path under references/, templates/ or scripts/.
func validSkillTarget(skill, file string) error {
	if !skillName(skill) {
		return fmt.Errorf("invalid skill name %q", skill)
	}
	if file == "SKILL.md" {
		return nil
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(file)))
	if clean != file || strings.HasPrefix(clean, "/") || strings.Contains(clean, "..") {
		return fmt.Errorf("invalid skill file %q", file)
	}
	for _, dir := range []string{"references/", "templates/", "scripts/"} {
		if strings.HasPrefix(clean, dir) && len(clean) > len(dir) {
			return nil
		}
	}
	return fmt.Errorf("skill file %q is not SKILL.md or under references/, templates/ or scripts/", file)
}

func skillName(s string) bool {
	if s == "" || len(s) > 64 || s[0] == '-' || s[len(s)-1] == '-' {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// validSkillFile checks a proposed SKILL.md parses and names its directory.
func validSkillFile(skill, content string) error {
	s, ok := skills.ParseSkill(content, "SKILL.md", paths.ScopeUser)
	if !ok {
		return errors.New("SKILL.md has no frontmatter with a name and a description")
	}
	if s.Name != skill {
		return fmt.Errorf("SKILL.md names %q, not its directory %q", s.Name, skill)
	}
	return nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
