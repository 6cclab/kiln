package agents

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseAgent(t *testing.T) {
	t.Run("reads the fields real definitions actually use", func(t *testing.T) {
		// Shape taken from ~/projects/devops/homelab/.claude/agents/k8s-infra.md.
		source := "---\n" +
			"name: k8s-infra\n" +
			"description: Kubernetes cluster infrastructure\n" +
			"model: sonnet\n" +
			"tools: Read, Glob, Grep\n" +
			"color: green\n" +
			"paths: [cluster/**]\n" +
			"---\n" +
			"\n" +
			"You are a Kubernetes expert."
		agent, ok := ParseAgent(source, "/x/k8s-infra.md", Project)
		if !ok {
			t.Fatal("expected agent to parse")
		}
		if agent.Name != "k8s-infra" {
			t.Errorf("name = %q", agent.Name)
		}
		if agent.Model != "sonnet" {
			t.Errorf("model = %q", agent.Model)
		}
		if !reflect.DeepEqual(agent.Tools, []string{"Read", "Glob", "Grep"}) {
			t.Errorf("tools = %v", agent.Tools)
		}
		if agent.Prompt != "You are a Kubernetes expert." {
			t.Errorf("prompt = %q", agent.Prompt)
		}
	})

	t.Run("keeps a definition that carries unknown keys", func(t *testing.T) {
		// role, paths, skills and color all appear in real files and are
		// not Claude Code fields. Refusing over them would break a
		// directory this harness does not own.
		agent, ok := ParseAgent("---\nname: a\ndescription: d\nrole: x\nskills: [y]\n---\nbody", "/x/a.md", Project)
		if !ok || agent.Name != "a" {
			t.Errorf("got %+v, ok=%v", agent, ok)
		}
	})

	t.Run("accepts a YAML list for tools as well as the comma form", func(t *testing.T) {
		agent, ok := ParseAgent("---\nname: a\ndescription: d\ntools:\n  - Read\n  - Grep\n---\nb", "/x/a.md", Project)
		if !ok || !reflect.DeepEqual(agent.Tools, []string{"Read", "Grep"}) {
			t.Errorf("got %+v, ok=%v", agent.Tools, ok)
		}
	})

	t.Run("falls back to the filename when name is omitted", func(t *testing.T) {
		agent, ok := ParseAgent("---\ndescription: d\n---\nbody", "/x/from-file.md", Project)
		if !ok || agent.Name != "from-file" {
			t.Errorf("got %+v, ok=%v", agent, ok)
		}
	})

	t.Run("rejects a file with no description, since nothing could dispatch to it", func(t *testing.T) {
		if _, ok := ParseAgent("---\nname: a\n---\nbody", "/x/a.md", Project); ok {
			t.Error("expected rejection")
		}
		if _, ok := ParseAgent("no frontmatter at all", "/x/a.md", Project); ok {
			t.Error("expected rejection")
		}
	})

	t.Run("treats tools as absent rather than empty when the list is blank", func(t *testing.T) {
		// nil means inherit; a non-nil empty list would mean a tool-less agent.
		agent, ok := ParseAgent("---\nname: a\ndescription: d\ntools:\n---\nb", "/x/a.md", Project)
		if !ok || agent.Tools != nil {
			t.Errorf("got %+v, ok=%v", agent.Tools, ok)
		}
	})
}

