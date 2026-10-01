package tui

import "testing"

// Remaining render-golden states from the Phase 5 list (see
// docs/kiln-design.md / the plan): plain user/text blocks, a tool call
// still running, the generic and bash permission prompts at their
// selectable rows, the edit permission prompt, the empty plan, the
// remaining status-line modes, and a palette with its second row
// selected. See golden_test.go for the harness these use, and
// blocks_golden_test.go / status_golden_test.go / note_behaviour_test.go
// for the states already pinned.

func TestRenderGolden_UserPlain(t *testing.T) {
	withRenderEnv(t, 80)
	assertRenderGolden(t, "user-plain", RenderUserMessage("add a README with one line describing this project", 80))
}

func TestRenderGolden_TextPlain(t *testing.T) {
	withRenderEnv(t, 80)
	assertRenderGolden(t, "text-plain", RenderAssistantText([]string{"Done. Added the README.", "It has one line describing the project."}))
}

func TestRenderGolden_ToolRunning(t *testing.T) {
	withRenderEnv(t, 80)
	view := ToolCallView{
		Name:       "Bash",
		PrimaryArg: "npm test -- upload",
		Status:     CallRunning,
	}
	assertRenderGolden(t, "tool-running", RenderToolCall(view))
}

func TestRenderGolden_Plan0of5(t *testing.T) {
	withRenderEnv(t, 80)
	items := []TodoView{
		{Content: "Read the upload handler", Status: TodoPendingStatus},
		{Content: "Add the retry loop", Status: TodoPendingStatus},
		{Content: "Wire the retry loop into the route", Status: TodoPendingStatus},
		{Content: "Add a test", Status: TodoPendingStatus},
		{Content: "Update the docs", Status: TodoPendingStatus},
	}
	assertRenderGolden(t, "plan-0of5", RenderPlan(items, 80))
}

func TestRenderGolden_StatusLineBypass(t *testing.T) {
	withRenderEnv(t, 80)
	s := StatusState{Mode: "bypassPermissions", Cwd: "~/src/relay-api", Git: &GitStatus{Branch: "main"}, ContextWindow: 200_000}
	assertRenderGolden(t, "status-line-bypass", []string{RenderStatusLine(s, 80)})
}

func TestRenderGolden_StatusLineDontAsk(t *testing.T) {
	withRenderEnv(t, 80)
	s := StatusState{Mode: "dontAsk", Cwd: "~/src/relay-api", Git: &GitStatus{Branch: "main"}, ContextWindow: 200_000}
	assertRenderGolden(t, "status-line-dontask", []string{RenderStatusLine(s, 80)})
}

func TestRenderGolden_PaletteSlashRow0(t *testing.T) {
	withRenderEnv(t, 80)
	p := &Popup{Kind: KindSlashCommand, Selected: 0, Items: []AutocompleteItem{
		{Value: "model", Description: "Set the AI model"},
		{Value: "track-work", Description: "Track work as epics and stories"},
		{Value: "cost", Description: "Show cumulative session cost"},
	}}
	assertRenderGolden(t, "palette-slash-row0", p.Render(80, 5))
}

// TestRenderGolden_PermGeneric3Opt pins the generic (non-bash, non-edit)
// permission prompt's three options with row 0 selected, and the same
// prompt with row 1 selected (raised background + amber key move).
func TestRenderGolden_PermGeneric3Opt(t *testing.T) {
	withRenderEnv(t, 80)
	req := PermissionRequest{ToolName: "grep", PrimaryArg: "TODO", Grantable: true}
	assertRenderGolden(t, "perm-generic-3opt", RenderPermissionPrompt(req, "/", 80, 0, false, ""))
	assertRenderGolden(t, "perm-generic-3opt-sel1", RenderPermissionPrompt(req, "/", 80, 1, false, ""))
}

func TestRenderGolden_PermBash4Opt(t *testing.T) {
	withRenderEnv(t, 80)
	req := BashPermissionRequest{Command: "npm test -- upload", Cwd: "/home/dev/relay-api", Grantable: true, DontAskRules: []string{"npm test *"}}
	assertRenderGolden(t, "perm-bash-4opt", RenderBashPermissionPrompt(req, 80, 0))
}

func TestRenderGolden_PermEdit(t *testing.T) {
	withRenderEnv(t, 80)
	req := EditPermissionRequest{
		Kind: EditKindEdit,
		Path: "src/math.js",
		Hunks: []DiffHunk{
			{LineNum: 2, Old: "  return a - b", New: "  return a + b"},
		},
	}
	assertRenderGolden(t, "perm-edit", RenderEditPermissionPrompt(req, 80, 0, false, ""))
}
