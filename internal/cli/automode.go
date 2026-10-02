package cli

import (
	"context"

	"github.com/andrepato/harness/internal/agent"
	"github.com/andrepato/harness/internal/automode"
	"github.com/andrepato/harness/internal/claude/permission"
	claudesettings "github.com/andrepato/harness/internal/claude/settings"
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
	})
}

// autoModeHistory is a permission.Request's History for a call on lane:
// the branch's messages, read only if the classifier runs.
func autoModeHistory(ctx context.Context, lane automode.EntryLister) func() []msg.Message {
	return func() []msg.Message { return automode.BranchMessages(ctx, lane) }
}
