package harness

import (
	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/session"
)

// OpAt is one of the 13 leaves of pi's OperationState.at (session/types.d.ts
// in pi-agent-core). Only the ones this phase implements are ever written
// by this package; the rest are declared for completeness and documented
// in doc.go.
type OpAt string

const (
	AtStarting                OpAt = "starting"
	AtCheckpoint              OpAt = "checkpoint"
	AtAssistantReady          OpAt = "assistant.ready"
	AtAssistantEffectPending  OpAt = "assistant.effect_pending"
	AtTools                   OpAt = "tools"
	AtDeferredPending         OpAt = "deferred.pending"
	AtDeferredReady           OpAt = "deferred.ready"
	AtSummaryPending          OpAt = "summary.pending"
	AtSummaryReady            OpAt = "summary.ready"
	AtNavigationReadyToCommit OpAt = "navigation.ready_to_commit"
)

// notImplementedStates lists the `at` leaves this phase never produces.
// Resume and the turn loop return a clear error naming the state rather
// than guessing at its semantics.
var notImplementedStates = map[OpAt]bool{
	AtDeferredPending: true,
	AtDeferredReady:   true,
	AtSummaryPending:  true,
	AtSummaryReady:    true,
}

// OpControl is the run/steer/abort control block carried on every OpState.
type OpControl struct {
	Status string `json:"status"` // "running" | "aborted"
}

// Continuation records why the loop needs another assistant turn.
type Continuation struct {
	Kind                 string `json:"kind"` // "need_assistant"
	OverflowRecoveryUsed bool   `json:"overflowRecoveryUsed"`
}

// OpCompactionSettings is the compaction knobs snapshotted into an
// operation at start (see CompactionSettings in compaction.go for the
// Options-level type this is copied from).
type OpCompactionSettings struct {
	Enabled          bool `json:"enabled"`
	KeepRecentTokens int  `json:"keepRecentTokens"`
	ReserveTokens    int  `json:"reserveTokens"`
}

// OpSettings is the operation-wide behavior snapshot, fixed for the life
// of the operation.
type OpSettings struct {
	Compaction    OpCompactionSettings `json:"compaction"`
	FollowUpMode  string               `json:"followUpMode"`  // "all"
	SteeringMode  string               `json:"steeringMode"`  // "all"
	ToolExecution string               `json:"toolExecution"` // "sequential"
}

// GenerationConfiguration is the model/tool configuration in effect for
// one assistant request.
type GenerationConfiguration struct {
	ActiveToolNames []string         `json:"activeToolNames"`
	Model           session.ModelRef `json:"model"`
	ThinkingLevel   string           `json:"thinkingLevel"`
}

// GenerationContext is the per-request context carried through
// assistant.ready/assistant.effect_pending.
type GenerationContext struct {
	Configuration        GenerationConfiguration `json:"configuration"`
	OverflowRecoveryUsed bool                    `json:"overflowRecoveryUsed"`
	RetryPolicy          RetryPolicy             `json:"retryPolicy"`
	StepID               string                  `json:"stepId"`
	StreamOptions        map[string]any          `json:"streamOptions"`
	TriggerEntryID       string                  `json:"triggerEntryId"`
}

// ToolCallState is one call's progress inside a tools batch.
type ToolCallState struct {
	ResultEntryID string `json:"resultEntryId"`
	SourceIndex   int    `json:"sourceIndex"`
	Status        string `json:"status"` // planned | effect_pending | outcome_ready
	Replay        string `json:"replay,omitempty"`
	Terminate     *bool  `json:"terminate,omitempty"`
}

// Batch is the set of tool calls from one assistant message being
// executed.
type Batch struct {
	AssistantEntryID string                  `json:"assistantEntryId"`
	Calls            []ToolCallState         `json:"calls"`
	Configuration    GenerationConfiguration `json:"configuration"`
	TurnID           string                  `json:"turnId"`
}

// OpState is the durable FSM checkpoint written to pi.op.state. Only the
// fields relevant to At are populated; the rest are left zero.
type OpState struct {
	At                     OpAt               `json:"at"`
	Control                OpControl          `json:"control"`
	Settings               OpSettings         `json:"settings"`
	LatestAssistantEntryID *string            `json:"latestAssistantEntryId"`
	Continuation           *Continuation      `json:"continuation,omitempty"`
	GenerationContext      *GenerationContext `json:"generationContext,omitempty"`
	NextAttempt            *int               `json:"nextAttempt,omitempty"`
	Attempt                *int               `json:"attempt,omitempty"`
	ContextWindow          *int               `json:"contextWindow,omitempty"`
	IntendedOutputLimit    *int               `json:"intendedOutputLimit,omitempty"`
	ResponseEntryID        *string            `json:"responseEntryId,omitempty"`
	UsageID                *string            `json:"usageId,omitempty"`
	Batch                  *Batch             `json:"batch,omitempty"`
	TriggerEntryID         string             `json:"triggerEntryId,omitempty"`
}

// OpIntent describes why an operation started.
type OpIntent struct {
	Kind           string   `json:"kind"` // "run"
	PromptEntryIDs []string `json:"promptEntryIds,omitempty"`
}

// OpMeta is the immutable metadata written once at operation start.
type OpMeta struct {
	Intent      OpIntent `json:"intent"`
	Lane        string   `json:"lane"`
	OperationID string   `json:"operationId"`
	SourceTipID *string  `json:"sourceTipId"`
	StartedAt   int64    `json:"startedAt"`
}

// OpResult is the terminal record written to pi.result.
type OpResult struct {
	EndedAt     int64   `json:"endedAt"`
	FromTipID   *string `json:"fromTipId"`
	Kind        string  `json:"kind"` // "run"
	OperationID string  `json:"operationId"`
	StartedAt   int64   `json:"startedAt"`
	Status      string  `json:"status"` // "completed" | "aborted" | "failed"
	TipID       *string `json:"tipId"`
	Error       string  `json:"error,omitempty"`
}

// PendingToolResultPayload is what pi.pending.entry holds while a tool
// result is materialized but not yet committed as a branch entry: the
// ToolResultMessage itself (not wrapped in an Entry), matching the shape
// pi writes at pi.pending.entry.<resultEntryId>.
type PendingToolResultPayload struct {
	Payload msg.ToolResultMessage `json:"payload"`
}

// opStateAddr, opMetaAddr and opResultAddr address the typed FSM values
// this package reads and writes, keyed by operationID. They mirror
// session.OperationState/OperationMeta/OperationResult (which are typed
// json.RawMessage for a storage-agnostic caller) but decode straight into
// this package's own OpState/OpMeta/OpResult.
func opStateAddr(operationID string) session.Value[OpState] {
	return session.NewValue[OpState](session.NamespaceOpState, operationID)
}

func opMetaAddr(operationID string) session.Value[OpMeta] {
	return session.NewValue[OpMeta](session.NamespaceOpMeta, operationID)
}

func opResultAddr(operationID string) session.Value[OpResult] {
	return session.NewValue[OpResult](session.NamespaceResult, operationID)
}
