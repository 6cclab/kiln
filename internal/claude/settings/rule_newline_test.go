package settings

import "testing"

// A rule's "*" matches newlines, as Claude Code compiles its wildcard
// patterns with the dotAll flag. Without it, both directions failed: an
// allow rule missed a multi-line commit message (a needless prompt), and,
// worse, a deny or ask rule missed a command with a quoted newline in an
// argument, so "rm -rf 'a<newline>b'" ran past a Bash(rm *) deny.
func TestRuleGlobMatchesAcrossNewlines(t *testing.T) {
	cwd := t.TempDir()
	cases := []struct {
		name string
		p    Permissions
		cmd  string
		want Decision
	}{
		{"allow covers a multi-line commit message", Permissions{Allow: []string{"Bash(git commit *)"}},
			"git commit -q -m \"Add store\n\ninternal/store/filestore.go: new\"", Allow},
		{"allow covers a compound line with a multi-line message", Permissions{Allow: []string{"Bash(git add *)", "Bash(git commit *)"}},
			"git add -A && git commit -m \"a\n\nb\"", Allow},
		{"deny covers rm with a quoted newline", Permissions{Allow: []string{"Bash"}, Deny: []string{"Bash(rm *)"}},
			"rm -rf \"a\nb\"", Deny},
		{"deny colon form covers rm with a quoted newline", Permissions{Allow: []string{"Bash"}, Deny: []string{"Bash(rm:*)"}},
			"rm -rf 'x\n/'", Deny},
		{"deny covers git push with a newline in an argument", Permissions{Allow: []string{"Bash"}, Deny: []string{"Bash(git push *)"}},
			"git push origin \"main\nfoo\"", Deny},
		{"deny covers curl with a newline in its data", Permissions{Allow: []string{"Bash"}, Deny: []string{"Bash(curl *)"}},
			"curl -d \"a\nb\" https://example.com", Deny},
		{"ask covers a newline argument", Permissions{Allow: []string{"Bash"}, Ask: []string{"Bash(rm *)"}},
			"rm \"a\nb\"", Ask},
		// A newline outside quotes separates commands; each is judged.
		{"deny still covers the second line of a script", Permissions{Allow: []string{"Bash"}, Deny: []string{"Bash(rm *)"}},
			"echo hi\nrm -rf /tmp/x", Deny},
		// Matching across a newline does not make an unrelated command match.
		{"allow does not stretch to another command", Permissions{Allow: []string{"Bash(git commit *)"}},
			"git commit -m x\nrm -rf /tmp/x", Ask},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DecideIn(c.p, cwd, "bash", c.cmd, ModeManual); got != c.want {
				t.Errorf("DecideIn(%q) = %v, want %v", c.cmd, got, c.want)
			}
		})
	}
	if !MatchesRule("Bash(rm *)", "bash", "rm -rf \"a\nb\"") {
		t.Error("MatchesRule(Bash(rm *)) misses a quoted newline")
	}
}
