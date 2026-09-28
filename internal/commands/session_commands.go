package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/andrepato/harness/internal/claude/paths"
	"github.com/andrepato/harness/internal/harness"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/plural"
	"github.com/andrepato/harness/internal/session"
	"github.com/andrepato/harness/internal/session/jsonl"
)

// SessionGate is the subset of permission.Gate that /add-dir needs. A
// narrow interface rather than the concrete type so this package does not
// require internal/claude/permission at compile time for callers that
// don't wire it up.
type SessionGate interface {
	AddRoot(dir string) string
	Roots() []string
}

// SessionCommandDeps is everything sessionCommands binds against.
type SessionCommandDeps struct {
	Lane *harness.Lane
	// Repo lists past sessions for /resume. Required.
	Repo *jsonl.Repo
	// Gate is the permission gate, for /add-dir. Nil disables it.
	Gate SessionGate
	Cwd  string
	// SessionsDir is shown in /resume's hint text.
	SessionsDir string
	// CurrentID is this session's id, left out of /resume's choices.
	CurrentID string
	// Relaunch, when set, makes /resume <id> switch sessions: it records
	// the id and the command exits, and the process restarts resuming it.
	// Nil keeps /resume to naming the restart command.
	Relaunch func(id string)
}

// ago renders a millisecond timestamp as "<n><unit> ago".
func ago(ms int64) string {
	seconds := time.Now().UnixMilli()/1000 - ms/1000
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds ago", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%dm ago", seconds/60)
	case seconds < 86400:
		return fmt.Sprintf("%dh ago", seconds/3600)
	default:
		return fmt.Sprintf("%dd ago", seconds/86400)
	}
}

// entryText extracts readable text from an entry's message, ignoring
// entries whose message has no prose (e.g. tool results).
func entryText(e session.Entry) (role, text string, ok bool) {
	if e.Type != session.EntryMessage || e.Message == nil {
		return "", "", false
	}
	t := strings.TrimSpace(msg.TextOf(blocksOf(e.Message)))
	if t == "" {
		return "", "", false
	}
	return string(e.Message.MessageRole()), t, true
}

// blocksOf returns a message's content blocks, for the message kinds that
// carry prose (user, assistant); other kinds contribute no text.
func blocksOf(m msg.Message) msg.Blocks {
	switch v := m.(type) {
	case msg.UserMessage:
		return v.Content
	case msg.AssistantMessage:
		return v.Content
	case msg.SystemMessage:
		return v.Content
	default:
		return nil
	}
}

