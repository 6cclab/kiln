package session

import (
	"encoding/json"
	"testing"
)

func TestSetValueBuildsCorrectWrite(t *testing.T) {
	addr := SessionName()
	w, err := SetValue(addr, "my session")
	if err != nil {
		t.Fatalf("SetValue: %v", err)
	}
	if w.Kind != "value" || w.Op != "set" {
		t.Fatalf("SetValue write = %+v, want kind=value op=set", w)
	}
	if w.Namespace != NamespaceSessionName || w.Key != "" {
		t.Fatalf("SetValue namespace/key = %q/%q, want %q/\"\"", w.Namespace, w.Key, NamespaceSessionName)
	}
	if string(w.Value) != `"my session"` {
		t.Fatalf("SetValue value = %s, want %q", w.Value, `"my session"`)
	}
}

func TestSetValueRawBuildsCorrectWrite(t *testing.T) {
	raw := json.RawMessage(`{"a":1}`)
	w := SetValueRaw("ns", "key", raw)
	if w.Kind != "value" || w.Op != "set" || w.Namespace != "ns" || w.Key != "key" {
		t.Fatalf("SetValueRaw = %+v, want kind=value op=set ns=ns key=key", w)
	}
	if string(w.Value) != `{"a":1}` {
		t.Fatalf("SetValueRaw value = %s", w.Value)
	}
}

func TestDeleteValueBuildsCorrectWrite(t *testing.T) {
	w := DeleteValue(EntryLabel("entry-1"))
	if w.Kind != "value" || w.Op != "delete" {
		t.Fatalf("DeleteValue = %+v, want kind=value op=delete", w)
	}
	if w.Namespace != NamespaceEntryLabel || w.Key != "entry-1" {
		t.Fatalf("DeleteValue namespace/key = %q/%q", w.Namespace, w.Key)
	}
	if w.Value != nil {
		t.Fatalf("DeleteValue value = %v, want nil (absent)", w.Value)
	}
}

func TestAppendListWriteBuildsCorrectWrite(t *testing.T) {
	addr := PendingAssistantFrames("op-1", "entry-1")
	w, err := AppendListWrite(addr, json.RawMessage(`{"frame":1}`))
	if err != nil {
		t.Fatalf("AppendListWrite: %v", err)
	}
	if w.Kind != "list" || w.Op != "append" {
		t.Fatalf("AppendListWrite = %+v, want kind=list op=append", w)
	}
	if w.Namespace != NamespacePendingAssistantFrame || w.Key != "op-1:entry-1" {
		t.Fatalf("AppendListWrite namespace/key = %q/%q", w.Namespace, w.Key)
	}
	if string(w.Value) != `{"frame":1}` {
		t.Fatalf("AppendListWrite value = %s", w.Value)
	}
}

func TestDeleteListWriteBuildsCorrectWrite(t *testing.T) {
	addr := PendingAssistantFrames("op-1", "entry-1")
	w := DeleteListWrite(addr)
	if w.Kind != "list" || w.Op != "delete" {
		t.Fatalf("DeleteListWrite = %+v, want kind=list op=delete", w)
	}
	if w.Namespace != NamespacePendingAssistantFrame || w.Key != "op-1:entry-1" {
		t.Fatalf("DeleteListWrite namespace/key = %q/%q", w.Namespace, w.Key)
	}
}

func TestGetTypedValueDecodesJSON(t *testing.T) {
	raw := json.RawMessage(`{"model":{"provider":"ollama","modelId":"m1"},"thinkingLevel":"off","activeToolNames":["bash"]}`)
	cfg, err := GetTypedValue[LaneConfiguration](raw)
	if err != nil {
		t.Fatalf("GetTypedValue: %v", err)
	}
	if cfg.Model.Provider != "ollama" || cfg.Model.ModelID != "m1" {
		t.Fatalf("GetTypedValue model = %+v", cfg.Model)
	}
	if cfg.ThinkingLevel != "off" {
		t.Fatalf("GetTypedValue thinkingLevel = %q", cfg.ThinkingLevel)
	}
	if len(cfg.ActiveToolNames) != 1 || cfg.ActiveToolNames[0] != "bash" {
		t.Fatalf("GetTypedValue activeToolNames = %v", cfg.ActiveToolNames)
	}
}

