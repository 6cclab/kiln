package sandbox

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// The macOS sandbox: a Seatbelt profile run through /usr/bin/sandbox-exec.
//
// The profile denies everything by default and allows what a shell and
// common command-line tools need. Its shape follows the defaults Claude
// Code documents for its own macOS sandbox
// (code.claude.com/docs/en/sandboxing):
//
//   - reads: the whole filesystem, minus denyRead (allowRead re-opens a
//     narrower path inside a denied one; the narrower rule wins);
//   - writes: the workspace roots, the sandbox temp directory and
//     allowWrite entries, minus denyWrite and the protected paths;
//   - network: nothing but the loopback port of kiln's proxy (and a
//     user-configured proxy port), so DNS and direct connections fail;
//   - Unix sockets, local listening, Apple Events and the system TLS trust
//     service only when their settings allow them.
//
// Seatbelt decides on the path the kernel resolves, so a symlink inside a
// writable root that points outside it does not make its target writable.
// Deny rules are matched without regard to ASCII case, as APFS opens
// ".GIT/HOOKS" as ".git/hooks".

// baseMachServices are the system services a command-line tool looks up in
// ordinary use: user and group lookups, logging, preferences, the per-user
// temp directory (confstr), and notifications.
var baseMachServices = []string{
	"com.apple.system.opendirectoryd.libinfo",
	"com.apple.system.opendirectoryd.membership",
	"com.apple.system.logger",
	"com.apple.logd",
	"com.apple.diagnosticd",
	"com.apple.system.notification_center",
	"com.apple.cfprefsd.agent",
	"com.apple.cfprefsd.daemon",
	"com.apple.bsd.dirhelper",
	"com.apple.SystemConfiguration.configd",
}

// appleEventServices are looked up to send Apple Events and launch
// applications (open, osascript); only with allowAppleEvents.
var appleEventServices = []string{
	"com.apple.coreservices.launchservicesd",
	"com.apple.coreservices.appleevents",
	"com.apple.lsd.mapdb",
	"com.apple.lsd.modifydb",
}

