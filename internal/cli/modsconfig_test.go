package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/andrepato/harness/internal/claude/paths"
	"github.com/andrepato/harness/internal/claude/permission"
	claudeplugins "github.com/andrepato/harness/internal/claude/plugins"
	claudeskills "github.com/andrepato/harness/internal/claude/skills"
	slashcommands "github.com/andrepato/harness/internal/commands"
	"github.com/andrepato/harness/internal/mods"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
	"github.com/andrepato/harness/internal/tools"
)

func TestModsConfigRows(t *testing.T) {
	rows := modsConfigRows(func() string { return "kiln/m" }, nil)
	if len(rows) != 1 || rows[0].Key != "model" || rows[0].Value != "kiln/m" || rows[0].Via != "/model" {
		t.Errorf("rows without a gate = %+v", rows)
	}
	gate := permission.NewGate(permission.GateOptions{Mode: "plan"})
	rows = modsConfigRows(nil, gate)
	if len(rows) != 1 || rows[0].Key != "permissionMode" || rows[0].Value != "plan" || rows[0].Kind != "choice" ||
		!reflect.DeepEqual(rows[0].Options[:2], []string{"manual", "acceptEdits"}) {
		t.Errorf("rows without a model = %+v", rows)
	}
	if got := modsConfigRows(nil, nil); len(got) != 0 {
		t.Errorf("no model and no gate listed %v", got)
	}
}

func TestSkillPromptInputNamesSourceAndPlugin(t *testing.T) {
	for scope, want := range map[paths.Scope]string{
		paths.ScopeUser: "userSettings", paths.ScopeProject: "projectSettings",
		paths.ScopeLocal: "projectSettings", paths.ScopePlugin: "plugin", "other": "",
	} {
		if got := skillSourceWord(scope); got != want {
			t.Errorf("skillSourceWord(%q) = %q, want %q", scope, got, want)
		}
	}
	in := skillPromptInput("squad:go", "t", map[string]any{"a": 1}, paths.ScopePlugin, "/p/SKILL.md")
	if in.Plugin != "squad" || in.Source != "plugin" || in.Skill != "squad:go" || in.Path != "/p/SKILL.md" {
		t.Errorf("plugin skill input = %+v", in)
	}
	if in := skillPromptInput("commit", "t", nil, paths.ScopeUser, ""); in.Plugin != "" {
		t.Errorf("a user skill names plugin %q", in.Plugin)
	}
}

const skillHookSource = `
export const register = (on) => {
  on('skill.prompt', { skill: 'greet' }, async ($, e, next) => {
    const r = await next(e);
    return { ...r, text: r.text + ' [' + e.source + '|' + (e.pluginName || '-') + '|' + e.frontmatter.extra + ']' };
  });
};
`

func startedSkillRuntime(t *testing.T) *modsRuntime {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	p := newTestPlugin(t, "rewriter")
	if err := os.WriteFile(filepath.Join(p.Root, "hooks", "register.js"), []byte(skillHookSource), 0o644); err != nil {
		t.Fatal(err)
	}
	r := newModsRuntime([]claudeplugins.Plugin{p}, "model-x", 2000)
	if errs := r.start(context.Background(), t.TempDir(), "s", egressDeps{}); len(errs) != 0 {
		t.Fatalf("start: %v", errs)
	}
	t.Cleanup(r.close)
	return r
}

func TestSkillToolAndSlashCommandRaiseSkillPrompt(t *testing.T) {
	rt := startedSkillRuntime(t)
	get := func() *modsRuntime { return rt }
	skill := claudeskills.Skill{Name: "greet", Description: "d", Content: "Hello.", UserInvocable: true,
		FilePath: "/skills/greet/SKILL.md", Scope: paths.ScopeUser, Frontmatter: map[string]any{"extra": "x"}}

	rec := tools.SkillRecord{Name: "greet", Body: "Hello.", Dir: "/skills/greet", Frontmatter: skill.Frontmatter, Source: "userSettings", Path: skill.FilePath}
	tl := tools.SkillToolWith([]tools.SkillRecord{rec}, nil, skillRewriter(get))
	res, err := tl.Execute(context.Background(), json.RawMessage(`{"skill":"greet"}`), func(tool.Result) {}, tool.Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := msg.TextOf(res.Content), "Base directory for this skill: /skills/greet\n\nHello. [userSettings|-|x]"; got != want {
		t.Errorf("Skill tool = %q, want %q", got, want)
	}

	cmds, err := slashcommands.SkillSourceWith([]claudeskills.Skill{skill}, skillSlashRewriter(get)).Load()
	if err != nil || len(cmds) != 1 {
		t.Fatalf("Load = %v, %v", cmds, err)
	}
	out, err := cmds[0].Run(context.Background(), "more")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Hello.\n\nmore [userSettings|-|x]"; out.Prompt != want {
		t.Errorf("/greet prompt = %q, want %q", out.Prompt, want)
	}

	// A runtime with no engine, or a nil one, leaves the text.
	for _, r := range []*modsRuntime{nil, {}} {
		r := r
		if got := skillRewriter(func() *modsRuntime { return r })(context.Background(), rec, "same"); got != "same" {
			t.Errorf("rewrite without an engine = %q", got)
		}
		if got := skillSlashRewriter(func() *modsRuntime { return r })(context.Background(), skill, "same"); got != "same" {
			t.Errorf("slash rewrite without an engine = %q", got)
		}
	}
}

func TestConfigServiceListsKilnRows(t *testing.T) {
	var s mods.Services
	modsToolDeps{rt: &modsRuntime{modelFn: func() string { return "m" }}, gate: permission.NewGate(permission.GateOptions{Mode: "auto"})}.fill(&s)
	rows := s.Config()
	if len(rows) != 2 || rows[0].Value != "m" || rows[1].Value != "auto" {
		t.Errorf("Config() = %+v", rows)
	}
	var none mods.Services
	modsToolDeps{}.fill(&none)
	if got := none.Config(); len(got) != 0 {
		t.Errorf("Config() with no runtime or gate = %+v", got)
	}
}
