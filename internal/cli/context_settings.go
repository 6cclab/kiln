package cli

import (
	"os"
	"strings"

	"github.com/andrepato/harness/internal/agent"
	claudememory "github.com/andrepato/harness/internal/claude/memory"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	slashcommands "github.com/andrepato/harness/internal/commands"
)

// contextRuntimeSettings describes values the next request would use. Sources
// name runtime flags, environment, resumed session, or the accepted file.
type contextRuntimeSettings struct {
	Model, Effort, Permission, AutoMemoryDirectory string
	Sources                                        map[string]string
}

func requestedModelAndSource(cwd string, args Args, s claudesettings.Settings) (string, string) {
	if args.Model != "" {
		return args.Model, "--model"
	}
	if ref, ok := agent.ResumedModel(agent.Options{Cwd: cwd, Resume: args.Resume, ResumeLatest: args.ResumeLatest || args.ContinueLatest}); ok {
		return ref.Provider + "/" + ref.ModelID, "resumed session"
	}
	model := sessionModel(s.Model)
	if strings.Contains(s.Model, "/") {
		return model, s.Provenance()["model"]
	}
	if os.Getenv("HARNESS_MODEL") != "" {
		return model, "HARNESS_MODEL"
	}
	return model, "default"
}

func modelAcceptsEffort(model string) bool {
	providerID, modelID, ok := splitProviderModel(model)
	if !ok {
		return false
	}
	resolved, err := buildRegistry().Resolve(providerID, modelID)
	return err == nil && effortSettingApplies(resolved.Model)
}

func effectiveContextSettings(cwd string, args Args, s claudesettings.Settings, acceptsEffort bool) contextRuntimeSettings {
	model, modelSource := requestedModelAndSource(cwd, args, s)
	sources := s.Provenance()
	sources["model"] = modelSource
	mode := resolvePermissionMode(args, s)
	switch {
	case args.PermissionMode != "":
		sources["permissions.defaultMode"] = "--permission-mode"
	case os.Getenv("HARNESS_PERMISSION_MODE") != "":
		sources["permissions.defaultMode"] = "HARNESS_PERMISSION_MODE"
	}
	effort := effortOrSetting(args.Effort, s.EffortLevel, acceptsEffort)
	switch {
	case args.Effort != "":
		sources["effortLevel"] = "--effort"
	case !acceptsEffort || effort == "":
		sources["effortLevel"] = "default"
	}
	if override := planEffortOverride(); override != "" && mode == claudesettings.ModePlan {
		effort = override
		sources["effortLevel"] = envExpPlanEffort
	}
	dirSetting, ignoredUntrusted := trustedAutoMemoryDirectory(cwd, args, s)
	if ignoredUntrusted {
		userOnly := claudesettings.LoadSettings(cwd, claudesettings.LoadOptions{Sources: userOnlySources(settingsSources(args.SettingSources)), Extra: args.Settings})
		sources["autoMemoryDirectory"] = userOnly.Provenance()["autoMemoryDirectory"]
	}
	// The loader applies the same unsafe-path rejection as chat startup.
	auto := claudememory.LoadAutoMemory(cwd, claudememory.AutoMemoryOptions{Directory: dirSetting, Enabled: s.AutoMemoryEnabled, EnvDisabled: os.Getenv("CLAUDE_CODE_DISABLE_AUTO_MEMORY") == "1", BudgetTokens: 1})
	if auto.DirectoryOverrideRejected || dirSetting == "" {
		sources["autoMemoryDirectory"] = "default"
	}
	return contextRuntimeSettings{Model: model, Effort: effort, Permission: string(mode), AutoMemoryDirectory: auto.Dir, Sources: sources}
}

func (r contextRuntimeSettings) effectiveForLane(model, effort, permission string) contextRuntimeSettings {
	cloned := make(map[string]string, len(r.Sources))
	for k, v := range r.Sources {
		cloned[k] = v
	}
	r.Sources = cloned
	if model != "" && model != r.Model {
		r.Model = model
		r.Sources["model"] = "session override"
	}
	if effort != r.Effort {
		r.Effort = effort
		r.Sources["effortLevel"] = "session override"
	}
	if permission != "" && permission != r.Permission {
		r.Permission = permission
		r.Sources["permissions.defaultMode"] = "session override"
	}
	return r
}

func (r contextRuntimeSettings) entries() []slashcommands.ContextSetting {
	dirValue := r.AutoMemoryDirectory
	if r.Sources["autoMemoryDirectory"] == "default" {
		dirValue = "default project directory"
	}
	return []slashcommands.ContextSetting{
		{Name: "model", Value: r.Model, Source: r.Sources["model"]},
		{Name: "effort", Value: valueOrDefault(r.Effort), Source: r.Sources["effortLevel"]},
		{Name: "permission", Value: r.Permission, Source: r.Sources["permissions.defaultMode"]},
		{Name: "auto-memory dir", Value: dirValue, Source: r.Sources["autoMemoryDirectory"]},
	}
}

func valueOrDefault(value string) string {
	if value == "" {
		return "model default"
	}
	return value
}