// seatbeltProfile renders p as a Seatbelt profile. It refuses a path it
// cannot express safely (a double quote, backslash or control character)
// rather than emit a profile that means something else.
func seatbeltProfile(p Plan) (string, error) {
	var b strings.Builder
	var bad error
	str := func(s string) string {
		if err := checkPath(s); err != nil && bad == nil {
			bad = err
		}
		return `"` + s + `"`
	}
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	w("(version 1)")
	w("(deny default)")
	w("(allow process-exec)")
	w("(allow process-fork)")
	w("(allow signal (target same-sandbox))")
	w("(allow process-info* (target same-sandbox))")
	w("(allow sysctl-read)")
	w("(allow file-ioctl)")
	w("(allow user-preference-read)")
	w("(allow ipc-posix-sem)")
	w("(allow ipc-posix-shm)")
	w("(allow system-socket)")

	mach := append([]string(nil), baseMachServices...)
	if p.WeakerNetwork {
		mach = append(mach, "com.apple.trustd.agent")
	}
	if p.AppleEvents {
		mach = append(mach, appleEventServices...)
	}
	var lookups []string
	allMach := false
	for _, n := range append(mach, p.MachLookup...) {
		switch {
		case n == "*":
			allMach = true
		case strings.HasSuffix(n, "*"):
			lookups = append(lookups, "(global-name-prefix "+str(strings.TrimSuffix(n, "*"))+")")
		case n != "":
			lookups = append(lookups, "(global-name "+str(n)+")")
		}
	}
	if allMach {
		w("(allow mach-lookup)")
	} else {
		w("(allow mach-lookup %s)", strings.Join(lookups, " "))
	}
	if p.AppleEvents {
		w("(allow appleevent-send)")
	}

	// Reads.
	w("(allow file-read*)")
	if !p.FilesystemDisabled {
		for _, r := range orderedReadRules(p.DenyRead, p.AllowRead) {
			verb := "allow"
			if r.deny {
				verb = "deny"
			}
			for _, m := range matchers(r.rule, r.deny) {
				w("(%s file-read* %s)", verb, m)
			}
		}
		// Path lookups need to stat the directories above an allowed read.
		for _, r := range p.AllowRead {
			for _, a := range ancestors(r.Path) {
				for _, s := range spellings(a) {
					w("(allow file-read-metadata (literal %s))", str(s))
				}
			}
		}
	}

	// Writes.
	if p.FilesystemDisabled {
		w("(allow file-write*)")
	} else {
		var allow []string
		for _, root := range p.WriteRoots {
			allow = append(allow, "(subpath "+str(realPath(root))+")")
		}
		for _, g := range p.WriteGlobs {
			allow = append(allow, matchers(Rule{Path: realPath(g.Path), Segs: g.Segs}, false)...)
		}
		allow = append(allow,
			`(literal "/dev/null")`, `(literal "/dev/zero")`, `(literal "/dev/dtracehelper")`,
			`(literal "/dev/stdout")`, `(literal "/dev/stderr")`, `(regex #"^/dev/fd/")`)
		w("(allow file-write* %s)", strings.Join(allow, " "))
		for _, g := range p.GitDirs {
			for _, rule := range gitDirRules(g) {
				w("%s", rule)
			}
		}
		var deny []string
		for _, r := range p.DenyWrite {
			deny = append(deny, matchers(r, true)...)
		}
		for _, l := range p.DenyWriteLiteral {
			for _, s := range spellings(l) {
				deny = append(deny, "(regex #\"^"+foldCase(regexQuote(s))+"$\")")
			}
		}
		if len(deny) > 0 {
			w("(deny file-write* %s)", strings.Join(deny, " "))
		}
	}

	// Terminals: a sandboxed command has no terminal of its own (the bash
	// tools give it pipes), and opening the user's (/dev/tty, a
	// /dev/ttysNNN) would let it read keystrokes typed to kiln or push
	// input and escape sequences into the terminal. Last, so nothing
	// above re-allows it.
	w(`(deny file-read* file-write* file-ioctl (regex #"^/dev/tty") (regex #"^/dev/pty") (literal "/dev/console") (literal "/dev/ptmx"))`)

	// Network.
	for _, port := range []int{p.HTTPProxyPort, p.SOCKSProxyPort} {
		if port > 0 {
			w(`(allow network-outbound (remote ip "localhost:%d"))`, port)
		}
	}
	if p.AllowLocalBinding {
		w(`(allow network-bind (local ip "*:*"))`)
		w(`(allow network-inbound (local ip "*:*"))`)
		w(`(allow network-outbound (remote ip "localhost:*"))`)
	}
	if p.AllowAllUnixSockets {
		w("(allow network-outbound (remote unix-socket))")
		w("(allow network-bind (local unix-socket))")
	} else if len(p.UnixSockets) > 0 {
		var socks []string
		for _, s := range p.UnixSockets {
			for _, sp := range spellings(s) {
				socks = append(socks, "(path-literal "+str(sp)+")")
			}
		}
		w("(allow network-outbound (remote unix-socket %s))", strings.Join(socks, " "))
	}
	if bad != nil {
		return "", bad
	}
	return b.String(), nil
}

// checkPath refuses a path a Seatbelt string or regex literal cannot carry
// unchanged.
func checkPath(s string) error {
	for _, r := range s {
		if r == '"' || r == '\\' || r < 0x20 || r == 0x7f {
			return fmt.Errorf("sandbox: cannot express path %q in a Seatbelt profile", s)
		}
	}
	return nil
}

type readRule struct {
	rule Rule
	deny bool
}

