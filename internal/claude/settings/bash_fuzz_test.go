package settings

import (
	"context"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestBashDifferential generates command lines from a grammar of the
// constructs kiln's lexers once got wrong (operators, quotes, escapes,
// comments, continuations, heredocs, substitutions, nested shells), runs
// each in real bash with a PATH of stub commands that log their argv, and
// checks that every command bash ran is one kiln's analysis collected —
// unless kiln reported the line unparseable or running something it
// cannot name, which no allow rule approves.
//
// A few hundred seeded cases run by default; KILN_BASH_FUZZ=<n> runs n,
// and KILN_BASH_FUZZ_SEED=<s> picks the seed.
func TestBashDifferential(t *testing.T) {
	// The bash kiln runs commands with (execenv's resolveShell): /bin/bash
	// when present, which on macOS is bash 3.2.
	bash := "/bin/bash"
	if _, err := os.Stat(bash); err != nil {
		if bash, err = exec.LookPath("bash"); err != nil {
			t.Skip("bash not found")
		}
	}
	n, seed := 300, uint64(1)
	if v, err := strconv.Atoi(os.Getenv("KILN_BASH_FUZZ")); err == nil && v > 0 {
		n = v
	}
	if v, err := strconv.ParseUint(os.Getenv("KILN_BASH_FUZZ_SEED"), 10, 64); err == nil {
		seed = v
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	work := filepath.Join(dir, "work")
	for _, d := range []string{bin, work} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// Each stub process logs to a file of its own: commands run
	// concurrently (pipes, process substitutions), and one record written
	// in pieces would interleave with another.
	const stub = "#!/bin/sh\nprintf '%s\\036' \"${0##*/} $*\" >> \"$KILN_FUZZ_LOG.$$\"\n"
	for _, name := range []string{"cmda", "cmdb", "cmdc", "cmdd", "cmdf"} {
		body := stub
		if name == "cmdf" {
			body += "exit 1\n"
		}
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The programs the grammar runs commands through.
	for _, prog := range []string{"bash", "sh", "env", "nohup", "xargs"} {
		p, err := exec.LookPath(prog)
		if err != nil {
			continue
		}
		if prog == "bash" {
			p = bash
		}
		if err := os.Symlink(p, filepath.Join(bin, prog)); err != nil {
			t.Fatal(err)
		}
	}
	major := bashMajor(t, bash)

	g := &fuzzGen{r: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15)), pipeAll: major >= 4}
	var ran, checked, opaque, failures, loose int
	for i := 0; i < n; i++ {
		script := g.list(3) + "\nwait"
		// A log per case: a process substitution is not waited for, so its
		// command can log after bash exits.
		executed := runStubbed(t, bash, work, bin, filepath.Join(dir, "log"+strconv.Itoa(i)), script)
		ran += len(executed)
		a := analyzeBash(script, work, dir, false)
		if !a.parsed || a.unknown {
			opaque++
			continue
		}
		checked++
		for _, e := range executed {
			if !collected(a, e) && major < 4 && collectedName(a, e) &&
				strings.Contains(script, "{a,b}") && strings.Contains(script, "<(") {
				// bash 3.2 expands "{a,b}x" inside "<(…)" to its first
				// alternative only; bash 4+ (and kiln) expand both. The
				// command itself was collected.
				loose++
				continue
			}
			if !collected(a, e) {
				failures++
				if failures <= 10 {
					t.Errorf("bash ran %q, which kiln did not collect, from %q (collected: %q)", e, script, lines(a))
				}
				break
			}
		}
	}
	t.Logf("bash %d.x, seed %d: %d lines, %d checked, %d unparseable or unknown, %d commands run, %d missed, %d matched by name only (bash 3 brace expansion)",
		major, seed, n, checked, opaque, ran, failures, loose)
	if ran == 0 {
		t.Fatal("no stub command ever ran: the harness is broken")
	}
}

// collected reports whether the analysis has a command matching what bash
// ran: the same name and, when every word is literal, the same words (a
// trailing run of extra arguments allowed, for xargs).
func collected(a *bashAnalysis, exec string) bool {
	name, _, _ := strings.Cut(exec, " ")
	for _, c := range a.cmds {
		if c.name != name {
			continue
		}
		line := strings.TrimRight(c.line, " ")
		if !c.literal || exec == line || strings.HasPrefix(exec, line+" ") {
			return true
		}
	}
	return false
}

// collectedName reports a collected command of the same name.
func collectedName(a *bashAnalysis, exec string) bool {
	name, _, _ := strings.Cut(exec, " ")
	for _, c := range a.cmds {
		if c.name == name {
			return true
		}
	}
	return false
}

func lines(a *bashAnalysis) []string {
	var out []string
	for _, c := range a.cmds {
		out = append(out, c.line)
	}
	return out
}

func bashMajor(t *testing.T, bash string) int {
	out, err := exec.Command(bash, "-c", "echo ${BASH_VERSINFO[0]}").Output()
	if err != nil {
		t.Fatal(err)
	}
	v, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return v
}

// runStubbed runs script in bash with only the stubs on PATH and returns
// the command lines they logged.
func runStubbed(t *testing.T, bash, dir, bin, log, script string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bash, "--noprofile", "--norc", "-c", script)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + bin, "HOME=" + dir, "KILN_FUZZ_LOG=" + log}
	cmd.Stdin = strings.NewReader("")
	_ = cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("bash timed out on:\n%s", script)
	}
	if strings.Contains(script, "<(") || strings.Contains(script, ">(") {
		// A process substitution is not waited for; give it a moment.
		time.Sleep(20 * time.Millisecond)
	}
	files, _ := filepath.Glob(log + ".*")
	var out []string
	for _, f := range files {
		data, _ := os.ReadFile(f)
		for _, l := range strings.Split(string(data), "\036") {
			if l = strings.TrimRight(l, " "); l != "" {
				out = append(out, l)
			}
		}
	}
	return out
}

