// Package cli parses command-line arguments for the harness binary.
//
// Ported from the TypeScript reference at src/cli-args.ts. The flag names and
// semantics come from `claude --help`, so muscle memory carries over: someone
// who types `claude -c` should get the same result from `harness -c`.
package cli

import (
	"regexp"
	"strconv"
	"strings"
)

// Args is the result of parsing argv. Mirrors ParsedArgs in cli-args.ts.
type Args struct {
	// Command is the first non-flag argument, when it names a subcommand.
	Command string
	// Positional is the positional arguments after the subcommand.
	Positional []string

	Print        bool
	PrintPrompt  string
	OutputFormat string // "text" | "json" | "stream-json"

	ContinueLatest bool
	// Resume holds the session id to resume, or "" with ResumeLatest=true when
	// `--resume` was given with no id (resume the latest).
	Resume       string
	ResumeLatest bool
	ResumeSet    bool
	SessionID    string
	ForkSession  bool
	Name         string

	Model  string
	Effort string // "" | "low" | "medium" | "high" | "xhigh" | "max"

	PermissionMode string

	// MaxTurns caps the number of assistant turns a print-mode (-p) run may
	// take before it is cancelled. 0 means unlimited. Interactive mode
	// ignores this field entirely — see runPrintMode's handling in chat.go.
	MaxTurns int
	// MaxTurnsErr is set instead of MaxTurns when --max-turns's value
	// wasn't a positive integer. runPrintMode reports it and exits 1
	// before doing anything else; there is no general ParseError path in
	// this file to route it through (see Parse's doc comment).
	MaxTurnsErr string

	AddDir          []string
	AllowedTools    []string
	DisallowedTools []string

	Settings        string
	SettingSources  []string
	MCPConfig       string
	StrictMCPConfig bool

	SystemPrompt       string
	AppendSystemPrompt string

	ScreenReader bool
	Verbose      bool
	Debug        bool
	Version      bool
	Help         bool
	// Fullscreen selects kiln's alt-screen TUI mode: a scrolling transcript
	// viewport with the input pinned at the bottom, toggled at runtime with
	// ctrl+f. This is the default now — set true unless --inline was given
	// (see Parse's post-processing below). Falls back to inline under
	// --ax-screen-reader (see internal/cli/tui.go's RunInteractive).
	Fullscreen bool
	// Inline opts out of the fullscreen (alt-screen) default, keeping the
	// transcript in native scrollback. --fullscreen is still accepted (as a
	// no-op) for compatibility with scripts/muscle memory from before
	// fullscreen became the default.
	Inline bool

	// Unknown is flags that look like flags but are not recognized.
	Unknown []string
}

// valued is the set of flags that take a value. Everything else is boolean.
var valued = map[string]bool{
	"--output-format":        true,
	"--resume":               true,
	"-r":                     true,
	"--session-id":           true,
	"--name":                 true,
	"-n":                     true,
	"--model":                true,
	"--effort":               true,
	"--permission-mode":      true,
	"--max-turns":            true,
	"--add-dir":              true,
	"--allowed-tools":        true,
	"--allowedTools":         true,
	"--disallowed-tools":     true,
	"--disallowedTools":      true,
	"--settings":             true,
	"--setting-sources":      true,
	"--mcp-config":           true,
	"--system-prompt":        true,
	"--append-system-prompt": true,
}

// commands is the set of subcommand words, so `harness login anthropic` is
// not read as a prompt.
var commands = map[string]bool{
	"providers": true,
	"models":    true,
	"login":     true,
	"logout":    true,
	"doctor":    true,
	"mcp":       true,
	"session":   true,
}

var wsOrComma = regexp.MustCompile(`[\s,]+`)

// SplitToolList splits "Bash(git *) Edit" on whitespace, but keeps a rule
// with spaces inside parentheses intact. It also accepts the comma form
// ("Read,Write,Edit") people use interchangeably, except inside parens.
func SplitToolList(value string) []string {
	var out []string
	var current strings.Builder
	depth := 0
	for _, ch := range value {
		switch ch {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		}
		if isSpace(ch) && depth == 0 {
			if current.Len() > 0 {
				out = append(out, current.String())
			}
			current.Reset()
			continue
		}
		current.WriteRune(ch)
	}
	if current.Len() > 0 {
		out = append(out, current.String())
	}

	result := make([]string, 0, len(out))
	for _, r := range out {
		if strings.Contains(r, ",") && !strings.Contains(r, "(") {
			for _, part := range strings.Split(r, ",") {
				if part != "" {
					result = append(result, part)
				}
			}
		} else if r != "" {
			result = append(result, r)
		}
	}
	return result
}

func isSpace(ch rune) bool {
	switch ch {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	default:
		return false
	}
}

