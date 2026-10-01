package settings

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// These tests use only Decide, LoadSettings and MatchesRule, the API the
// code before Read/Edit path rules already had, so each one can be run
// against that code to show it fails there: it compared the rule text with
// the raw path argument, so every anchored or depth-floating rule missed.
//
// Ground truth: https://code.claude.com/docs/en/permissions, "Read and
// Edit" (the anchor, depth and example tables) and "Symlinks".

// pathFixture is a temp HOME and a temp project, the process chdir'd into
// the project (the "current directory" rules anchor at).
type pathFixture struct {
	home, proj string
}

func newPathFixture(t *testing.T) pathFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := t.TempDir()
	t.Chdir(proj)
	return pathFixture{home: home, proj: proj}
}

func (f pathFixture) p(rel string) string { return filepath.Join(f.proj, rel) }
func (f pathFixture) h(rel string) string { return filepath.Join(f.home, rel) }

func mkfile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// denies reports whether a lone deny rule (a CLI rule: no source file)
// blocks tool on path.
func denies(rule, tool, path string) bool {
	return Decide(Permissions{Deny: []string{rule}}, tool, path, ModeAuto) == Deny
}

// asks reports whether a lone ask rule fires.
func asks(rule, tool, path string) bool {
	return Decide(Permissions{Ask: []string{rule}}, tool, path, ModeAuto) == Ask
}

// allows reports whether a lone allow rule approves an edit-family call in
// manual mode (where an unmatched edit would ask).
func allows(rule, tool, path string) bool {
	return Decide(Permissions{Allow: []string{rule}}, tool, path, ModeManual) == Allow
}

// TestPathRules_AnchorTable is the doc's four-pattern table plus its
// warning that "/x" is not absolute.
func TestPathRules_AnchorTable(t *testing.T) {
	f := newPathFixture(t)
	outside := t.TempDir() // another absolute location, not under proj or home

	cases := []struct {
		name, rule, path string
		want             bool
	}{
		{"// is absolute", "Read(/" + outside + "/secrets/**)", filepath.Join(outside, "secrets/a/key"), true},
		{"// stays under its path", "Read(/" + outside + "/secrets/**)", filepath.Join(outside, "public/key"), false},
		{"~/ is the home directory", "Read(~/Documents/*.pdf)", f.h("Documents/a.pdf"), true},
		{"~/: * stays in one segment", "Read(~/Documents/*.pdf)", f.h("Documents/sub/a.pdf"), false},
		{"~/ is not the project", "Read(~/Documents/*.pdf)", f.p("Documents/a.pdf"), false},
		{"/ is the settings source (cwd for CLI rules)", "Read(/src/**/*.ts)", f.p("src/a/b.ts"), true},
		{"/ pattern still filters", "Read(/src/**/*.ts)", f.p("src/a/b.js"), false},
		{"relative bare pattern at the top", "Read(*.env)", f.p("x.env"), true},
		{"relative bare pattern at depth", "Read(*.env)", f.p("a/b/y.env"), true},
		{"relative bare pattern only under cwd", "Read(*.env)", filepath.Join(outside, "y.env"), false},
		{"/abs is not absolute: misses the absolute path", "Read(" + outside + "/file)", filepath.Join(outside, "file"), false},
		{"/abs is not absolute: hits under the anchor", "Read(" + outside + "/file)", f.p(outside + "/file"), true},
		{"relative path argument resolves against cwd", "Read(/src/**)", "src/a.go", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := denies(c.rule, "read", c.path); got != c.want {
				t.Errorf("deny %s on %s = %v, want %v", c.rule, c.path, got, c.want)
			}
		})
	}
}