// fuzzGen generates command lines.
type fuzzGen struct {
	r       *rand.Rand
	pipeAll bool // the bash under test knows |& (bash 4+)
}

func (g *fuzzGen) pick(xs ...string) string { return xs[g.r.IntN(len(xs))] }

func (g *fuzzGen) name() string { return g.pick("cmda", "cmdb", "cmdc", "cmdd", "cmdf") }

var fuzzArgs = []string{
	"x", "'y z'", `"q r"`, `\#`, `a\ b`, `$'t\x41'`, `\;`, `\&`, `'#'`, `"#"`, "x#y",
	`\>`, `\|`, `\<`, `\(`, `\)`, `'\'`, `"a\"b"`, `$'\''`, "--", "-n", `"a;b"`, `'a&&b'`,
	`$'a\'b'`, `"#'"`, `\\`, "{a,b}x", "a=b",
}

func (g *fuzzGen) simple() string {
	var b strings.Builder
	switch g.r.IntN(10) {
	case 0:
		b.WriteString("FOO=1 ")
	case 1:
		b.WriteString("time ")
	case 2:
		b.WriteString("command ")
	case 3:
		b.WriteString("env A=1 ")
	case 4:
		b.WriteString("nohup ")
	}
	b.WriteString(g.name())
	for range g.r.IntN(3) {
		if g.r.IntN(6) == 0 {
			b.WriteString(" \\\n")
		} else {
			b.WriteString(" ")
		}
		b.WriteString(g.pick(fuzzArgs...))
	}
	return b.String()
}

func fuzzQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (g *fuzzGen) list(d int) string {
	if d <= 0 {
		return g.simple()
	}
	switch g.r.IntN(26) {
	case 0, 1, 2:
		return g.simple()
	case 3, 4:
		return g.list(d-1) + g.pick("; ", " && ", " || ", " | ", "\n", " & ") + g.list(d-1)
	case 5:
		if g.pipeAll {
			return g.list(d-1) + " |& " + g.list(d-1)
		}
		return g.list(d-1) + " | " + g.list(d-1)
	case 6:
		return "( " + g.list(d-1) + " )"
	case 7:
		return "{ " + g.list(d-1) + "; }"
	case 8:
		return "if " + g.list(d-1) + "; then " + g.list(d-1) + "; else " + g.list(d-1) + "; fi"
	case 9:
		return "for i in 1 2; do " + g.list(d-1) + "; done"
	case 10:
		return "while cmdf; do " + g.list(d-1) + "; done; until cmda; do " + g.list(d-1) + "; done"
	case 11:
		return "f() { " + g.list(d-1) + "; }; f"
	case 12:
		return g.simple() + " $(" + g.list(d-1) + ")"
	case 13:
		return g.simple() + " \"$(" + g.list(d-1) + ")\""
	case 14:
		return g.simple() + " `" + g.simple() + "`"
	case 15:
		return g.simple() + " <(" + g.list(d-1) + ")"
	case 16:
		return g.pick("bash", "sh") + " -c " + fuzzQuote(g.list(d-1))
	case 17:
		return "eval " + fuzzQuote(g.simple())
	case 18:
		return g.simple() + " <<" + g.pick("EOF", "'EOF'", "-EOF") + "\nline $(" + g.simple() + ") `" + g.simple() + "`\n\tEOF\nEOF\n" + g.simple()
	case 19:
		return g.pick("bash", "sh", "bash -s", "bash /dev/stdin", "bash -") + " <<" + g.pick("'EOF'", "EOF", "\"EOF\"") + "\n" + g.list(d-1) + "\nEOF\n" + g.simple()
	case 20:
		return g.simple() + " #" + g.pick("", " c", " don't", ` "`, " $(x") + g.pick("", " \\", "\\") + "\n" + g.simple()
	case 21:
		return g.name() + " a\\" + g.pick(" ", "\t", ";", "|", "&", "(", ")", "<", ">") + "#'\n" + g.name() + " '; " + g.simple() + "; " + g.name() + " ' #'"
	case 23, 24:
		// Where bash 3.2 and the parser part ways: CR bytes, arithmetic
		// and subscripts, case inside a substitution, an expansion
		// spanning a heredoc's delimiter line.
		switch g.r.IntN(7) {
		case 0:
			return g.simple() + " \\\r\n" + g.simple()
		case 1:
			return g.name() + " a\r# ; " + g.simple()
		case 2:
			return g.name() + " $(( 'a[$(" + g.simple() + ")]' ))"
		case 3:
			return g.name() + " ${x['a[$(" + g.simple() + ")]']}"
		case 4:
			return g.name() + " $(case x in y) " + g.simple() + ";; esac)"
		case 5:
			return g.name() + " <<E\n" + g.pick("${x:-", "$(", "$((") + "\nE\n" + g.simple() + "\n" + g.pick("}", ")", "))") + "\nE"
		default:
			return "unset 'a[$(" + g.simple() + ")]'; " + g.simple()
		}
	case 22:
		switch g.r.IntN(5) {
		case 0:
			return g.name() + " $$'\\'; " + g.simple() + "; " + g.name() + " \\'"
		case 1:
			return g.name() + " <<'E \\'\nE \\\n" + g.simple()
		case 2:
			return g.name() + " a \\>" + g.pick("&", "|") + " " + g.simple()
		case 3:
			return "bash <<< " + fuzzQuote(g.simple())
		default:
			return g.simple() + " | xargs " + g.name() + " x"
		}
	default:
		return g.simple() + " " + g.pick(">/dev/null", "2>&1", "&>/dev/null", "> out", "< /dev/null") + g.pick("", "; ") + g.simple()
	}
}
