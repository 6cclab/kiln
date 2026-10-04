package tui

import (
	"strings"
	"testing"
)

// The "don't ask again" option names every rule it saves, and is left out
// when the gate would not honour it (permission.Request.Grantable).

func TestRenderGolden_PermBashDontAsk1Rule(t *testing.T) {
	withRenderEnv(t, 80)
	req := BashPermissionRequest{Command: "git status && npm test", Grantable: true, DontAskRules: []string{"npm test *"}}
	assertRenderGolden(t, "perm-bash-dontask-1rule", RenderBashPermissionPrompt(req, 80, 1))
}

func TestRenderGolden_PermBashDontAsk2Rules(t *testing.T) {
	withRenderEnv(t, 80)
	req := BashPermissionRequest{Command: "git status && npm test && make build", Grantable: true,
		DontAskRules: []string{"npm test *", "make build *"}}
	assertRenderGolden(t, "perm-bash-dontask-2rules", RenderBashPermissionPrompt(req, 80, 1))
}

// TestRenderGolden_PermBashDontAsk5Rules: five rules do not fit an
// 80-column row; the option names every one, wrapping onto a second row.
func TestRenderGolden_PermBashDontAsk5Rules(t *testing.T) {
	withRenderEnv(t, 80)
	req := BashPermissionRequest{Command: "npm ci && npm test && make build && go vet ./... && cargo fmt", Grantable: true,
		DontAskRules: []string{"npm ci *", "npm test *", "make build *", "go vet *", "cargo fmt *"}}
	assertRenderGolden(t, "perm-bash-dontask-5rules", RenderBashPermissionPrompt(req, 80, 1))
}

// TestRenderGolden_PermBashDontAskHidden: not grantable, so the options
// are Yes / switch to auto mode / No, numbered 1-3.
func TestRenderGolden_PermBashDontAskHidden(t *testing.T) {
	withRenderEnv(t, 80)
	req := BashPermissionRequest{Command: "npm test -- upload"}
	assertRenderGolden(t, "perm-bash-dontask-hidden", RenderBashPermissionPrompt(req, 80, 0))
}

func TestRenderGolden_PermGenericDontAskHidden(t *testing.T) {
	withRenderEnv(t, 80)
	req := PermissionRequest{ToolName: "web_fetch", PrimaryArg: "https://example.com"}
	assertRenderGolden(t, "perm-generic-dontask-hidden", RenderPermissionPrompt(req, "/", 80, 0, false, ""))
}

// TestDontAskLabel: the option names every rule it will save, whole,
// however narrow the row. A "+2 more" hid two persistent sed -i rules from
// the person agreeing to save them.
func TestDontAskLabel(t *testing.T) {
	rules := []string{"git mv *", "sed -i '' 's/^package main$/package store/' internal/store/store.go", "sed -i '' 's/x/y/' internal/store/filestore.go"}
	if got, want := dontAskLabel(rules, 300), "Yes, and don’t ask again for: "+strings.Join(rules, ", "); got != want {
		t.Errorf("dontAskLabel(wide) = %q, want %q", got, want)
	}
	if got, want := dontAskLabel(rules, 76), "Yes, and don’t ask again for: "+strings.Join(rules, ",\n"); got != want {
		t.Errorf("dontAskLabel(narrow) = %q, want one rule per row %q", got, want)
	}
	for _, width := range []int{120, 80, 50} {
		req := BashPermissionRequest{Command: "git mv a b && sed -i '' x y && sed -i '' z w", Grantable: true, DontAskRules: rules}
		joined := strings.Join(stripAll(RenderBashPermissionPrompt(req, width, 1)), "")
		flat := strings.Join(strings.Fields(joined), "")
		for _, r := range rules {
			if !strings.Contains(flat, strings.Join(strings.Fields(r), "")) {
				t.Errorf("width %d: rule %q not shown in full:\n%s", width, r, strings.Join(stripAll(RenderBashPermissionPrompt(req, width, 1)), "\n"))
			}
		}
		if strings.Contains(joined, "more") {
			t.Errorf("width %d: label counts hidden rules", width)
		}
		for _, line := range RenderBashPermissionPrompt(req, width, 1) {
			if VisibleWidth(line) > width {
				t.Errorf("width %d: row %d wide: %q", width, VisibleWidth(line), stripANSI(line))
			}
		}
	}
}

func stripAll(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = stripANSI(l)
	}
	return out
}