// TestPathRules_DocExamples is the doc's bulleted example list.
func TestPathRules_DocExamples(t *testing.T) {
	f := newPathFixture(t)

	t.Run("Edit(/docs/**) is <cwd>/docs, not /docs or <cwd>/.claude/docs", func(t *testing.T) {
		if !denies("Edit(/docs/**)", "edit", f.p("docs/a.md")) {
			t.Error("did not match <cwd>/docs/a.md")
		}
		if denies("Edit(/docs/**)", "edit", "/docs/a.md") {
			t.Error("matched /docs/a.md")
		}
		if denies("Edit(/docs/**)", "edit", f.p(".claude/docs/a.md")) {
			t.Error("matched <cwd>/.claude/docs/a.md")
		}
	})
	t.Run("Read(~/.zshrc) is the home directory's .zshrc only", func(t *testing.T) {
		if !denies("Read(~/.zshrc)", "read", f.h(".zshrc")) {
			t.Error("did not match ~/.zshrc")
		}
		if denies("Read(~/.zshrc)", "read", f.h("sub/.zshrc")) {
			t.Error("matched ~/sub/.zshrc")
		}
		if denies("Read(~/.zshrc)", "read", f.p(".zshrc")) {
			t.Error("matched <cwd>/.zshrc")
		}
	})
	t.Run("Edit(//tmp/scratch.txt) is /tmp/scratch.txt", func(t *testing.T) {
		if !denies("Edit(//tmp/scratch.txt)", "edit", "/tmp/scratch.txt") {
			t.Error("did not match /tmp/scratch.txt")
		}
		if denies("Edit(//tmp/scratch.txt)", "edit", f.p("tmp/scratch.txt")) {
			t.Error("matched <cwd>/tmp/scratch.txt")
		}
	})
	t.Run("Read(src/**): allow is <cwd>/src only, deny is any depth", func(t *testing.T) {
		// Allow is observed on an edit tool; Read allow on a read tool is
		// invisible through Decide (read never asks).
		if !allows("Edit(src/**)", "edit", f.p("src/a.ts")) {
			t.Error("allow did not match <cwd>/src/a.ts")
		}
		if allows("Edit(src/**)", "edit", f.p("vendor/src/a.ts")) {
			t.Error("allow matched a nested src")
		}
		if !denies("Read(src/**)", "read", f.p("vendor/pkg/src/lib.js")) {
			t.Error("deny did not match a nested src")
		}
		if !asks("Read(src/**)", "read", f.p("vendor/pkg/src/lib.js")) {
			t.Error("ask did not match a nested src")
		}
	})
}

// TestPathRules_DepthTable is the ".env" deny table.
func TestPathRules_DepthTable(t *testing.T) {
	f := newPathFixture(t)
	parent := filepath.Dir(f.proj)
	other := t.TempDir()

	for _, rule := range []string{"Read(.env)", "Read(**/.env)"} {
		t.Run(rule, func(t *testing.T) {
			for _, p := range []string{f.p(".env"), f.p("a/b/.env")} {
				if !denies(rule, "read", p) {
					t.Errorf("did not block %s", p)
				}
			}
			for _, p := range []string{filepath.Join(parent, ".env"), filepath.Join(other, ".env")} {
				if denies(rule, "read", p) {
					t.Errorf("blocked %s, outside the current directory", p)
				}
			}
		})
	}
	t.Run("Read(//**/.env) is anywhere", func(t *testing.T) {
		for _, p := range []string{f.p("a/.env"), filepath.Join(parent, ".env"), filepath.Join(other, "x/.env")} {
			if !denies("Read(//**/.env)", "read", p) {
				t.Errorf("did not block %s", p)
			}
		}
	})
}

// TestPathRules_SrcTable is the doc's src/ vs vendor/pkg/src table.
func TestPathRules_SrcTable(t *testing.T) {
	f := newPathFixture(t)
	top, nested := f.p("src/app.ts"), f.p("vendor/pkg/src/lib.js")
	cases := []struct {
		rule      string
		list      string
		top, nest bool
	}{
		{"Edit(src/**)", "allow", true, false},
		{"Edit(src/**)", "deny", true, true},
		{"Edit(src/**)", "ask", true, true},
		{"Edit(/src/**)", "allow", true, false},
		{"Edit(/src/**)", "deny", true, false},
		{"Edit(/src/**)", "ask", true, false},
		{"Edit(**/src/**)", "allow", true, true},
		{"Edit(**/src/**)", "deny", true, true},
		{"Edit(**/src/**)", "ask", true, true},
		{"Edit(src/components/**)", "deny", false, false},
	}
	for _, c := range cases {
		t.Run(c.list+" "+c.rule, func(t *testing.T) {
			check := map[string]func(rule, tool, path string) bool{"allow": allows, "deny": denies, "ask": asks}[c.list]
			if got := check(c.rule, "edit", top); got != c.top {
				t.Errorf("src/app.ts: %v, want %v", got, c.top)
			}
			if got := check(c.rule, "edit", nested); got != c.nest {
				t.Errorf("vendor/pkg/src/lib.js: %v, want %v", got, c.nest)
			}
		})
	}
}

