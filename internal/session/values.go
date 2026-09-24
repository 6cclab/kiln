package session

import (
	"encoding/json"
	"strconv"
)

// Value is a typed address of one scalar in the value store: a namespace
// (e.g. "pi.branch.tip") and a key (e.g. a lane or branch name). T only
// guides the typed helpers below; on the wire every value is a JSON value
// keyed by (namespace, key). See values.js in pi for the source encoding
// this mirrors.
type Value[T any] struct {
	Namespace string
	Key       string
}

// ValueList is a typed address of one list in the list store.
type ValueList[T any] struct {
	Namespace string
	Key       string
}

// NewValue builds a Value address, key defaulting to "".
func NewValue[T any](namespace string, key string) Value[T] {
	return Value[T]{Namespace: namespace, Key: key}
}

// NewValueList builds a ValueList address, key defaulting to "".
func NewValueList[T any](namespace string, key string) ValueList[T] {
	return ValueList[T]{Namespace: namespace, Key: key}
}

// Namespace constants, matching pi's values.ts exactly.
const (
	NamespaceBranchTip             = "pi.branch.tip"
	NamespaceLaneConfig            = "pi.lane.config"
	NamespaceLaneState             = "pi.lane.state"
	NamespaceOpMeta                = "pi.op.meta"
	NamespaceOpState               = "pi.op.state"
	NamespaceOpToolArgs            = "pi.op.tool_args"
	NamespaceOpToolMemo            = "pi.op.tool_memo"
	NamespaceOpPreparation         = "pi.op.preparation"
	NamespacePendingEntry          = "pi.pending.entry"
	NamespacePendingToolOutput     = "pi.pending.tool_output"
	NamespacePendingAssistantFrame = "pi.pending.assistant_frame"
	NamespaceResult                = "pi.result"
	NamespaceSessionName           = "pi.session.name"
	NamespaceEntryLabel            = "pi.entry.label"
)

// BranchTip addresses the current tip entry id of a branch (nil at root).
func BranchTip(branch string) Value[*string] { return NewValue[*string](NamespaceBranchTip, branch) }

// BranchTipInventoryPrefix addresses every branch tip (used with ScanValues).
func BranchTipInventoryPrefix() Value[*string] { return NewValue[*string](NamespaceBranchTip, "") }

// LaneConfig addresses a lane's model/thinking/tool configuration.
func LaneConfig(lane string) Value[LaneConfiguration] {
	return NewValue[LaneConfiguration](NamespaceLaneConfig, lane)
}

// LaneStateValue addresses a lane's current/last operation and inbox.
func LaneStateValue(lane string) Value[LaneState] {
	return NewValue[LaneState](NamespaceLaneState, lane)
}

// OperationResult addresses an operation's terminal result record.
func OperationResult(operationID string) Value[json.RawMessage] {
	return NewValue[json.RawMessage](NamespaceResult, operationID)
}

// OperationMeta addresses an operation's immutable metadata.
func OperationMeta(operationID string) Value[json.RawMessage] {
	return NewValue[json.RawMessage](NamespaceOpMeta, operationID)
}

// OperationState addresses an operation's durable FSM checkpoint.
func OperationState(operationID string) Value[json.RawMessage] {
	return NewValue[json.RawMessage](NamespaceOpState, operationID)
}

// OperationToolArgs addresses one tool call's captured arguments.
func OperationToolArgs(operationID, stepID string, sourceIndex int) Value[json.RawMessage] {
	return NewValue[json.RawMessage](NamespaceOpToolArgs, operationID+":"+stepID+":"+strconv.Itoa(sourceIndex))
}

// PendingEntry addresses a not-yet-committed entry payload.
func PendingEntry(entryID string) Value[json.RawMessage] {
	return NewValue[json.RawMessage](NamespacePendingEntry, entryID)
}

// PendingToolOutput addresses a not-yet-committed tool result payload.
func PendingToolOutput(operationID, invocationID string) Value[json.RawMessage] {
	return NewValue[json.RawMessage](NamespacePendingToolOutput, operationID+":"+invocationID)
}

// PendingAssistantFrames addresses the streamed-frame list for one response.
func PendingAssistantFrames(operationID, responseEntryID string) ValueList[json.RawMessage] {
	return NewValueList[json.RawMessage](NamespacePendingAssistantFrame, operationID+":"+responseEntryID)
}

// SessionName addresses the session's optional display name.
func SessionName() Value[string] { return NewValue[string](NamespaceSessionName, "") }

// EntryLabel addresses one entry's optional display label.
func EntryLabel(entryID string) Value[string] { return NewValue[string](NamespaceEntryLabel, entryID) }

// SetValue builds a ValueWrite that sets a scalar to next.
func SetValue[T any](addr Value[T], next T) (ValueWrite, error) {
	raw, err := json.Marshal(next)
	if err != nil {
		return ValueWrite{}, err
	}
	return ValueWrite{Kind: "value", Op: "set", Namespace: addr.Namespace, Key: addr.Key, Value: raw}, nil
}

// SetValueRaw builds a ValueWrite that sets a scalar from a raw JSON value.
func SetValueRaw(namespace, key string, next json.RawMessage) ValueWrite {
	return ValueWrite{Kind: "value", Op: "set", Namespace: namespace, Key: key, Value: next}
}

// DeleteValue builds a ValueWrite that deletes a scalar.
func DeleteValue[T any](addr Value[T]) ValueWrite {
	return ValueWrite{Kind: "value", Op: "delete", Namespace: addr.Namespace, Key: addr.Key}
}

// AppendListWrite builds a ValueWrite that appends one list element.
func AppendListWrite[T any](addr ValueList[T], element T) (ValueWrite, error) {
	raw, err := json.Marshal(element)
	if err != nil {
		return ValueWrite{}, err
	}
	return ValueWrite{Kind: "list", Op: "append", Namespace: addr.Namespace, Key: addr.Key, Value: raw}, nil
}

// DeleteListWrite builds a ValueWrite that deletes an entire list.
func DeleteListWrite[T any](addr ValueList[T]) ValueWrite {
	return ValueWrite{Kind: "list", Op: "delete", Namespace: addr.Namespace, Key: addr.Key}
}

// GetTypedValue decodes a raw stored scalar into T.
func GetTypedValue[T any](raw json.RawMessage) (T, error) {
	var out T
	if len(raw) == 0 {
		return out, nil
	}
	err := json.Unmarshal(raw, &out)
	return out, err
}