// orderedReadRules orders denyRead and allowRead so that the narrower
// rule wins where they overlap (Claude Code: "When read rules overlap,
// the rule with the narrower path applies"): Seatbelt applies the last
// matching rule, so broader rules come first, and a deny comes after an
// allow of the same reach. A wildcard rule counts as narrower than the
// directory it is under.
func orderedReadRules(deny, allow []Rule) []readRule {
	var all []readRule
	for _, r := range allow {
		all = append(all, readRule{r, false})
	}
	for _, r := range deny {
		all = append(all, readRule{r, true})
	}
	depth := func(r Rule) int {
		return strings.Count(filepath.Clean(r.Path), "/")*2 + len(r.Segs)*2
	}
	sort.SliceStable(all, func(i, j int) bool {
		di, dj := depth(all[i].rule), depth(all[j].rule)
		if di != dj {
			return di < dj
		}
		return !all[i].deny && all[j].deny
	})
	return all
}

// matchers renders a Rule as Seatbelt filters, one per spelling of its
// base (as written and with symlinks resolved). Deny filters ignore ASCII
// case.
func matchers(r Rule, deny bool) []string {
	var out []string
	for _, base := range spellings(r.Path) {
		if checkPath(base+"/"+strings.Join(r.Segs, "/")) != nil {
			// Unrepresentable: fall back to a filter that matches
			// everything for a deny (fail closed), nothing for an allow.
			if deny {
				out = append(out, `(regex #"^/")`)
			}
			continue
		}
		re := regexQuote(base) + globRegex(r.Segs) + "(/.*)?"
		if deny {
			re = foldCase(re)
		} else if len(r.Segs) == 0 {
			out = append(out, `(subpath "`+base+`")`)
			continue
		}
		out = append(out, `(regex #"^`+re+`$")`)
	}
	return out
}

// globRegex turns gitignore-style segments into a regular expression for
// the part of a path under a rule's base: "**" any number of directories,
// "*" any run within one segment, "?" one character, "[…]" a class.
func globRegex(segs []string) string {
	var b strings.Builder
	for i, s := range segs {
		if s == "**" {
			if i == len(segs)-1 {
				b.WriteString("/.*")
			} else {
				b.WriteString("(/[^/]+)*")
			}
			continue
		}
		b.WriteString("/")
		for j := 0; j < len(s); j++ {
			c := s[j]
			switch c {
			case '*':
				b.WriteString("[^/]*")
			case '?':
				b.WriteString("[^/]")
			case '[':
				end := strings.IndexByte(s[j+1:], ']')
				if end < 0 {
					b.WriteString(`\[`)
					continue
				}
				class := strings.ReplaceAll(s[j+1:j+1+end], "/", "")
				if rest, ok := strings.CutPrefix(class, "!"); ok {
					class = "^" + rest // gitignore's negated class
				}
				b.WriteString("[" + class + "]")
				j += end + 1
			default:
				b.WriteString(regexQuote(string(c)))
			}
		}
	}
	return b.String()
}

// regexQuote escapes the regular-expression metacharacters in a literal.
func regexQuote(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`.+*?()|[]{}^$`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// foldCase makes a regular expression match ASCII letters in either case,
// leaving character classes and escapes alone.
func foldCase(re string) string {
	var b strings.Builder
	inClass, esc := false, false
	for _, r := range re {
		switch {
		case esc:
			b.WriteRune(r)
			esc = false
		case r == '\\':
			b.WriteRune(r)
			esc = true
		case inClass:
			b.WriteRune(r)
			if r == ']' {
				inClass = false
			}
		case r == '[':
			b.WriteRune(r)
			inClass = true
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			lo, up := strings.ToLower(string(r)), strings.ToUpper(string(r))
			b.WriteString("[" + lo + up + "]")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ancestors returns the directories above p, nearest last, root excluded.
func ancestors(p string) []string {
	var out []string
	for d := filepath.Dir(filepath.Clean(p)); d != filepath.Dir(d); d = filepath.Dir(d) {
		out = append([]string{d}, out...)
	}
	return out
}