// TestPathRules_Negation covers "!" carve-outs: same list, same source, only
// out of relative rules listed before, never reopening a blocked directory.
func TestPathRules_Negation(t *testing.T) {
	f := newPathFixture(t)
	deny := func(rules ...string) func(path string) bool {
		return func(path string) bool {
			return Decide(Permissions{Deny: rules}, "read", path, ModeAuto) == Deny
		}
	}
	t.Run("!sample.env carves out of *.env", func(t *testing.T) {
		d := deny("Read(*.env)", "Read(!sample.env)")
		if !d(f.p("a/prod.env")) {
			t.Error("prod.env not blocked")
		}
		if d(f.p("a/sample.env")) {
			t.Error("sample.env blocked despite the carve-out")
		}
	})
	t.Run("a ! rule listed first carves nothing", func(t *testing.T) {
		if !deny("Read(!sample.env)", "Read(*.env)")(f.p("sample.env")) {
			t.Error("sample.env not blocked")
		}
	})
	t.Run("! cannot reach a ~/ rule", func(t *testing.T) {
		if !deny("Read(~/notes/**)", "Read(!~/notes/public/**)")(f.h("notes/public/a.md")) {
			t.Error("carve-out reached a ~/ rule")
		}
	})
	t.Run("! cannot reopen a file in a blocked directory", func(t *testing.T) {
		if !deny("Read(secrets/**)", "Read(!secrets/public/**)")(f.p("secrets/public/a")) {
			t.Error("secrets/public/a reopened")
		}
	})
	t.Run("a ! rule is never an allow", func(t *testing.T) {
		if allows("Edit(!src/**)", "edit", f.p("x/a.go")) {
			t.Error("a negated allow rule approved something")
		}
	})
}

// TestPathRules_NegationSameSourceOnly loads two settings files: a project
// "!" rule must not cancel a user-settings deny.
func TestPathRules_NegationSameSourceOnly(t *testing.T) {
	f := newPathFixture(t)
	writeSettings(t, f.h(".claude/settings.json"), `{"permissions":{"deny":["Read(*.env)"]}}`)
	writeSettings(t, f.p(".claude/settings.json"), `{"permissions":{"deny":["Read(!sample.env)"]}}`)
	s := LoadSettings(f.proj, LoadOptions{})
	if Decide(s.Permissions, "read", f.p("sample.env"), ModeAuto) != Deny {
		t.Error("a project ! rule cancelled a user deny")
	}

	writeSettings(t, f.p(".claude/settings.json"), `{"permissions":{"deny":["Read(*.env)","Read(!sample.env)"]}}`)
	s = LoadSettings(f.proj, LoadOptions{})
	if Decide(s.Permissions, "read", f.p("sample.env"), ModeAuto) != Deny {
		t.Error("the user deny stopped applying")
	}
	writeSettings(t, f.h(".claude/settings.json"), `{}`)
	s = LoadSettings(f.proj, LoadOptions{})
	if Decide(s.Permissions, "read", f.p("sample.env"), ModeAuto) == Deny {
		t.Error("a same-file ! rule did not carve out")
	}
}

// TestPathRules_InvalidPattern: an unusable deny/ask pattern still guards
// that exact path; an unusable allow pattern approves nothing.
func TestPathRules_InvalidPattern(t *testing.T) {
	f := newPathFixture(t)
	if !denies("Read([2024-06 Reports)", "read", f.p("[2024-06 Reports")) {
		t.Error("invalid deny pattern did not guard its exact path")
	}
	if !asks("Edit([unclosed)", "edit", f.p("[unclosed")) {
		t.Error("invalid ask pattern did not guard its exact path")
	}
	if allows("Edit([unclosed)", "edit", f.p("[unclosed")) {
		t.Error("invalid allow pattern approved its path")
	}
	if !denies("Read(../secret)", "read", filepath.Join(filepath.Dir(f.proj), "secret")) {
		t.Error("a deny pattern with .. did not guard the path it names")
	}
}

