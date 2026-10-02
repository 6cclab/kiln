package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/tool"
)

// fakeSandbox wraps every command in a script that says it was wrapped
// and fails, and records what it was asked.
type fakeSandbox struct {
	offers   bool
	disables []bool
}

func (f *fakeSandbox) Active() bool            { return true }
func (f *fakeSandbox) OffersUnsandboxed() bool { return f.offers }
func (f *fakeSandbox) ForCommand(cmd string, disable bool) execenv.CommandSandbox {
	f.disables = append(f.disables, disable)
	if disable {
		return nil
	}
	return fakeCommand{}
}

type fakeCommand struct{}

func (fakeCommand) Wrap(shell, command, cwd string) (execenv.Wrapped, error) {
	return execenv.Wrapped{
		Argv: []string{shell, "-c", `echo "wrapped:$1 [$FAKE_SB]"; echo "Operation not permitted"; exit 3`, "sh", command},
		Env:  map[string]string{"FAKE_SB": "on"},
	}, nil
}

func (fakeCommand) Explain(output string, code int) string {
	if strings.Contains(output, "Operation not permitted") {
		return "[fake sandbox note]"
	}
	return ""
}

func TestBashToolRunsThroughSandbox(t *testing.T) {
	env := execenv.New(t.TempDir())
	sb := &fakeSandbox{offers: true}
	env.Sandbox = sb
	bash := BashTool(env)

	if !strings.Contains(string(bash.Parameters), "dangerouslyDisableSandbox") {
		t.Error("schema lacks dangerouslyDisableSandbox while the retry is allowed")
	}
	if !strings.Contains(bash.Description, "OS sandbox") {
		t.Error("description does not mention the sandbox")
	}

	args, _ := json.Marshal(map[string]any{"command": "touch x"})
	res, err := bash.Execute(context.Background(), args, func(tool.Result) {}, tool.Invocation{})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(res)
	if !res.IsError || !strings.Contains(text, "wrapped:touch x [on]") {
		t.Errorf("the command did not run through the sandbox wrapper: %q", text)
	}
	if !strings.Contains(text, "[fake sandbox note]") {
		t.Errorf("failure note missing: %q", text)
	}

	args, _ = json.Marshal(map[string]any{"command": "echo plain", "dangerouslyDisableSandbox": true})
	res, _ = bash.Execute(context.Background(), args, func(tool.Result) {}, tool.Invocation{})
	if text := resultText(res); res.IsError || strings.TrimSpace(text) != "plain" {
		t.Errorf("unsandboxed run: %q", text)
	}
	if len(sb.disables) != 2 || sb.disables[0] || !sb.disables[1] {
		t.Errorf("disable flags passed = %v", sb.disables)
	}

	// Strict sandbox mode: the parameter is not offered.
	env.Sandbox = &fakeSandbox{offers: false}
	if strings.Contains(string(BashTool(env).Parameters), "dangerouslyDisableSandbox") {
		t.Error("schema offers dangerouslyDisableSandbox with allowUnsandboxedCommands false")
	}
}