func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// SessionCommands returns the source for /resume, /rewind, /export,
// /memory, /add-dir, /init and /config.
//
// /resume <id> does not swap sessions inside the process: resuming
// replaces the session, tools, model and gate, and doing that in place
// would leave half-torn-down state on any failure. It hands the id to
// deps.Relaunch and exits instead; the process then re-executes itself
// with --resume <id> after its normal exit path has run (cmd/kiln).
func SessionCommands(deps SessionCommandDeps) Source {
	cmds := []Command{
		{
			Name:         "resume",
			Description:  "Switch to a past session in this directory",
			ArgumentHint: "[session-id]",
			ArgumentCompletions: func(prefix string) []Completion {
				if deps.Repo == nil {
					return nil
				}
				found, err := deps.Repo.List(deps.Cwd)
				if err != nil {
					return nil
				}
				sort.Slice(found, func(i, j int) bool { return found[i].ModifiedAt > found[j].ModifiedAt })
				if len(found) > 30 {
					found = found[:30]
				}
				wanted := strings.ToLower(strings.TrimSpace(prefix))
				var out []Completion
				for _, m := range found {
					if m.ID == deps.CurrentID {
						continue
					}
					prompt := jsonl.FirstPrompt(m.Path)
					if wanted != "" && !strings.Contains(strings.ToLower(m.ID), wanted) && !strings.Contains(strings.ToLower(prompt), wanted) {
						continue
					}
					out = append(out, Completion{
						Value:       m.ID,
						Label:       fmt.Sprintf("%s  %s", ago(m.ModifiedAt), shortID(m.ID)),
						Description: orDefault(prompt, "(no prompt yet)"),
					})
				}
				return out
			},
			Run: func(ctx context.Context, args string) (Result, error) {
				if args = strings.TrimSpace(args); args != "" {
					id, problem, err := resolveSessionID(deps, args)
					if err != nil {
						return Result{}, err
					}
					if problem != "" {
						return Result{Output: []string{problem}}, nil
					}
					if deps.Relaunch == nil {
						return Result{Output: []string{
							"Resuming replaces the session, tools and model, so it happens at startup:",
							"",
							fmt.Sprintf("  kiln --resume %s", id),
						}}, nil
					}
					deps.Relaunch(id)
					return Result{Output: []string{"Resuming " + id + "…"}, Exit: true}, nil
				}
				if deps.Repo == nil {
					return Result{Output: []string{"No past sessions in this directory."}}, nil
				}
				found, err := deps.Repo.List(deps.Cwd)
				if err != nil {
					return Result{}, err
				}
				others := found[:0]
				for _, m := range found {
					if m.ID != deps.CurrentID {
						others = append(others, m)
					}
				}
				found = others
				if len(found) == 0 {
					return Result{Output: []string{"No past sessions in this directory."}}, nil
				}
				sort.Slice(found, func(i, j int) bool { return found[i].ModifiedAt > found[j].ModifiedAt })
				top := found
				if len(top) > 15 {
					top = top[:15]
				}
				lines := []string{plural.Count(len(found), "past session") + ":", ""}
				for _, m := range top {
					lines = append(lines, fmt.Sprintf("  %s  %8s  %s", shortID(m.ID), ago(m.ModifiedAt), truncate(orDefault(jsonl.FirstPrompt(m.Path), "(no prompt yet)"), 70)))
				}
				lines = append(lines, "", "Switch with /resume <id> (type /resume and a space to pick one)")
				return Result{Output: lines}, nil
			},
		},
		{
			Name:         "rewind",
			Description:  "Go back to an earlier point in this conversation",
			ArgumentHint: "[entry-id]",
			ArgumentCompletions: func(prefix string) []Completion {
				if deps.Lane == nil {
					return nil
				}
				entries, err := deps.Lane.FindEntries(context.Background())
				if err != nil {
					return nil
				}
				var turns []session.Entry
				for _, e := range entries {
					if role, _, ok := entryText(e); ok && role == "user" {
						turns = append(turns, e)
					}
				}
				if len(turns) > 20 {
					turns = turns[len(turns)-20:]
				}
				// newest first
				for i, j := 0, len(turns)-1; i < j; i, j = i+1, j-1 {
					turns[i], turns[j] = turns[j], turns[i]
				}
				wanted := strings.ToLower(strings.TrimSpace(prefix))
				var out []Completion
				for _, e := range turns {
					_, text, _ := entryText(e)
					label := truncateRunes(collapseSpace(text), 60)
					if wanted != "" && !strings.Contains(strings.ToLower(label+" "+e.ID), wanted) {
						continue
					}
					out = append(out, Completion{Value: e.ID, Label: label, Description: e.ID})
				}
				return out
			},
			Run: func(ctx context.Context, args string) (Result, error) {
				if deps.Lane == nil {
					return Result{Output: []string{"Nothing to rewind to yet."}}, nil
				}
				entries, err := deps.Lane.FindEntries(ctx)
				if err != nil {
					return Result{}, err
				}

				var turns []session.Entry
				for _, e := range entries {
					if role, _, ok := entryText(e); ok && role == "user" {
						turns = append(turns, e)
					}
				}
				trimmed := strings.TrimSpace(args)
				if trimmed == "" {
					if len(turns) > 10 {
						turns = turns[len(turns)-10:]
					}
					if len(turns) == 0 {
						return Result{Output: []string{"Nothing to rewind to yet."}}, nil
					}
					lines := []string{"Rewind to before which message?", ""}
					for _, e := range turns {
						_, text, _ := entryText(e)
						lines = append(lines, fmt.Sprintf("  %s  %s", shortID(e.ID), truncateRunes(collapseSpace(text), 60)))
					}
					lines = append(lines, "", "Rewind with /rewind <id>, or press esc twice for a picker.")
					return Result{Output: lines}, nil
				}

				// An id, or any unique prefix of one (the list shows 8
				// characters), naming one of your messages. Like the
				// picker, this goes back to just before that message.
				var match *session.Entry
				for i, e := range turns {
					if strings.HasPrefix(e.ID, trimmed) {
						if match != nil {
							return Result{}, fmt.Errorf("%q matches more than one message; use more of the id", trimmed)
						}
						match = &turns[i]
					}
				}
				if match == nil {
					return Result{}, fmt.Errorf("no message of yours has an id starting %q; /rewind lists them", trimmed)
				}
				if err := deps.Lane.NavigateTree(ctx, match.ParentID); err != nil {
					return Result{}, err
				}
				_, text, _ := entryText(*match)
				// Clear redraws the transcript as it now stands, then shows
				// this note under it.
				return Result{Output: []string{"Rewound to before: " + truncateRunes(collapseSpace(text), 60)}, Clear: true}, nil
			},
		},
		{
			Name:         "export",
			Description:  "Write this conversation to a markdown file",
			ArgumentHint: "[path]",
			Run: func(ctx context.Context, args string) (Result, error) {
				var entries []session.Entry
				if deps.Lane != nil {
					var err error
					entries, err = deps.Lane.FindEntries(ctx)
					if err != nil {
						return Result{}, err
					}
				}
				parts := []string{"# Session transcript", "", fmt.Sprintf("- directory: `%s`", deps.Cwd), ""}
				for _, e := range entries {
					role, text, ok := entryText(e)
					if !ok {
						continue
					}
					parts = append(parts, "## "+role, "", text, "")
				}

				target := strings.TrimSpace(args)
				if target == "" {
					target = "kiln-transcript-" + time.Now().Format("2006-01-02-150405") + ".md"
				}
				if !filepath.IsAbs(target) {
					target = filepath.Join(deps.Cwd, target)
				}
				if err := os.WriteFile(target, []byte(strings.Join(parts, "\n")), 0o644); err != nil {
					return Result{}, err
				}
				return Result{Output: []string{fmt.Sprintf("Wrote %d entries to %s", len(entries), target)}}, nil
			},
		},
		{
			Name:         "memory",
			Description:  "Open CLAUDE.md in your editor",
			ArgumentHint: "[user|project]",
			ArgumentCompletions: func(prefix string) []Completion {
				trimmed := strings.TrimSpace(prefix)
				var out []Completion
				for _, scope := range []string{"user", "project"} {
					if !strings.HasPrefix(scope, trimmed) {
						continue
					}
					desc := filepath.Join(deps.Cwd, paths.CLAUDEMD)
					if scope == "user" {
						desc = filepath.Join(userHome(), ".claude", paths.CLAUDEMD)
					}
					out = append(out, Completion{Value: scope, Label: scope, Description: desc})
				}
				return out
			},
			Run: func(ctx context.Context, args string) (Result, error) {
				scope := strings.TrimSpace(args)
				if scope == "" {
					scope = "project"
				}
				var path string
				if scope == "user" {
					path = filepath.Join(userHome(), ".claude", paths.CLAUDEMD)
				} else {
					path = filepath.Join(deps.Cwd, paths.CLAUDEMD)
				}

				editor := os.Getenv("VISUAL")
				if editor == "" {
					editor = os.Getenv("EDITOR")
				}
				if editor == "" {
					return Result{Output: []string{"No $EDITOR set. Edit manually:", "  " + path}}, nil
				}

				// $EDITOR may carry flags ("code --wait"), so it runs through
				// the shell with the path as a positional argument.
				cmd := exec.Command("/bin/sh", "-c", editor+` "$1"`, "sh", path)
				before := modTime(path)
				shown := path
				if rel, err := filepath.Rel(deps.Cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
					shown = rel
				} else if home := userHome(); strings.HasPrefix(path, home+string(filepath.Separator)) {
					shown = "~" + strings.TrimPrefix(path, home)
				}
				return Result{
					Output: []string{"Memory file: " + path},
					Exec:   cmd,
					ExecDone: func(err error) string {
						switch {
						case err != nil:
							return fmt.Sprintf("%s exited with an error: %s", editor, err)
						case modTime(path).Equal(before):
							return "No changes to " + shown + "."
						default:
							return "Saved " + shown + "."
						}
					},
				}, nil
			},
		},
		{
			Name:         "add-dir",
			Description:  "Allow tools to work in another directory",
			ArgumentHint: "<path>",
			ArgumentCompletions: func(prefix string) []Completion {
				typed := strings.TrimSpace(prefix)
				var base, leaf string
				if typed == "" {
					base = deps.Cwd
				} else if strings.HasSuffix(typed, "/") {
					base = typed
				} else {
					base = filepath.Dir(typed)
					leaf = filepath.Base(typed)
				}
				root := base
				if base != "" && !filepath.IsAbs(base) {
					root = filepath.Join(deps.Cwd, base)
				}
				entries, err := os.ReadDir(root)
				if err != nil {
					return nil
				}
				var out []Completion
				for _, e := range entries {
					if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || !strings.HasPrefix(e.Name(), leaf) {
						continue
					}
					out = append(out, Completion{Value: filepath.Join(root, e.Name()), Label: e.Name(), Description: root})
					if len(out) >= 50 {
						break
					}
				}
				return out
			},
			Run: func(ctx context.Context, args string) (Result, error) {
				if deps.Gate == nil {
					return Result{Output: []string{"Workspace roots are not enforced in this session."}}, nil
				}
				trimmed := strings.TrimSpace(args)
				if trimmed == "" {
					lines := []string{"Workspace roots:"}
					for _, r := range deps.Gate.Roots() {
						lines = append(lines, "  "+r)
					}
					return Result{Output: lines}, nil
				}
				target := trimmed
				if !filepath.IsAbs(target) {
					target = filepath.Join(deps.Cwd, target)
				}
				added := deps.Gate.AddRoot(target)
				return Result{Output: []string{fmt.Sprintf("Added %s. Tools may now work there without prompting.", added)}}, nil
			},
		},
		{
			Name:        "init",
			Description: "Generate a CLAUDE.md describing this codebase",
			Run: func(ctx context.Context, args string) (Result, error) {
				return Result{Prompt: strings.Join([]string{
					"Analyze this codebase and write a CLAUDE.md at the project root.",
					"",
					"Cover: what the project does, how to build/test/run it, the layout of",
					"the main directories, and any conventions a newcomer would otherwise",
					"get wrong. Read the actual files - package manifests, configs, a few",
					"representative sources - rather than guessing from names.",
					"",
					"Write instructions, not history: no changelog, no dated notes.",
					"If a CLAUDE.md already exists, improve it rather than replacing it.",
				}, "\n")}, nil
			},
		},
		{
			Name:        "config",
			Description: "Show settings and where they came from",
			Run: func(ctx context.Context, args string) (Result, error) {
				files := paths.SettingsFiles(deps.Cwd)
				lines := []string{"Settings are read in this order, later overriding earlier:", ""}
				for _, f := range files {
					lines = append(lines, fmt.Sprintf("  %-8s %s", f.Scope, f.Path))
				}
				lines = append(lines, "", "Edit a file directly, then restart. Permission lists concatenate", "across scopes rather than replacing.")
				return Result{Output: lines}, nil
			},
		},
	}

	return StaticSource(OriginBuiltin, cmds)
}

func userHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}

// modTime is path's modification time, zero when it does not exist.
func modTime(path string) time.Time {
	if info, err := os.Stat(path); err == nil {
		return info.ModTime()
	}
	return time.Time{}
}

// resolveSessionID finds the past session in this directory whose id is,
// or starts with, prefix. problem explains an unknown, ambiguous or current
// session to the user; err is a failure to list sessions at all.
func resolveSessionID(deps SessionCommandDeps, prefix string) (id, problem string, err error) {
	if deps.Repo == nil {
		return "", "No past sessions in this directory.", nil
	}
	found, err := deps.Repo.List(deps.Cwd)
	if err != nil {
		return "", "", err
	}
	var matches []string
	for _, m := range found {
		if m.ID == prefix {
			matches = []string{m.ID}
			break
		}
		if strings.HasPrefix(m.ID, prefix) {
			matches = append(matches, m.ID)
		}
	}
	switch {
	case len(matches) == 1 && matches[0] == deps.CurrentID:
		return "", matches[0] + " is this session.", nil
	case len(matches) == 1:
		return matches[0], "", nil
	case len(matches) > 1:
		return "", fmt.Sprintf("%q matches %d sessions; type more of the id.", prefix, len(matches)), nil
	}
	return "", fmt.Sprintf("No session %q in this directory. Type /resume and a space to pick one.", prefix), nil
}

// shortID is the 8-character form of a session or entry id the listings
// show; commands taking an id accept any unique prefix of it.
func shortID(id string) string {
	return id[:min(8, len(id))]
}
