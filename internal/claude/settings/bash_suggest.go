package settings

import (
	"path/filepath"
	"regexp"
	"strings"
)

// "Yes, and don't ask again" on a bash prompt saves allow rules built from
// the commands the line runs, the way Claude Code's permissions doc
// describes it ("Compound commands"): one rule per command that still
// needs approval, never one for the whole line, so approving
// "git status && npm test" saves a rule for npm test that a later
// "npm test" on its own matches. The rules are only offered when they are
// enough: BashDontAskRules checks the line against them before returning
// them, so the prompt never promises a grant the gate would not honour.

// MaxDontAskRules is the most rules one approval saves (Claude Code: "Up
// to 5 rules may be saved for a single compound command"). A line needing
// more is not offered "don't ask again" at all: saving only some of them
// would not stop the next prompt for the same line.
const MaxDontAskRules = 5

// neverPrefix are commands no prefix rule is built for: a shell, or a
// program that runs its arguments as a command. "sh *" or "env *" would
// approve anything ("sh -c '…'", "env rm -rf …"). Such a command gets an
// exact rule instead, which approves only what was shown.
var neverPrefix = set(
	"sh", "bash", "zsh", "fish", "csh", "tcsh", "ksh", "dash", "cmd", "powershell", "pwsh",
	"env", "xargs", "nice", "stdbuf", "nohup", "timeout", "time", "command", "builtin", "noglob",
	"sudo", "doas", "pkexec", "watch", "setsid", "ionice", "flock", "exec", "chroot", "unshare", "nsenter",
	"eval", "source", ".",
)

// subcommandWord is a second word that names a subcommand ("test", "run",
// "compose"), not an option, a path, a file name or a number.
var subcommandWord = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// BashDontAskRules returns the rules (the text inside "Bash(…)") that a
// "don't ask again" on this command line saves: one per command in it that
// no allow rule covers and that is not read-only, deduplicated, in the
// order they run. It returns nil when no safe set exists: the line cannot
// be parsed, runs something kiln cannot name, writes outside the working
// directory, has a command with a non-literal word or no rule shape (a
// [[ ]] test, a declaration), needs more than MaxDontAskRules rules, or
// would still not be allowed with them saved (a deny or ask rule, or a
// file operand a path rule guards).
func BashDontAskRules(permissions Permissions, cwd, toolName, cmd string) []string {
	if !isBashTool(toolName) {
		return nil
	}
	c := newMatchCtx(cwd)
	a := analyzeBash(cmd, c.cwd, c.home, false)
	if !a.parsed || a.unknown || a.outsideWrite || len(a.cmds) == 0 {
		return nil
	}
	roShape := a.roShape() && !a.nonLocalRedirect
	var rules []string
	seen := map[string]bool{}
	for _, bc := range a.cmds {
		if allowedByRule(permissions.Allow, bc, toolName) || (bc.readOnly && bc.local && roShape) {
			continue
		}
		r, ok := dontAskRuleFor(bc)
		if !ok {
			return nil
		}
		if !seen[r] {
			seen[r] = true
			rules = append(rules, r)
		}
	}
	if len(rules) == 0 || len(rules) > MaxDontAskRules {
		return nil
	}
	with := permissions
	with.Allow = append(append([]string(nil), permissions.Allow...), BashRules(rules)...)
	h := RuleHits(with, cwd, toolName, cmd)
	if !h.Allow || h.Deny || h.Ask || h.Unsure {
		return nil
	}
	return rules
}

// BashRules wraps rule texts as Bash permission rules: "npm test *" ->
// "Bash(npm test *)".
func BashRules(rules []string) []string {
	out := make([]string, len(rules))
	for i, r := range rules {
		out[i] = "Bash(" + r + ")"
	}
	return out
}

// dontAskRuleFor is the rule for one command: "<command> <subcommand> *"
// when its second word names a subcommand ("npm test *", "make build *"),
// otherwise the command exactly as allow rules see it ("tee out.txt").
// A shell or command-running wrapper, an exec wrapper that only an exact
// rule may approve, or a command after an assignment allow rules keep, is
// always exact. ok is false when no rule can be written: a word is not
// literal, the command has no words, or its text would read as a pattern.
func dontAskRuleFor(c bashCmd) (string, bool) {
	if !c.ruleLiteral || len(c.ruleWords) == 0 {
		return "", false
	}
	words := c.ruleWords
	name := words[0].lit
	if !c.keptAssigns && !c.exactOnly && !neverPrefix[filepath.Base(name)] &&
		len(words) >= 2 && subcommandWord.MatchString(words[1].lit) && plainRuleText(name) {
		return name + " " + words[1].lit + " *", true
	}
	if !plainRuleText(c.allowRaw) {
		return "", false
	}
	return c.allowRaw, true
}

// plainRuleText reports text that reads back as itself inside "Bash(…)":
// one line, no "*" (a wildcard in a rule), not ending in ":" (the legacy
// ":*" form).
func plainRuleText(s string) bool {
	return s != "" && !strings.ContainsAny(s, "*\n\r") && !strings.HasSuffix(s, ":")
}
