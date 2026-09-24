package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestProviders_Faux checks `harness providers` lists the faux provider
// once HARNESS_FAUX_ADDR is set, with "configured" status (faux needs no
// auth at all).
func TestProviders_Faux(t *testing.T) {
	startFaux(t, unreadScript)

	var stdout, stderr bytes.Buffer
	code := Providers(context.Background(), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "faux") {
		t.Errorf("providers output missing faux:\n%s", out)
	}
	if !strings.Contains(out, "configured") {
		t.Errorf("providers output missing a \"configured\" status:\n%s", out)
	}
}

// TestModels_Faux checks `harness models faux` lists faux-1 with its tier
// and usable-token budget.
func TestModels_Faux(t *testing.T) {
	startFaux(t, unreadScript)

	var stdout, stderr bytes.Buffer
	code := Models(context.Background(), &stdout, &stderr, "faux")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "faux-1") {
		t.Errorf("models output missing faux-1:\n%s", out)
	}
	if !strings.Contains(out, "small") {
		t.Errorf("models output missing the small tier for a 32768-token window:\n%s", out)
	}
}

// TestModels_UnknownProvider checks `harness models <bogus>` fails cleanly.
func TestModels_UnknownProvider(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Models(context.Background(), &stdout, &stderr, "not-a-real-provider")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
}

// TestDoctor_Runs checks `harness doctor` produces a report without
// crashing, against the faux provider so it needs no network.
func TestDoctor_Runs(t *testing.T) {
	startFaux(t, unreadScript)
	scratchProject(t)

	args := baseArgs()
	var stdout, stderr bytes.Buffer
	code := Doctor(context.Background(), args, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"model", "tier", "tools", "mcp", "hooks", "agents", "settings"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q line:\n%s", want, out)
		}
	}
}

// TestDoctor_BypassPermissionsIsAProblem checks /doctor's problem list
// flags bypassPermissions, matching inspect-commands.ts's own check.
func TestDoctor_BypassPermissionsIsAProblem(t *testing.T) {
	startFaux(t, unreadScript)
	scratchProject(t)

	args := baseArgs()
	args.PermissionMode = "bypassPermissions"
	var stdout, stderr bytes.Buffer
	code := Doctor(context.Background(), args, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "bypassPermissions") {
		t.Errorf("doctor output did not flag bypassPermissions:\n%s", stdout.String())
	}
}

// TestMCP_NoConfig checks `harness mcp` with no ~/.claude.json reports the
// "none configured" message rather than crashing.
func TestMCP_NoConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	var stdout, stderr bytes.Buffer
	code := MCP(context.Background(), baseArgs(), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "No MCP servers configured") {
		t.Errorf("mcp output = %q", stdout.String())
	}
}

// TestParse_Help checks --help sets Args.Help, and that main.go's own
// contract (print cli.Help, exit 0) has something non-empty to print.
func TestParse_Help(t *testing.T) {
	args := Parse([]string{"--help"})
	if !args.Help {
		t.Fatal("Help = false, want true")
	}
	if strings.TrimSpace(Help) == "" {
		t.Fatal("cli.Help is empty")
	}
}

// TestParse_UnknownFlag checks an unrecognized flag is collected rather
// than silently ignored, matching main.go's "unknown flag(s): ..." exit 1.
func TestParse_UnknownFlag(t *testing.T) {
	args := Parse([]string{"--this-flag-does-not-exist"})
	if len(args.Unknown) != 1 || args.Unknown[0] != "--this-flag-does-not-exist" {
		t.Fatalf("Unknown = %v, want [--this-flag-does-not-exist]", args.Unknown)
	}
}

func TestVersion_Prints(t *testing.T) {
	var stdout bytes.Buffer
	if code := Version(&stdout); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatal("Version printed nothing")
	}
}
