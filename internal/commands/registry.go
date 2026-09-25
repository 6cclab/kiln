package commands

import (
	"context"
	"regexp"
	"sort"
	"strings"
)

// Origin records where a command came from. Shown in the palette so origin
// is never ambiguous, matching registry.ts's CommandOrigin.
type Origin string

const (
	OriginBuiltin  Origin = "builtin"
	OriginProject  Origin = "project"
	OriginPersonal Origin = "personal"
	OriginPlugin   Origin = "plugin"
	OriginSkill    Origin = "skill"
)

// Completion is one argument-completion row, consumed by the TUI's
// autocomplete provider. Mirrors pi-tui's AutocompleteItem.
type Completion struct {
	Value       string
	Label       string
	Description string
}

// Item is one row inside a ModalSpec's list.
type Item struct {
	Value       string
	Label       string
	Description string

	// Group is an optional section header this item is displayed under
	// (e.g. /mcp's "User MCPs (<path>)"). Consecutive items sharing the
	// same non-empty Group are rendered under one header; Kind-aware
	// dialogs (see ModalSpec.Kind) are the only ones that read it — the
	// generic commandDialog ignores it. Empty means no section header.
	Group string
	// Marker is a one-glyph status indicator shown before Label, e.g.
	// "✔"/"✘"/"⚠"/"◯" for /mcp server rows. Empty means no glyph column.
	Marker string
	// Error is a short, one-line failure reason (e.g. an MCP server's
	// connect error), shown in a Kind-aware detail view.
	Error string
	// Detail is the failure's full underlying text, shown dim and wrapped
	// under Error in a detail view. Empty means none.
	Detail string
	// Tools lists this item's own rows for a detail view (e.g. an MCP
	// server's tool names). Empty means none.
	Tools []string
	// Ms is a latency figure (e.g. an MCP server's connect time in
	// milliseconds) shown in a detail view. Zero means not applicable.
	Ms int64
}

// Action is one keybinding a ModalSpec offers against its selected item.
type Action struct {
	Key   string
	Label string
}

// ModalSpec is a TUI-neutral description of a panel a command wants to
// open instead of (or alongside) printing text. The registry has no
// business importing the TUI; a command that returns one in print mode
// simply falls back to its Output.
type ModalSpec struct {
	Title   string
	Header  []string
	Items   []Item
	Actions []Action

	// Kind selects which Dialog implementation the TUI builds for this
	// spec: "" for the generic, flat-list dialog; "model" for /model's
	// provider-ordered list plus the effort row; "mcp" for /mcp's
	// sectioned server list plus a per-server detail view. Consumed by
	// internal/tui.NewCommandDialog (see internal/tui/dialog_model.go and
	// dialog_mcp.go's doc comments for the exact dispatch it needs).
	Kind string
	// Effort is the current reasoning-effort label for /model's
	// "◐ <Effort> effort ←/→ to adjust" row (e.g. "Medium"). Empty omits
	// the row entirely.
	Effort string
	// SetEffort cycles the effort level (←/→) when set, returning the new
	// label or an error. Nil renders the ◐ row static (arrows are a
	// no-op) — used today, since the harness has no effort concept yet.
	SetEffort func(level string) (label string, err error)

	// Select runs when an item is chosen (Enter in the TUI, or 's' for
	// /model's session-only switch). It returns a short status message to
	// show, or an error.
	Select func(value string) (string, error)
	// SelectDefault runs instead of Select on Enter when set (only
	// /model's spec sets it): it applies the switch AND persists the
	// choice as the default for new sessions. Select still runs for 's'
	// (session-only, no persistence).
	SelectDefault func(value string) (string, error)
	// Act runs when an Action's key is pressed against a selected item's
	// value. It returns a short status message to show, or an error.
	Act func(key, value string) (string, error)
}

// Result is what a command's Run returns.
type Result struct {
	// Output is the text to display in the transcript, as lines. Nil means
	// nothing to print.
	Output []string
	// Prompt is text to send to the model as a prompt, if the command
	// expands to one (e.g. /init).
	Prompt string
	// Modal is a panel to open instead of printing. Commands that open one
	// also populate Output, so print mode degrades gracefully.
	Modal *ModalSpec
	// Exit signals the interactive shell to quit after this command (e.g.
	// /exit, /quit). Print mode ignores it.
	Exit bool
}

// Command is one slash command.
type Command struct {
	Name        string
	Description string
	// ArgumentHint is the placeholder shown after the name, e.g.
	// "<provider>". Matches Claude Code's argument-hint.
	ArgumentHint string
	Origin       Origin
	// Namespace renders as "namespace:name" for plugin commands.
	Namespace string

	Run func(ctx context.Context, args string) (Result, error)
	// ArgumentCompletions, if set, offers completions for the text typed
	// after the command name.
	ArgumentCompletions func(prefix string) []Completion
}

