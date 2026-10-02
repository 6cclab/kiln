package settings

import (
	"fmt"
	"strings"
)

// Permission rules feed the sandbox (code.claude.com/docs/en/sandboxing,
// "Permission rules"): Edit allow rules add write access the way
// sandbox.filesystem.allowWrite does, Edit deny rules add to denyWrite,
// Read deny rules add to denyRead, and WebFetch(domain:…) allow and deny
// rules add to the network allow and deny lists.
//
// As with the sandbox keys themselves (sandbox.go), a rule from a
// repository's settings does not open the sandbox wide: an Edit allow rule
// covering the home directory or more, or WebFetch(domain:*), counts only
// from user settings, --settings or the command line.

// SandboxRulePath is the path a Read or Edit rule covers, in the form the
// sandbox profile is built from: Base, an absolute directory or file, and
// Segs, the gitignore-style segments still to match under it ("**", "*",
// "?", "[…]"). Segs empty means Base itself and everything under it.
type SandboxRulePath struct {
	Base string
	Segs []string
}

// repoRule reports a rule read from a repository's settings file
// (project or local scope; --settings carries its own Root).
func repoRule(src RuleSource) bool {
	return src.File != "" && src.Root == "" && src.Scope != "user"
}

// SandboxRulePaths returns the paths permission rules add to the sandbox
// filesystem lists, each anchored the way the rule is matched for the file
// tools (pathrules.go). "!" carve-outs and bare (pathless) rules add
// nothing.
func SandboxRulePaths(p Permissions, cwd string) (allowWrite, denyWrite, denyRead []SandboxRulePath) {
	c := newMatchCtx(cwd)
	collect := func(rules []string, from []RuleSource, list ruleList, kind fileKind) []SandboxRulePath {
		var out []SandboxRulePath
		for i, rule := range rules {
			f, ok := splitFileRule(rule)
			if !ok || f.kind != kind || f.empty() {
				continue
			}
			src := sourceAt(from, i)
			r, ok := c.compilePathRule(f.kind, f.pattern, src, list)
			if !ok || r.neg {
				continue
			}
			if list == listAllow && repoRule(src) && coversHome(r.base, c.home) {
				continue
			}
			// The base and segments as written, not in the case-folded
			// form a deny rule compares in: the sandbox profile decides
			// how to match case for its platform.
			segs := r.segs
			if r.loose {
				segs = r.rawSegs
			}
			out = append(out, SandboxRulePath{Base: r.base, Segs: append([]string(nil), segs...)})
		}
		return out
	}
	allowWrite = collect(p.Allow, p.AllowFrom, listAllow, kindEdit)
	denyWrite = collect(p.Deny, p.DenyFrom, listDeny, kindEdit)
	denyRead = collect(p.Deny, p.DenyFrom, listDeny, kindRead)
	return allowWrite, denyWrite, denyRead
}

// SandboxRuleDomains returns the hosts WebFetch(domain:…) allow and deny
// rules name.
func SandboxRuleDomains(p Permissions) (allow, deny []string) {
	pick := func(rules []string, from []RuleSource, isAllow bool) []string {
		var out []string
		for i, rule := range rules {
			m := parenRule.FindStringSubmatch(strings.TrimSpace(rule))
			if m == nil || !sameTool(strings.ToLower(strings.TrimSpace(m[1])), "webfetch") {
				continue
			}
			d, ok := strings.CutPrefix(strings.TrimSpace(m[2]), "domain:")
			if !ok || strings.TrimSpace(d) == "" {
				continue
			}
			if isAllow && strings.TrimSpace(d) == "*" && repoRule(sourceAt(from, i)) {
				continue
			}
			out = append(out, strings.TrimSpace(d))
		}
		return out
	}
	return pick(p.Allow, p.AllowFrom, true), pick(p.Deny, p.DenyFrom, false)
}

// SandboxRuleWarnings names the permission rules SandboxRulePaths and
// SandboxRuleDomains leave out of the sandbox because a repository's
// settings would have opened it wide.
func SandboxRuleWarnings(p Permissions, cwd string) []string {
	c := newMatchCtx(cwd)
	var out []string
	for i, rule := range p.Allow {
		src := sourceAt(p.AllowFrom, i)
		if !repoRule(src) {
			continue
		}
		if f, ok := splitFileRule(rule); ok && f.kind == kindEdit && !f.empty() {
			if r, ok := c.compilePathRule(f.kind, f.pattern, src, listAllow); ok && !r.neg && coversHome(r.base, c.home) {
				out = append(out, fmt.Sprintf("%s: %s does not widen the sandbox (it covers the home directory or more); it still applies to the edit tools", src.File, rule))
			}
		}
		if m := parenRule.FindStringSubmatch(strings.TrimSpace(rule)); m != nil && sameTool(strings.ToLower(strings.TrimSpace(m[1])), "webfetch") {
			if d, ok := strings.CutPrefix(strings.TrimSpace(m[2]), "domain:"); ok && strings.TrimSpace(d) == "*" {
				out = append(out, fmt.Sprintf("%s: %s does not open the sandbox to every host; it still applies to web_fetch", src.File, rule))
			}
		}
	}
	return out
}