// TestPromptOptions_NoSwitchToTheModeAlreadyOn: an edit prompt raised in
// accept edits (a protected path) does not offer to switch to accept
// edits, nor does one a mode switch would not have avoided (protected
// path, ask rule, hook); a bash prompt forced that way does not offer
// auto mode. Keys and rendering agree.
func TestPromptOptions_NoSwitchToTheModeAlreadyOn(t *testing.T) {
	withRenderEnv(t, 100)
	cases := []struct {
		name string
		req  PermissionRequest
		want []promptOptionKind
	}{
		{"edit in manual", PermissionRequest{ToolName: "edit"}, []promptOptionKind{optAllow, optSwitchAcceptEditsAllow, optDenyOutright}},
		{"edit in acceptEdits", PermissionRequest{ToolName: "edit", InAcceptEdits: true}, []promptOptionKind{optAllow, optDenyOutright}},
		{"write, protected path in manual", PermissionRequest{ToolName: "write", ModeSwitchMoot: true}, []promptOptionKind{optAllow, optDenyOutright}},
		{"bash forced by an ask rule", PermissionRequest{ToolName: "bash", ModeSwitchMoot: true}, []promptOptionKind{optAllow, optDenyOutright}},
		{"bash in acceptEdits keeps auto mode", PermissionRequest{ToolName: "bash", InAcceptEdits: true}, []promptOptionKind{optAllow, optSwitchAutoAllow, optDenyOutright}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := promptOptionsFor(c.req)
			if len(got) != len(c.want) {
				t.Fatalf("options = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("options = %v, want %v", got, c.want)
				}
			}
			p := NewPromptState(t.TempDir())
			reply := p.AskTool(c.req)
			rendered := stripANSI(strings.Join(p.Render(100), "\n"))
			hasSwitch := strings.Contains(rendered, "switch to")
			wantSwitch := len(c.want) == 3
			if hasSwitch != wantSwitch {
				t.Errorf("rendered switch option = %v, want %v:\n%s", hasSwitch, wantSwitch, rendered)
			}
			p.HandleKey(key("2"))
			choice := <-reply
			if !wantSwitch && (choice.Kind != ChoiceDeny || p.switchMode != "") {
				t.Errorf(`"2" = %+v switchMode=%q, want No`, choice, p.switchMode)
			}
		})
	}
}

// TestPromptState_DontAskHiddenKeys: with the option hidden, its "a"
// shortcut does nothing, the rows below it move up a number, and the
// placeholder counts three options.
func TestPromptState_DontAskHiddenKeys(t *testing.T) {
	p := NewPromptState("/tmp")
	reply := p.AskTool(PermissionRequest{ToolName: "bash", PrimaryArg: "npm test"})
	p.HandleKey(key("a"))
	if !p.Active() {
		t.Fatal(`"a" answered a prompt that does not offer "don't ask again"`)
	}
	p.HandleKey(key("4"))
	if !p.Active() {
		t.Fatal(`"4" answered a three-option prompt`)
	}
	if got := placeholderForOptionCount(len(promptOptionsFor(p.pending.request))); got != "press 1, 2 or 3" {
		t.Errorf("placeholder = %q", got)
	}
	p.HandleKey(key("2"))
	if choice := <-reply; choice.Kind != ChoiceAllow || p.switchMode != "auto" {
		t.Errorf(`"2" = %+v switchMode=%q, want allow and switch to auto mode`, choice, p.switchMode)
	}

	// The same prompt's arrow keys stop at the last of three rows.
	reply = p.AskTool(PermissionRequest{ToolName: "bash", PrimaryArg: "npm test"})
	for range 5 {
		p.HandleKey(key("down"))
	}
	p.HandleKey(key("enter"))
	if choice := <-reply; choice.Kind != ChoiceDeny {
		t.Errorf("last row = %+v, want deny", choice)
	}

	// The generic prompt: "2" is now "No, and tell kiln what to do instead".
	p.AskTool(PermissionRequest{ToolName: "web_fetch", PrimaryArg: "https://example.com"})
	p.HandleKey(key("a"))
	if !p.Active() || p.feedback != nil {
		t.Fatal(`"a" acted on a generic prompt without "don't ask again"`)
	}
	p.HandleKey(key("2"))
	if p.feedback == nil {
		t.Error(`"2" on the generic prompt without "don't ask again" did not open the feedback field`)
	}
	rendered := strings.Join(p.Render(80), "\n")
	if strings.Contains(rendered, "ask again") {
		t.Errorf("hidden option rendered:\n%s", rendered)
	}
}

// TestPromptState_AutoModeHasNoSwitchToAuto: a prompt raised in auto mode
// (the classifier failed or blocked too often) does not offer to switch to
// auto mode. The rows below move up; keys and rendering agree.
func TestPromptState_AutoModeHasNoSwitchToAuto(t *testing.T) {
	withRenderEnv(t, 80)
	req := PermissionRequest{ToolName: "bash", PrimaryArg: "npm test", InAutoMode: true, Grantable: true, DontAskRules: []string{"npm test *"}}
	got := promptOptionsFor(req)
	want := []promptOptionKind{optAllow, optAllowAlways, optDenyOutright}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("options = %v, want %v", got, want)
	}

	p := NewPromptState("/tmp")
	reply := p.AskTool(req)
	rendered := stripANSI(strings.Join(p.Render(80), "\n"))
	if strings.Contains(rendered, "switch to auto mode") || !strings.Contains(rendered, "3  No") {
		t.Errorf("rendered:\n%s", rendered)
	}
	p.HandleKey(key("3"))
	if choice := <-reply; choice.Kind != ChoiceDeny || p.switchMode != "" {
		t.Errorf(`"3" = %+v switchMode=%q, want deny`, choice, p.switchMode)
	}

	// Outside auto mode the option is still there.
	if n := len(promptOptionsFor(PermissionRequest{ToolName: "bash"})); n != 3 {
		t.Errorf("manual-mode options = %d, want 3 (Yes / switch to auto / No)", n)
	}
}

// TestPromptState_BashGrantableWithoutRulesHidden: a bash request the gate
// marked grantable but without rules cannot name what it saves, so the
// option is not offered.
func TestPromptState_BashGrantableWithoutRulesHidden(t *testing.T) {
	if n := len(promptOptionsFor(PermissionRequest{ToolName: "bash", Grantable: true})); n != 3 {
		t.Errorf("options = %d, want 3", n)
	}
}