func TestLoadAgents(t *testing.T) {
	dir := t.TempDir()
	agentsDir := filepath.Join(dir, ".claude", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(agentsDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.md", "---\nname: a\ndescription: first\n---\nbody a")
	write("b.md", "---\nname: b\ndescription: second\n---\nbody b")
	write("broken.md", "not an agent")
	write("notes.txt", "ignored")

	t.Run("loads the valid definitions and skips the rest", func(t *testing.T) {
		agentsList := LoadAgents(dir)
		names := map[string]bool{}
		for _, a := range agentsList {
			names[a.Name] = true
		}
		if !names["a"] || !names["b"] {
			t.Errorf("missing a/b, got %v", names)
		}
		if names["broken"] {
			t.Error("a malformed file became an agent")
		}
		if names["notes"] {
			t.Error("a non-markdown file became an agent")
		}
	})

	t.Run("returns nothing for a directory with no agents", func(t *testing.T) {
		empty := t.TempDir()
		var projectAgents []Definition
		for _, a := range LoadAgents(empty) {
			if a.Source == Project {
				projectAgents = append(projectAgents, a)
			}
		}
		if len(projectAgents) != 0 {
			t.Errorf("got %v", projectAgents)
		}
	})
}

func TestResolveAgentModel(t *testing.T) {
	ollama := []Candidate{
		{ID: "qwen3.8:latest", Provider: "ollama"},
		{ID: "qwen3-cc:latest", Provider: "ollama"},
	}
	anthropic := []Candidate{
		{ID: "claude-sonnet-4-5", Provider: "anthropic"},
		{ID: "claude-opus-4-1", Provider: "anthropic"},
	}
	localParent := ModelChoice{ProviderID: "ollama", ModelID: "qwen3.8:latest"}

	t.Run("honors a bare alias when the parent's provider has a match", func(t *testing.T) {
		out := ResolveAgentModel("sonnet", ModelChoice{ProviderID: "anthropic", ModelID: "claude-opus-4-1"}, anthropic)
		want := ModelChoice{ProviderID: "anthropic", ModelID: "claude-sonnet-4-5"}
		if out != want {
			t.Errorf("got %+v, want %+v", out, want)
		}
	})

	t.Run("inherits when the alias means nothing on this provider", func(t *testing.T) {
		// The motivating case: every definition on this machine says
		// "model: sonnet", and there is no Sonnet on a self-hosted Ollama.
		if got := ResolveAgentModel("sonnet", localParent, ollama); got != localParent {
			t.Errorf("got %+v, want %+v", got, localParent)
		}
	})

	t.Run("does not cross providers for a bare alias", func(t *testing.T) {
		all := append(append([]Candidate{}, ollama...), anthropic...)
		if got := ResolveAgentModel("opus", localParent, all); got != localParent {
			t.Errorf("got %+v, want %+v", got, localParent)
		}
	})

	t.Run("resolves an explicit provider/model literally", func(t *testing.T) {
		got := ResolveAgentModel("ollama/qwen3-cc:latest", localParent, ollama)
		want := ModelChoice{ProviderID: "ollama", ModelID: "qwen3-cc:latest"}
		if got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	})

	t.Run("inherits rather than failing on a typo", func(t *testing.T) {
		if got := ResolveAgentModel("ollama/nope", localParent, ollama); got != localParent {
			t.Errorf("got %+v", got)
		}
		if got := ResolveAgentModel("inherit", localParent, ollama); got != localParent {
			t.Errorf("got %+v", got)
		}
		if got := ResolveAgentModel("", localParent, ollama); got != localParent {
			t.Errorf("got %+v", got)
		}
	})
}

func TestResolveModel(t *testing.T) {
	ollama := []Candidate{
		{ID: "qwen3.8:latest", Provider: "ollama"},
		{ID: "qwen3-cc:latest", Provider: "ollama"},
	}
	anthropic := []Candidate{
		{ID: "claude-sonnet-4-5", Provider: "anthropic"},
		{ID: "claude-opus-4-1", Provider: "anthropic"},
		{ID: "claude-haiku-4-5", Provider: "anthropic"},
	}
	localParent := ModelChoice{ProviderID: "ollama", ModelID: "qwen3.8:latest"}

	t.Run("role hit resolves the role's value and reports kind role", func(t *testing.T) {
		roles := map[string]string{"fast": "ollama/qwen3-cc:latest"}
		got, kind := ResolveModel("fast", roles, localParent, ollama)
		want := ModelChoice{ProviderID: "ollama", ModelID: "qwen3-cc:latest"}
		if got != want || kind != ResolveRole {
			t.Errorf("got %+v/%s, want %+v/%s", got, kind, want, ResolveRole)
		}
	})

	t.Run("role value not among candidates falls back to the parent", func(t *testing.T) {
		roles := map[string]string{"fast": "ollama/nope"}
		got, kind := ResolveModel("fast", roles, localParent, ollama)
		if got != localParent || kind != ResolveFallback {
			t.Errorf("got %+v/%s, want %+v/%s", got, kind, localParent, ResolveFallback)
		}
	})

	t.Run("alias resolves through its built-in role when configured", func(t *testing.T) {
		roles := map[string]string{"structured": "anthropic/claude-sonnet-4-5"}
		parent := ModelChoice{ProviderID: "anthropic", ModelID: "claude-opus-4-1"}
		got, kind := ResolveModel("sonnet", roles, parent, anthropic)
		want := ModelChoice{ProviderID: "anthropic", ModelID: "claude-sonnet-4-5"}
		if got != want || kind != ResolveAlias {
			t.Errorf("got %+v/%s, want %+v/%s", got, kind, want, ResolveAlias)
		}
	})

	t.Run("alias with no configured role behaves exactly as before", func(t *testing.T) {
		parent := ModelChoice{ProviderID: "anthropic", ModelID: "claude-opus-4-1"}
		got, kind := ResolveModel("sonnet", nil, parent, anthropic)
		want := ModelChoice{ProviderID: "anthropic", ModelID: "claude-sonnet-4-5"}
		if got != want || kind != ResolveAlias {
			t.Errorf("got %+v/%s, want %+v/%s", got, kind, want, ResolveAlias)
		}
	})

	t.Run("explicit provider/model is unchanged by roles", func(t *testing.T) {
		roles := map[string]string{"fast": "ollama/qwen3.8:latest"}
		got, kind := ResolveModel("ollama/qwen3-cc:latest", roles, localParent, ollama)
		want := ModelChoice{ProviderID: "ollama", ModelID: "qwen3-cc:latest"}
		if got != want || kind != ResolveExplicit {
			t.Errorf("got %+v/%s, want %+v/%s", got, kind, want, ResolveExplicit)
		}
	})

	t.Run("inherit and empty report kind inherited", func(t *testing.T) {
		if got, kind := ResolveModel("", nil, localParent, ollama); got != localParent || kind != ResolveInherited {
			t.Errorf("got %+v/%s", got, kind)
		}
		if got, kind := ResolveModel("inherit", nil, localParent, ollama); got != localParent || kind != ResolveInherited {
			t.Errorf("got %+v/%s", got, kind)
		}
	})
}

func TestValidateRoles(t *testing.T) {
	candidates := []Candidate{
		{ID: "claude-sonnet-4-5", Provider: "anthropic"},
		{ID: "qwen3.8:latest", Provider: "ollama"},
	}

	t.Run("no problems when every role resolves", func(t *testing.T) {
		roles := map[string]string{"structured": "anthropic/claude-sonnet-4-5", "fast": "ollama/qwen3.8:latest"}
		if got := ValidateRoles(roles, candidates); len(got) != 0 {
			t.Errorf("got %v, want none", got)
		}
	})

	t.Run("flags a value that is not provider/model shaped", func(t *testing.T) {
		roles := map[string]string{"fast": "sonnet"}
		got := ValidateRoles(roles, candidates)
		if len(got) != 1 || !strings.Contains(got[0], "fast") || !strings.Contains(got[0], "not provider/model") {
			t.Errorf("got %v", got)
		}
	})

	t.Run("flags a value not among candidates, sorted by role name", func(t *testing.T) {
		roles := map[string]string{"heavy": "anthropic/claude-opus-4-1", "fast": "ollama/nope"}
		got := ValidateRoles(roles, candidates)
		if len(got) != 2 {
			t.Fatalf("got %v", got)
		}
		if !strings.HasPrefix(got[0], "fast:") || !strings.HasPrefix(got[1], "heavy:") {
			t.Errorf("got %v, want fast before heavy", got)
		}
	})
}