// QualifiedName is "namespace:name" for a namespaced command, bare "name"
// otherwise.
func QualifiedName(c Command) string {
	if c.Namespace != "" {
		return c.Namespace + ":" + c.Name
	}
	return c.Name
}

// Source is a pluggable provider of commands. Re-read on demand (Load is
// called every time the registry's cache is invalidated) so edits to
// .claude/commands appear without a restart.
type Source struct {
	Origin Origin
	Load   func() ([]Command, error)
}

// StaticSource builds a Source for a fixed list, used by the built-ins.
func StaticSource(origin Origin, cmds []Command) Source {
	out := make([]Command, len(cmds))
	for i, c := range cmds {
		c.Origin = origin
		out[i] = c
	}
	return Source{
		Origin: origin,
		Load:   func() ([]Command, error) { return out, nil },
	}
}

// Registry is the slash-command registry: one flat namespace, pluggable
// sources, later sources winning on a name collision.
type Registry struct {
	sources []Source
	cache   []Command
	cached  bool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// Add registers a source. Sources are least-specific first: a project
// command deliberately shadows a built-in of the same name, matching how
// the settings hierarchy resolves.
func (r *Registry) Add(source Source) {
	r.sources = append(r.sources, source)
	r.cache = nil
	r.cached = false
}

// Invalidate drops the cache so the next List re-reads every source.
func (r *Registry) Invalidate() {
	r.cache = nil
	r.cached = false
}

// List returns every command, deduped by qualified name (later source
// wins), sorted by qualified name, cached until Invalidate. A source whose
// Load fails contributes no commands rather than failing the whole list —
// a malformed file in .claude/commands must not take down the palette.
func (r *Registry) List() []Command {
	if r.cached {
		return r.cache
	}

	byName := make(map[string]Command)
	for _, s := range r.sources {
		cmds, err := s.Load()
		if err != nil {
			continue
		}
		for _, c := range cmds {
			byName[QualifiedName(c)] = c
		}
	}

	out := make([]Command, 0, len(byName))
	for _, c := range byName {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return QualifiedName(out[i]) < QualifiedName(out[j]) })

	r.cache = out
	r.cached = true
	return out
}

// Get looks up a command by qualified name (a leading "/" is stripped).
func (r *Registry) Get(name string) (Command, bool) {
	wanted := strings.TrimPrefix(name, "/")
	for _, c := range r.List() {
		if QualifiedName(c) == wanted {
			return c, true
		}
	}
	return Command{}, false
}

// commandLineRe matches a command name and its trailing argument text,
// after leading slashes have been collapsed.
var commandLineRe = regexp.MustCompile(`^([A-Za-z0-9_:-]+)\s*([\s\S]*)$`)

// Execute parses and runs a line of input beginning with "/". It returns
// nil, nil when the line is not a command, so the caller can send it to
// the model instead.
//
// An unknown "/word" IS treated as a failed command rather than passed
// through as a prompt: silently sending "/compcat fix this" to the model
// as prose is far more confusing than an error.
func (r *Registry) Execute(ctx context.Context, line string) (*Result, error) {
	if !strings.HasPrefix(line, "/") {
		return nil, nil
	}

	// Collapse repeated leading slashes. A completion bug once turned "/" +
	// "/model" into "//model", which fell through to the model as a prompt
	// and burned a full local-model turn before failing confusingly.
	// Anything starting with "/" is a command attempt and must be handled
	// as one.
	stripped := strings.TrimLeft(line, "/")
	m := commandLineRe.FindStringSubmatch(stripped)
	if m == nil {
		return &Result{Output: []string{"Not a valid command: " + line}}, nil
	}

	name, rest := m[1], strings.TrimSpace(m[2])
	cmd, ok := r.Get(name)
	if !ok {
		return &Result{Output: []string{"Unknown command: /" + name + ". Type / to see what is available."}}, nil
	}
	res, err := cmd.Run(ctx, rest)
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// AutocompleteItems returns palette rows for the TUI's autocomplete
// provider.
func (r *Registry) AutocompleteItems() []Completion {
	list := r.List()
	out := make([]Completion, 0, len(list))
	for _, c := range list {
		qn := QualifiedName(c)
		label := "/" + qn
		if c.ArgumentHint != "" {
			label += " " + c.ArgumentHint
		}
		out = append(out, Completion{Value: "/" + qn, Label: label, Description: c.Description})
	}
	return out
}
