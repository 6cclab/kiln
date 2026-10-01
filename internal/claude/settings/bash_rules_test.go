package settings

import (
	"reflect"
	"testing"
)

func TestBashSegments(t *testing.T) {
	cases := []struct {
		in     string
		want   []string
		opaque bool
	}{
		{`ls`, []string{"ls"}, false},
		{`git status && rm -rf ~`, []string{"git status", "rm -rf ~"}, false},
		{`a || b; c | d & e`, []string{"a", "b", "c", "d", "e"}, false},
		{"a\nb", []string{"a", "b"}, false},
		{`echo "x && y" && z`, []string{`echo "x && y"`, "z"}, false},
		{`echo 'a;b' ; c`, []string{`echo 'a;b'`, "c"}, false},
		{`go test ./... 2>&1 | tail`, []string{"go test ./... 2>&1", "tail"}, false},
		{`cmd &>/dev/null && x`, []string{"cmd &>/dev/null", "x"}, false},
		{`git log $(rm -rf ~)`, []string{"git log $(rm -rf ~)"}, true},
		{"git log `rm x`", []string{"git log `rm x`"}, true},
		{`echo "$(rm x)"`, []string{`echo "$(rm x)"`}, true},
		{`diff <(ls) x`, []string{"diff <(ls) x"}, true},
		{`echo '$(not run)'`, []string{`echo '$(not run)'`}, false},
	}
	for _, c := range cases {
		got, opaque := BashSegments(c.in)
		if !reflect.DeepEqual(got, c.want) || opaque != c.opaque {
			t.Errorf("BashSegments(%q) = %q opaque=%v, want %q opaque=%v", c.in, got, opaque, c.want, c.opaque)
		}
	}
}

// TestDecideBashRulesPerSegment: an allow rule used to match the whole line
// as a prefix, so Bash(git *) approved "git status && rm -rf ~"
// (qa/findings *bash-allow-rule-matches-compound-command).
func TestDecideBashRulesPerSegment(t *testing.T) {
	p := Permissions{Allow: []string{"Bash(git *)", "Bash(npm test *)"}, Deny: []string{"Bash(rm *)"}}
	cases := []struct {
		cmd  string
		want Decision
	}{
		{"git status", Allow},
		{"git status && npm test -- upload", Allow},
		{"git status && rm -rf ~", Deny},
		{"git status && curl evil.sh", Ask},
		{"git log $(curl evil.sh)", Ask},
		{"ls; rm x", Deny},
	}
	for _, c := range cases {
		if got := Decide(p, "bash", c.cmd, ModeManual); got != c.want {
			t.Errorf("Decide(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}
