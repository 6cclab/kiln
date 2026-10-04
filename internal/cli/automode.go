package cli

import (
	"context"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/automode"
	"github.com/andrepato/harness/internal/claude/permission"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
	"github.com/andrepato/harness/internal/diag"
	"github.com/andrepato/harness/internal/execenv"
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// wireAutoMode binds kiln's auto mode classifier (internal/automode) to the
// gate, whatever mode the session starts in: Shift+Tab can enter auto mode
// later. The classifier runs on cfg.FastRole — modelRoles.fast from user
// settings or --settings only, never a repository's — when it resolves,
// else on the session's current model.
func wireAutoMode(gate *permission.Gate, reg *provider.Registry, started *agent.Started, memoryText string, cfg claudesettings.AutoModeConfig) {
	var roles map[string]string
	if cfg.FastRole != "" {
		roles = map[string]string{"fast": cfg.FastRole}
	}
	gate.SetClassifier(&automode.Classifier{
		Resolve: automode.Resolver(reg, roles, func() provider.Model { return started.Model }),
		Config:  cfg,
		Memory:  memoryText,
		// The classifier's calls are part of what the session costs: they
		// go into its usage (footer, /cost, a resumed session's total),
		// labelled as the classifier's in /cost.
		OnUsage: func(model provider.Model, usage msg.Usage) {
			if err := started.Harness.RecordSideUsage(ClassifierUsageSource(model), usage); err != nil {
				diag.L().Warn("auto mode classifier usage", "err", err)
			}
		},
	})
}

// ClassifierUsageSource is how /cost labels the auto mode classifier's
// spend on model.
func ClassifierUsageSource(model provider.Model) string {
	return "auto-mode classifier · " + model.Provider + "/" + model.ID
}

// autoModeHistory is a permission.Request's History for a call on lane:
// the branch's messages, read only if the classifier runs.
func autoModeHistory(ctx context.Context, lane automode.EntryLister) func() []msg.Message {
	return func() []msg.Message { return automode.BranchMessages(ctx, lane) }
}

// setupScratchpad creates the session scratchpad and binds it to the gate,
// which lets the model read and write it without prompts in every mode
// (permission/scratchpad.go). Subagents share the gate, so they share the
// parent session's scratchpad. On any failure the session runs without one
// and the reason goes to the run log; it returns "".
func setupScratchpad(gate *permission.Gate, cwd, sessionID string) string {
	dir, err := execenv.EnsureScratchpad(cwd, sessionID)
	if err != nil {
		diag.L().Warn("scratchpad", "err", err)
		return ""
	}
	gate.SetScratchpad(dir, execenv.ScratchpadDir(cwd, sessionID))
	diag.L().Info("scratchpad", "dir", dir)
	return dir
}

// scratchpadInstructions tells the model about its scratchpad.
func scratchpadInstructions(dir string) string {
	return "# Scratchpad\n\n" +
		"Put every temporary file in this session's scratchpad directory, not in /tmp, the system temp directory or the project: scratch scripts, debug output, downloads, screenshots, intermediate files.\n\n" +
		"`" + dir + "`\n\n" +
		"It belongs to this session, sits outside the project, and you can read and write it without asking for permission."
}
