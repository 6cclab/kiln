package writesettings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAddRuleCreatesFileAndDirs(t *testing.T) {
	cwd := t.TempDir()
	if err := AddRule(cwd, Allow, "Bash(ls:*)"); err != nil {
		t.Fatal(err)
	}
	path := LocalSettingsPath(cwd)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if data[len(data)-1] != '\n' {
		t.Error("expected trailing newline")
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	perms := parsed["permissions"].(map[string]any)
	allow := perms["allow"].([]any)
	if len(allow) != 1 || allow[0] != "Bash(ls:*)" {
		t.Errorf("got %v", allow)
	}
}

func TestAddRuleDedupes(t *testing.T) {
	cwd := t.TempDir()
	if err := AddRule(cwd, Allow, "Read"); err != nil {
		t.Fatal(err)
	}
	if err := AddRule(cwd, Allow, "Read"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(LocalSettingsPath(cwd))
	var parsed map[string]any
	json.Unmarshal(data, &parsed)
	allow := parsed["permissions"].(map[string]any)["allow"].([]any)
	if len(allow) != 1 {
		t.Errorf("expected dedup, got %v", allow)
	}
}

func TestAddRulePreservesOtherKeys(t *testing.T) {
	cwd := t.TempDir()
	path := LocalSettingsPath(cwd)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	initial := `{"env":{"FOO":"bar"},"permissions":{"deny":["Bash(rm:*)"]}}`
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := AddRule(cwd, Allow, "Read"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	var parsed map[string]any
	json.Unmarshal(data, &parsed)
	if parsed["env"].(map[string]any)["FOO"] != "bar" {
		t.Errorf("env not preserved: %v", parsed)
	}
	deny := parsed["permissions"].(map[string]any)["deny"].([]any)
	if len(deny) != 1 || deny[0] != "Bash(rm:*)" {
		t.Errorf("deny not preserved: %v", deny)
	}
	allow := parsed["permissions"].(map[string]any)["allow"].([]any)
	if len(allow) != 1 || allow[0] != "Read" {
		t.Errorf("allow not added: %v", allow)
	}
}

func TestRemoveRule(t *testing.T) {
	cwd := t.TempDir()
	if err := AddRule(cwd, Deny, "Bash(rm:*)"); err != nil {
		t.Fatal(err)
	}
	if err := AddRule(cwd, Deny, "Write"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveRule(cwd, Deny, "Write"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(LocalSettingsPath(cwd))
	var parsed map[string]any
	json.Unmarshal(data, &parsed)
	deny := parsed["permissions"].(map[string]any)["deny"].([]any)
	if len(deny) != 1 || deny[0] != "Bash(rm:*)" {
		t.Errorf("got %v", deny)
	}
}

func TestRemoveRuleNoOpWhenListAbsent(t *testing.T) {
	cwd := t.TempDir()
	// Nothing to remove; there is not even a file yet.
	if err := RemoveRule(cwd, Allow, "Read"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(LocalSettingsPath(cwd)); err == nil {
		t.Error("expected no file to be created")
	}
}

func TestReadMalformedFileTreatedAsEmpty(t *testing.T) {
	cwd := t.TempDir()
	path := LocalSettingsPath(cwd)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AddRule(cwd, Allow, "Read"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("file is not valid JSON after AddRule: %s", data)
	}
}