// Parse parses argv (without the program name) into Args.
//
// Both `--flag value` and `--flag=value` are accepted. A VALUED flag never
// swallows a following token that itself looks like a flag (starts with
// "-"), because some valued flags (e.g. --resume) are valid with no value at
// all — the following token in that case is the next flag, not this one's
// value.
func Parse(argv []string) Args {
	args := Args{
		Positional:      []string{},
		AddDir:          []string{},
		AllowedTools:    []string{},
		DisallowedTools: []string{},
		Unknown:         []string{},
	}

	for i := 0; i < len(argv); i++ {
		token := argv[i]

		if !strings.HasPrefix(token, "-") {
			if args.Command == "" && len(args.Positional) == 0 && commands[token] {
				args.Command = token
			} else {
				args.Positional = append(args.Positional, token)
			}
			continue
		}

		// `--flag=value` and `--flag value` are the same thing.
		flag := token
		var value string
		hasValue := false
		if eq := strings.IndexByte(token, '='); eq != -1 {
			flag = token[:eq]
			value = token[eq+1:]
			hasValue = true
		}
		if !hasValue && valued[flag] {
			if i+1 < len(argv) {
				next := argv[i+1]
				if !strings.HasPrefix(next, "-") {
					value = next
					hasValue = true
					i++
				}
			}
		}

		switch flag {
		case "-p", "--print":
			args.Print = true
		case "--output-format":
			if value == "json" || value == "stream-json" || value == "text" {
				args.OutputFormat = value
			}
		case "-c", "--continue":
			args.ContinueLatest = true
		case "-r", "--resume":
			args.ResumeSet = true
			if hasValue {
				args.Resume = value
				args.ResumeLatest = false
			} else {
				args.ResumeLatest = true
			}
		case "--session-id":
			args.SessionID = value
		case "--fork-session":
			args.ForkSession = true
		case "-n", "--name":
			args.Name = value
		case "--model":
			args.Model = value
		case "--effort":
			switch value {
			case "low", "medium", "high", "xhigh", "max":
				args.Effort = value
			}
		case "--permission-mode":
			args.PermissionMode = value
		case "--max-turns":
			if n, err := strconv.Atoi(value); hasValue && err == nil && n > 0 {
				args.MaxTurns = n
			} else {
				args.MaxTurnsErr = "kiln: --max-turns expects a positive integer"
			}
		case "--add-dir":
			// Repeatable, and `claude --help` documents it as variadic.
			if value != "" {
				for _, d := range wsOrComma.Split(value, -1) {
					if d != "" {
						args.AddDir = append(args.AddDir, d)
					}
				}
			}
		case "--allowed-tools", "--allowedTools":
			if value != "" {
				args.AllowedTools = append(args.AllowedTools, SplitToolList(value)...)
			}
		case "--disallowed-tools", "--disallowedTools":
			if value != "" {
				args.DisallowedTools = append(args.DisallowedTools, SplitToolList(value)...)
			}
		case "--settings":
			args.Settings = value
		case "--setting-sources":
			if hasValue {
				var sources []string
				for _, s := range strings.Split(value, ",") {
					s = strings.TrimSpace(s)
					if s != "" {
						sources = append(sources, s)
					}
				}
				args.SettingSources = sources
			}
		case "--mcp-config":
			args.MCPConfig = value
		case "--strict-mcp-config":
			args.StrictMCPConfig = true
		case "--system-prompt":
			args.SystemPrompt = value
		case "--append-system-prompt":
			args.AppendSystemPrompt = value
		case "--ax-screen-reader":
			args.ScreenReader = true
		case "--fullscreen":
			// Accepted as a no-op: fullscreen is already the default (see
			// the post-processing below). Kept for compatibility with
			// scripts/muscle memory from before it was.
		case "--inline":
			args.Inline = true
		case "--verbose":
			args.Verbose = true
		case "--debug":
			args.Debug = true
		case "-v", "--version":
			args.Version = true
		case "-h", "--help":
			args.Help = true
		default:
			// Collected rather than ignored: a mistyped flag that silently does
			// nothing is how a script ends up not doing what it says.
			args.Unknown = append(args.Unknown, flag)
		}
	}

	// In print mode the prompt is whatever positional text is left.
	if args.Print && len(args.Positional) > 0 {
		args.PrintPrompt = strings.Join(args.Positional, " ")
	}

	// Fullscreen is the default; --inline opts out.
	args.Fullscreen = !args.Inline

	return args
}

// Help is the help text, copied from cli-args.ts.
const Help = `kiln - a coding agent with Claude Code's interface, on any model

usage:
  kiln [options]                     start an interactive session
  kiln -p "prompt" [options]         run one prompt and exit
  kiln <command> [args]

commands:
  providers                          list providers and auth status
  models                             list models, tiers and budgets
  login <provider>                   log in with an API key or subscription
  logout <provider>                  drop a stored credential

session:
  -c, --continue                     continue the most recent session here
  -r, --resume [id]                  resume a session by id
      --session-id <uuid>            use a specific session id
      --fork-session                 branch instead of continuing in place
  -n, --name <name>                  name the session

model:
      --model <provider/model>       e.g. ollama/qwen3.8:latest
      --effort <level>               low | medium | high | xhigh | max

permissions:
      --permission-mode <mode>       manual | acceptEdits | auto | plan |
                                     dontAsk | bypassPermissions
      --max-turns <n>                stop after n assistant turns (-p only)
      --add-dir <dirs>               extra directories tools may touch
      --allowed-tools "Bash(git *)"  allow without prompting
      --disallowed-tools "Write"     deny outright

config:
      --settings <file>              extra settings file
      --setting-sources <list>       user,project,local
      --mcp-config <file>            MCP servers (default ~/.claude.json)
      --strict-mcp-config            use only --mcp-config
      --system-prompt <text>         replace the system prompt
      --append-system-prompt <text>  add to the system prompt

output:
  -p, --print                        non-interactive: print and exit
      --output-format <fmt>          text | json | stream-json
      --verbose                      report tool calls on stderr
      --debug                        debug-level run log; prints its path (see kiln doctor)
      --ax-screen-reader             flat text, no borders or animation (always inline: screen readers read scrollback)
      --inline                       native scrollback instead of the alt-screen TUI (default fullscreen; ctrl+f toggles)
      --fullscreen                   accepted for compatibility; fullscreen is already the default

  -v, --version
  -h, --help`
