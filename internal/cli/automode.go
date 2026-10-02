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
// later. The classifier runs on the "fast" model role when one resolves,
// else on the session's current model. It returns the Intents every prompt
// path records into, so the classifier sees what the user typed rather than
// the hook context and @file contents kiln attached.
func wireAutoMode(gate *permission.Gate, reg *provider.Registry, roles map[string]string, started *agent.Started, memoryText string, cfg claudesettings.AutoModeConfig) *automode.Intents {
	intents := &automode.Intents{}
	gate.SetClassifier(&automode.Classifier{
		Resolve: automode.Resolver(reg, roles, func() provider.Model { return started.Model }),
		Config:  cfg,
		Memory:  memoryText,
		Intents: intents,
	})
	return intents
}

// autoModeHistory is a permission.Request's History for a call on lane:
// the branch's messages, read only if the classifier runs.
func autoModeHistory(ctx context.Context, lane automode.EntryLister) func() []msg.Message {
	return func() []msg.Message { return automode.BranchMessages(ctx, lane) }
}
