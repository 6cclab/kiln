package tools

// The `skill` tool: a model-invocable way to read a named skill's full
// instructions, with a base directory so the skill's own relative file
// references resolve. A Go port of the shape Claude Code exposes for a
// plugin or project skill that isn't only reachable through the slash
// palette (see internal/claude/skills' own doc comment on
// DisableModelInvocation vs UserInvocable — the two are independent
// axes, and this tool is the model-invocable side of that).
import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/andrepato/harness/internal/tool"
)

// SkillRecord is one skill the `skill` tool can invoke: already
// qualified (project/user skills keep their bare name; plugin skills are
// "<plugin>:<name>" — see internal/claude/plugins.Skills), and already
// filtered to skills the model may invoke (disable-model-invocation:true
// skills must not be passed in; user-invocable:false skills should be —
// that flag only hides a skill from the slash menu, not from this tool).
type SkillRecord struct {
	Name        string
	Description string
	// Body is the skill's content (SKILL.md after its frontmatter).
	Body string
	// Dir is the skill's base directory (SKILL.md's parent), so its
	// relative file references resolve.
	Dir string
}

var skillParameters = json.RawMessage(`{
	"type": "object",
	"properties": {
		"skill": {"type": "string", "description": "The skill's name, exactly as listed in the system prompt's <available_skills> index."},
		"args": {"type": "string", "description": "Optional further instructions for the skill."}
	},
	"required": ["skill"]
}`)

type skillArgs struct {
	Skill string `json:"skill"`
	Args  string `json:"args"`
}

// skillToolDescription is fixed, generic text: it names no skill. The
// catalog (every skill's name, description and location) is carried
// exactly once, in the system prompt's <available_skills> index
// (internal/cli/chat.go's formatSkillsIndex) — paying for it again here,
// per skill, on every turn, would double its cost for no new
// information. This tool's own cost stays flat regardless of how many
// skills are installed.
const skillToolDescription = `Invoke a named skill by its exact name to read its full instructions and act on them. Pass the skill name and, optionally, further instructions for it.

Available skills are listed in the system prompt's <available_skills> index. When a skill there matches the task, invoke it with this tool before doing the task any other way.

Invoking an unknown or unlisted name returns an error naming what is available; a skill omitted from the index for space is still invocable here by its exact name.`

// SkillTool builds the `skill` tool from the already-resolved,
// already-filtered set of model-invocable skills for this session
// (project, user and active-plugin skills combined — see
// internal/cli/chat.go's wiring). Records are indexed by exact name;
// invoking an unlisted name (unknown, or one excluded for
// disable-model-invocation) returns an error result listing what is
// available, rather than failing the whole turn.
func SkillTool(records []SkillRecord) *tool.Tool {
	byName := make(map[string]SkillRecord, len(records))
	names := make([]string, 0, len(records))
	for _, r := range records {
		if _, exists := byName[r.Name]; !exists {
			names = append(names, r.Name)
		}
		byName[r.Name] = r
	}
	sort.Strings(names)

	return &tool.Tool{
		Name:        "skill",
		Label:       "Skill",
		Description: skillToolDescription,
		Parameters:  skillParameters,
		Execute: func(ctx context.Context, raw json.RawMessage, _ tool.Update, _ tool.Invocation) (tool.Result, error) {
			var a skillArgs
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &a); err != nil {
					return tool.Result{}, fmt.Errorf("skill: decoding arguments: %w", err)
				}
			}
			record, ok := byName[a.Skill]
			if !ok {
				available := "none"
				if len(names) > 0 {
					available = strings.Join(names, ", ")
				}
				return tool.Errorf("unknown skill %q. Available skills: %s", a.Skill, available), nil
			}
			body := record.Body
			if a.Args != "" {
				body = body + "\n\n" + a.Args
			}
			return tool.Text(fmt.Sprintf("Base directory for this skill: %s\n\n%s", record.Dir, body)), nil
		},
	}
}

// SkillDir returns a skill's base directory: filePath's own parent
// directory, i.e. SKILL.md's containing folder. Exported so
// internal/cli/chat.go's wiring can build []SkillRecord from
// skills.Skill/plugins.Skills values without duplicating the logic.
func SkillDir(filePath string) string {
	return filepath.Dir(filePath)
}