func TestGetTypedValueEmptyRawReturnsZeroValue(t *testing.T) {
	cfg, err := GetTypedValue[LaneConfiguration](nil)
	if err != nil {
		t.Fatalf("GetTypedValue(nil): %v", err)
	}
	if cfg.Model != (ModelRef{}) || cfg.ThinkingLevel != "" || cfg.ActiveToolNames != nil {
		t.Fatalf("GetTypedValue(nil) = %+v, want zero value", cfg)
	}

	s, err := GetTypedValue[string](json.RawMessage{})
	if err != nil {
		t.Fatalf("GetTypedValue(empty): %v", err)
	}
	if s != "" {
		t.Fatalf("GetTypedValue(empty) = %q, want empty string", s)
	}
}

func TestNamespaceAddressHelpers(t *testing.T) {
	cases := []struct {
		name      string
		namespace string
		key       string
	}{
		{"BranchTip", NamespaceBranchTip, "main"},
		{"BranchTipInventoryPrefix", NamespaceBranchTip, ""},
		{"LaneConfig", NamespaceLaneConfig, "main"},
		{"LaneStateValue", NamespaceLaneState, "main"},
		{"OperationResult", NamespaceResult, "op-1"},
		{"OperationMeta", NamespaceOpMeta, "op-1"},
		{"OperationState", NamespaceOpState, "op-1"},
		{"PendingEntry", NamespacePendingEntry, "entry-1"},
		{"SessionName", NamespaceSessionName, ""},
		{"EntryLabel", NamespaceEntryLabel, "entry-1"},
	}
	got := map[string]struct {
		namespace string
		key       string
	}{
		"BranchTip":                {BranchTip("main").Namespace, BranchTip("main").Key},
		"BranchTipInventoryPrefix": {BranchTipInventoryPrefix().Namespace, BranchTipInventoryPrefix().Key},
		"LaneConfig":               {LaneConfig("main").Namespace, LaneConfig("main").Key},
		"LaneStateValue":           {LaneStateValue("main").Namespace, LaneStateValue("main").Key},
		"OperationResult":          {OperationResult("op-1").Namespace, OperationResult("op-1").Key},
		"OperationMeta":            {OperationMeta("op-1").Namespace, OperationMeta("op-1").Key},
		"OperationState":           {OperationState("op-1").Namespace, OperationState("op-1").Key},
		"PendingEntry":             {PendingEntry("entry-1").Namespace, PendingEntry("entry-1").Key},
		"SessionName":              {SessionName().Namespace, SessionName().Key},
		"EntryLabel":               {EntryLabel("entry-1").Namespace, EntryLabel("entry-1").Key},
	}
	for _, c := range cases {
		g := got[c.name]
		if g.namespace != c.namespace || g.key != c.key {
			t.Errorf("%s = (%q,%q), want (%q,%q)", c.name, g.namespace, g.key, c.namespace, c.key)
		}
	}
}

func TestOperationToolArgsKeyComposition(t *testing.T) {
	addr := OperationToolArgs("op-1", "step-2", 3)
	if addr.Namespace != NamespaceOpToolArgs {
		t.Fatalf("OperationToolArgs namespace = %q", addr.Namespace)
	}
	if addr.Key != "op-1:step-2:3" {
		t.Fatalf("OperationToolArgs key = %q, want %q", addr.Key, "op-1:step-2:3")
	}
}

func TestPendingToolOutputKeyComposition(t *testing.T) {
	addr := PendingToolOutput("op-1", "inv-2")
	if addr.Namespace != NamespacePendingToolOutput {
		t.Fatalf("PendingToolOutput namespace = %q", addr.Namespace)
	}
	if addr.Key != "op-1:inv-2" {
		t.Fatalf("PendingToolOutput key = %q, want %q", addr.Key, "op-1:inv-2")
	}
}
