package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeUserModelRouterSettings writes ~/.claude/settings.json with
// modelRoles that resolve kiln/auto against the faux provider (faux-1
// plans and implements), for the subcommand listing tests below.
func writeUserModelRouterSettings(t *testing.T, home string) {
	t.Helper()
	path := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"modelRoles": {"structured": "faux/faux-1", "fast": "faux/faux-1"}, "modelRouter": {"classifier": false}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

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
	if !strings.Contains(out, "medium") {
		t.Errorf("models output missing the medium tier for a 128000-token window:\n%s", out)
	}
}

// TestProviders_KilnAutoWhenConfigured checks `harness providers` lists
// the "kiln" provider (kiln/auto) once modelRoles resolve it, through the
// same path (loadModelRouter) startup uses.
func TestProviders_KilnAutoWhenConfigured(t *testing.T) {
	startFaux(t, unreadScript)
	scratchProject(t)
	writeUserModelRouterSettings(t, os.Getenv("HOME"))

	var stdout, stderr bytes.Buffer
	code := Providers(context.Background(), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "kiln") {
		t.Errorf("providers output missing kiln:\n%s", stdout.String())
	}
}

// TestModels_KilnAutoWhenConfigured checks `harness models` lists
// kiln/auto once modelRoles resolve it.
func TestModels_KilnAutoWhenConfigured(t *testing.T) {
	startFaux(t, unreadScript)
	scratchProject(t)
	writeUserModelRouterSettings(t, os.Getenv("HOME"))

	var stdout, stderr bytes.Buffer
	code := Models(context.Background(), &stdout, &stderr, "")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "kiln") || !strings.Contains(out, "auto") {
		t.Errorf("models output missing kiln/auto:\n%s", out)
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

// TestDoctor_ModelRouterLine checks /doctor reports kiln/auto's role
// summary once modelRoles resolve it, and stays quiet (no "modelRouter"
// line) when nothing is configured (TestDoctor_Runs covers that case
// implicitly: its output is asserted not to need a modelRouter line).
func TestDoctor_ModelRouterLine(t *testing.T) {
	startFaux(t, unreadScript)
	scratchProject(t)
	writeUserModelRouterSettings(t, os.Getenv("HOME"))

	args := baseArgs()
	var stdout, stderr bytes.Buffer
	code := Doctor(context.Background(), args, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "modelRouter kiln/auto:") || !strings.Contains(out, "plan→faux-1") {
		t.Errorf("doctor output missing the modelRouter line:\n%s", out)
	}
}

// TestDoctor_NoModelRouterConfigured checks /doctor prints no
// "modelRouter" line when the user configured neither modelRoles nor
// modelRouter (the common case) — quiet, like the roles table.
func TestDoctor_NoModelRouterConfigured(t *testing.T) {
	startFaux(t, unreadScript)
	scratchProject(t)

	args := baseArgs()
	var stdout, stderr bytes.Buffer
	code := Doctor(context.Background(), args, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "modelRouter") {
		t.Errorf("doctor output has an unwanted modelRouter line:\n%s", stdout.String())
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
	if code := VersionCmd(&stdout); code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Fatal("Version printed nothing")
	}
}

// TestVersionLabel pins the fix for
// qa/findings/20260926T231105Z-banner-version-hash.json: the banner used
// to glue a "v" onto ANY Version starting with a digit, which a git short
// hash ("61bae41") or a dirty one ("61bae41-dirty") does just as often as
// a real semver tag does. Only a real semver gets the "v" prefix.
func TestVersionLabel(t *testing.T) {
	cases := map[string]string{
		"0.9.2":           "v0.9.2",
		"v0.9.2":          "v0.9.2",
		"1.2.3-4-gabcdef": "v1.2.3-4-gabcdef",
		"61bae41":         "61bae41",
		"2f74412-dirty":   "2f74412-dirty",
		"dev":             "dev",
	}
	for in, want := range cases {
		if got := versionLabel(in); got != want {
			t.Errorf("versionLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