// TestPathRules_Symlinks covers the doc's symlink rules with real links.
func TestPathRules_Symlinks(t *testing.T) {
	f := newPathFixture(t)
	mkfile(t, f.h(".ssh/id_rsa"))
	mkfile(t, f.p("src/real.go"))
	if err := os.MkdirAll(f.p("project"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.h(".ssh/id_rsa"), f.p("project/key")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.h(".ssh"), f.p("sshdir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.p("src/real.go"), f.p("project/inner.go")); err != nil {
		t.Fatal(err)
	}

	t.Run("a link to a denied file is denied", func(t *testing.T) {
		p := Permissions{Allow: []string{"Read(./project/**)"}, Deny: []string{"Read(~/.ssh/**)"}}
		if Decide(p, "read", f.p("project/key"), ModeManual) != Deny {
			t.Error("project/key -> ~/.ssh/id_rsa was not denied")
		}
	})
	t.Run("a path through a linked directory is denied", func(t *testing.T) {
		if !denies("Read(~/.ssh/**)", "read", f.p("sshdir/id_rsa")) {
			t.Error("sshdir/id_rsa was not denied")
		}
		if !denies("Edit(~/.ssh/**)", "write", f.p("sshdir/new_key")) {
			t.Error("a new file through the linked directory was not denied")
		}
	})
	t.Run("an allow rule needs both the link and its target", func(t *testing.T) {
		if allows("Edit(project/**)", "edit", f.p("project/key")) {
			t.Error("allowed a link inside project/ pointing outside it")
		}
		if allows("Edit(project/**)", "edit", f.p("project/inner.go")) {
			t.Error("allowed a link whose target (src/) the rule does not cover")
		}
		p := Permissions{Allow: []string{"Edit(project/**)", "Edit(/src/**)"}}
		if Decide(p, "edit", f.p("project/inner.go"), ModeManual) != Allow {
			t.Error("did not allow a link whose path and target are both allowed")
		}
	})
	t.Run("a rule written through a linked directory applies at the real location", func(t *testing.T) {
		real := t.TempDir()
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		realHosts := filepath.Join(real, "hosts")
		if !denies("Read(/"+link+"/**)", "read", realHosts) {
			t.Error("Read(//<link>/**) did not block <real>/hosts")
		}
		if !asks("Edit(/"+link+"/**)", "edit", realHosts) {
			t.Error("ask rule through a link did not apply at the real location")
		}
	})
	t.Run("//tmp covers /private/tmp where /tmp is a link", func(t *testing.T) {
		if fi, err := os.Lstat("/tmp"); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Skip("/tmp is not a symlink here")
		}
		if !denies("Edit(//tmp/**)", "edit", "/private/tmp/x.txt") {
			t.Error("Edit(//tmp/**) did not block /private/tmp/x.txt")
		}
	})
	t.Run("//etc covers /private/etc on macOS", func(t *testing.T) {
		if runtime.GOOS != "darwin" {
			t.Skip("macOS only")
		}
		if !denies("Read(//etc/**)", "read", "/private/etc/hosts") {
			t.Error("Read(//etc/**) did not block /private/etc/hosts")
		}
	})
}

// TestPathRules_ToolMapping: Edit rules cover every edit tool, Read rules
// every read tool, a Read deny also blocks edit/write (not notebook edit).
func TestPathRules_ToolMapping(t *testing.T) {
	f := newPathFixture(t)
	target := f.p("secrets/a")
	for _, tool := range []string{"edit", "write", "multi_edit", "notebook_edit"} {
		if !denies("Edit(secrets/**)", tool, target) {
			t.Errorf("Edit deny did not cover %s", tool)
		}
	}
	for _, tool := range []string{"read", "grep", "glob", "ls"} {
		if !denies("Read(secrets/**)", tool, target) {
			t.Errorf("Read deny did not cover %s", tool)
		}
		if denies("Edit(secrets/**)", tool, target) {
			t.Errorf("Edit deny covered %s", tool)
		}
	}
	for _, tool := range []string{"edit", "write", "multi_edit"} {
		if !denies("Read(secrets/**)", tool, target) {
			t.Errorf("Read deny did not also block %s", tool)
		}
	}
	if denies("Read(secrets/**)", "notebook_edit", target) {
		t.Error("Read deny blocked notebook_edit (Claude Code does not)")
	}
	if asks("Read(secrets/**)", "write", target) {
		t.Error("a Read ask rule fired for write")
	}
	if allows("Read(secrets/**)", "write", target) {
		t.Error("a Read allow rule approved a write")
	}
	if !allows("Edit", "write", target) {
		t.Error("bare Edit allow did not cover write")
	}
	if !denies("Edit", "write", target) {
		t.Error("bare Edit deny did not cover write")
	}
	if !denies("Write", "write", target) || denies("Write", "edit", target) {
		t.Error("bare Write must match the write tool, and only it")
	}
}

// TestPathRules_AliasRules: Write(...)/MultiEdit(...)/NotebookEdit(...)/
// Glob(...) deny and ask rules are honoured as Edit/Read; allow rules are
// ignored.
func TestPathRules_AliasRules(t *testing.T) {
	f := newPathFixture(t)
	doc := f.p("docs/a.md")
	for _, rule := range []string{"Write(docs/**)", "MultiEdit(docs/**)", "NotebookEdit(docs/**)"} {
		for _, tool := range []string{"edit", "write"} {
			if !denies(rule, tool, doc) {
				t.Errorf("deny %s did not block %s", rule, tool)
			}
			if !asks(rule, tool, doc) {
				t.Errorf("ask %s did not fire for %s", rule, tool)
			}
			if allows(rule, tool, doc) {
				t.Errorf("allow %s approved %s (Claude Code ignores it)", rule, tool)
			}
		}
	}
	if !denies("Glob(secrets/**)", "read", f.p("secrets/k")) {
		t.Error("deny Glob(...) was not honoured as Read")
	}
}

// TestPathRules_ToolPathForms: the rule sees the file the tool will open,
// however the argument spells it.
func TestPathRules_ToolPathForms(t *testing.T) {
	f := newPathFixture(t)
	for _, arg := range []string{"~/.ssh/id_rsa", "@~/.ssh/id_rsa", "file://" + f.h(".ssh/id_rsa"), f.p("sub/../../" + filepath.Base(f.proj) + "/../" + filepath.Base(f.home) + "/.ssh/id_rsa")} {
		if !denies("Read(~/.ssh/**)", "read", arg) {
			t.Errorf("Read(~/.ssh/**) did not block %q", arg)
		}
	}
	if !denies("Read(.env)", "read", "@.env") {
		t.Error("Read(.env) did not block @.env")
	}
}

// TestPathRules_CaseInsensitiveFS: on macOS's default filesystem ".ENV"
// opens .env, so Read(.env) must block it.
func TestPathRules_CaseInsensitiveFS(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	f := newPathFixture(t)
	mkfile(t, f.p("a/.env"))
	if _, err := os.Stat(f.p("a/.ENV")); err != nil {
		t.Skip("this volume is case-sensitive")
	}
	if !denies("Read(.env)", "read", f.p("a/.ENV")) {
		t.Error("Read(.env) did not block a/.ENV, which opens a/.env")
	}
}

// TestPathRules_SourceAnchors loads each settings scope and checks where its
// "/path" rule anchors: project and local at the project, user at
// ~/.claude, --settings at that file's directory.
func TestPathRules_SourceAnchors(t *testing.T) {
	f := newPathFixture(t)
	extraDir := t.TempDir()
	writeSettings(t, f.h(".claude/settings.json"), `{"permissions":{"deny":["Read(/user-secrets/**)"]}}`)
	writeSettings(t, f.p(".claude/settings.json"), `{"permissions":{"deny":["Edit(/src/**)"]}}`)
	writeSettings(t, f.p(".claude/settings.local.json"), `{"permissions":{"deny":["Read(/local-secrets/**)"]}}`)
	writeSettings(t, filepath.Join(extraDir, "extra.json"), `{"permissions":{"deny":["Read(/extra-secrets/**)"]}}`)
	s := LoadSettings(f.proj, LoadOptions{Extra: filepath.Join(extraDir, "extra.json")})

	cases := []struct {
		name, tool, path string
		want             bool
	}{
		{"user /path is under ~/.claude", "read", f.h(".claude/user-secrets/k"), true},
		{"user /path is not in the project", "read", f.p("user-secrets/k"), false},
		{"project /path is under the project", "edit", f.p("src/a.go"), true},
		{"project /path is not under ~/.claude", "edit", f.h(".claude/src/a.go"), false},
		{"local /path is under the project", "read", f.p("local-secrets/k"), true},
		{"--settings /path is under that file's directory", "read", filepath.Join(extraDir, "extra-secrets/k"), true},
		{"--settings /path is not in the project", "read", f.p("extra-secrets/k"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Decide(s.Permissions, c.tool, c.path, ModeAuto) == Deny; got != c.want {
				t.Errorf("denied %s = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

// TestPathRules_Bash: Read/Edit deny rules reach the files a bash command
// names, and redirection targets.
func TestPathRules_Bash(t *testing.T) {
	newPathFixture(t)
	readDeny := Permissions{Deny: []string{"Read(.env)"}}
	editDeny := Permissions{Deny: []string{"Edit(~/.ssh/**)"}}
	cases := []struct {
		name string
		p    Permissions
		cmd  string
		want bool
	}{
		{"cat", readDeny, "cat .env", true},
		{"cat nested", readDeny, "cat sub/dir/.env", true},
		{"head with a flag value", readDeny, "head -n 5 ./.env", true},
		{"after cd", readDeny, "cd sub && cat .env", true},
		{"input redirect", readDeny, "wc -l < .env", true},
		{"output redirect is blocked by a Read deny too", readDeny, "echo X=1 > .env", true},
		{"quoted", readDeny, `cat ".env"`, true},
		{"in a pipeline", readDeny, "ls && cat .env | grep KEY", true},
		{"grep's file operand", readDeny, "grep KEY .env", true},
		{"grep's pattern is not a file", readDeny, "grep .env notes.txt", false},
		{"a subshell", readDeny, "(cat .env)", true},
		{"rtk read", readDeny, "rtk read .env", true},
		{"unrelated file", readDeny, "cat README.md", false},
		{"append redirect", editDeny, "echo key >> ~/.ssh/authorized_keys", true},
		{"$HOME", editDeny, "tee $HOME/.ssh/config", true},
		{"cp destination", editDeny, "cp id.pub ~/.ssh/authorized_keys", true},
		{"sed -i", editDeny, "sed -i 's/a/b/' ~/.ssh/config", true},
		{"&> redirect", editDeny, "make &> ~/.ssh/log", true},
		{"reading is not editing", editDeny, "cat ~/.ssh/config", false},
		{"fd duplication is not a file", editDeny, "go test ./... 2>&1", false},
		{"a write elsewhere", editDeny, "echo x > out.txt", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Decide(c.p, "bash", c.cmd, ModeBypassPermissions) == Deny; got != c.want {
				t.Errorf("%q denied = %v, want %v", c.cmd, got, c.want)
			}
		})
	}
	// bash_background runs a command too.
	if Decide(readDeny, "bash_background", "tail -f .env", ModeBypassPermissions) != Deny {
		t.Error("bash_background was not checked")
	}
}

// TestMatchesRule_PathRules: MatchesRule judges a path rule as a CLI deny
// rule in the current directory.
func TestMatchesRule_PathRules(t *testing.T) {
	f := newPathFixture(t)
	if !MatchesRule("Read(.env)", "read", f.p("a/.env")) {
		t.Error("Read(.env) did not match a nested .env")
	}
	if !MatchesRule("Edit(~/.ssh/**)", "write", "~/.ssh/k") {
		t.Error("Edit(~/.ssh/**) did not match write ~/.ssh/k")
	}
	if MatchesRule("Edit(~/.ssh/**)", "read", "~/.ssh/k") {
		t.Error("an Edit rule matched a read")
	}
}

func writeSettings(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
